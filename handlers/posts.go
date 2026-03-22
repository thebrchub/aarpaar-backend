package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-json"
	goredis "github.com/redis/go-redis/v9"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/shivanand-burli/go-starter-kit/storage"
	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/models"
	"github.com/thebrchub/aarpaar/services"
)

// ---------------------------------------------------------------------------
// Create Post
// POST /api/v1/arena/posts
// ---------------------------------------------------------------------------

func CreatePostHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req models.CreatePostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	limits := services.GetArenaLimits()

	// Validate caption length (paid users get higher limit)
	maxCaption := limits.FreeCaptionLength
	if IsUserVIP(r.Context(), userID) {
		maxCaption = limits.MaxCaptionLength
	}
	if len(req.Caption) > maxCaption {
		JSONError(w, fmt.Sprintf("Caption too long (max %d chars)", maxCaption), http.StatusBadRequest)
		return
	}

	// Validate media count
	if len(req.Media) > limits.MaxMediaPerPost {
		JSONError(w, fmt.Sprintf("Too many media items (max %d)", limits.MaxMediaPerPost), http.StatusBadRequest)
		return
	}

	// Must have either caption or media
	if strings.TrimSpace(req.Caption) == "" && len(req.Media) == 0 {
		JSONError(w, "Post must have a caption or media", http.StatusBadRequest)
		return
	}

	// Validate media types
	for _, m := range req.Media {
		if m.MediaType != config.MediaTypeImage && m.MediaType != config.MediaTypeVideo {
			JSONError(w, "Invalid media type: "+m.MediaType, http.StatusBadRequest)
			return
		}
		if m.ObjectKey == "" {
			JSONError(w, "Media objectKey is required", http.StatusBadRequest)
			return
		}
	}

	// Default visibility
	vis := config.PostVisibilityPublic
	if req.Visibility == config.PostVisibilityFriends {
		vis = config.PostVisibilityFriends
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// VIP users have unlimited posts — skip the limit check entirely
	if !IsUserVIP(ctx, userID) {
		// EXISTS with OFFSET: returns true if the user already has >= N posts.
		// Short-circuits at the Nth row instead of scanning all rows like COUNT(*).
		var limitReached bool
		_ = postgress.GetRawDB().QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM posts WHERE user_id = $1 OFFSET $2)`,
			userID, limits.MaxPostsPerUser,
		).Scan(&limitReached)
		if limitReached {
			JSONError(w, fmt.Sprintf("Post limit reached (%d). Donate to unlock unlimited posts!", limits.MaxPostsPerUser), http.StatusForbidden)
			return
		}
	}

	// Insert post
	var postID int64
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`INSERT INTO posts (user_id, caption, post_type, visibility)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		userID, req.Caption, config.PostTypeOriginal, vis,
	).Scan(&postID)
	if err != nil {
		log.Printf("[arena] create post failed user=%s: %v", userID, err)
		JSONError(w, "Failed to create post", http.StatusInternalServerError)
		return
	}

	// Insert media (single multi-row INSERT instead of N separate calls)
	if len(req.Media) > 0 {
		values := make([]string, len(req.Media))
		params := make([]any, 0, len(req.Media)*8)
		for i, m := range req.Media {
			base := i * 8
			values[i] = fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
				base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8)
			params = append(params, postID, m.MediaType, m.ObjectKey, m.Width, m.Height, m.DurationMs, m.PreviewHash, m.SortOrder)
		}
		_, err := postgress.Exec(
			fmt.Sprintf(`INSERT INTO post_media (post_id, media_type, object_key, width, height, duration_ms, preview_hash, sort_order)
				VALUES %s`, strings.Join(values, ", ")),
			params...,
		)
		if err != nil {
			log.Printf("[arena] insert media failed post=%d: %v", postID, err)
		}
	}

	// Return the created post
	post, err := getPostByID(ctx, postID, userID)
	if err != nil {
		JSONError(w, "Post created but failed to fetch", http.StatusInternalServerError)
		return
	}

	// Invalidate feed caches so new post appears immediately
	chat.RunBackground(func() { InvalidateFeedCaches() })

	JSONSuccess(w, post)
}

// ---------------------------------------------------------------------------
// Get Post
// GET /api/v1/arena/posts/{postId}
// ---------------------------------------------------------------------------

