package handlers

import (
	"net/http"
	"strconv"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/shivanand-burli/go-starter-kit/middleware"
	"github.com/shivanand-burli/go-starter-kit/helper"
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
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var req struct {
		PostIDs []int64 `json:"postIds"`
	}
	if err := helper.ReadJSON(r, &req); err != nil || len(req.PostIDs) == 0 {
		helper.Error(w, http.StatusBadRequest, "postIds array is required")
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
		helper.Error(w, http.StatusInternalServerError, "Failed to record views")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
