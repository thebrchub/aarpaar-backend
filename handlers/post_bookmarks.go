package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/models"
	"github.com/thebrchub/aarpaar/services"
)

// ---------------------------------------------------------------------------
// Bookmark a Post
// POST /api/v1/arena/posts/{postId}/bookmark
// ---------------------------------------------------------------------------

func BookmarkPostHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	postID, err := strconv.ParseInt(r.PathValue("postId"), 10, 64)
	if err != nil {
		JSONError(w, "Invalid post ID", http.StatusBadRequest)
		return
	}

	// Plain reposts redirect bookmarks to the original post.
	postID = services.ResolveOriginalPostID(r.Context(), postID)

	// FK constraint will reject if post doesn't exist; ON CONFLICT handles dupes.
	// No need for a separate EXISTS check — saves one round-trip.
	result, err := postgress.Exec(
		`INSERT INTO post_bookmarks (user_id, post_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		userID, postID,
	)
	if err != nil {
		log.Printf("[arena] bookmark post failed user=%s post=%d: %v", userID, postID, err)
		JSONError(w, "Failed to bookmark post", http.StatusInternalServerError)
		return
	}
	if result == 0 {
		// Either post doesn't exist (FK violation caught above) or already bookmarked
		JSONMessage(w, "success", "Already bookmarked")
		return
	}

	// Invalidate bookmarks cache for this user (async to not block response)
	chat.RunBackground(func() { invalidateBookmarksCache(userID) })

	JSONMessage(w, "success", "Post bookmarked")
}

// ---------------------------------------------------------------------------
// Remove Bookmark
// DELETE /api/v1/arena/posts/{postId}/bookmark
// ---------------------------------------------------------------------------

func UnbookmarkPostHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	postID, err := strconv.ParseInt(r.PathValue("postId"), 10, 64)
	if err != nil {
		JSONError(w, "Invalid post ID", http.StatusBadRequest)
		return
	}

	// Plain reposts redirect unbookmarks to the original post.
	postID = services.ResolveOriginalPostID(r.Context(), postID)

	_, err = postgress.Exec(
		`DELETE FROM post_bookmarks WHERE user_id = $1 AND post_id = $2`,
		userID, postID,
	)
	if err != nil {
		log.Printf("[arena] unbookmark post failed user=%s post=%d: %v", userID, postID, err)
		JSONError(w, "Failed to remove bookmark", http.StatusInternalServerError)
		return
	}

	// Invalidate bookmarks cache for this user (async to not block response)
	chat.RunBackground(func() { invalidateBookmarksCache(userID) })

	JSONMessage(w, "success", "Bookmark removed")
}

// ---------------------------------------------------------------------------
// Get Bookmarked Posts (personal feed)
// GET /api/v1/arena/bookmarks
// ---------------------------------------------------------------------------

func GetBookmarksHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	limit, offset := parseFeedPagination(r)

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Cache per-user bookmarks feed for 1 minute
	cacheKey := fmt.Sprintf("%s%s:%d:%d", config.CacheBookmarks, userID, limit, offset)
	rdb := redis.GetRawClient()
	if cached, err := rdb.Get(ctx, cacheKey).Bytes(); err == nil && len(cached) > 0 {
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
		return
	}

	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT p.id, p.user_id, u.username, u.name, u.avatar_url,
		        p.caption, p.post_type, p.original_post_id, p.visibility,
		        p.is_pinned, p.like_count, p.comment_count, p.repost_count,
		        p.view_count, p.bookmark_count,
		        my_like.user_id IS NOT NULL,
		        true,
		        my_repost.id IS NOT NULL,
		        p.created_at
		 FROM post_bookmarks bm
		 JOIN posts p ON p.id = bm.post_id
		 JOIN users u ON u.id = p.user_id
		 LEFT JOIN post_likes my_like ON my_like.post_id = p.id AND my_like.user_id = $1
		 LEFT JOIN posts my_repost ON my_repost.original_post_id = p.id AND my_repost.user_id = $1 AND my_repost.post_type = 'repost' AND my_repost.caption = ''
		 WHERE bm.user_id = $1
		 ORDER BY bm.created_at DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		log.Printf("[arena] bookmarks feed query failed: %v", err)
		JSONError(w, "Failed to load bookmarks", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	posts := make([]models.PostResponse, 0)
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			continue
		}
		posts = append(posts, p)
	}

	if err := attachMedia(ctx, posts, Store); err != nil {
		log.Printf("[arena] attach media failed: %v", err)
	}
	attachOriginalPosts(ctx, posts, userID)

	if respBytes, err := json.Marshal(posts); err == nil {
		rdb.Set(ctx, cacheKey, respBytes, 1*time.Minute)
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(respBytes)
		return
	}

	JSONSuccess(w, posts)
}

// invalidateBookmarksCache deletes all cached bookmark feed pages for a user.
func invalidateBookmarksCache(userID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Delete common page combinations (first few pages cover 99% of usage)
	rdb := redis.GetRawClient()
	keys := make([]string, 0, 6)
	for _, limit := range []int{config.DefaultFeedLimit, config.MaxFeedLimit} {
		for _, offset := range []int{0, config.DefaultFeedLimit, config.DefaultFeedLimit * 2} {
			keys = append(keys, fmt.Sprintf("%s%s:%d:%d", config.CacheBookmarks, userID, limit, offset))
		}
	}
	rdb.Del(ctx, keys...)
}
