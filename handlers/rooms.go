package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"log"
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

	// 4. Build the response in two parts:
	//    a) rooms with member_ids (not full member objects)
	//    b) deduplicated users map for all members across rooms
	roomsQuery := `
		WITH user_rooms AS (
			SELECT rm.room_id, rm.last_read_at
			FROM room_members rm
			JOIN rooms r ON rm.room_id = r.id
			WHERE rm.user_id = $1 AND rm.status = 'active'
			  AND (r.last_message_at < $2 OR r.last_message_at IS NULL)
			ORDER BY r.last_message_at DESC NULLS LAST
			LIMIT $3
		),
		last_msgs AS (
			SELECT DISTINCT ON (m.room_id) m.room_id, m.content
			FROM messages m
			JOIN user_rooms ur ON m.room_id = ur.room_id
			ORDER BY m.room_id, m.created_at DESC
		),
		unread AS (
			SELECT m.room_id, COUNT(*)::int AS unread_count
			FROM messages m
			JOIN user_rooms ur ON m.room_id = ur.room_id
			WHERE m.created_at > ur.last_read_at AND m.sender_id != $1
			GROUP BY m.room_id
		),
		member_counts AS (
			SELECT rm2.room_id, COUNT(*)::int AS member_count
			FROM room_members rm2
			JOIN user_rooms ur ON rm2.room_id = ur.room_id
			WHERE rm2.status = 'active'
			GROUP BY rm2.room_id
		),
		member_ids AS (
			SELECT rm2.room_id, json_agg(rm2.user_id) AS ids
			FROM room_members rm2
			JOIN user_rooms ur ON rm2.room_id = ur.room_id
			WHERE rm2.status = 'active'
			GROUP BY rm2.room_id
		),
		all_member_users AS (
			SELECT DISTINCT u.id, u.username, u.name,
				COALESCE(u.avatar_url, '') AS avatar_url,
				CASE WHEN u.show_last_seen THEN u.last_seen_at ELSE NULL END AS last_seen_at,
				u.is_private
			FROM room_members rm2
			JOIN users u ON u.id = rm2.user_id
			JOIN user_rooms ur ON rm2.room_id = ur.room_id
			WHERE rm2.status = 'active'
		)
		SELECT
			COALESCE((SELECT json_agg(t ORDER BY t.last_message_at DESC NULLS LAST)
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
					COALESCE(mi.ids, '[]'::json) AS member_ids
				FROM user_rooms ur
				JOIN rooms r ON ur.room_id = r.id
				LEFT JOIN last_msgs lm ON lm.room_id = r.id
				LEFT JOIN unread uc ON uc.room_id = r.id
				LEFT JOIN member_counts mc ON mc.room_id = r.id
				LEFT JOIN member_ids mi ON mi.room_id = r.id
			) t), '[]'::json),
			COALESCE((SELECT json_object_agg(amu.id, json_build_object(
				'username', amu.username,
				'name', amu.name,
				'avatar_url', amu.avatar_url,
				'last_seen_at', amu.last_seen_at,
				'is_private', amu.is_private
			)) FROM all_member_users amu), '{}'::json);
	`

	// 5. Execute — two columns: rooms JSON array + users JSON object
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()
	var roomsJSON, usersJSON []byte
	err := postgress.GetRawDB().QueryRowContext(ctx, roomsQuery, userID, cursor, limit).Scan(&roomsJSON, &usersJSON)
	if err != nil {
		log.Printf("[rooms] GetRooms query failed user=%s: %v", userID, err)
		JSONError(w, "Failed to fetch rooms", http.StatusInternalServerError)
		return
	}

	// 6. Enrich users map with real-time is_online status
	usersJSON = enrichUsersMapWithOnlineStatus(usersJSON, userID)

	// 7. Build final response: {"rooms": [...], "users": {...}}
	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"rooms":`))
	w.Write(roomsJSON)
	w.Write([]byte(`,"users":`))
	w.Write(usersJSON)
	w.Write([]byte(`}`))
}

// CreateDMHandler creates a new DM room between two users.
// If a DM room already exists between them, returns the existing room.
//

type CreateDMRequest struct {
	Username string `json:"username"`
}

type CreateDMResponse struct {
	RoomID         string `json:"room_id"`
	Existing       bool   `json:"existing"`
	Pending        bool   `json:"pending,omitempty"`
	TargetName     string `json:"target_name"`
	TargetUsername string `json:"target_username"`
	TargetAvatar   string `json:"target_avatar_url"`
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
	var targetName, targetAvatar string
	err := postgress.GetRawDB().QueryRow(
		`SELECT id, is_private, COALESCE(name,''), COALESCE(avatar_url,'') FROM users WHERE username = $1 AND is_banned = false`, req.Username,
	).Scan(&targetUserID, &targetIsPrivate, &targetName, &targetAvatar)
	if err != nil {
		if err == sql.ErrNoRows {
			JSONError(w, "User not found", http.StatusNotFound)
		} else {
			log.Printf("[rooms] CreateDM user lookup failed username=%s: %v", req.Username, err)
			JSONError(w, "Database error", http.StatusInternalServerError)
		}
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
		log.Printf("[rooms] CreateDM blocked check failed user=%s target=%s: %v", userID, targetUserID, err)
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
		JSONSuccess(w, CreateDMResponse{
			RoomID:         existingRoomID,
			Existing:       true,
			TargetName:     targetName,
			TargetUsername: req.Username,
			TargetAvatar:   targetAvatar,
		})
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
		log.Printf("[rooms] CreateDM begin tx failed: %v", err)
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// Re-read is_private under FOR UPDATE lock to prevent TOCTOU race
	err = tx.QueryRow(
		`SELECT is_private FROM users WHERE id = $1 FOR UPDATE`, targetUserID,
	).Scan(&targetIsPrivate)
	if err != nil {
		log.Printf("[rooms] CreateDM re-read is_private failed target=%s: %v", targetUserID, err)
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
		log.Printf("[rooms] CreateDM friendship check failed: %v", err)
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
		log.Printf("[rooms] CreateDM insert room failed: %v", err)
		JSONError(w, "Failed to create room", http.StatusInternalServerError)
		return
	}

	// Sender is always active; target may be pending
	_, err = tx.Exec(
		`INSERT INTO room_members (room_id, user_id, status) VALUES ($1, $2, $3), ($1, $4, $5)`,
		roomID, userID, config.RoomMemberActive, targetUserID, targetStatus,
	)
	if err != nil {
		log.Printf("[rooms] CreateDM insert members failed room=%s: %v", roomID, err)
		JSONError(w, "Failed to add room members", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("[rooms] CreateDM commit failed: %v", err)
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
			if err := postgress.GetRawDB().QueryRowContext(ctx,
				`SELECT COALESCE(name, 'Someone') FROM users WHERE id = $1`, userID,
			).Scan(&senderName); err != nil {
				log.Printf("[rooms] CreateDM sender name lookup failed user=%s: %v", userID, err)
				senderName = "Someone"
			}
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

	JSONSuccess(w, CreateDMResponse{
		RoomID:         roomID,
		Existing:       false,
		Pending:        isPending,
		TargetName:     targetName,
		TargetUsername: req.Username,
		TargetAvatar:   targetAvatar,
	})
}

// ---------------------------------------------------------------------------
// enrichUsersMapWithOnlineStatus enriches the deduplicated users JSON map
// (from GetRoomsHandler) with is_online. The map is keyed by user ID.
// Only friends and non-private users get is_online = true visibility.
// ---------------------------------------------------------------------------

func enrichUsersMapWithOnlineStatus(raw []byte, currentUserID string) []byte {
	e := chat.GetEngine()
	if e == nil {
		return raw
	}

	type userEntry struct {
		Username  string  `json:"username"`
		Name      string  `json:"name"`
		AvatarURL string  `json:"avatar_url"`
		LastSeen  *string `json:"last_seen_at,omitempty"`
		IsPrivate bool    `json:"is_private"`
		IsOnline  bool    `json:"is_online"`
	}

	var users map[string]userEntry
	if err := json.Unmarshal(raw, &users); err != nil {
		return raw
	}
	if len(users) == 0 {
		return raw
	}

	// Build friend set for privacy filtering
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

	// Build blocked set — blocked users cannot see each other's status
	blockedSet := make(map[string]bool)
	blockedRows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT blocked_id FROM blocked_users WHERE blocker_id = $1
		 UNION ALL
		 SELECT blocker_id FROM blocked_users WHERE blocked_id = $1`, currentUserID,
	)
	if err == nil {
		for blockedRows.Next() {
			var bid string
			if blockedRows.Scan(&bid) == nil {
				blockedSet[bid] = true
			}
		}
		blockedRows.Close()
	}

	for uid, u := range users {
		if uid == currentUserID {
			u.IsOnline = e.IsUserOnline(uid)
			users[uid] = u
		} else if blockedSet[uid] {
			// Blocked user (either direction): hide online status and last seen
			u.IsOnline = false
			u.LastSeen = nil
			users[uid] = u
		} else if friendSet[uid] || !u.IsPrivate {
			u.IsOnline = e.IsUserOnline(uid)
			users[uid] = u
		} else {
			// Private non-friend: hide online status and last seen
			u.IsOnline = false
			u.LastSeen = nil
			users[uid] = u
		}
	}

	enriched, err := json.Marshal(users)
	if err != nil {
		return raw
	}
	return enriched
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
