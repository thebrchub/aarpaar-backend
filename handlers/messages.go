package handlers

import (
	"context"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/shivanand-burli/go-starter-kit/helper"
	"github.com/shivanand-burli/go-starter-kit/middleware"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/config"
)

// GetRoomMessagesHandler returns paginated messages for a room.
func GetRoomMessagesHandler(w http.ResponseWriter, r *http.Request) {
	// 1. Authenticate — make sure the caller is logged in
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	// 2. Extract room ID from the URL path using Go 1.22+ pattern matching
	//    Route is registered as: GET /api/v1/rooms/{roomId}/messages
	roomID := r.PathValue("roomId")
	if roomID == "" {
		helper.Error(w, http.StatusBadRequest, "Missing room ID")
		return
	}

	// 3. Verify the caller is an active member of this room (cached 2min)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()

	memberKey := config.CacheRoomMember + roomID + ":" + userID
	rdb := redis.GetRawClient()
	if cached, err := rdb.Get(ctx, memberKey).Result(); err == nil {
		if cached != "1" {
			helper.Error(w, http.StatusForbidden, "Not a member of this room")
			return
		}
	} else {
		var isMember bool
		err := postgress.GetPool().QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM room_members WHERE room_id = $1 AND user_id = $2 AND status = 'active')`,
			roomID, userID,
		).Scan(&isMember)
		if err != nil {
			log.Printf("[messages] membership check failed room=%s user=%s: %v", roomID, userID, err)
			helper.Error(w, http.StatusInternalServerError, "Database error")
			return
		}
		if isMember {
			rdb.Set(ctx, memberKey, "1", 2*time.Minute)
		} else {
			rdb.Set(ctx, memberKey, "0", 30*time.Second)
			helper.Error(w, http.StatusForbidden, "Not a member of this room")
			return
		}
	}

	// 4. Parse cursor: message ID to paginate from (default = newest)
	cursorStr := r.URL.Query().Get("cursor")
	cursor := int64(math.MaxInt64)
	if cursorStr != "" {
		if parsed, err := strconv.ParseInt(cursorStr, 10, 64); err == nil {
			cursor = parsed
		}
	}

	// 5. Parse limit: how many messages to return
	limitStr := r.URL.Query().Get("limit")
	limit := config.DefaultMessageLimit
	if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 && parsed <= config.MaxMessageLimit {
		limit = parsed
	}

	// 6. Zero-allocation SQL: Postgres builds the JSON array for us.
	//    Receipt status is computed via a CTE that runs once, instead of
	//    correlated subqueries per row (100 subqueries → 1).
	query := `
		WITH receipt_times AS (
			SELECT MIN(rm2.last_read_at) AS min_read,
			       MIN(rm2.last_delivered_at) AS min_delivered
			FROM room_members rm2
			WHERE rm2.room_id = $1 AND rm2.user_id != $4
		)
		SELECT COALESCE(json_agg(t), '[]')::text
		FROM (
			SELECT m.id, m.sender_id, m.content, m.created_at,
				COALESCE(u.name, '') AS sender_name,
				COALESCE(u.avatar_url, '') AS sender_avatar,
				CASE
					WHEN m.sender_id = $4 THEN
						CASE
							WHEN m.created_at <= rt.min_read THEN 'read'
							WHEN m.created_at <= rt.min_delivered THEN 'delivered'
							ELSE 'sent'
						END
					ELSE NULL
				END AS status
			FROM messages m
			LEFT JOIN users u ON u.id = m.sender_id
			CROSS JOIN receipt_times rt
			WHERE m.room_id = $1 AND m.id < $2
			ORDER BY m.id DESC
			LIMIT $3
		) t;
	`

	// 7. Execute and pipe the raw JSON bytes to the response
	var rawJSONBytes []byte
	err := postgress.GetPool().QueryRow(ctx, query, roomID, cursor, limit, userID).Scan(&rawJSONBytes)
	if err != nil {
		log.Printf("[messages] GetRoomMessages query failed room=%s: %v", roomID, err)
		helper.Error(w, http.StatusInternalServerError, "Database error")
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(rawJSONBytes)
}
