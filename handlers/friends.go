package handlers

import (
	"context"
	"database/sql"
	"log"
	"net/http"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// Send Friend Request
//
// POST /api/v1/friends/request (requires auth)
// Body: { "username": "bob" }
//
// Creates a persistent friend_requests row in Postgres.
// If the target already sent us a request, auto-accepts (mutual).
// ---------------------------------------------------------------------------

type FriendRequestBody struct {
	Username string `json:"username"`
}

func SendFriendRequestHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var body FriendRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Username == "" {
		JSONError(w, "username is required", http.StatusBadRequest)
		return
	}

	// Resolve username → UUID
	var targetID string
	err := postgress.GetRawDB().QueryRow(
		`SELECT id FROM users WHERE username = $1 AND is_banned = false`, body.Username,
	).Scan(&targetID)
	if err != nil || targetID == "" {
		JSONError(w, "User not found", http.StatusNotFound)
		return
	}

	if targetID == userID {
		JSONError(w, "Cannot friend yourself", http.StatusBadRequest)
		return
	}

	// Check if blocked
	var blocked bool
	if err := postgress.GetRawDB().QueryRow(
		`SELECT EXISTS (SELECT 1 FROM blocked_users WHERE
			(blocker_id = $1 AND blocked_id = $2) OR (blocker_id = $2 AND blocked_id = $1))`,
		userID, targetID,
	).Scan(&blocked); err != nil {
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}
	if blocked {
		JSONError(w, "User not found", http.StatusNotFound)
		return
	}

	// Check if already friends
	uid1, uid2 := sortUUIDs(userID, targetID)
	var alreadyFriends bool
	if err := postgress.GetRawDB().QueryRow(
		`SELECT EXISTS (SELECT 1 FROM friendships WHERE user_id_1 = $1 AND user_id_2 = $2)`,
		uid1, uid2,
	).Scan(&alreadyFriends); err != nil {
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}
	if alreadyFriends {
		JSONMessage(w, "already_friends", "You are already friends")
		return
	}

	// Check if they already sent us a request (reverse) → auto-accept
	var reverseReqID int64
	err = postgress.GetRawDB().QueryRow(
		`SELECT id FROM friend_requests WHERE sender_id = $1 AND receiver_id = $2 AND status = $3`,
		targetID, userID, config.FriendReqPending,
	).Scan(&reverseReqID)

	if err == nil && reverseReqID > 0 {
		// Mutual! Accept the reverse request and create friendship
		acceptFriendship(w, userID, targetID, reverseReqID)
		return
	}

	// Insert or update our outgoing request
	_, err = postgress.Exec(
		`INSERT INTO friend_requests (sender_id, receiver_id, status)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (sender_id, receiver_id) DO UPDATE SET status = $3, updated_at = NOW()`,
		userID, targetID, config.FriendReqPending,
	)
	if err != nil {
		JSONError(w, "Failed to send request", http.StatusInternalServerError)
		return
	}

	// Notify target via WebSocket
	notifyUser(context.Background(), targetID, map[string]interface{}{
		config.FieldType: config.MsgTypeFriendRequest,
		config.FieldFrom: userID,
		"username":       body.Username,
	})

	JSONMessage(w, "pending", "Friend request sent")
}

// ---------------------------------------------------------------------------
// Accept Friend Request
//
// POST /api/v1/friends/accept (requires auth)
// Body: { "username": "alice" }
// ---------------------------------------------------------------------------

func AcceptFriendRequestHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var body FriendRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Username == "" {
		JSONError(w, "username is required", http.StatusBadRequest)
		return
	}

	var senderID string
	if err := postgress.GetRawDB().QueryRow(
		`SELECT id FROM users WHERE username = $1`, body.Username,
	).Scan(&senderID); err != nil || senderID == "" {
		JSONError(w, "User not found", http.StatusNotFound)
		return
	}

	// Find the pending request where sender=them, receiver=me
	var reqID int64
	err := postgress.GetRawDB().QueryRow(
		`SELECT id FROM friend_requests WHERE sender_id = $1 AND receiver_id = $2 AND status = $3`,
		senderID, userID, config.FriendReqPending,
	).Scan(&reqID)
	if err != nil {
		JSONError(w, "No pending friend request from this user", http.StatusNotFound)
		return
	}

	acceptFriendship(w, userID, senderID, reqID)
}

// ---------------------------------------------------------------------------
// Reject Friend Request
//
// POST /api/v1/friends/reject (requires auth)
// Body: { "username": "alice" }
// ---------------------------------------------------------------------------

func RejectFriendRequestHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var body FriendRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Username == "" {
		JSONError(w, "username is required", http.StatusBadRequest)
		return
	}

	var senderID string
	if err := postgress.GetRawDB().QueryRow(
		`SELECT id FROM users WHERE username = $1`, body.Username,
	).Scan(&senderID); err != nil || senderID == "" {
		JSONError(w, "User not found", http.StatusNotFound)
		return
	}

	rows, err := postgress.Exec(
		`UPDATE friend_requests SET status = $1, updated_at = NOW()
		 WHERE sender_id = $2 AND receiver_id = $3 AND status = $4`,
		config.FriendReqRejected, senderID, userID, config.FriendReqPending,
	)
	if err != nil {
		JSONError(w, "Failed to reject", http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		JSONError(w, "No pending friend request from this user", http.StatusNotFound)
		return
	}

	JSONMessage(w, "success", "Friend request rejected")
}

// ---------------------------------------------------------------------------
// Get Friends List
//
// GET /api/v1/friends (requires auth)
// Returns JSON array of friend user objects.
// ---------------------------------------------------------------------------

func GetFriendsHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	query := `
		SELECT COALESCE(json_agg(t), '[]')::text
		FROM (
			SELECT u.id, u.name, u.username, u.avatar_url, u.is_private, f.created_at AS friends_since,
			       CASE WHEN u.show_last_seen THEN u.last_seen_at ELSE NULL END AS last_seen_at
			FROM friendships f
			JOIN users u ON u.id = CASE
				WHEN f.user_id_1 = $1 THEN f.user_id_2
				ELSE f.user_id_1
			END
			WHERE (f.user_id_1 = $1 OR f.user_id_2 = $1)
			  AND u.is_banned = false
			ORDER BY u.name
		) t
	`

	var raw []byte
	err := postgress.GetRawDB().QueryRow(query, userID).Scan(&raw)
	if err != nil {
		JSONError(w, "Failed to fetch friends", http.StatusInternalServerError)
		return
	}

	// Enrich each friend with real-time is_online status from the engine
	raw = enrichFriendsWithOnlineStatus(raw)

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
}

// ---------------------------------------------------------------------------
// Get Friend Requests
//
// GET /api/v1/friends/requests?type=received (requires auth)
// Query param: type = "received" (default) or "sent"
// ---------------------------------------------------------------------------

func GetFriendRequestsHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	reqType := r.URL.Query().Get("type")
	if reqType == "" {
		reqType = "received"
	}

	var query string
	if reqType == "sent" {
		query = `
			SELECT COALESCE(json_agg(t), '[]')::text
			FROM (
				SELECT fr.id AS request_id, fr.status, fr.created_at,
				       u.id AS user_id, u.name, u.username, u.avatar_url
				FROM friend_requests fr
				JOIN users u ON u.id = fr.receiver_id
				WHERE fr.sender_id = $1 AND fr.status = 'pending'
				ORDER BY fr.created_at DESC
			) t
		`
	} else {
		query = `
			SELECT COALESCE(json_agg(t), '[]')::text
			FROM (
				SELECT fr.id AS request_id, fr.status, fr.created_at,
				       u.id AS user_id, u.name, u.username, u.avatar_url
				FROM friend_requests fr
				JOIN users u ON u.id = fr.sender_id
				WHERE fr.receiver_id = $1 AND fr.status = 'pending'
				ORDER BY fr.created_at DESC
			) t
		`
	}

	var raw []byte
	err := postgress.GetRawDB().QueryRow(query, userID).Scan(&raw)
	if err != nil {
		JSONError(w, "Failed to fetch requests", http.StatusInternalServerError)
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
}

// ---------------------------------------------------------------------------
// Remove Friend
//
// DELETE /api/v1/friends/{username} (requires auth)
// ---------------------------------------------------------------------------

func RemoveFriendHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	username := r.PathValue("username")
	if username == "" {
		JSONError(w, "username is required", http.StatusBadRequest)
		return
	}

	var targetID string
	if err := postgress.GetRawDB().QueryRow(
		`SELECT id FROM users WHERE username = $1`, username,
	).Scan(&targetID); err != nil || targetID == "" {
		JSONError(w, "User not found", http.StatusNotFound)
		return
	}

	uid1, uid2 := sortUUIDs(userID, targetID)
	rows, err := postgress.Exec(
		`DELETE FROM friendships WHERE user_id_1 = $1 AND user_id_2 = $2`, uid1, uid2,
	)
	if err != nil {
		JSONError(w, "Failed to remove friend", http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		JSONError(w, "Not friends with this user", http.StatusNotFound)
		return
	}

	// Also clean up any friend_requests between the two
	postgress.Exec(
		`DELETE FROM friend_requests WHERE
			(sender_id = $1 AND receiver_id = $2) OR (sender_id = $2 AND receiver_id = $1)`,
		userID, targetID,
	)

	JSONMessage(w, "success", "Friend removed")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// sortUUIDs returns the two UUIDs in lexicographic order for the friendships table.
func sortUUIDs(a, b string) (string, string) {
	if a < b {
		return a, b
	}
	return b, a
}

// acceptFriendship marks a friend request as accepted, creates the friendship,
// and notifies both users. Used by both AcceptFriendRequestHandler and the
// auto-accept path in SendFriendRequestHandler.
func acceptFriendship(w http.ResponseWriter, accepterID, requesterID string, requestID int64) {
	tx, err := postgress.GetRawDB().Begin()
	if err != nil {
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// Update the request status
	_, err = tx.Exec(
		`UPDATE friend_requests SET status = $1, updated_at = NOW() WHERE id = $2`,
		config.FriendReqAccepted, requestID,
	)
	if err != nil {
		JSONError(w, "Failed to accept", http.StatusInternalServerError)
		return
	}

	// Create friendship (sorted UUIDs)
	uid1, uid2 := sortUUIDs(accepterID, requesterID)
	_, err = tx.Exec(
		`INSERT INTO friendships (user_id_1, user_id_2) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		uid1, uid2,
	)
	if err != nil {
		JSONError(w, "Failed to create friendship", http.StatusInternalServerError)
		return
	}

	// Check if a DM room already exists — if not, create one
	var dmRoomID string
	err = tx.QueryRow(
		`SELECT rm1.room_id FROM room_members rm1
		 JOIN room_members rm2 ON rm1.room_id = rm2.room_id
		 JOIN rooms r ON r.id = rm1.room_id
		 WHERE rm1.user_id = $1 AND rm2.user_id = $2 AND r.type = 'DM'
		 LIMIT 1`,
		accepterID, requesterID,
	).Scan(&dmRoomID)

	if err == sql.ErrNoRows || dmRoomID == "" {
		// Create a new DM room
		err = tx.QueryRow(`INSERT INTO rooms (type) VALUES ('DM') RETURNING id`).Scan(&dmRoomID)
		if err != nil {
			JSONError(w, "Failed to create DM room", http.StatusInternalServerError)
			return
		}
		_, err = tx.Exec(
			`INSERT INTO room_members (room_id, user_id, status) VALUES ($1, $2, $3), ($1, $4, $3)`,
			dmRoomID, accepterID, config.RoomMemberActive, requesterID,
		)
		if err != nil {
			JSONError(w, "Failed to add room members", http.StatusInternalServerError)
			return
		}
	} else {
		// Room exists — make sure both members are active (upgrade pending → active)
		if _, err := tx.Exec(
			`UPDATE room_members SET status = $1 WHERE room_id = $2 AND user_id IN ($3, $4)`,
			config.RoomMemberActive, dmRoomID, accepterID, requesterID,
		); err != nil {
			log.Printf("[friends] Failed to activate room members room=%s: %v", dmRoomID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		JSONError(w, "Failed to commit", http.StatusInternalServerError)
		return
	}

	// Auto-subscribe both users to the DM room
	if e := chat.GetEngine(); e != nil {
		e.JoinRoomForUser(accepterID, dmRoomID)
		e.JoinRoomForUser(requesterID, dmRoomID)
	}

	// Notify both via WebSocket
	ctx := context.Background()
	payload := map[string]interface{}{
		config.FieldType: config.MsgTypeFriendAccepted,
		"dm_room_id":     dmRoomID,
	}
	notifyUser(ctx, accepterID, payload)
	notifyUser(ctx, requesterID, payload)

	// Also activate any pending DM rooms between them
	redis.GetRawClient().Del(ctx,
		config.FRIEND_REQUEST_COLON+accepterID,
		config.FRIEND_REQUEST_COLON+requesterID,
	)

	JSONSuccess(w, map[string]string{
		"status":     "friends",
		"message":    "You are now friends!",
		"dm_room_id": dmRoomID,
	})
}

// ---------------------------------------------------------------------------
// enrichFriendsWithOnlineStatus injects "is_online" into each friend by
// deserializing into lightweight structs, setting the field, and
// re-serializing once. This replaces the previous sjson.SetBytes approach
// which was O(N²) — each call copied the entire growing JSON buffer.
// ---------------------------------------------------------------------------

func enrichFriendsWithOnlineStatus(raw []byte) []byte {
	e := chat.GetEngine()
	if e == nil {
		return raw
	}

	type friend struct {
		ID        string  `json:"id"`
		Username  *string `json:"username,omitempty"`
		Name      *string `json:"name,omitempty"`
		AvatarURL *string `json:"avatar_url,omitempty"`
		LastSeen  *string `json:"last_seen_at,omitempty"`
		IsOnline  bool    `json:"is_online"`
	}

	var friends []friend
	if err := json.Unmarshal(raw, &friends); err != nil {
		return raw
	}

	for i := range friends {
		friends[i].IsOnline = e.IsUserOnline(friends[i].ID)
	}

	enriched, err := json.Marshal(friends)
	if err != nil {
		return raw
	}
	return enriched
}
