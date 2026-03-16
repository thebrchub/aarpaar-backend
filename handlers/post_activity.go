package handlers

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/services"
)

// ---------------------------------------------------------------------------
// Post Activity (owner-only analytics — like Twitter's Post Activity)
// GET /api/v1/arena/posts/{postId}/activity
// ---------------------------------------------------------------------------

func GetPostActivityHandler(w http.ResponseWriter, r *http.Request) {
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

	var a struct {
		PostID        int64 `json:"postId"`
		Impressions   int   `json:"impressions"`
		Engagements   int   `json:"engagements"`
		DetailExpands int   `json:"detailExpands"`
		ProfileVisits int   `json:"profileVisits"`
		LikeCount     int   `json:"likeCount"`
		CommentCount  int   `json:"commentCount"`
		RepostCount   int   `json:"repostCount"`
		QuoteCount    int   `json:"quoteCount"`
		BookmarkCount int   `json:"bookmarkCount"`
	}

	// Use materialized repost_count and quote_count columns — no correlated subqueries.
	err = postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT p.id,
		        p.view_count,
		        p.like_count + p.comment_count + p.repost_count + p.bookmark_count,
		        p.detail_expand_count,
		        p.profile_click_count,
		        p.like_count,
		        p.comment_count,
		        p.repost_count - p.quote_count,
		        p.quote_count,
		        p.bookmark_count
		 FROM posts p
		 WHERE p.id = $1 AND p.user_id = $2`,
		postID, userID,
	).Scan(
		&a.PostID, &a.Impressions, &a.Engagements,
		&a.DetailExpands, &a.ProfileVisits,
		&a.LikeCount, &a.CommentCount, &a.RepostCount, &a.QuoteCount, &a.BookmarkCount,
	)
	if err != nil {
		JSONError(w, "Post not found or not yours", http.StatusNotFound)
		return
	}

	JSONSuccess(w, a)
}

// ---------------------------------------------------------------------------
// Get Reposts of a Post
// GET /api/v1/arena/posts/{postId}/reposts
//
// Returns two sections:
//   - repostCount: number of plain reposts (no caption)
//   - quotes: list of quote reposts (with caption, userId, username)
// ---------------------------------------------------------------------------

func GetRepostsHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	_ = userID

	postID, err := strconv.ParseInt(r.PathValue("postId"), 10, 64)
	if err != nil {
		JSONError(w, "Invalid post ID", http.StatusBadRequest)
		return
	}

	limit, offset := parsePagination(r)

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Use materialized quote_count: plain reposts = repost_count - quote_count
	var plainCount int
	var quoteTotal int
	_ = postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT repost_count - quote_count, quote_count FROM posts WHERE id = $1`,
		postID,
	).Scan(&plainCount, &quoteTotal)

	// Fetch quote reposts (with caption) — paginated
	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT p.id, p.user_id, u.username, u.name, u.avatar_url, p.caption, p.created_at
		 FROM posts p
		 JOIN users u ON u.id = p.user_id
		 WHERE p.original_post_id = $1 AND p.post_type = 'repost' AND p.caption != ''
		 ORDER BY p.created_at DESC
		 LIMIT $2 OFFSET $3`,
		postID, limit, offset,
	)
	if err != nil {
		log.Printf("[arena] get reposts query failed: %v", err)
		JSONError(w, "Failed to load reposts", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type quoteRepost struct {
		PostID      int64     `json:"postId"`
		UserID      string    `json:"userId"`
		Username    string    `json:"username"`
		DisplayName string    `json:"displayName"`
		AvatarURL   string    `json:"avatarUrl"`
		Caption     string    `json:"caption"`
		CreatedAt   time.Time `json:"createdAt"`
	}

	quotes := make([]quoteRepost, 0)
	for rows.Next() {
		var q quoteRepost
		if rows.Scan(&q.PostID, &q.UserID, &q.Username, &q.DisplayName, &q.AvatarURL, &q.Caption, &q.CreatedAt) == nil {
			quotes = append(quotes, q)
		}
	}

	JSONSuccess(w, map[string]any{
		"repostCount": plainCount,
		"quoteCount":  quoteTotal,
		"quotes":      quotes,
	})
}

// ---------------------------------------------------------------------------
// Record Profile Click from a Post
// POST /api/v1/arena/posts/{postId}/profile-click
//
// Called when a user taps the author's avatar/username on a post.
// ---------------------------------------------------------------------------

func RecordProfileClickHandler(w http.ResponseWriter, r *http.Request) {
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

	// Buffer in Redis — flushed to Postgres by arena flusher
	services.BufferProfileClick(r.Context(), userID, postID)

	w.WriteHeader(http.StatusNoContent)
}
