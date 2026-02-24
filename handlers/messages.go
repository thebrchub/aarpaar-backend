package handlers

import (
	"math"
	"net/http"
	"strconv"

	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// GetRoomMessagesHandler returns paginated messages for a room.
//
// Uses cursor-based pagination:
//   - cursor = the last message ID the client has (older messages are loaded)
//   - limit  = how many messages to return (default 50, max 100)
//
// The SQL uses json_agg so Postgres returns the final JSON string directly.
// We pipe those bytes straight to the HTTP response — zero Go struct allocations.
//
// GET /api/v1/rooms/{roomId}/messages?cursor=123&limit=50 (requires auth)
// ---------------------------------------------------------------------------

func GetRoomMessagesHandler(w http.ResponseWriter, r *http.Request) {
	// 1. Authenticate — make sure the caller is logged in
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 2. Extract room ID from the URL path using Go 1.22+ pattern matching
	//    Route is registered as: GET /api/v1/rooms/{roomId}/messages
	roomID := r.PathValue("roomId")
	if roomID == "" {
		JSONError(w, "Missing room ID", http.StatusBadRequest)
		return
	}

	// 3. Verify the caller is an active member of this room
	var isMember bool
	err := postgress.GetRawDB().QueryRow(
		`SELECT EXISTS (SELECT 1 FROM room_members WHERE room_id = $1 AND user_id = $2 AND status = 'active')`,
		roomID, userID,
	).Scan(&isMember)
	if err != nil || !isMember {
		JSONError(w, "Not a member of this room", http.StatusForbidden)
		return
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
	//    For messages sent by the current user, we compute a receipt status:
	//      "read"      = the other member has read past this message
	//      "delivered"  = the message was delivered to the other member's device
	//      "sent"       = server has the message but recipient hasn't received it yet
	//    For messages received (from others), status is null.
	query := `
		SELECT COALESCE(json_agg(t), '[]')::text
		FROM (
			SELECT m.id, m.sender_id, m.content, m.created_at,
				CASE
					WHEN m.sender_id = $4 THEN
						CASE
							WHEN m.created_at <= (
								SELECT MIN(rm2.last_read_at) FROM room_members rm2
								WHERE rm2.room_id = $1 AND rm2.user_id != $4
							) THEN 'read'
							WHEN m.created_at <= (
								SELECT MIN(rm2.last_delivered_at) FROM room_members rm2
								WHERE rm2.room_id = $1 AND rm2.user_id != $4
							) THEN 'delivered'
							ELSE 'sent'
						END
					ELSE NULL
				END AS status
			FROM messages m
			WHERE m.room_id = $1 AND m.id < $2
			ORDER BY m.id DESC
			LIMIT $3
		) t;
	`

	// 7. Execute and pipe the raw JSON bytes to the response
	var rawJSONBytes []byte
	err = postgress.GetRawDB().QueryRow(query, roomID, cursor, limit, userID).Scan(&rawJSONBytes)
	if err != nil {
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(rawJSONBytes)
}
