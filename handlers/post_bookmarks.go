package handlers

import (
	"log"
	"net/http"
	"strconv"

	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/models"
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

	_, err = postgress.Exec(
		`DELETE FROM post_bookmarks WHERE user_id = $1 AND post_id = $2`,
		userID, postID,
	)
	if err != nil {
		log.Printf("[arena] unbookmark post failed user=%s post=%d: %v", userID, postID, err)
		JSONError(w, "Failed to remove bookmark", http.StatusInternalServerError)
		return
	}

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

	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT p.id, p.user_id, u.username, u.name, u.avatar_url,
		        p.caption, p.post_type, p.original_post_id, p.visibility,
		        p.is_pinned, p.like_count, p.comment_count, p.repost_count,
		        p.view_count, p.bookmark_count,
		        my_like.user_id IS NOT NULL,
		        true,
		        p.created_at
		 FROM post_bookmarks bm
		 JOIN posts p ON p.id = bm.post_id
		 JOIN users u ON u.id = p.user_id
		 LEFT JOIN post_likes my_like ON my_like.post_id = p.id AND my_like.user_id = $1
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

	JSONSuccess(w, posts)
}