func GetPostHandler(w http.ResponseWriter, r *http.Request) {
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

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Cache single-post detail for 2 minutes (user-specific for hasLiked/hasBookmarked)
	cacheKey := fmt.Sprintf("%s%d:%s", config.CachePost, postID, userID)
	rdb := redis.GetRawClient()
	if cached, err := rdb.Get(ctx, cacheKey).Bytes(); err == nil && len(cached) > 0 {
		// Still record the detail expand
		chat.RunBackground(func() {
			services.BufferDetailExpand(context.Background(), userID, postID)
		})
		var post models.PostResponse
		if json.Unmarshal(cached, &post) == nil {
			posts := []models.PostResponse{post}
			overlayPendingLikes(ctx, rdb, userID, posts)
			if patched, err := json.Marshal(posts[0]); err == nil {
				w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
				w.WriteHeader(http.StatusOK)
				w.Write(patched)
				return
			}
		}
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
		return
	}

	post, err := getPostByID(ctx, postID, userID)
	if err != nil {
		JSONError(w, "Post not found", http.StatusNotFound)
		return
	}

	// Record detail expand (unique per user, buffered in Redis)
	chat.RunBackground(func() {
		services.BufferDetailExpand(context.Background(), userID, postID)
	})

	if respBytes, err := json.Marshal(post); err == nil {
		rdb.Set(ctx, cacheKey, respBytes, 2*time.Minute)
	}

	posts := []models.PostResponse{post}
	overlayPendingLikes(ctx, rdb, userID, posts)
	post = posts[0]

	if respBytes, err := json.Marshal(post); err == nil {
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(respBytes)
		return
	}

	JSONSuccess(w, post)
}

// ---------------------------------------------------------------------------
// Delete Post
// DELETE /api/v1/arena/posts/{postId}
// ---------------------------------------------------------------------------

func DeletePostHandler(w http.ResponseWriter, r *http.Request) {
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

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Fetch media keys for cleanup
	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT object_key FROM post_media WHERE post_id = $1`, postID,
	)
	if err == nil {
		defer rows.Close()
		var keys []string
		for rows.Next() {
			var key string
			if rows.Scan(&key) == nil {
				keys = append(keys, key)
			}
		}
		if rErr := rows.Err(); rErr != nil {
			log.Printf("[arena] rows iteration error post=%d: %v", postID, rErr)
		}

		// Delete from storage
		if len(keys) > 0 && Store != nil {
			chat.RunBackground(func() {
				bgCtx, bgCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer bgCancel()
				if err := Store.DeleteBatch(bgCtx, keys); err != nil {
					log.Printf("[arena] delete media from storage failed post=%d: %v", postID, err)
				}
			})
		}
	}

	// If this is a repost, decrement counters on the original post.
	var postType string
	var origPostID *int64
	var caption string
	_ = postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT post_type, original_post_id, caption FROM posts WHERE id = $1 AND user_id = $2`,
		postID, userID,
	).Scan(&postType, &origPostID, &caption)

	// Delete post (cascades to media, likes, comments)
	result, err := postgress.Exec(
		`DELETE FROM posts WHERE id = $1 AND user_id = $2`, postID, userID,
	)
	if err != nil {
		log.Printf("[arena] delete post failed post=%d user=%s: %v", postID, userID, err)
		JSONError(w, "Failed to delete post", http.StatusInternalServerError)
		return
	}
	if result == 0 {
		JSONError(w, "Post not found or not yours", http.StatusNotFound)
		return
	}

	// Decrement repost/quote counters on the original post in background.
	if postType == config.PostTypeRepost && origPostID != nil {
		origID := *origPostID
		captionTrimmed := strings.TrimSpace(caption)
		chat.RunBackground(func() {
			if captionTrimmed != "" {
				_, _ = postgress.Exec(
					`UPDATE posts SET repost_count = GREATEST(repost_count - 1, 0), quote_count = GREATEST(quote_count - 1, 0) WHERE id = $1`, origID,
				)
			} else {
				_, _ = postgress.Exec(
					`UPDATE posts SET repost_count = GREATEST(repost_count - 1, 0) WHERE id = $1`, origID,
				)
			}
		})
	}

	// Invalidate feed caches + single post cache
	chat.RunBackground(func() {
		InvalidateFeedCaches()
		redis.GetRawClient().Del(context.Background(), fmt.Sprintf("%s%d:%s", config.CachePost, postID, userID))
	})

	JSONMessage(w, "success", "Post deleted")
}

// ---------------------------------------------------------------------------
// Repost
// POST /api/v1/arena/posts/{postId}/repost
// ---------------------------------------------------------------------------

