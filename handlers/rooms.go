package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/services"
)

// pgCtx creates a context from an HTTP request with PGTimeout.
// Cancels when either the request is cancelled or PGTimeout expires.
func pgCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), time.Duration(config.PGTimeout)*time.Second)
}

// GetRoomsHandler returns paginated chat rooms for the authenticated user.
//
// @Summary		List chat rooms
// @Description	Returns paginated chat rooms with last message preview, unread count, and members. Uses cursor-based pagination.
// @Tags		Rooms
// @Produce		json
// @Param		cursor	query	string	false	"RFC 3339 timestamp to paginate from (default: now)"
// @Param		limit	query	int		false	"Number of rooms to return (default 50, max 50)"
// @Success		200	{array}	RoomListItem
// @Failure		401	{object}	StatusMessage
// @Failure		500	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/rooms [get]
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
				r.avatar_url AS group_avatar,
				COALESCE(r.created_by::text, '') AS created_by,
				lm.content AS last_message_preview,
				r.last_message_at,
				COALESCE(uc.unread_count, 0) AS unread_count,
				COALESCE(mc.member_count, 0) AS member_count,
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
				SELECT COUNT(*)::int AS member_count
				FROM room_members rm3
				WHERE rm3.room_id = r.id AND rm3.status = 'active'
			) mc ON true
			LEFT JOIN LATERAL (
				SELECT json_agg(json_build_object(
					'id', u.id,
					'username', u.username,
					'name', u.name,
					'avatar_url', COALESCE(u.avatar_url, ''),
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
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()
	var rawJSONBytes []byte
	err := postgress.GetRawDB().QueryRowContext(ctx, query, userID, cursor, limit).Scan(&rawJSONBytes)
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

// CreateDMHandler creates a new DM room between two users.
// If a DM room already exists between them, returns the existing room.
//
// @Summary		Create or get DM room
// @Description	Creates a DM room with the target user. Returns existing room if one already exists. Private accounts get a pending DM request.
// @Tags		Rooms
// @Accept		json
// @Produce		json
// @Param		body	body	CreateDMRequest	true	"Target username"
// @Success		200	{object}	CreateDMFullResponse
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Failure		404	{object}	StatusMessage
// @Failure		500	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/rooms [post]

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

	// Resolve username to UUID + check privacy (initial check, re-verified under FOR UPDATE below)
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
	if err := postgress.GetRawDB().QueryRow(
		`SELECT EXISTS (SELECT 1 FROM blocked_users WHERE
			(blocker_id = $1 AND blocked_id = $2) OR (blocker_id = $2 AND blocked_id = $1))`,
		userID, targetUserID,
	).Scan(&blocked); err != nil {
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}
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

	// Use a transaction with FOR UPDATE on the target user row to prevent
	// race conditions where is_private toggles between the check and room creation.
	tx, err := postgress.GetRawDB().Begin()
	if err != nil {
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// Re-read is_private under FOR UPDATE lock to prevent TOCTOU race
	err = tx.QueryRow(
		`SELECT is_private FROM users WHERE id = $1 FOR UPDATE`, targetUserID,
	).Scan(&targetIsPrivate)
	if err != nil {
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Check friendship
	uid1, uid2 := sortUUIDs(userID, targetUserID)
	var areFriends bool
	if err := tx.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM friendships WHERE user_id_1 = $1 AND user_id_2 = $2)`,
		uid1, uid2,
	).Scan(&areFriends); err != nil {
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Determine target's room member status using Instagram-style contact model:
	// - Friends → both 'active' (direct DM)
	// - Non-friend + target is private + sender is free → reject with 403
	// - Non-friend + target is private + sender is paid → sender 'active', target 'pending' (message request)
	// - Non-friend + target is public → sender 'active', target 'pending' (message request)
	targetStatus := config.RoomMemberActive
	isPending := false
	if !areFriends {
		if targetIsPrivate {
			// Check if sender is a paid user (total donations >= premium_connect threshold)
			ctx2, cancel2 := pgCtx(r)
			defer cancel2()
			var cfg struct {
				MinDonation float64 `json:"min_donation"`
			}
			cfg.MinDonation = 50 // default threshold
			_ = GetAppSetting(ctx2, "premium_connect", &cfg)
			totalDonated := GetUserTotalDonation(ctx2, userID)
			if totalDonated < cfg.MinDonation {
				JSONError(w, "This user has a private account. Send a friend request first.", http.StatusForbidden)
				return
			}
		}
		targetStatus = config.RoomMemberPending
		isPending = true
	}

	// Create room + members within the same transaction
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

		// Push notification for offline target
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
			defer cancel()
			var senderName string
			_ = postgress.GetRawDB().QueryRowContext(ctx,
				`SELECT COALESCE(name, 'Someone') FROM users WHERE id = $1`, userID,
			).Scan(&senderName)
			services.SendPushToUser(ctx, targetUserID, services.PushPayload{
				Data: map[string]string{
					"type":       "dm_request",
					"roomId":     roomID,
					"senderId":   userID,
					"senderName": senderName,
				},
			})
		}()
	}

	JSONSuccess(w, map[string]interface{}{
		"room_id":  roomID,
		"existing": false,
		"pending":  isPending,
	})
}

// ---------------------------------------------------------------------------
// enrichRoomsWithOnlineStatus injects "is_online" into each member of every
// room by deserializing into lightweight structs, setting the field, and
// re-serializing once. This replaces the previous sjson.SetBytes approach
// which was O(N²) — each call copied the entire growing JSON buffer.
// ---------------------------------------------------------------------------

func enrichRoomsWithOnlineStatus(raw []byte, currentUserID string) []byte {
	e := chat.GetEngine()
	if e == nil {
		return raw
	}

	// Lightweight struct that only captures the fields we need to enrich.
	// json.RawMessage preserves all other fields without allocating Go structs.
	type member struct {
		ID        string  `json:"id"`
		Username  *string `json:"username"`
		Name      *string `json:"name"`
		AvatarURL *string `json:"avatar_url,omitempty"`
		LastSeen  *string `json:"last_seen_at,omitempty"`
		IsOnline  bool    `json:"is_online"`
	}
	type room struct {
		RoomID      string   `json:"room_id"`
		Name        *string  `json:"name"`
		Type        string   `json:"type"`
		GroupAvatar *string  `json:"group_avatar,omitempty"`
		CreatedBy   *string  `json:"created_by,omitempty"`
		LastPreview *string  `json:"last_message_preview,omitempty"`
		LastMsgAt   *string  `json:"last_message_at,omitempty"`
		UnreadCount int      `json:"unread_count"`
		MemberCount int      `json:"member_count"`
		Members     []member `json:"members"`
	}

	var rooms []room
	if err := json.Unmarshal(raw, &rooms); err != nil {
		return raw
	}

	// Build friend set + public user set for privacy filtering of is_online.
	// Only expose online status for friends and public accounts.
	friendSet := make(map[string]bool)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()
	friendRows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT user_id_2 FROM friendships WHERE user_id_1 = $1
		 UNION ALL
		 SELECT user_id_1 FROM friendships WHERE user_id_2 = $1`, currentUserID,
	)
	if err == nil {
		for friendRows.Next() {
			var fid string
			if friendRows.Scan(&fid) == nil {
				friendSet[fid] = true
			}
		}
		friendRows.Close()
	}

	// Collect all unique member IDs to batch-check privacy
	memberIDSet := make(map[string]bool)
	for i := range rooms {
		for j := range rooms[i].Members {
			memberIDSet[rooms[i].Members[j].ID] = true
		}
	}
	privateSet := make(map[string]bool)
	if len(memberIDSet) > 0 {
		ids := make([]string, 0, len(memberIDSet))
		for id := range memberIDSet {
			ids = append(ids, id)
		}
		ph, phArgs := buildINClause(ids, 1)
		privRows, pErr := postgress.GetRawDB().QueryContext(ctx,
			fmt.Sprintf(`SELECT id FROM users WHERE id IN (%s) AND is_private = true`, ph), phArgs...)
		if pErr == nil {
			for privRows.Next() {
				var pid string
				if privRows.Scan(&pid) == nil {
					privateSet[pid] = true
				}
			}
			privRows.Close()
		}
	}

	for i := range rooms {
		for j := range rooms[i].Members {
			mid := rooms[i].Members[j].ID
			// Show is_online only for self, friends, or public accounts
			if mid == currentUserID || friendSet[mid] || !privateSet[mid] {
				rooms[i].Members[j].IsOnline = e.IsUserOnline(mid)
			}
			// else: leave IsOnline as false (zero value)
		}
	}

	enriched, err := json.Marshal(rooms)
	if err != nil {
		return raw
	}
	return enriched
}
