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
	"github.com/jackc/pgx/v5"
	goredis "github.com/redis/go-redis/v9"
	"github.com/shivanand-burli/go-starter-kit/helper"
	"github.com/shivanand-burli/go-starter-kit/middleware"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/models"
	"github.com/thebrchub/aarpaar/services"
)

// ---------------------------------------------------------------------------
// Create Comment
// POST /api/v1/arena/posts/{postId}/comments
// ---------------------------------------------------------------------------

func CreateCommentHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	postID, err := strconv.ParseInt(r.PathValue("postId"), 10, 64)
	if err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid post ID")
		return
	}

	// Plain reposts (no caption) redirect comments to the original post (Twitter-like).
	postID = services.ResolveOriginalPostID(r.Context(), postID)

	var req models.CreateCommentRequest
	if err := helper.ReadJSON(r, &req); err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Must have body or gif
	if strings.TrimSpace(req.Body) == "" && req.GifURL == "" {
		helper.Error(w, http.StatusBadRequest, "Comment must have a body or GIF")
		return
	}

	limits := services.GetArenaLimits()
	maxComment := limits.FreeCommentLength
	if IsUserVIP(r.Context(), userID) {
		maxComment = limits.MaxCommentLength
	}
	if len(req.Body) > maxComment {
		helper.Error(w, http.StatusBadRequest, fmt.Sprintf("Comment too long (max %d chars)", maxComment))
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Verify post exists
	var postExists bool
	_ = postgress.GetPool().QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM posts WHERE id = $1)`, postID,
	).Scan(&postExists)
	if !postExists {
		helper.Error(w, http.StatusNotFound, "Post not found")
		return
	}

	var depth int
	var parentPath string

	if req.ParentID != nil {
		// It's a reply — get parent's path and depth
		var pPath string
		var pDepth int
		err := postgress.GetPool().QueryRow(ctx,
			`SELECT path::text, depth FROM post_comments WHERE id = $1 AND post_id = $2`,
			*req.ParentID, postID,
		).Scan(&pPath, &pDepth)
		if err != nil {
			helper.Error(w, http.StatusNotFound, "Parent comment not found")
			return
		}
		if pDepth >= config.MaxCommentDepth {
			helper.Error(w, http.StatusBadRequest, fmt.Sprintf("Max reply depth reached (%d)", config.MaxCommentDepth))
			return
		}
		depth = pDepth + 1
		parentPath = pPath
	}

	// Insert comment, then set its ltree path
	var commentID int64
	err = postgress.GetPool().QueryRow(ctx,
		`INSERT INTO post_comments (post_id, user_id, body, path, depth, gif_url, gif_width, gif_height)
		 VALUES ($1, $2, $3, '', $4, $5, $6, $7) RETURNING id`,
		postID, userID, req.Body, depth, nilIfEmpty(req.GifURL), nilIfZero(req.GifWidth), nilIfZero(req.GifHeight),
	).Scan(&commentID)
	if err != nil {
		log.Printf("[arena] create comment failed user=%s post=%d: %v", userID, postID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to create comment")
		return
	}

	// Build ltree path: parent.commentId or just commentId for top-level
	var path string
	if parentPath != "" {
		path = fmt.Sprintf("%s.c%d", parentPath, commentID)
	} else {
		path = fmt.Sprintf("c%d", commentID)
	}

	_, err = postgress.Exec(ctx,
		`UPDATE post_comments SET path = $1::ltree WHERE id = $2`, path, commentID,
	)
	if err != nil {
		log.Printf("[arena] set comment path failed comment=%d: %v", commentID, err)
	}

	// Invalidate comment cache + single-post cache + commenter's feed caches
	// so the commenter sees up-to-date commentCount immediately.
	rdb := redis.GetRawClient()
	chat.Pool.Submit(func() {
		invalidateCommentCache(rdb, postID)
		invalidatePostAndFeedCaches(rdb, postID, userID)
	})

	// Notify post owner and parent comment author
	chat.Pool.Submit(func() {
		bgCtx := context.Background()
		postOwnerID := services.GetPostOwnerCached(bgCtx, postID)
		if postOwnerID != "" && postOwnerID != userID && services.ShouldNotify(bgCtx, postOwnerID, services.NotifPrefComments) {
			notifyUser(bgCtx, postOwnerID, map[string]interface{}{
				config.FieldType: config.MsgTypePostCommented,
				"postId":         postID,
				"commentId":      commentID,
			})
		}
		if req.ParentID != nil {
			var parentAuthorID string
			_ = postgress.GetPool().QueryRow(bgCtx,
				`SELECT user_id FROM post_comments WHERE id = $1`, *req.ParentID,
			).Scan(&parentAuthorID)
			if parentAuthorID != "" && parentAuthorID != userID && parentAuthorID != postOwnerID && services.ShouldNotify(bgCtx, parentAuthorID, services.NotifPrefComments) {
				notifyUser(bgCtx, parentAuthorID, map[string]interface{}{
					config.FieldType: config.MsgTypeCommentReplied,
					"postId":         postID,
					"commentId":      commentID,
					"parentId":       *req.ParentID,
				})
			}
		}
	})

	helper.JSON(w, http.StatusOK, models.CommentResponse{
		ID:        commentID,
		PostID:    postID,
		UserID:    userID,
		Body:      req.Body,
		Depth:     depth,
		LikeCount: 0,
		GifURL:    req.GifURL,
		GifWidth:  req.GifWidth,
		GifHeight: req.GifHeight,
		ParentID:  req.ParentID,
		CreatedAt: time.Now(),
	})
}

// ---------------------------------------------------------------------------
// Get Comments for a Post
// GET /api/v1/arena/posts/{postId}/comments
// ---------------------------------------------------------------------------

func GetCommentsHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	postID, err := strconv.ParseInt(r.PathValue("postId"), 10, 64)
	if err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid post ID")
		return
	}

	// Plain reposts redirect comments to the original post (same as create).
	postID = services.ResolveOriginalPostID(r.Context(), postID)

	limit, offset := parsePagination(r)

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Cache comments per post+parent+page for 30s
	parentIDParam := r.URL.Query().Get("parentId")
	cacheKey := fmt.Sprintf("%s%d:%s:%s:%d:%d", config.CacheComments, postID, userID, parentIDParam, limit, offset)
	rdb := redis.GetRawClient()
	if cached, err := rdb.Get(ctx, cacheKey).Bytes(); err == nil && len(cached) > 0 {
		var comments []models.CommentResponse
		if json.Unmarshal(cached, &comments) == nil {
			overlayPendingCommentLikes(ctx, rdb, userID, comments)
			if patched, err := json.Marshal(comments); err == nil {
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

	// If parentId is set, fetch replies to that comment; otherwise top-level only

	var rows pgx.Rows
	if parentIDParam != "" {
		parentID, pErr := strconv.ParseInt(parentIDParam, 10, 64)
		if pErr != nil {
			helper.Error(w, http.StatusBadRequest, "Invalid parentId")
			return
		}
		// Fetch direct replies to a specific comment
		rows, err = postgress.GetPool().Query(ctx,
			`SELECT c.id, c.post_id, c.user_id, u.username, u.avatar_url,
			        c.body, c.depth, c.like_count,
			        my_cl.user_id IS NOT NULL,
			        c.gif_url, c.gif_width, c.gif_height,
			        c.created_at, c.path::text,
			        c.reply_count
			 FROM post_comments c
			 JOIN users u ON u.id = c.user_id
			 LEFT JOIN comment_likes my_cl ON my_cl.comment_id = c.id AND my_cl.user_id = $1
			 WHERE c.post_id = $2
			   AND c.path <@ (SELECT path FROM post_comments WHERE id = $3)::ltree
			   AND c.depth = (SELECT depth FROM post_comments WHERE id = $3) + 1
			 ORDER BY c.created_at
			 LIMIT $4 OFFSET $5`,
			userID, postID, parentID, limit, offset,
		)
	} else {
		// Fetch top-level comments with reply counts
		rows, err = postgress.GetPool().Query(ctx,
			`SELECT c.id, c.post_id, c.user_id, u.username, u.avatar_url,
			        c.body, c.depth, c.like_count,
			        my_cl.user_id IS NOT NULL,
			        c.gif_url, c.gif_width, c.gif_height,
			        c.created_at, c.path::text,
			        c.reply_count
			 FROM post_comments c
			 JOIN users u ON u.id = c.user_id
			 LEFT JOIN comment_likes my_cl ON my_cl.comment_id = c.id AND my_cl.user_id = $1
			 WHERE c.post_id = $2 AND c.depth = 0
			 ORDER BY c.like_count DESC, c.created_at
			 LIMIT $3 OFFSET $4`,
			userID, postID, limit, offset,
		)
	}
	if err != nil {
		log.Printf("[arena] get comments failed post=%d: %v", postID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to load comments")
		return
	}
	defer rows.Close()

	comments := make([]models.CommentResponse, 0)
	for rows.Next() {
		var c models.CommentResponse
		var gifURL sql.NullString
		var gifWidth, gifHeight sql.NullInt16
		var pathStr string

		if err := rows.Scan(&c.ID, &c.PostID, &c.UserID, &c.Username, &c.AvatarURL,
			&c.Body, &c.Depth, &c.LikeCount, &c.HasLiked,
			&gifURL, &gifWidth, &gifHeight,
			&c.CreatedAt, &pathStr, &c.ReplyCount); err != nil {
			continue
		}

		if gifURL.Valid {
			c.GifURL = gifURL.String
		}
		if gifWidth.Valid {
			c.GifWidth = int(gifWidth.Int16)
		}
		if gifHeight.Valid {
			c.GifHeight = int(gifHeight.Int16)
		}

		// Extract parentID from ltree path
		if c.Depth > 0 {
			c.ParentID = extractParentID(pathStr)
		}

		comments = append(comments, c)
	}

	if respBytes, err := json.Marshal(comments); err == nil {
		rdb.Set(ctx, cacheKey, respBytes, config.CacheTTLMedium)
	}

	overlayPendingCommentLikes(ctx, rdb, userID, comments)

	if respBytes, err := json.Marshal(comments); err == nil {
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(respBytes)
		return
	}

	helper.JSON(w, http.StatusOK, comments)
}

// ---------------------------------------------------------------------------
// Delete Comment
// DELETE /api/v1/arena/posts/{postId}/comments/{commentId}
// ---------------------------------------------------------------------------

func DeleteCommentHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	commentID, err := strconv.ParseInt(r.PathValue("commentId"), 10, 64)
	if err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid comment ID")
		return
	}

	// Fetch the comment's ltree path and post_id (also verifies ownership)
	var commentPath string
	var commentPostID int64
	err = postgress.GetPool().QueryRow(ctx,
		`SELECT path::text, post_id FROM post_comments WHERE id = $1 AND user_id = $2`,
		commentID, userID,
	).Scan(&commentPath, &commentPostID)
	if err != nil || commentPath == "" {
		helper.Error(w, http.StatusNotFound, "Comment not found or not yours")
		return
	}

	// Delete the comment and all its descendants in one shot
	_, err = postgress.Exec(ctx,
		`DELETE FROM post_comments WHERE path <@ $1::ltree`,
		commentPath,
	)
	if err != nil {
		log.Printf("[arena] delete comment tree failed comment=%d user=%s: %v", commentID, userID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to delete comment")
		return
	}

	// Invalidate comment cache + single-post cache + user's feed caches
	rdb := redis.GetRawClient()
	chat.Pool.Submit(func() {
		invalidateCommentCache(rdb, commentPostID)
		invalidatePostAndFeedCaches(rdb, commentPostID, userID)
	})

	helper.JSON(w, http.StatusOK, map[string]string{"status": "success", "message": "Comment deleted"})
}

// ---------------------------------------------------------------------------
// Like / Unlike Comment
// POST   /api/v1/arena/comments/{commentId}/like
// DELETE /api/v1/arena/comments/{commentId}/like
// ---------------------------------------------------------------------------

func LikeCommentHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	commentID, err := strconv.ParseInt(r.PathValue("commentId"), 10, 64)
	if err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid comment ID")
		return
	}

	services.BufferCommentLike(r.Context(), userID, commentID)

	// Write per-user comment overlay set (survives flusher drain) + dirty flag.
	rdb := redis.GetRawClient()
	cidStr := strconv.FormatInt(commentID, 10)
	pipe := rdb.Pipeline()
	pipe.SAdd(r.Context(), config.COMMENT_PENDING_LIKES+userID, cidStr)
	pipe.SRem(r.Context(), config.COMMENT_PENDING_UNLIKES+userID, cidStr)
	pipe.Expire(r.Context(), config.COMMENT_PENDING_LIKES+userID, config.DirtyFlagTTL)
	pipe.Expire(r.Context(), config.COMMENT_PENDING_UNLIKES+userID, config.DirtyFlagTTL)
	pipe.Set(r.Context(), config.COMMENT_LIKES_DIRTY_PREFIX+userID, 1, config.DirtyFlagTTL)
	pipe.Exec(r.Context())

	helper.JSON(w, http.StatusOK, map[string]string{"status": "success", "message": "Comment liked"})
}

func UnlikeCommentHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	commentID, err := strconv.ParseInt(r.PathValue("commentId"), 10, 64)
	if err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid comment ID")
		return
	}

	services.BufferCommentUnlike(r.Context(), userID, commentID)

	// Write per-user comment overlay set (survives flusher drain) + dirty flag.
	rdb := redis.GetRawClient()
	cidStr := strconv.FormatInt(commentID, 10)
	pipe := rdb.Pipeline()
	pipe.SAdd(r.Context(), config.COMMENT_PENDING_UNLIKES+userID, cidStr)
	pipe.SRem(r.Context(), config.COMMENT_PENDING_LIKES+userID, cidStr)
	pipe.Expire(r.Context(), config.COMMENT_PENDING_LIKES+userID, config.DirtyFlagTTL)
	pipe.Expire(r.Context(), config.COMMENT_PENDING_UNLIKES+userID, config.DirtyFlagTTL)
	pipe.Set(r.Context(), config.COMMENT_LIKES_DIRTY_PREFIX+userID, 1, config.DirtyFlagTTL)
	pipe.Exec(r.Context())

	helper.JSON(w, http.StatusOK, map[string]string{"status": "success", "message": "Comment unliked"})
}

// ---------------------------------------------------------------------------
// Report Comment
// POST /api/v1/arena/comments/{commentId}/report
// ---------------------------------------------------------------------------

func ReportCommentHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	commentID, err := strconv.ParseInt(r.PathValue("commentId"), 10, 64)
	if err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid comment ID")
		return
	}

	var req models.ReportRequest
	if err := helper.ReadJSON(r, &req); err != nil || strings.TrimSpace(req.Reason) == "" {
		helper.Error(w, http.StatusBadRequest, "Reason is required")
		return
	}

	_, err = postgress.Exec(ctx,
		`INSERT INTO comment_reports (reporter_id, comment_id, reason) VALUES ($1, $2, $3)`,
		userID, commentID, req.Reason,
	)
	if err != nil {
		if strings.Contains(err.Error(), "violates foreign key") {
			helper.Error(w, http.StatusNotFound, "Comment not found")
		} else {
			log.Printf("[arena] report comment failed user=%s comment=%d: %v", userID, commentID, err)
			helper.Error(w, http.StatusInternalServerError, "Failed to report comment")
		}
		return
	}

	helper.JSON(w, http.StatusOK, map[string]string{"status": "success", "message": "Comment reported"})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// extractParentID parses the parent comment ID from an ltree path.
// e.g., "c5.c12.c18" → parentID = 12 (the second-to-last segment)
func extractParentID(path string) *int64 {
	parts := strings.Split(path, ".")
	if len(parts) < 2 {
		return nil
	}
	parentLabel := parts[len(parts)-2]
	// Remove "c" prefix
	if len(parentLabel) > 1 && parentLabel[0] == 'c' {
		if id, err := strconv.ParseInt(parentLabel[1:], 10, 64); err == nil {
			return &id
		}
	}
	return nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nilIfZero(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

// invalidateCommentCache deletes all cached comment pages for a post.
// Uses SCAN — write-path only (comment create/delete), acceptable at scale.
func invalidateCommentCache(rdb goredis.UniversalClient, postID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Cache keys now include userId — use SCAN to match all users.
	// Write-path only (comment create/delete), acceptable at scale.
	pattern := fmt.Sprintf("%s%d:*", config.CacheComments, postID)
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

// invalidatePostAndFeedCaches busts the single-post cache for a post (all users)
// and the acting user's own feed caches so they see the updated commentCount
// immediately. Write-path only (comment create/delete) — acceptable at scale.
func invalidatePostAndFeedCaches(rdb goredis.UniversalClient, postID int64, userID string) {
	invalidatePostCache(postID)
	invalidateUserFeedCaches(userID)
}

// overlayPendingCommentLikes checks the Redis comment like/unlike buffers
// and patches hasLiked + likeCount. Same pattern as overlayPendingLikes
// for posts. Gated by dirty flag — only runs for users who just liked/unliked.
func overlayPendingCommentLikes(ctx context.Context, rdb goredis.UniversalClient, userID string, comments []models.CommentResponse) {
	if len(comments) == 0 {
		return
	}
	if rdb.Exists(ctx, config.COMMENT_LIKES_DIRTY_PREFIX+userID).Val() == 0 {
		return
	}

	pipe := rdb.Pipeline()
	type check struct {
		likeCmd   *goredis.BoolCmd
		unlikeCmd *goredis.BoolCmd
	}
	checks := make([]check, len(comments))
	for i, c := range comments {
		idStr := strconv.FormatInt(c.ID, 10)
		checks[i] = check{
			likeCmd:   pipe.SIsMember(ctx, config.COMMENT_PENDING_LIKES+userID, idStr),
			unlikeCmd: pipe.SIsMember(ctx, config.COMMENT_PENDING_UNLIKES+userID, idStr),
		}
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return
	}

	for i, c := range checks {
		pendingLike, _ := c.likeCmd.Result()
		pendingUnlike, _ := c.unlikeCmd.Result()
		if pendingLike && !comments[i].HasLiked {
			comments[i].HasLiked = true
			comments[i].LikeCount++
		} else if pendingUnlike && comments[i].HasLiked {
			comments[i].HasLiked = false
			if comments[i].LikeCount > 0 {
				comments[i].LikeCount--
			}
		}
	}
}
