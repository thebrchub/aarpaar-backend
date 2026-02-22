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

	// 6. Zero-allocation SQL: Postgres builds the JSON array for us
	query := `
		SELECT COALESCE(json_agg(t), '[]')::text
		FROM (
			SELECT id, sender_id, content, created_at 
			FROM messages 
			WHERE room_id = $1 AND id < $2 
			ORDER BY id DESC
			LIMIT $3
		) t;
	`

	// 7. Execute and pipe the raw JSON bytes to the response
	var rawJSONBytes []byte
	err = postgress.GetRawDB().QueryRow(query, roomID, cursor, limit).Scan(&rawJSONBytes)
	if err != nil {
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(rawJSONBytes)
}
