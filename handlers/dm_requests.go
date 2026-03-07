package handlers

import (
	"context"
	"log"
	"net/http"

	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
)

// GetDMRequestsHandler returns pending DM requests (Instagram-style "Message Requests" inbox).
//
// @Summary		Get DM requests
// @Description	Returns rooms where the caller has a pending DM invitation from a private account.
// @Tags		Rooms
// @Produce		json
// @Success		200	{array}	DMRequestItem
// @Failure		401	{object}	StatusMessage
// @Failure		500	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/rooms/requests [get]
func GetDMRequestsHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	limit, offset := parsePagination(r)

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
			LIMIT $2 OFFSET $3
		) t
	`

	var raw []byte
	err := postgress.GetRawDB().QueryRow(query, userID, limit, offset).Scan(&raw)
	if err != nil {
		log.Printf("[dm] GetDMRequests query failed user=%s: %v", userID, err)
		JSONError(w, "Failed to fetch DM requests", http.StatusInternalServerError)
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
}

// AcceptDMRequestHandler accepts a pending DM request.
//
// @Summary		Accept DM request
// @Description	Flips room membership from pending to active. Notifies the sender.
// @Tags		Rooms
// @Produce		json
// @Param		roomId	path	string	true	"Room UUID"
// @Success		200	{object}	StatusMessage
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Failure		404	{object}	StatusMessage
// @Failure		500	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/rooms/{roomId}/accept [post]
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
		log.Printf("[dm] AcceptDMRequest update failed room=%s user=%s: %v", roomID, userID, err)
		JSONError(w, "Failed to accept", http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		JSONError(w, "No pending DM request for this room", http.StatusNotFound)
		return
	}

	// Notify the sender that their DM was accepted
	var senderID string
	err = postgress.GetRawDB().QueryRow(
		`SELECT user_id FROM room_members WHERE room_id = $1 AND user_id != $2 LIMIT 1`,
		roomID, userID,
	).Scan(&senderID)
	if err != nil {
		log.Printf("[dm] Failed to find sender for room=%s: %v", roomID, err)
	}

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

// RejectDMRequestHandler rejects a pending DM request and deletes the room.
//
// @Summary		Reject DM request
// @Description	Deletes the room and all its members/messages.
// @Tags		Rooms
// @Produce		json
// @Param		roomId	path	string	true	"Room UUID"
// @Success		200	{object}	StatusMessage
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Failure		404	{object}	StatusMessage
// @Failure		500	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/rooms/{roomId}/reject [post]
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
	err := postgress.GetRawDB().QueryRow(
		`SELECT EXISTS (SELECT 1 FROM room_members WHERE room_id = $1 AND user_id = $2 AND status = $3)`,
		roomID, userID, config.RoomMemberPending,
	).Scan(&exists)
	if err != nil {
		log.Printf("[dm] RejectDMRequest EXISTS check failed room=%s user=%s: %v", roomID, userID, err)
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}

	if !exists {
		JSONError(w, "No pending DM request for this room", http.StatusNotFound)
		return
	}

	// Unsubscribe all members from this room before deleting
	if e := chat.GetEngine(); e != nil {
		// Find the other member to unsubscribe them too
		var otherUserID string
		if qErr := postgress.GetRawDB().QueryRow(
			`SELECT user_id FROM room_members WHERE room_id = $1 AND user_id != $2 LIMIT 1`,
			roomID, userID,
		).Scan(&otherUserID); qErr != nil {
			log.Printf("[dm] Failed to find other member for room=%s: %v", roomID, qErr)
		}
		e.LeaveRoomForUser(userID, roomID)
		if otherUserID != "" {
			e.LeaveRoomForUser(otherUserID, roomID)
		}
	}

	// Delete the room (cascades to room_members and messages)
	if _, err := postgress.Exec(`DELETE FROM rooms WHERE id = $1`, roomID); err != nil {
		log.Printf("[dm] Failed to delete room %s: %v", roomID, err)
		JSONError(w, "Failed to reject DM request", http.StatusInternalServerError)
		return
	}

	JSONMessage(w, "success", "DM request rejected")
}
