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
	"github.com/shivanand-burli/go-starter-kit/postgress"
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

	// Validate caption length
	if len(req.Caption) > limits.MaxCaptionLength {
		JSONError(w, fmt.Sprintf("Caption too long (max %d chars)", limits.MaxCaptionLength), http.StatusBadRequest)
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

	// Insert media
	for _, m := range req.Media {
		_, err := postgress.Exec(
			`INSERT INTO post_media (post_id, media_type, object_key, width, height, duration_ms, preview_hash, sort_order)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			postID, m.MediaType, m.ObjectKey, m.Width, m.Height, m.DurationMs, m.PreviewHash, m.SortOrder,
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

	post, err := getPostByID(ctx, postID, userID)
	if err != nil {
		JSONError(w, "Post not found", http.StatusNotFound)
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
		var keys []string
		for rows.Next() {
			var key string
			if rows.Scan(&key) == nil {
				keys = append(keys, key)
			}
		}
		rows.Close()

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
	if len(req.Caption) > limits.MaxCaptionLength {
		JSONError(w, fmt.Sprintf("Caption too long (max %d chars)", limits.MaxCaptionLength), http.StatusBadRequest)
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

	// Prevent duplicate repost
	var alreadyReposted bool
	_ = postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM posts WHERE user_id = $1 AND original_post_id = $2 AND post_type = $3)`,
		userID, postID, config.PostTypeRepost,
	).Scan(&alreadyReposted)
	if alreadyReposted {
		JSONError(w, "Already reposted", http.StatusConflict)
		return
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

	// Increment repost count on original
	_, _ = postgress.Exec(
		`UPDATE posts SET repost_count = repost_count + 1 WHERE id = $1`, postID,
	)

	post, err := getPostByID(ctx, newPostID, userID)
	if err != nil {
		JSONError(w, "Repost created but failed to fetch", http.StatusInternalServerError)
		return
	}
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

	limit, offset := parseFeedPagination(r)

	ctx, cancel := pgCtx(r)
	defer cancel()

	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT p.id, p.user_id, u.username, u.name, u.avatar_url,
		        p.caption, p.post_type, p.original_post_id, p.visibility,
		        p.is_pinned, p.like_count, p.comment_count, p.repost_count,
		        EXISTS(SELECT 1 FROM post_likes pl WHERE pl.post_id = p.id AND pl.user_id = $1),
		        p.created_at
		 FROM posts p
		 JOIN users u ON u.id = p.user_id
		 WHERE p.visibility = 'public'
		 ORDER BY p.created_at DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset,
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

	limit, offset := parseFeedPagination(r)

	ctx, cancel := pgCtx(r)
	defer cancel()

	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT p.id, p.user_id, u.username, u.name, u.avatar_url,
		        p.caption, p.post_type, p.original_post_id, p.visibility,
		        p.is_pinned, p.like_count, p.comment_count, p.repost_count,
		        EXISTS(SELECT 1 FROM post_likes pl WHERE pl.post_id = p.id AND pl.user_id = $1),
		        p.created_at
		 FROM posts p
		 JOIN users u ON u.id = p.user_id
		 WHERE p.user_id IN (
		     SELECT CASE WHEN user_id_1 = $1 THEN user_id_2 ELSE user_id_1 END
		     FROM friendships WHERE (user_id_1 = $1 OR user_id_2 = $1)
		 )
		 ORDER BY p.created_at DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset,
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

	query := fmt.Sprintf(`
		SELECT p.id, p.user_id, u.username, u.name, u.avatar_url,
		       p.caption, p.post_type, p.original_post_id, p.visibility,
		       p.is_pinned, p.like_count, p.comment_count, p.repost_count,
		       EXISTS(SELECT 1 FROM post_likes pl WHERE pl.post_id = p.id AND pl.user_id = $1),
		       p.created_at
		FROM posts p
		JOIN users u ON u.id = p.user_id
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

	// Trending = most engagement in last 24h
	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT p.id, p.user_id, u.username, u.name, u.avatar_url,
		        p.caption, p.post_type, p.original_post_id, p.visibility,
		        p.is_pinned, p.like_count, p.comment_count, p.repost_count,
		        EXISTS(SELECT 1 FROM post_likes pl WHERE pl.post_id = p.id AND pl.user_id = $1),
		        p.created_at
		 FROM posts p
		 JOIN users u ON u.id = p.user_id
		 WHERE p.visibility = 'public'
		   AND p.created_at > NOW() - INTERVAL '24 hours'
		 ORDER BY (p.like_count + p.comment_count * 2 + p.repost_count * 3) DESC, p.created_at DESC
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
		var keys []string
		for rows.Next() {
			var key string
			if rows.Scan(&key) == nil {
				keys = append(keys, key)
			}
		}
		rows.Close()
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
		&p.HasLiked, &p.CreatedAt,
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

		// Generate presigned GET URL
		if store != nil {
			url, err := store.PresignGet(ctx, &storage.PresignGetInput{
				Key:    objectKey,
				Expiry: config.PresignGetExpiry,
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

func getPostByID(ctx context.Context, postID int64, viewerID string) (models.PostResponse, error) {
	var p models.PostResponse
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT p.id, p.user_id, u.username, u.name, u.avatar_url,
		        p.caption, p.post_type, p.original_post_id, p.visibility,
		        p.is_pinned, p.like_count, p.comment_count, p.repost_count,
		        EXISTS(SELECT 1 FROM post_likes pl WHERE pl.post_id = p.id AND pl.user_id = $1),
		        p.created_at
		 FROM posts p
		 JOIN users u ON u.id = p.user_id
		 WHERE p.id = $2`,
		viewerID, postID,
	).Scan(
		&p.ID, &p.UserID, &p.Username, &p.DisplayName, &p.AvatarURL,
		&p.Caption, &p.PostType, &p.OriginalPostID, &p.Visibility,
		&p.IsPinned, &p.LikeCount, &p.CommentCount, &p.RepostCount,
		&p.HasLiked, &p.CreatedAt,
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
	return p, nil
}