func RepostHandler(w http.ResponseWriter, r *http.Request) {
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

	var req models.RepostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	limits := services.GetArenaLimits()
	maxCaption := limits.FreeCaptionLength
	if IsUserVIP(r.Context(), userID) {
		maxCaption = limits.MaxCaptionLength
	}
	if len(req.Caption) > maxCaption {
		JSONError(w, fmt.Sprintf("Caption too long (max %d chars)", maxCaption), http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Verify original exists
	var exists bool
	_ = postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM posts WHERE id = $1)`, postID,
	).Scan(&exists)
	if !exists {
		JSONError(w, "Original post not found", http.StatusNotFound)
		return
	}

	// Prevent duplicate plain (non-quote) repost — only one allowed per user per post.
	// Quote reposts (with caption) are allowed multiple times.
	captionTrimmed := strings.TrimSpace(req.Caption)
	if captionTrimmed == "" {
		var alreadyReposted bool
		_ = postgress.GetRawDB().QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM posts WHERE user_id = $1 AND original_post_id = $2 AND post_type = $3 AND caption = '')`,
			userID, postID, config.PostTypeRepost,
		).Scan(&alreadyReposted)
		if alreadyReposted {
			JSONError(w, "Already reposted", http.StatusConflict)
			return
		}
	}

	var newPostID int64
	err = postgress.GetRawDB().QueryRowContext(ctx,
		`INSERT INTO posts (user_id, caption, post_type, original_post_id)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		userID, req.Caption, config.PostTypeRepost, postID,
	).Scan(&newPostID)
	if err != nil {
		log.Printf("[arena] repost failed user=%s original=%d: %v", userID, postID, err)
		JSONError(w, "Failed to repost", http.StatusInternalServerError)
		return
	}

	// Increment repost count on original in background (non-blocking)
	chat.RunBackground(func() {
		if captionTrimmed != "" {
			_, _ = postgress.Exec(
				`UPDATE posts SET repost_count = repost_count + 1, quote_count = quote_count + 1 WHERE id = $1`, postID,
			)
		} else {
			_, _ = postgress.Exec(
				`UPDATE posts SET repost_count = repost_count + 1 WHERE id = $1`, postID,
			)
		}
	})

	post, err := getPostByID(ctx, newPostID, userID)
	if err != nil {
		JSONError(w, "Repost created but failed to fetch", http.StatusInternalServerError)
		return
	}

	// Invalidate single-post cache immediately so hasReposted / counts are fresh
	invalidatePostCache(postID)

	// Invalidate feed caches in background (heavier SCAN)
	chat.RunBackground(func() { InvalidateFeedCaches() })

	JSONSuccess(w, post)
}

// ---------------------------------------------------------------------------
// Pin / Unpin Post
// POST /api/v1/arena/posts/{postId}/pin
// DELETE /api/v1/arena/posts/{postId}/pin
// ---------------------------------------------------------------------------

func PinPostHandler(w http.ResponseWriter, r *http.Request) {
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

	pin := r.Method == http.MethodPost

	result, err := postgress.Exec(
		`UPDATE posts SET is_pinned = $1, updated_at = NOW()
		 WHERE id = $2 AND user_id = $3`, pin, postID, userID,
	)
	if err != nil || result == 0 {
		JSONError(w, "Post not found or not yours", http.StatusNotFound)
		return
	}

	if pin {
		JSONMessage(w, "success", "Post pinned")
	} else {
		JSONMessage(w, "success", "Post unpinned")
	}
}

// ---------------------------------------------------------------------------
// Global Pulse Feed (all public posts, most recent first)
// GET /api/v1/arena/feed/global
// ---------------------------------------------------------------------------

func GlobalFeedHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	cursor, limit := parseFeedCursor(r)

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Cache key truncates cursor to the minute so all requests in the same
	// 60-second window share a single cached page.
	truncated := cursor.Truncate(time.Minute).Unix()
	cacheKey := fmt.Sprintf("%s%s:%d:%d", config.CacheFeedGlobal, userID, truncated, limit)
	rdb := redis.GetRawClient()
	if cached, err := rdb.Get(ctx, cacheKey).Bytes(); err == nil && len(cached) > 0 {
		// Overlay pending likes from Redis buffer for read-your-own-writes
		var posts []models.PostResponse
		if json.Unmarshal(cached, &posts) == nil {
			overlayPendingLikes(ctx, rdb, userID, posts)
			if patched, err := json.Marshal(posts); err == nil {
				w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
				w.WriteHeader(http.StatusOK)
				w.Write(patched)
				return
			}
		}
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
		        my_bm.user_id IS NOT NULL,
		        my_repost.id IS NOT NULL,
		        p.created_at
		 FROM posts p
		 JOIN users u ON u.id = p.user_id
		 LEFT JOIN post_likes my_like ON my_like.post_id = p.id AND my_like.user_id = $1
		 LEFT JOIN post_bookmarks my_bm ON my_bm.post_id = p.id AND my_bm.user_id = $1
		 LEFT JOIN posts my_repost ON my_repost.original_post_id = p.id AND my_repost.user_id = $1 AND my_repost.post_type = 'repost' AND my_repost.caption = ''
		 WHERE p.visibility = 'public' AND p.created_at < $2
		 ORDER BY p.created_at DESC
		 LIMIT $3`,
		userID, cursor, limit,
	)
	if err != nil {
		log.Printf("[arena] global feed query failed: %v", err)
		JSONError(w, "Failed to load feed", http.StatusInternalServerError)
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

	// Attach media with presigned URLs
	if err := attachMedia(ctx, posts, Store); err != nil {
		log.Printf("[arena] attach media failed: %v", err)
	}
	attachOriginalPosts(ctx, posts, userID)

	// Cache the Postgres-truth response (without pending overlay)
	if respBytes, err := json.Marshal(posts); err == nil {
		rdb.Set(ctx, cacheKey, respBytes, 1*time.Minute)
	}

	// Overlay pending likes from Redis buffer for read-your-own-writes
	overlayPendingLikes(ctx, rdb, userID, posts)

	if respBytes, err := json.Marshal(posts); err == nil {
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(respBytes)
		return
	}

	JSONSuccess(w, posts)
}

