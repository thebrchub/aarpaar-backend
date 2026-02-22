package handlers

import (
	"context"
	"net/http"

	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// Get DM Requests (Instagram "Message Requests" inbox)
//
// GET /api/v1/rooms/requests (requires auth)
// Returns rooms where the caller's room_members.status = 'pending'.
// ---------------------------------------------------------------------------

func GetDMRequestsHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	query := `
		SELECT COALESCE(json_agg(t), '[]')::text
		FROM (
			SELECT
				r.id AS room_id,
				r.type,
				r.last_message_at,
				u.id AS sender_id,
				u.name AS sender_name,
				u.username AS sender_username,
				u.avatar_url AS sender_avatar,
				lm.content AS last_message_preview
			FROM room_members my_rm
			JOIN rooms r ON my_rm.room_id = r.id
			JOIN room_members other_rm ON other_rm.room_id = r.id AND other_rm.user_id != $1
			JOIN users u ON u.id = other_rm.user_id
			LEFT JOIN LATERAL (
				SELECT content FROM messages m
				WHERE m.room_id = r.id ORDER BY m.created_at DESC LIMIT 1
			) lm ON true
			WHERE my_rm.user_id = $1 AND my_rm.status = 'pending'
			ORDER BY r.last_message_at DESC NULLS LAST
		) t
	`

	var raw []byte
	err := postgress.GetRawDB().QueryRow(query, userID).Scan(&raw)
	if err != nil {
		JSONError(w, "Failed to fetch DM requests", http.StatusInternalServerError)
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
}

// ---------------------------------------------------------------------------
// Accept DM Request
//
// POST /api/v1/rooms/{roomId}/accept (requires auth)
// Flips room_members.status from 'pending' → 'active' for the caller.
// ---------------------------------------------------------------------------

func AcceptDMRequestHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	roomID := r.PathValue("roomId")
	if roomID == "" {
		JSONError(w, "Missing room ID", http.StatusBadRequest)
		return
	}

	rows, err := postgress.Exec(
		`UPDATE room_members SET status = $1
		 WHERE room_id = $2 AND user_id = $3 AND status = $4`,
		config.RoomMemberActive, roomID, userID, config.RoomMemberPending,
	)
	if err != nil {
		JSONError(w, "Failed to accept", http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		JSONError(w, "No pending DM request for this room", http.StatusNotFound)
		return
	}

	// Notify the sender that their DM was accepted
	var senderID string
	postgress.GetRawDB().QueryRow(
		`SELECT user_id FROM room_members WHERE room_id = $1 AND user_id != $2 LIMIT 1`,
		roomID, userID,
	).Scan(&senderID)

	// Auto-subscribe the accepting user to the now-active room
	if e := chat.GetEngine(); e != nil {
		e.JoinRoomForUser(userID, roomID)
	}

	if senderID != "" {
		notifyUser(context.Background(), senderID, map[string]interface{}{
			config.FieldType:   config.MsgTypeDMAccepted,
			config.FieldRoomID: roomID,
		})
	}

	JSONMessage(w, "success", "DM request accepted")
}

// ---------------------------------------------------------------------------
// Reject DM Request
//
// POST /api/v1/rooms/{roomId}/reject (requires auth)
// Deletes the room and all its members/messages.
// ---------------------------------------------------------------------------

func RejectDMRequestHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	roomID := r.PathValue("roomId")
	if roomID == "" {
		JSONError(w, "Missing room ID", http.StatusBadRequest)
		return
	}

	// Verify the caller has a pending membership in this room
	var exists bool
	postgress.GetRawDB().QueryRow(
		`SELECT EXISTS (SELECT 1 FROM room_members WHERE room_id = $1 AND user_id = $2 AND status = $3)`,
		roomID, userID, config.RoomMemberPending,
	).Scan(&exists)

	if !exists {
		JSONError(w, "No pending DM request for this room", http.StatusNotFound)
		return
	}

	// Unsubscribe all members from this room before deleting
	if e := chat.GetEngine(); e != nil {
		// Find the other member to unsubscribe them too
		var otherUserID string
		postgress.GetRawDB().QueryRow(
			`SELECT user_id FROM room_members WHERE room_id = $1 AND user_id != $2 LIMIT 1`,
			roomID, userID,
		).Scan(&otherUserID)
		e.LeaveRoomForUser(userID, roomID)
		if otherUserID != "" {
			e.LeaveRoomForUser(otherUserID, roomID)
		}
	}

	// Delete the room (cascades to room_members and messages)
	postgress.Exec(`DELETE FROM rooms WHERE id = $1`, roomID)

	JSONMessage(w, "success", "DM request rejected")
}
