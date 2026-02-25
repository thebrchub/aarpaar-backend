package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// GetRoomsHandler returns paginated chat rooms for the authenticated user.
//
// Each room includes:
//   - room_id, name, type
//   - last_message_preview (content of the newest message)
//   - last_message_at (timestamp of the newest message)
//   - unread_count (messages since the user last read)
//   - members (array of {id, username, name} for every member in the room)
//
// Uses cursor-based pagination:
//   - cursor = RFC 3339 timestamp of the last room's last_message_at
//   - limit  = how many rooms to return (default 50, max 50)
//
// The SQL uses json_agg so Postgres returns the JSON directly.
// We pipe those bytes to the response — zero Go struct allocations.
//
// GET /api/v1/rooms?cursor=2025-01-01T00:00:00Z&limit=50 (requires auth)
// ---------------------------------------------------------------------------

func GetRoomsHandler(w http.ResponseWriter, r *http.Request) {
	// 1. Get the authenticated user ID from context
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 2. Parse cursor: RFC 3339 timestamp to paginate from (default = now)
	cursorStr := r.URL.Query().Get("cursor")
	cursor := time.Now().UTC()
	if cursorStr != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, cursorStr); err == nil {
			cursor = parsed
		}
	}

	// 3. Parse limit: how many rooms to return (default & max = 50)
	limitStr := r.URL.Query().Get("limit")
	limit := config.DefaultRoomLimit
	if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 && parsed <= config.DefaultRoomLimit {
		limit = parsed
	}

	// 4. Build the JSON response entirely in Postgres using LATERAL JOINs.
	//    Uses rooms.last_message_at for fast sorting (updated via trigger).
	//    Members sub-select returns each room participant's id, username, name.
	//    For DM rooms, includes last_seen_at (respects show_last_seen privacy).
	query := `
		SELECT COALESCE(json_agg(t), '[]')::text
		FROM (
			SELECT 
				r.id AS room_id, 
				r.name, 
				r.type,
				lm.content AS last_message_preview,
				r.last_message_at,
				COALESCE(uc.unread_count, 0) AS unread_count,
				COALESCE(mem.members, '[]'::json) AS members
			FROM room_members rm
			JOIN rooms r ON rm.room_id = r.id
			LEFT JOIN LATERAL (
				SELECT content 
				FROM messages m 
				WHERE m.room_id = r.id 
				ORDER BY m.created_at DESC 
				LIMIT 1
			) lm ON true
			LEFT JOIN LATERAL (
				SELECT COUNT(id)::int AS unread_count 
				FROM messages m 
				WHERE m.room_id = r.id AND m.created_at > rm.last_read_at
				  AND m.sender_id != $1
			) uc ON true
			LEFT JOIN LATERAL (
				SELECT json_agg(json_build_object(
					'id', u.id,
					'username', u.username,
					'name', u.name,
					'last_seen_at', CASE WHEN u.show_last_seen THEN u.last_seen_at ELSE NULL END
				)) AS members
				FROM room_members rm2
				JOIN users u ON u.id = rm2.user_id
				WHERE rm2.room_id = r.id AND rm2.status = 'active'
			) mem ON true
			WHERE rm.user_id = $1 AND rm.status = 'active'
			  AND (r.last_message_at < $2 OR r.last_message_at IS NULL)
			ORDER BY r.last_message_at DESC NULLS LAST
			LIMIT $3
		) t;
	`

	// 5. Execute and pipe the raw JSON bytes to the response
	var rawJSONBytes []byte
	err := postgress.GetRawDB().QueryRow(query, userID, cursor, limit).Scan(&rawJSONBytes)
	if err != nil {
		JSONError(w, "Failed to fetch rooms", http.StatusInternalServerError)
		return
	}

	// 6. Enrich DM room members with real-time is_online status from the engine
	rawJSONBytes = enrichRoomsWithOnlineStatus(rawJSONBytes, userID)

	// 7. Send directly to the client — zero struct allocations
	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(rawJSONBytes)
}

// ---------------------------------------------------------------------------
// CreateDMHandler creates a new DM room between two users.
// If a DM room already exists between them, returns the existing room.
//
// POST /api/v1/rooms (requires auth)
// Body: { "username": "target-username" }
// ---------------------------------------------------------------------------

type CreateDMRequest struct {
	Username string `json:"username"`
}

type CreateDMResponse struct {
	RoomID   string `json:"room_id"`
	Existing bool   `json:"existing"`
}

func CreateDMHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req CreateDMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Username == "" {
		JSONError(w, "username is required", http.StatusBadRequest)
		return
	}

	// Resolve username to UUID + check privacy
	var targetUserID string
	var targetIsPrivate bool
	err := postgress.GetRawDB().QueryRow(
		`SELECT id, is_private FROM users WHERE username = $1 AND is_banned = false`, req.Username,
	).Scan(&targetUserID, &targetIsPrivate)
	if err != nil || targetUserID == "" {
		JSONError(w, "User not found", http.StatusNotFound)
		return
	}

	if targetUserID == userID {
		JSONError(w, "Cannot create a DM with yourself", http.StatusBadRequest)
		return
	}

	// Check if blocked
	var blocked bool
	postgress.GetRawDB().QueryRow(
		`SELECT EXISTS (SELECT 1 FROM blocked_users WHERE
			(blocker_id = $1 AND blocked_id = $2) OR (blocker_id = $2 AND blocked_id = $1))`,
		userID, targetUserID,
	).Scan(&blocked)
	if blocked {
		JSONError(w, "User not found", http.StatusNotFound)
		return
	}

	// Check if a DM room already exists between these two users
	existingQuery := `
		SELECT rm1.room_id 
		FROM room_members rm1
		JOIN room_members rm2 ON rm1.room_id = rm2.room_id
		JOIN rooms r ON r.id = rm1.room_id
		WHERE rm1.user_id = $1 AND rm2.user_id = $2 AND r.type = 'DM'
		LIMIT 1;
	`

	var existingRoomID string
	err = postgress.GetRawDB().QueryRow(existingQuery, userID, targetUserID).Scan(&existingRoomID)
	if err == nil && existingRoomID != "" {
		JSONSuccess(w, CreateDMResponse{RoomID: existingRoomID, Existing: true})
		return
	}
	if err != nil && err != sql.ErrNoRows {
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Check friendship
	uid1, uid2 := sortUUIDs(userID, targetUserID)
	var areFriends bool
	postgress.GetRawDB().QueryRow(
		`SELECT EXISTS (SELECT 1 FROM friendships WHERE user_id_1 = $1 AND user_id_2 = $2)`,
		uid1, uid2,
	).Scan(&areFriends)

	// Determine target's room member status:
	// - Public account OR friends → active (instant DM)
	// - Private account AND not friends → pending (DM request)
	targetStatus := config.RoomMemberActive
	isPending := false
	if targetIsPrivate && !areFriends {
		targetStatus = config.RoomMemberPending
		isPending = true
	}

	// Create room + members in a transaction
	tx, err := postgress.GetRawDB().Begin()
	if err != nil {
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var roomID string
	err = tx.QueryRow(
		`INSERT INTO rooms (type) VALUES ('DM') RETURNING id`,
	).Scan(&roomID)
	if err != nil {
		JSONError(w, "Failed to create room", http.StatusInternalServerError)
		return
	}

	// Sender is always active; target may be pending
	_, err = tx.Exec(
		`INSERT INTO room_members (room_id, user_id, status) VALUES ($1, $2, $3), ($1, $4, $5)`,
		roomID, userID, config.RoomMemberActive, targetUserID, targetStatus,
	)
	if err != nil {
		JSONError(w, "Failed to add room members", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		JSONError(w, "Failed to create DM room", http.StatusInternalServerError)
		return
	}

	// Auto-subscribe creator to the room immediately
	if e := chat.GetEngine(); e != nil {
		e.JoinRoomForUser(userID, roomID)
		// If not pending (active DM), also subscribe the target
		if !isPending {
			e.JoinRoomForUser(targetUserID, roomID)
		}
	}

	// If pending, notify target via WebSocket
	if isPending {
		notifyUser(context.Background(), targetUserID, map[string]interface{}{
			config.FieldType:   config.MsgTypeDMRequest,
			config.FieldRoomID: roomID,
			config.FieldFrom:   userID,
		})
	}

	JSONSuccess(w, map[string]interface{}{
		"room_id":  roomID,
		"existing": false,
		"pending":  isPending,
	})
}

// ---------------------------------------------------------------------------
// enrichRoomsWithOnlineStatus adds an "is_online" field to each member of
// every room in the JSON array. Uses the in-memory Engine for real-time status.
// ---------------------------------------------------------------------------

func enrichRoomsWithOnlineStatus(raw []byte, currentUserID string) []byte {
	e := chat.GetEngine()
	if e == nil {
		return raw
	}

	var rooms []map[string]interface{}
	if err := json.Unmarshal(raw, &rooms); err != nil {
		return raw
	}

	for _, room := range rooms {
		members, ok := room["members"].([]interface{})
		if !ok {
			continue
		}
		for _, m := range members {
			member, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			id, _ := member["id"].(string)
			if id != "" {
				member["is_online"] = e.IsUserOnline(id)
			}
		}
	}

	enriched, err := json.Marshal(rooms)
	if err != nil {
		return raw
	}
	return enriched
}