// ---------------------------------------------------------------------------
// Your Network Feed (posts from friends)
// GET /api/v1/arena/feed/network
// ---------------------------------------------------------------------------

func NetworkFeedHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	cursor, limit := parseFeedCursor(r)

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Cache per-user: truncate cursor to 30s window so nearby requests share cache.
	// Include generation counter so friendship changes invalidate immediately.
	gen := services.GetNetworkFeedGen(ctx, userID)
	truncated := cursor.Truncate(30 * time.Second).Unix()
	cacheKey := fmt.Sprintf("%s%s:%s:%d:%d", config.CacheFeedNetwork, userID, gen, truncated, limit)
	rdb := redis.GetRawClient()
	if cached, err := rdb.Get(ctx, cacheKey).Bytes(); err == nil && len(cached) > 0 {
		var posts []models.PostResponse
		if json.Unmarshal(cached, &posts) == nil {
			overlayPendingLikes(ctx, rdb, userID, posts)
			if patched, err := json.Marshal(posts); err == nil {
				w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
				w.WriteHeader(http.StatusOK)
				w.Write(patched)
				return
			}
		}
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
		return
	}

	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`WITH my_friends AS (
			SELECT user_id_2 AS friend_id FROM friendships WHERE user_id_1 = $1
			UNION ALL
			SELECT user_id_1 FROM friendships WHERE user_id_2 = $1
		)
		SELECT p.id, p.user_id, u.username, u.name, u.avatar_url,
		        p.caption, p.post_type, p.original_post_id, p.visibility,
		        p.is_pinned, p.like_count, p.comment_count, p.repost_count,
		        p.view_count, p.bookmark_count,
		        my_like.user_id IS NOT NULL,
		        my_bm.user_id IS NOT NULL,
		        my_repost.id IS NOT NULL,
		        p.created_at
		 FROM posts p
		 JOIN users u ON u.id = p.user_id
		 JOIN my_friends mf ON mf.friend_id = p.user_id
		 LEFT JOIN post_likes my_like ON my_like.post_id = p.id AND my_like.user_id = $1
		 LEFT JOIN post_bookmarks my_bm ON my_bm.post_id = p.id AND my_bm.user_id = $1
		 LEFT JOIN posts my_repost ON my_repost.original_post_id = p.id AND my_repost.user_id = $1 AND my_repost.post_type = 'repost' AND my_repost.caption = ''
		 WHERE p.created_at < $2
		 ORDER BY p.created_at DESC
		 LIMIT $3`,
		userID, cursor, limit,
	)
	if err != nil {
		log.Printf("[arena] network feed query failed: %v", err)
		JSONError(w, "Failed to load feed", http.StatusInternalServerError)
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
		rdb.Set(ctx, cacheKey, respBytes, 30*time.Second)
	}

	overlayPendingLikes(ctx, rdb, userID, posts)

	if respBytes, err := json.Marshal(posts); err == nil {
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(respBytes)
		return
	}

	JSONSuccess(w, posts)
}

// ---------------------------------------------------------------------------
// User Profile Posts
// GET /api/v1/arena/users/{userId}/posts
// ---------------------------------------------------------------------------

