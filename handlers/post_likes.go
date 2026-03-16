package handlers

import (
	"context"
	"net/http"
	"strconv"

	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/services"
)

// ---------------------------------------------------------------------------
// Like a Post
// POST /api/v1/arena/posts/{postId}/like
// ---------------------------------------------------------------------------

func LikePostHandler(w http.ResponseWriter, r *http.Request) {
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

	// Buffer in Redis — O(1) SADD, flushed to Postgres by arena flusher.
	// No direct Postgres write, no trigger storm on viral posts.
	services.BufferLike(r.Context(), userID, postID)

	// Notify post owner (skip self-like)
	chat.RunBackground(func() {
		bgCtx := context.Background()
		ownerID := services.GetPostOwnerCached(bgCtx, postID)
		if ownerID != "" && ownerID != userID {
			notifyUser(bgCtx, ownerID, map[string]interface{}{
				config.FieldType: config.MsgTypePostLiked,
				"postId":         postID,
				"likedBy":        userID,
			})
		}
	})

	JSONMessage(w, "success", "Post liked")
}

// ---------------------------------------------------------------------------
// Unlike a Post
// DELETE /api/v1/arena/posts/{postId}/like
// ---------------------------------------------------------------------------

func UnlikePostHandler(w http.ResponseWriter, r *http.Request) {
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

	// Buffer in Redis — O(1) SADD, flushed to Postgres by arena flusher.
	services.BufferUnlike(r.Context(), userID, postID)

	JSONMessage(w, "success", "Post unliked")
}

// ---------------------------------------------------------------------------
// Get Post Likers
// GET /api/v1/arena/posts/{postId}/likes
// ---------------------------------------------------------------------------

func GetPostLikersHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	_ = userID // authenticated but not filtered

	postID, err := strconv.ParseInt(r.PathValue("postId"), 10, 64)
	if err != nil {
		JSONError(w, "Invalid post ID", http.StatusBadRequest)
		return
	}

	limit, offset := parsePagination(r)

	ctx, cancel := pgCtx(r)
	defer cancel()

	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT u.id, u.username, u.name, u.avatar_url
		 FROM post_likes pl
		 JOIN users u ON u.id = pl.user_id
		 WHERE pl.post_id = $1
		 ORDER BY pl.created_at DESC
		 LIMIT $2 OFFSET $3`,
		postID, limit, offset,
	)
	if err != nil {
		JSONError(w, "Failed to load likers", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type liker struct {
		ID        string `json:"id"`
		Username  string `json:"username"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatarUrl"`
	}
	likers := make([]liker, 0)
	for rows.Next() {
		var l liker
		if rows.Scan(&l.ID, &l.Username, &l.Name, &l.AvatarURL) == nil {
			likers = append(likers, l)
		}
	}
	JSONSuccess(w, likers)
}
