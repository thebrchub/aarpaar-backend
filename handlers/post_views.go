package handlers

import (
	"net/http"
	"strconv"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// Record Post Views (batch)
// POST /api/v1/arena/posts/views
//
// Body: {"postIds": [1, 2, 3, ...]}
// Buffers views in Redis (O(1) per post). The arena flusher periodically
// batch-inserts them into Postgres and increments view_count.
// ---------------------------------------------------------------------------

const maxViewBatchSize = 50

func RecordViewsHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		PostIDs []int64 `json:"postIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.PostIDs) == 0 {
		JSONError(w, "postIds array is required", http.StatusBadRequest)
		return
	}

	if len(req.PostIDs) > maxViewBatchSize {
		req.PostIDs = req.PostIDs[:maxViewBatchSize]
	}

	// Pipeline all SADDs into a single Redis round-trip instead of 50 separate calls
	ctx := r.Context()
	pipe := redis.GetRawClient().Pipeline()
	for _, pid := range req.PostIDs {
		pipe.SAdd(ctx, config.ARENA_VIEWS_BUFFER, userID+":"+strconv.FormatInt(pid, 10))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		JSONError(w, "Failed to record views", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