func UserPostsHandler(w http.ResponseWriter, r *http.Request) {
	viewerID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || viewerID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	targetUserID := r.PathValue("userId")
	if targetUserID == "" {
		JSONError(w, "userId is required", http.StatusBadRequest)
		return
	}

	limit, offset := parseFeedPagination(r)

	ctx, cancel := pgCtx(r)
	defer cancel()

	// If viewing own posts, show all. Otherwise, only public.
	visFilter := "AND p.visibility = 'public'"
	if targetUserID == viewerID {
		visFilter = ""
	}

	// Cache per viewer+target+page for 30s
	cacheKey := fmt.Sprintf("%s%s:%s:%d:%d", config.CacheFeedUser, viewerID, targetUserID, limit, offset)
	rdb := redis.GetRawClient()
	if cached, err := rdb.Get(ctx, cacheKey).Bytes(); err == nil && len(cached) > 0 {
		var posts []models.PostResponse
		if json.Unmarshal(cached, &posts) == nil {
			overlayPendingLikes(ctx, rdb, viewerID, posts)
			if patched, err := json.Marshal(posts); err == nil {
				w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
				w.WriteHeader(http.StatusOK)
				w.Write(patched)
				return
			}
		}
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
		return
	}

	query := fmt.Sprintf(`
		SELECT p.id, p.user_id, u.username, u.name, u.avatar_url,
		       p.caption, p.post_type, p.original_post_id, p.visibility,
		       p.is_pinned, p.like_count, p.comment_count, p.repost_count,
		       p.view_count, p.bookmark_count,
		       my_like.user_id IS NOT NULL,
		       my_bm.user_id IS NOT NULL,
		       my_repost.id IS NOT NULL,
		       p.created_at
		FROM posts p
		JOIN users u ON u.id = p.user_id
		LEFT JOIN post_likes my_like ON my_like.post_id = p.id AND my_like.user_id = $1
		LEFT JOIN post_bookmarks my_bm ON my_bm.post_id = p.id AND my_bm.user_id = $1
		LEFT JOIN posts my_repost ON my_repost.original_post_id = p.id AND my_repost.user_id = $1 AND my_repost.post_type = 'repost' AND my_repost.caption = ''
		WHERE p.user_id = $2 %s
		ORDER BY p.is_pinned DESC, p.created_at DESC
		LIMIT $3 OFFSET $4`, visFilter)

	rows, err := postgress.GetRawDB().QueryContext(ctx, query, viewerID, targetUserID, limit, offset)
	if err != nil {
		log.Printf("[arena] user posts query failed: %v", err)
		JSONError(w, "Failed to load posts", http.StatusInternalServerError)
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
	attachOriginalPosts(ctx, posts, viewerID)

	if respBytes, err := json.Marshal(posts); err == nil {
		rdb.Set(ctx, cacheKey, respBytes, 30*time.Second)
	}

	overlayPendingLikes(ctx, rdb, viewerID, posts)

	if respBytes, err := json.Marshal(posts); err == nil {
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(respBytes)
		return
	}

	JSONSuccess(w, posts)
}

// ---------------------------------------------------------------------------
// Trending Posts (HIGH HEAT)
// GET /api/v1/arena/feed/trending
// ---------------------------------------------------------------------------

func TrendingFeedHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	limit, offset := parseFeedPagination(r)

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Cache trending feed for 2 minutes (trending ranking is time-insensitive)
	cacheKey := fmt.Sprintf("%s%s:%d:%d", config.CacheFeedTrending, userID, limit, offset)
	rdb := redis.GetRawClient()
	if cached, err := rdb.Get(ctx, cacheKey).Bytes(); err == nil && len(cached) > 0 {
		var posts []models.PostResponse
		if json.Unmarshal(cached, &posts) == nil {
			overlayPendingLikes(ctx, rdb, userID, posts)
			if patched, err := json.Marshal(posts); err == nil {
				w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
				w.WriteHeader(http.StatusOK)
				w.Write(patched)
				return
			}
		}
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
		return
	}

	// Trending = posts that received engagement in the last 24 hours.
	// Uses denormalized counters + last_engaged_at (bumped by the flusher)
	// instead of expensive CTEs scanning engagement tables.
	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT p.id, p.user_id, u.username, u.name, u.avatar_url,
		        p.caption, p.post_type, p.original_post_id, p.visibility,
		        p.is_pinned, p.like_count, p.comment_count, p.repost_count,
		        p.view_count, p.bookmark_count,
		        my_like.user_id IS NOT NULL,
		        my_bm.user_id IS NOT NULL,
		        my_repost.id IS NOT NULL,
		        p.created_at
		 FROM posts p
		 JOIN users u ON u.id = p.user_id
		 LEFT JOIN post_likes my_like ON my_like.post_id = p.id AND my_like.user_id = $1
		 LEFT JOIN post_bookmarks my_bm ON my_bm.post_id = p.id AND my_bm.user_id = $1
		 LEFT JOIN posts my_repost ON my_repost.original_post_id = p.id AND my_repost.user_id = $1 AND my_repost.post_type = 'repost' AND my_repost.caption = ''
		 WHERE p.visibility = 'public'
		   AND p.last_engaged_at > NOW() - INTERVAL '24 hours'
		 ORDER BY (p.like_count + p.comment_count * 2 + p.repost_count * 3 + p.view_count / 10) DESC, p.created_at DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		log.Printf("[arena] trending feed query failed: %v", err)
		JSONError(w, "Failed to load trending", http.StatusInternalServerError)
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

	// Cache Postgres-truth (without pending overlay)
	if respBytes, err := json.Marshal(posts); err == nil {
		rdb.Set(ctx, cacheKey, respBytes, 2*time.Minute)
	}

	overlayPendingLikes(ctx, rdb, userID, posts)

	if respBytes, err := json.Marshal(posts); err == nil {
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(respBytes)
		return
	}

	JSONSuccess(w, posts)
}

// ---------------------------------------------------------------------------
// Report Post
// POST /api/v1/arena/posts/{postId}/report
// ---------------------------------------------------------------------------

