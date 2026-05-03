package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/shivanand-burli/go-starter-kit/helper"
	"github.com/shivanand-burli/go-starter-kit/middleware"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
)

// GetDMRequestsHandler returns pending DM requests (Instagram-style "Message Requests" inbox).
func GetDMRequestsHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	limit, offset := parsePagination(r)

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Cache DM requests per user+page for 15s
	cacheKey := fmt.Sprintf("%s%s:%d:%d", config.CacheDMRequests, userID, limit, offset)
	rdb := redis.GetRawClient()
	if cached, err := rdb.Get(ctx, cacheKey).Bytes(); err == nil && len(cached) > 0 {
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
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
				COALESCE(r.last_message_preview, '') AS last_message_preview
			FROM room_members my_rm
			JOIN rooms r ON my_rm.room_id = r.id
			JOIN room_members other_rm ON other_rm.room_id = r.id AND other_rm.user_id != $1
			JOIN users u ON u.id = other_rm.user_id
			WHERE my_rm.user_id = $1 AND my_rm.status = 'pending'
			ORDER BY r.last_message_at DESC NULLS LAST
			LIMIT $2 OFFSET $3
		) t
	`

	var raw []byte
	err := postgress.GetPool().QueryRow(ctx, query, userID, limit, offset).Scan(&raw)
	if err != nil {
		log.Printf("[dm] GetDMRequests query failed user=%s: %v", userID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to fetch DM requests")
		return
	}

	rdb.Set(ctx, cacheKey, raw, config.CacheTTLShort)

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
}

// AcceptDMRequestHandler accepts a pending DM request.
func AcceptDMRequestHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	roomID := r.PathValue("roomId")
	if roomID == "" {
		helper.Error(w, http.StatusBadRequest, "Missing room ID")
		return
	}

	rows, err := postgress.Exec(ctx,
		`UPDATE room_members SET status = $1
		 WHERE room_id = $2 AND user_id = $3 AND status = $4`,
		config.RoomMemberActive, roomID, userID, config.RoomMemberPending,
	)
	if err != nil {
		log.Printf("[dm] AcceptDMRequest update failed room=%s user=%s: %v", roomID, userID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to accept")
		return
	}
	if rows == 0 {
		helper.Error(w, http.StatusNotFound, "No pending DM request for this room")
		return
	}

	// Notify the sender that their DM was accepted
	var senderID string
	err = postgress.GetPool().QueryRow(ctx,
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

	helper.JSON(w, http.StatusOK, map[string]string{"status": "success", "message": "DM request accepted"})
}

// RejectDMRequestHandler rejects a pending DM request and deletes the room.
func RejectDMRequestHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	roomID := r.PathValue("roomId")
	if roomID == "" {
		helper.Error(w, http.StatusBadRequest, "Missing room ID")
		return
	}

	// Verify the caller has a pending membership in this room
	var exists bool
	err := postgress.GetPool().QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM room_members WHERE room_id = $1 AND user_id = $2 AND status = $3)`,
		roomID, userID, config.RoomMemberPending,
	).Scan(&exists)
	if err != nil {
		log.Printf("[dm] RejectDMRequest EXISTS check failed room=%s user=%s: %v", roomID, userID, err)
		helper.Error(w, http.StatusInternalServerError, "Database error")
		return
	}

	if !exists {
		helper.Error(w, http.StatusNotFound, "No pending DM request for this room")
		return
	}

	// Unsubscribe all members from this room before deleting
	if e := chat.GetEngine(); e != nil {
		// Find the other member to unsubscribe them too
		var otherUserID string
		if qErr := postgress.GetPool().QueryRow(ctx,
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
	if _, err := postgress.Exec(ctx, `DELETE FROM rooms WHERE id = $1`, roomID); err != nil {
		log.Printf("[dm] Failed to delete room %s: %v", roomID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to reject DM request")
		return
	}

	helper.JSON(w, http.StatusOK, map[string]string{"status": "success", "message": "DM request rejected"})
}