func ReportPostHandler(w http.ResponseWriter, r *http.Request) {
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

	var req models.ReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Reason) == "" {
		JSONError(w, "Reason is required", http.StatusBadRequest)
		return
	}

	_, err = postgress.Exec(
		`INSERT INTO post_reports (reporter_id, post_id, reason) VALUES ($1, $2, $3)`,
		userID, postID, req.Reason,
	)
	if err != nil {
		log.Printf("[arena] report post failed user=%s post=%d: %v", userID, postID, err)
		JSONError(w, "Failed to report post", http.StatusInternalServerError)
		return
	}

	JSONMessage(w, "success", "Post reported")
}

// ---------------------------------------------------------------------------
// Admin: Delete Any Post
// DELETE /api/v1/admin/arena/posts/{postId}
// ---------------------------------------------------------------------------

func AdminDeletePostHandler(w http.ResponseWriter, r *http.Request) {
	postID, err := strconv.ParseInt(r.PathValue("postId"), 10, 64)
	if err != nil {
		JSONError(w, "Invalid post ID", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Fetch media keys for cleanup
	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT object_key FROM post_media WHERE post_id = $1`, postID,
	)
	if err == nil {
		defer rows.Close()
		var keys []string
		for rows.Next() {
			var key string
			if rows.Scan(&key) == nil {
				keys = append(keys, key)
			}
		}
		if len(keys) > 0 && Store != nil {
			chat.RunBackground(func() {
				bgCtx, bgCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer bgCancel()
				_ = Store.DeleteBatch(bgCtx, keys)
			})
		}
	}

	result, err := postgress.Exec(`DELETE FROM posts WHERE id = $1`, postID)
	if err != nil {
		JSONError(w, "Failed to delete post", http.StatusInternalServerError)
		return
	}
	if result == 0 {
		JSONError(w, "Post not found", http.StatusNotFound)
		return
	}

	// Invalidate feed caches
	chat.RunBackground(func() { InvalidateFeedCaches() })

	JSONMessage(w, "success", "Post deleted by admin")
}

// ---------------------------------------------------------------------------
// Admin: Get Post Reports
// GET /api/v1/admin/arena/reports
// ---------------------------------------------------------------------------

func AdminGetPostReportsHandler(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r)

	ctx, cancel := pgCtx(r)
	defer cancel()

	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT pr.id, pr.reporter_id, pr.post_id, pr.reason, pr.created_at,
		        p.caption, p.user_id
		 FROM post_reports pr
		 JOIN posts p ON p.id = pr.post_id
		 ORDER BY pr.created_at DESC
		 LIMIT $1 OFFSET $2`, limit, offset,
	)
	if err != nil {
		JSONError(w, "Failed to load reports", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type report struct {
		ID         int64     `json:"id"`
		ReporterID string    `json:"reporterId"`
		PostID     int64     `json:"postId"`
		Reason     string    `json:"reason"`
		Caption    string    `json:"caption"`
		PostUserID string    `json:"postUserId"`
		CreatedAt  time.Time `json:"createdAt"`
	}

	reports := make([]report, 0)
	for rows.Next() {
		var rpt report
		if rows.Scan(&rpt.ID, &rpt.ReporterID, &rpt.PostID, &rpt.Reason, &rpt.CreatedAt, &rpt.Caption, &rpt.PostUserID) == nil {
			reports = append(reports, rpt)
		}
	}
	JSONSuccess(w, reports)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func scanPost(rows *sql.Rows) (models.PostResponse, error) {
	var p models.PostResponse
	err := rows.Scan(
		&p.ID, &p.UserID, &p.Username, &p.DisplayName, &p.AvatarURL,
		&p.Caption, &p.PostType, &p.OriginalPostID, &p.Visibility,
		&p.IsPinned, &p.LikeCount, &p.CommentCount, &p.RepostCount,
		&p.ViewCount, &p.BookmarkCount,
		&p.HasLiked, &p.HasBookmarked, &p.HasReposted, &p.CreatedAt,
	)
	return p, err
}

func attachMedia(ctx context.Context, posts []models.PostResponse, store storage.StorageService) error {
	if len(posts) == 0 {
		return nil
	}

	// Collect all post IDs
	ids := make([]int64, len(posts))
	idMap := make(map[int64]int, len(posts)) // postID -> index in posts slice
	for i, p := range posts {
		ids[i] = p.ID
		idMap[p.ID] = i
	}

	// Build query with IN clause
	params := make([]any, len(ids))
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		params[i] = id
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}

	query := fmt.Sprintf(
		`SELECT id, post_id, media_type, object_key, width, height, duration_ms, preview_hash, sort_order
		 FROM post_media WHERE post_id IN (%s) ORDER BY post_id, sort_order`,
		strings.Join(placeholders, ","),
	)

	rows, err := postgress.GetRawDB().QueryContext(ctx, query, params...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var m models.PostMediaResponse
		var postID int64
		var objectKey string
		var durationMs sql.NullInt32
		var previewHash sql.NullString

		if err := rows.Scan(&m.ID, &postID, &m.MediaType, &objectKey, &m.Width, &m.Height, &durationMs, &previewHash, &m.SortOrder); err != nil {
			continue
		}

		if durationMs.Valid {
			m.DurationMs = int(durationMs.Int32)
		}
		if previewHash.Valid {
			m.PreviewHash = previewHash.String
		}

		// Build media URL: use public URL if configured, otherwise presigned GET
		if config.StoragePublicURL != "" {
			m.URL = strings.TrimRight(config.StoragePublicURL, "/") + "/" + objectKey
		} else if store != nil {
			url, err := store.PresignGet(ctx, &storage.PresignGetInput{
				Key:    objectKey,
				Expiry: getPresignGetExpiry(),
			})
			if err == nil {
				m.URL = url
			}
		}

		if idx, ok := idMap[postID]; ok {
			if posts[idx].Media == nil {
				posts[idx].Media = make([]models.PostMediaResponse, 0, 1)
			}
			posts[idx].Media = append(posts[idx].Media, m)
		}
	}

	return nil
}

// attachOriginalPosts embeds the original post data for any reposts in the slice.
func attachOriginalPosts(ctx context.Context, posts []models.PostResponse, viewerID string) {
	// Collect original post IDs that need fetching
	origIDs := make(map[int64][]int) // originalPostID -> indices in posts slice
	for i := range posts {
		if posts[i].PostType == "repost" && posts[i].OriginalPostID != nil {
			oid := *posts[i].OriginalPostID
			origIDs[oid] = append(origIDs[oid], i)
		}
	}
	if len(origIDs) == 0 {
		return
	}

	// Build IN query for original posts
	params := make([]any, 0, len(origIDs)+1)
	params = append(params, viewerID)
	placeholders := make([]string, 0, len(origIDs))
	idx := 2
	for oid := range origIDs {
		params = append(params, oid)
		placeholders = append(placeholders, fmt.Sprintf("$%d", idx))
		idx++
	}

	query := fmt.Sprintf(
		`SELECT p.id, p.user_id, u.username, u.name, u.avatar_url,
		        p.caption, p.post_type, p.original_post_id, p.visibility,
		        p.is_pinned, p.like_count, p.comment_count, p.repost_count,
		        p.view_count, p.bookmark_count,
		        my_like.user_id IS NOT NULL,
		        my_bm.user_id IS NOT NULL,
		        my_repost.id IS NOT NULL,
		        p.created_at
		 FROM posts p
		 JOIN users u ON u.id = p.user_id
		 LEFT JOIN post_likes my_like ON my_like.post_id = p.id AND my_like.user_id = $1
		 LEFT JOIN post_bookmarks my_bm ON my_bm.post_id = p.id AND my_bm.user_id = $1
		 LEFT JOIN posts my_repost ON my_repost.original_post_id = p.id AND my_repost.user_id = $1 AND my_repost.post_type = 'repost' AND my_repost.caption = ''
		 WHERE p.id IN (%s)`,
		strings.Join(placeholders, ","),
	)

	rows, err := postgress.GetRawDB().QueryContext(ctx, query, params...)
	if err != nil {
		log.Printf("[arena] fetch original posts failed: %v", err)
		return
	}
	defer rows.Close()

	origPosts := make([]models.PostResponse, 0, len(origIDs))
	origMap := make(map[int64]*models.PostResponse)
	for rows.Next() {
		op, err := scanPost(rows)
		if err != nil {
			continue
		}
		origPosts = append(origPosts, op)
		origMap[op.ID] = &origPosts[len(origPosts)-1]
	}

	// Attach media to original posts
	if err := attachMedia(ctx, origPosts, Store); err != nil {
		log.Printf("[arena] attach original post media failed: %v", err)
	}
	// Update pointers after attachMedia (which modifies the slice elements)
	for i := range origPosts {
		origMap[origPosts[i].ID] = &origPosts[i]
	}

	// Link originals to reposts
	for oid, indices := range origIDs {
		if op, ok := origMap[oid]; ok {
			for _, i := range indices {
				posts[i].OriginalPost = op
			}
		}
	}
}

func getPresignGetExpiry() time.Duration {
	mins := services.GetArenaLimits().PresignGetMins
	if mins <= 0 {
		mins = config.DefaultPresignGetMins
	}
	return time.Duration(mins) * time.Minute
}

func parseFeedPagination(r *http.Request) (limit, offset int) {
	limit = config.DefaultFeedLimit
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		if l > config.MaxFeedLimit {
			l = config.MaxFeedLimit
		}
		limit = l
	}
	offset = 0
	if o, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && o > 0 {
		offset = o
	}
	return
}

// parseFeedCursor extracts cursor (RFC3339Nano timestamp) and limit for cursor-based pagination.
func parseFeedCursor(r *http.Request) (cursor time.Time, limit int) {
	limit = config.DefaultFeedLimit
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		if l > config.MaxFeedLimit {
			l = config.MaxFeedLimit
		}
		limit = l
	}
	cursor = time.Now().UTC()
	if c := r.URL.Query().Get("cursor"); c != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, c); err == nil {
			cursor = parsed
		}
	}
	return
}

func getPostByID(ctx context.Context, postID int64, viewerID string) (models.PostResponse, error) {
	var p models.PostResponse
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT p.id, p.user_id, u.username, u.name, u.avatar_url,
		        p.caption, p.post_type, p.original_post_id, p.visibility,
		        p.is_pinned, p.like_count, p.comment_count, p.repost_count,
		        p.view_count, p.bookmark_count,
		        my_like.user_id IS NOT NULL,
		        my_bm.user_id IS NOT NULL,
		        my_repost.id IS NOT NULL,
		        p.created_at
		 FROM posts p
		 JOIN users u ON u.id = p.user_id
		 LEFT JOIN post_likes my_like ON my_like.post_id = p.id AND my_like.user_id = $1
		 LEFT JOIN post_bookmarks my_bm ON my_bm.post_id = p.id AND my_bm.user_id = $1
		 LEFT JOIN posts my_repost ON my_repost.original_post_id = p.id AND my_repost.user_id = $1 AND my_repost.post_type = 'repost' AND my_repost.caption = ''
		 WHERE p.id = $2`,
		viewerID, postID,
	).Scan(
		&p.ID, &p.UserID, &p.Username, &p.DisplayName, &p.AvatarURL,
		&p.Caption, &p.PostType, &p.OriginalPostID, &p.Visibility,
		&p.IsPinned, &p.LikeCount, &p.CommentCount, &p.RepostCount,
		&p.ViewCount, &p.BookmarkCount,
		&p.HasLiked, &p.HasBookmarked, &p.HasReposted, &p.CreatedAt,
	)
	if err != nil {
		return p, err
	}

	// Attach media
	posts := []models.PostResponse{p}
	if attachErr := attachMedia(ctx, posts, Store); attachErr == nil {
		p.Media = posts[0].Media
	}
	if p.Media == nil {
		p.Media = make([]models.PostMediaResponse, 0)
	}

	// Embed original post for reposts
	if p.PostType == "repost" && p.OriginalPostID != nil {
		origPosts := []models.PostResponse{p}
		attachOriginalPosts(ctx, origPosts, viewerID)
		p.OriginalPost = origPosts[0].OriginalPost
	}

	return p, nil
}

// InvalidateFeedCaches deletes cached feed entries (global + trending) from Redis.
// Called after post creation, deletion, or repost.
func InvalidateFeedCaches() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rdb := redis.GetRawClient()

	// Use SCAN to find and delete all feed cache keys.
	// Pattern matches: feed:global:*, feed:trending:*
	for _, pattern := range []string{"feed:global:*", "feed:trending:*"} {
		var cursor uint64
		for {
			keys, next, err := rdb.Scan(ctx, cursor, pattern, 100).Result()
			if err != nil {
				break
			}
			if len(keys) > 0 {
				rdb.Del(ctx, keys...)
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
}

// invalidatePostCache removes all cached single-post entries for a given post ID
// (across all users) so that stale hasReposted / counts are not served.
func invalidatePostCache(postID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rdb := redis.GetRawClient()
	pattern := fmt.Sprintf("%s%d:*", config.CachePost, postID)
	var cursor uint64
	for {
		keys, next, err := rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			break
		}
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
}

// overlayPendingLikes checks the Redis like/unlike buffers for the current
// user and patches hasLiked + likeCount on each post. This provides
// read-your-own-writes consistency while likes are still buffered (not yet
// flushed to Postgres). Uses a single pipelined round trip — O(1) per post.
func overlayPendingLikes(ctx context.Context, rdb *goredis.Client, userID string, posts []models.PostResponse) {
	if len(posts) == 0 {
		return
	}

	pipe := rdb.Pipeline()
	type check struct {
		likeCmd   *goredis.BoolCmd
		unlikeCmd *goredis.BoolCmd
		postID    int64
	}
	checks := make([]check, len(posts))
	for i, p := range posts {
		entry := userID + ":" + strconv.FormatInt(p.ID, 10)
		checks[i] = check{
			likeCmd:   pipe.SIsMember(ctx, config.ARENA_LIKES_BUFFER, entry),
			unlikeCmd: pipe.SIsMember(ctx, config.ARENA_UNLIKES_BUFFER, entry),
			postID:    p.ID,
		}
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return // on error, serve what we have
	}

	for i, c := range checks {
		pendingLike, _ := c.likeCmd.Result()
		pendingUnlike, _ := c.unlikeCmd.Result()
		if pendingLike && !posts[i].HasLiked {
			posts[i].HasLiked = true
			posts[i].LikeCount++
		} else if pendingUnlike && posts[i].HasLiked {
			posts[i].HasLiked = false
			if posts[i].LikeCount > 0 {
				posts[i].LikeCount--
			}
		}
	}
}
