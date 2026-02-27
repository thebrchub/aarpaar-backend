package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/models"
)

// ---------------------------------------------------------------------------
// POST /api/v1/groups — Create a new group
// ---------------------------------------------------------------------------

func CreateGroupHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req models.CreateGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		JSONError(w, "Group name is required", http.StatusBadRequest)
		return
	}
	if len(req.Name) > 100 {
		JSONError(w, "Group name must be 100 characters or fewer", http.StatusBadRequest)
		return
	}

	// Normalize visibility — default to public
	visibility := config.VisibilityPublic
	if req.Visibility == config.VisibilityPrivate {
		visibility = config.VisibilityPrivate
	}

	// Generate invite code for the group
	inviteCode := generateInviteCode()

	// Limit initial members (don't include creator in count)
	if len(req.MemberIDs) > 49 { // 49 + creator = 50
		JSONError(w, "Too many initial members (max 49 plus yourself)", http.StatusBadRequest)
		return
	}

	// De-duplicate member IDs and remove self if accidentally included
	seen := map[string]bool{userID: true}
	uniqueMembers := make([]string, 0, len(req.MemberIDs))
	for _, id := range req.MemberIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		uniqueMembers = append(uniqueMembers, id)
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Only validate members if any were provided
	var finalMembers []string
	if len(uniqueMembers) > 0 {
		// Validate all member IDs exist and are not banned
		placeholders, args := buildINClause(uniqueMembers, 1)
		query := fmt.Sprintf(
			`SELECT id FROM users WHERE id IN (%s) AND is_banned = false`, placeholders,
		)
		rows, err := postgress.GetRawDB().QueryContext(ctx, query, args...)
		if err != nil {
			JSONError(w, "Database error", http.StatusInternalServerError)
			return
		}
		validMembers := make(map[string]bool)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				validMembers[id] = true
			}
		}
		rows.Close()

		if len(validMembers) != len(uniqueMembers) {
			JSONError(w, "One or more member IDs are invalid", http.StatusBadRequest)
			return
		}

		// Check for blocks: remove any member who has blocked the creator or vice versa
		blockPlaceholders, blockArgs := buildINClause(uniqueMembers, 1)
		blockArgs = append(blockArgs, userID)
		blockQuery := fmt.Sprintf(
			`SELECT DISTINCT CASE WHEN blocker_id = $%d THEN blocked_id ELSE blocker_id END
		 FROM blocked_users
		 WHERE (blocker_id = $%d AND blocked_id IN (%s))
		    OR (blocked_id = $%d AND blocker_id IN (%s))`,
			len(blockArgs), len(blockArgs), blockPlaceholders, len(blockArgs), blockPlaceholders,
		)
		blockRows, err := postgress.GetRawDB().QueryContext(ctx, blockQuery, blockArgs...)
		if err != nil {
			JSONError(w, "Database error", http.StatusInternalServerError)
			return
		}
		blocked := make(map[string]bool)
		for blockRows.Next() {
			var id string
			if err := blockRows.Scan(&id); err == nil {
				blocked[id] = true
			}
		}
		blockRows.Close()

		// Filter out blocked users
		for _, id := range uniqueMembers {
			if !blocked[id] {
				finalMembers = append(finalMembers, id)
			}
		}
	} // end if len(uniqueMembers) > 0

	// Create room + members in a transaction
	tx, err := postgress.GetRawDB().Begin()
	if err != nil {
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var roomID string
	err = tx.QueryRow(
		`INSERT INTO rooms (name, type, avatar_url, created_by, max_members, visibility, invite_code)
		 VALUES ($1, $2, $3, $4, 50, $5, $6)
		 RETURNING id`,
		req.Name, config.RoomTypeGroup, req.AvatarURL, userID, visibility, inviteCode,
	).Scan(&roomID)
	if err != nil {
		JSONError(w, "Failed to create group", http.StatusInternalServerError)
		return
	}

	// Insert creator as admin
	_, err = tx.Exec(
		`INSERT INTO room_members (room_id, user_id, status, role, joined_at)
		 VALUES ($1, $2, 'active', 'admin', NOW())`,
		roomID, userID,
	)
	if err != nil {
		JSONError(w, "Failed to add creator to group", http.StatusInternalServerError)
		return
	}

	// Batch-insert all other members in a single multi-row INSERT
	if len(finalMembers) > 0 {
		insertSQL := `INSERT INTO room_members (room_id, user_id, status, role, joined_at) VALUES `
		args := make([]interface{}, 0, len(finalMembers)*2)
		for i, memberID := range finalMembers {
			if i > 0 {
				insertSQL += ", "
			}
			insertSQL += fmt.Sprintf("($%d, $%d, 'active', 'member', NOW())", i*2+1, i*2+2)
			args = append(args, roomID, memberID)
		}
		if _, err = tx.Exec(insertSQL, args...); err != nil {
			JSONError(w, "Failed to add members to group", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		JSONError(w, "Failed to create group", http.StatusInternalServerError)
		return
	}

	// Auto-subscribe all online members to the room
	if e := chat.GetEngine(); e != nil {
		e.JoinRoomForUser(userID, roomID)
		for _, memberID := range finalMembers {
			e.JoinRoomForUser(memberID, roomID)
		}
	}

	// Broadcast group_created event to all members
	allMembers := append([]string{userID}, finalMembers...)
	broadcastGroupEvent(ctx, roomID, config.MsgTypeGroupCreated, map[string]interface{}{
		config.FieldRoomID:  roomID,
		config.FieldName:    req.Name,
		config.FieldMembers: allMembers,
	}, allMembers)

	JSONSuccess(w, map[string]interface{}{
		"roomId":     roomID,
		"name":       req.Name,
		"visibility": visibility,
		"inviteCode": inviteCode,
	})
}

// ---------------------------------------------------------------------------
// GET /api/v1/groups/{groupId} — Get group info + member list
// ---------------------------------------------------------------------------

func GetGroupHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		JSONError(w, "Missing group ID", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Verify requester is an active member
	if !isGroupMember(ctx, groupID, userID) {
		JSONError(w, "Not a member of this group", http.StatusForbidden)
		return
	}

	// Fetch group info
	var name, avatarURL, createdBy, visibility string
	var inviteCode *string
	var maxMembers int
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT COALESCE(name,''), COALESCE(avatar_url,''), COALESCE(created_by::text,''), max_members,
		        COALESCE(visibility,'public'), invite_code
		 FROM rooms WHERE id = $1 AND type = 'GROUP'`, groupID,
	).Scan(&name, &avatarURL, &createdBy, &maxMembers, &visibility, &inviteCode)
	if err != nil {
		JSONError(w, "Group not found", http.StatusNotFound)
		return
	}

	// Fetch members
	memberRows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT u.id, u.username, u.name, COALESCE(u.avatar_url,''), rm.role
		 FROM room_members rm
		 JOIN users u ON u.id = rm.user_id
		 WHERE rm.room_id = $1 AND rm.status = 'active'`,
		groupID,
	)
	if err != nil {
		JSONError(w, "Failed to fetch members", http.StatusInternalServerError)
		return
	}
	defer memberRows.Close()

	e := chat.GetEngine()
	var members []models.GroupMember
	for memberRows.Next() {
		var m models.GroupMember
		if err := memberRows.Scan(&m.ID, &m.Username, &m.Name, &m.AvatarURL, &m.Role); err != nil {
			continue
		}
		if e != nil {
			m.IsOnline = e.IsUserOnline(m.ID)
		}
		members = append(members, m)
	}
	if members == nil {
		members = []models.GroupMember{}
	}

	ic := ""
	if inviteCode != nil {
		ic = *inviteCode
	}

	JSONSuccess(w, models.GroupResponse{
		RoomID:      groupID,
		Name:        name,
		AvatarURL:   avatarURL,
		Type:        config.RoomTypeGroup,
		CreatedBy:   createdBy,
		MaxMembers:  maxMembers,
		Visibility:  visibility,
		InviteCode:  ic,
		MemberCount: len(members),
		Members:     members,
	})
}

// ---------------------------------------------------------------------------
// PATCH /api/v1/groups/{groupId} — Update group name/avatar (admin only)
// ---------------------------------------------------------------------------

func UpdateGroupHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		JSONError(w, "Missing group ID", http.StatusBadRequest)
		return
	}

	var req models.UpdateGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Check admin
	if !isGroupAdmin(ctx, groupID, userID) {
		JSONError(w, "Only admins can update the group", http.StatusForbidden)
		return
	}

	if req.Name != nil && len(*req.Name) > 100 {
		JSONError(w, "Group name must be 100 characters or fewer", http.StatusBadRequest)
		return
	}

	// Build update query dynamically
	if req.Name != nil {
		postgress.GetRawDB().ExecContext(ctx,
			`UPDATE rooms SET name = $1 WHERE id = $2`, *req.Name, groupID)
	}
	if req.AvatarURL != nil {
		postgress.GetRawDB().ExecContext(ctx,
			`UPDATE rooms SET avatar_url = $1 WHERE id = $2`, *req.AvatarURL, groupID)
	}
	if req.Visibility != nil {
		v := *req.Visibility
		if v == config.VisibilityPublic || v == config.VisibilityPrivate {
			postgress.GetRawDB().ExecContext(ctx,
				`UPDATE rooms SET visibility = $1 WHERE id = $2`, v, groupID)
		}
	}

	// Broadcast group_updated to all members
	memberIDs := getActiveGroupMemberIDs(ctx, groupID)
	updateData := map[string]interface{}{
		config.FieldRoomID: groupID,
	}
	if req.Name != nil {
		updateData[config.FieldName] = *req.Name
	}
	if req.AvatarURL != nil {
		updateData[config.FieldAvatarURL] = *req.AvatarURL
	}
	if req.Visibility != nil {
		updateData[config.FieldVisibility] = *req.Visibility
	}
	broadcastGroupEvent(ctx, groupID, config.MsgTypeGroupUpdated, updateData, memberIDs)

	JSONMessage(w, "ok", "Group updated")
}

// ---------------------------------------------------------------------------
// POST /api/v1/groups/{groupId}/members — Add members (admin only)
// ---------------------------------------------------------------------------

func AddGroupMembersHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		JSONError(w, "Missing group ID", http.StatusBadRequest)
		return
	}

	var req models.AddMembersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.MemberIDs) == 0 {
		JSONError(w, "No members specified", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Check admin
	if !isGroupAdmin(ctx, groupID, userID) {
		JSONError(w, "Only admins can add members", http.StatusForbidden)
		return
	}

	// Check max_members
	var maxMembers, currentCount int
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT r.max_members, COUNT(rm.user_id)
		 FROM rooms r
		 LEFT JOIN room_members rm ON rm.room_id = r.id AND rm.status = 'active'
		 WHERE r.id = $1 AND r.type = 'GROUP'
		 GROUP BY r.max_members`, groupID,
	).Scan(&maxMembers, &currentCount)
	if err != nil {
		JSONError(w, "Group not found", http.StatusNotFound)
		return
	}

	if currentCount+len(req.MemberIDs) > maxMembers {
		JSONError(w, fmt.Sprintf("Adding these members would exceed the group limit of %d", maxMembers), http.StatusBadRequest)
		return
	}

	// Validate members exist and are not banned
	placeholders, args := buildINClause(req.MemberIDs, 1)
	query := fmt.Sprintf(
		`SELECT id FROM users WHERE id IN (%s) AND is_banned = false`, placeholders,
	)
	rows, err := postgress.GetRawDB().QueryContext(ctx, query, args...)
	if err != nil {
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}
	validIDs := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			validIDs[id] = true
		}
	}
	rows.Close()

	// Check blocks between adder and new members
	blocked := getBlockedBetween(ctx, userID, req.MemberIDs)

	added := make([]string, 0)
	for _, memberID := range req.MemberIDs {
		if !validIDs[memberID] || blocked[memberID] {
			continue
		}

		// Upsert: if member previously left, reactivate them
		_, err := postgress.GetRawDB().ExecContext(ctx,
			`INSERT INTO room_members (room_id, user_id, status, role, joined_at)
			 VALUES ($1, $2, 'active', 'member', NOW())
			 ON CONFLICT (room_id, user_id)
			 DO UPDATE SET status = 'active', role = 'member', joined_at = NOW(), left_at = NULL`,
			groupID, memberID,
		)
		if err != nil {
			continue
		}
		added = append(added, memberID)
	}

	// Subscribe new members to the room
	if e := chat.GetEngine(); e != nil {
		for _, id := range added {
			e.JoinRoomForUser(id, groupID)
		}
	}

	// Notify all group members about newly added members
	allMembers := getActiveGroupMemberIDs(ctx, groupID)
	for _, addedID := range added {
		broadcastGroupEvent(ctx, groupID, config.MsgTypeMemberAdded, map[string]interface{}{
			config.FieldRoomID:  groupID,
			config.FieldUserID:  addedID,
			config.FieldAddedBy: userID,
		}, allMembers)
	}

	JSONSuccess(w, map[string]interface{}{
		"added": added,
	})
}

// ---------------------------------------------------------------------------
// DELETE /api/v1/groups/{groupId}/members/{userId} — Remove member or leave
// ---------------------------------------------------------------------------

func RemoveGroupMemberHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	groupID := r.PathValue("groupId")
	targetID := r.PathValue("userId")
	if groupID == "" || targetID == "" {
		JSONError(w, "Missing group or user ID", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	isSelf := userID == targetID

	if !isSelf {
		// Only admins can remove others
		if !isGroupAdmin(ctx, groupID, userID) {
			JSONError(w, "Only admins can remove members", http.StatusForbidden)
			return
		}
	} else {
		// Self-leave: just verify membership
		if !isGroupMember(ctx, groupID, userID) {
			JSONError(w, "Not a member of this group", http.StatusForbidden)
			return
		}
	}

	// Deactivate membership
	_, err := postgress.GetRawDB().ExecContext(ctx,
		`UPDATE room_members SET status = 'inactive', left_at = NOW()
		 WHERE room_id = $1 AND user_id = $2 AND status = 'active'`,
		groupID, targetID,
	)
	if err != nil {
		JSONError(w, "Failed to remove member", http.StatusInternalServerError)
		return
	}

	// Unsubscribe from the room
	if e := chat.GetEngine(); e != nil {
		e.LeaveRoomForUser(targetID, groupID)
	}

	// Broadcast appropriate event
	allMembers := getActiveGroupMemberIDs(ctx, groupID)
	// Include the removed user so they also get the event
	allMembers = append(allMembers, targetID)

	if isSelf {
		broadcastGroupEvent(ctx, groupID, config.MsgTypeMemberLeft, map[string]interface{}{
			config.FieldRoomID: groupID,
			config.FieldUserID: targetID,
		}, allMembers)
	} else {
		broadcastGroupEvent(ctx, groupID, config.MsgTypeMemberRemoved, map[string]interface{}{
			config.FieldRoomID:    groupID,
			config.FieldUserID:    targetID,
			config.FieldRemovedBy: userID,
		}, allMembers)
	}

	JSONMessage(w, "ok", "Member removed")
}

// ---------------------------------------------------------------------------
// POST /api/v1/groups/{groupId}/admins — Promote member to admin
// ---------------------------------------------------------------------------

func PromoteAdminHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		JSONError(w, "Missing group ID", http.StatusBadRequest)
		return
	}

	var req models.PromoteAdminRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.UserID == "" {
		JSONError(w, "userId is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	if !isGroupAdmin(ctx, groupID, userID) {
		JSONError(w, "Only admins can promote members", http.StatusForbidden)
		return
	}

	res, err := postgress.GetRawDB().ExecContext(ctx,
		`UPDATE room_members SET role = 'admin'
		 WHERE room_id = $1 AND user_id = $2 AND status = 'active'`,
		groupID, req.UserID,
	)
	if err != nil {
		JSONError(w, "Failed to promote member", http.StatusInternalServerError)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		JSONError(w, "User is not an active member of this group", http.StatusBadRequest)
		return
	}

	// Broadcast promotion event to all group members
	memberIDs := getActiveGroupMemberIDs(ctx, groupID)
	broadcastGroupEvent(ctx, groupID, config.MsgTypeMemberPromoted, map[string]interface{}{
		config.FieldRoomID: groupID,
		config.FieldUserID: req.UserID,
		config.FieldRole:   config.RoleAdmin,
	}, memberIDs)

	JSONMessage(w, "ok", "Member promoted to admin")
}

// ---------------------------------------------------------------------------
// DELETE /api/v1/groups/{groupId} — Delete group (original creator only)
// ---------------------------------------------------------------------------

func DeleteGroupHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		JSONError(w, "Missing group ID", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Only the original creator can delete the group
	var createdBy string
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT COALESCE(created_by::text, '') FROM rooms WHERE id = $1 AND type = 'GROUP'`, groupID,
	).Scan(&createdBy)
	if err != nil {
		JSONError(w, "Group not found", http.StatusNotFound)
		return
	}
	if createdBy != userID {
		JSONError(w, "Only the group creator can delete the group", http.StatusForbidden)
		return
	}

	// Clean up active call if one exists
	rdb := redis.GetRawClient()
	callState, callErr := loadGroupCallState(ctx, rdb, groupID)
	if callErr == nil && callState != nil {
		// End the LiveKit room
		if RTC != nil && RTC.IsConfigured() && callState.LKRoomName != "" {
			_ = RTC.DeleteRoom(ctx, callState.LKRoomName)
		}
		// Delete Redis key
		rdb.Del(ctx, config.GROUP_CALL_COLON+groupID)
		// Update call_logs
		if callState.CallID != "" {
			duration := int(time.Since(callState.StartedAt).Seconds())
			postgress.GetRawDB().ExecContext(ctx,
				`UPDATE call_logs SET ended_at = NOW(), duration_seconds = $2
				 WHERE call_id = $1 AND ended_at IS NULL`,
				callState.CallID, duration,
			)
		}
	}

	// Get all members before deleting (for notification)
	memberIDs := getActiveGroupMemberIDs(ctx, groupID)

	// Close the room on the engine (unsubscribe everyone)
	if e := chat.GetEngine(); e != nil {
		closedPayload, _ := json.Marshal(map[string]string{
			config.FieldType:   config.MsgTypeRoomClosed,
			config.FieldRoomID: groupID,
		})
		e.CloseRoom(groupID, closedPayload)
	}

	// Delete the room (CASCADE will remove room_members and messages)
	_, err = postgress.GetRawDB().ExecContext(ctx,
		`DELETE FROM rooms WHERE id = $1`, groupID,
	)
	if err != nil {
		JSONError(w, "Failed to delete group", http.StatusInternalServerError)
		return
	}

	// Notify members that the room was closed
	_ = memberIDs // Already notified via CloseRoom above

	JSONMessage(w, "ok", "Group deleted")
}

// ---------------------------------------------------------------------------
// GET /api/v1/groups — List / search public groups
// ---------------------------------------------------------------------------

func ListGroupsHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	search := strings.TrimSpace(r.URL.Query().Get("search"))

	baseQuery := `SELECT r.id, COALESCE(r.name,''), COALESCE(r.avatar_url,''),
		        COALESCE(r.visibility,'public'), COALESCE(r.created_by::text,''),
		        (SELECT COUNT(*) FROM room_members rm2 WHERE rm2.room_id = r.id AND rm2.status = 'active') AS member_count,
		        EXISTS(SELECT 1 FROM room_members rm3 WHERE rm3.room_id = r.id AND rm3.user_id = $1 AND rm3.status = 'active') AS is_member
		 FROM rooms r
		 WHERE r.type = 'GROUP' AND r.visibility = 'public'`

	var args []interface{}
	args = append(args, userID)

	if search != "" {
		baseQuery += ` AND LOWER(r.name) LIKE LOWER('%' || $2 || '%')`
		args = append(args, search)
	}
	baseQuery += ` ORDER BY member_count DESC LIMIT 50`

	rows, err := postgress.GetRawDB().QueryContext(ctx, baseQuery, args...)
	if err != nil {
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var groups []models.GroupListItem
	for rows.Next() {
		var g models.GroupListItem
		if err := rows.Scan(&g.RoomID, &g.Name, &g.AvatarURL, &g.Visibility, &g.CreatedBy, &g.MemberCount, &g.IsMember); err != nil {
			continue
		}
		groups = append(groups, g)
	}
	if groups == nil {
		groups = []models.GroupListItem{}
	}

	JSONSuccess(w, groups)
}

// ---------------------------------------------------------------------------
// POST /api/v1/groups/{groupId}/join — Self-join a public group
// ---------------------------------------------------------------------------

func JoinGroupHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		JSONError(w, "Missing group ID", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Check the group exists, is a GROUP, and is public
	var visibility string
	var maxMembers, currentCount int
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT COALESCE(r.visibility,'public'), r.max_members,
		        (SELECT COUNT(*) FROM room_members rm WHERE rm.room_id = r.id AND rm.status = 'active')
		 FROM rooms r WHERE r.id = $1 AND r.type = 'GROUP'`, groupID,
	).Scan(&visibility, &maxMembers, &currentCount)
	if err != nil {
		JSONError(w, "Group not found", http.StatusNotFound)
		return
	}

	if visibility != config.VisibilityPublic {
		JSONError(w, "This group is private — use an invite link to join", http.StatusForbidden)
		return
	}

	// Check if already a member
	if isGroupMember(ctx, groupID, userID) {
		JSONError(w, "You are already a member of this group", http.StatusConflict)
		return
	}

	if currentCount >= maxMembers {
		JSONError(w, "Group is full", http.StatusBadRequest)
		return
	}

	// Check if user is banned
	var isBanned bool
	postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT is_banned FROM users WHERE id = $1`, userID,
	).Scan(&isBanned)
	if isBanned {
		JSONError(w, "Account is banned", http.StatusForbidden)
		return
	}

	// Upsert membership (handles re-joining after leaving)
	_, err = postgress.GetRawDB().ExecContext(ctx,
		`INSERT INTO room_members (room_id, user_id, status, role, joined_at)
		 VALUES ($1, $2, 'active', 'member', NOW())
		 ON CONFLICT (room_id, user_id)
		 DO UPDATE SET status = 'active', role = 'member', joined_at = NOW(), left_at = NULL`,
		groupID, userID,
	)
	if err != nil {
		JSONError(w, "Failed to join group", http.StatusInternalServerError)
		return
	}

	// Subscribe to room
	if e := chat.GetEngine(); e != nil {
		e.JoinRoomForUser(userID, groupID)
	}

	// Notify all members
	allMembers := getActiveGroupMemberIDs(ctx, groupID)
	broadcastGroupEvent(ctx, groupID, config.MsgTypeMemberJoined, map[string]interface{}{
		config.FieldRoomID: groupID,
		config.FieldUserID: userID,
	}, allMembers)

	JSONMessage(w, "ok", "Joined group successfully")
}

// ---------------------------------------------------------------------------
// POST /api/v1/groups/invite/{inviteCode} — Join a group via invite link
// ---------------------------------------------------------------------------

func JoinGroupByInviteHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	code := r.PathValue("inviteCode")
	if code == "" {
		JSONError(w, "Missing invite code", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Look up the group by invite code
	var groupID string
	var maxMembers, currentCount int
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT r.id, r.max_members,
		        (SELECT COUNT(*) FROM room_members rm WHERE rm.room_id = r.id AND rm.status = 'active')
		 FROM rooms r WHERE r.invite_code = $1 AND r.type = 'GROUP'`, code,
	).Scan(&groupID, &maxMembers, &currentCount)
	if err != nil {
		JSONError(w, "Invalid or expired invite code", http.StatusNotFound)
		return
	}

	// Check if already a member
	if isGroupMember(ctx, groupID, userID) {
		JSONError(w, "You are already a member of this group", http.StatusConflict)
		return
	}

	if currentCount >= maxMembers {
		JSONError(w, "Group is full", http.StatusBadRequest)
		return
	}

	// Upsert membership
	_, err = postgress.GetRawDB().ExecContext(ctx,
		`INSERT INTO room_members (room_id, user_id, status, role, joined_at)
		 VALUES ($1, $2, 'active', 'member', NOW())
		 ON CONFLICT (room_id, user_id)
		 DO UPDATE SET status = 'active', role = 'member', joined_at = NOW(), left_at = NULL`,
		groupID, userID,
	)
	if err != nil {
		JSONError(w, "Failed to join group", http.StatusInternalServerError)
		return
	}

	// Subscribe to room
	if e := chat.GetEngine(); e != nil {
		e.JoinRoomForUser(userID, groupID)
	}

	// Notify all members
	allMembers := getActiveGroupMemberIDs(ctx, groupID)
	broadcastGroupEvent(ctx, groupID, config.MsgTypeMemberJoined, map[string]interface{}{
		config.FieldRoomID: groupID,
		config.FieldUserID: userID,
	}, allMembers)

	JSONSuccess(w, map[string]interface{}{
		"roomId":  groupID,
		"message": "Joined group via invite link",
	})
}

// ---------------------------------------------------------------------------
// POST /api/v1/groups/{groupId}/invite — Generate or regenerate invite code
// ---------------------------------------------------------------------------

func GenerateInviteHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		JSONError(w, "Missing group ID", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	if !isGroupAdmin(ctx, groupID, userID) {
		JSONError(w, "Only admins can generate invite codes", http.StatusForbidden)
		return
	}

	newCode := generateInviteCode()
	_, err := postgress.GetRawDB().ExecContext(ctx,
		`UPDATE rooms SET invite_code = $1 WHERE id = $2 AND type = 'GROUP'`, newCode, groupID,
	)
	if err != nil {
		JSONError(w, "Failed to generate invite code", http.StatusInternalServerError)
		return
	}

	JSONSuccess(w, map[string]interface{}{
		"inviteCode": newCode,
	})
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

// isGroupMember checks if a user is an active member of a group room.
func isGroupMember(ctx context.Context, groupID, userID string) bool {
	var exists bool
	postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM room_members rm
			JOIN rooms r ON r.id = rm.room_id
			WHERE rm.room_id = $1 AND rm.user_id = $2
			AND rm.status = 'active' AND r.type = 'GROUP'
		)`, groupID, userID,
	).Scan(&exists)
	return exists
}

// isGroupAdmin checks if a user is an admin of a group room.
func isGroupAdmin(ctx context.Context, groupID, userID string) bool {
	var exists bool
	postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM room_members rm
			JOIN rooms r ON r.id = rm.room_id
			WHERE rm.room_id = $1 AND rm.user_id = $2
			AND rm.status = 'active' AND rm.role = 'admin' AND r.type = 'GROUP'
		)`, groupID, userID,
	).Scan(&exists)
	return exists
}

// getActiveGroupMemberIDs returns all active member user IDs for a group.
func getActiveGroupMemberIDs(ctx context.Context, groupID string) []string {
	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT user_id FROM room_members WHERE room_id = $1 AND status = 'active'`, groupID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// getBlockedBetween returns a set of user IDs from `others` that are blocked
// by or have blocked `userID`.
func getBlockedBetween(ctx context.Context, userID string, others []string) map[string]bool {
	if len(others) == 0 {
		return nil
	}
	placeholders, args := buildINClause(others, 1)
	args = append(args, userID)
	uidParam := fmt.Sprintf("$%d", len(args))

	query := fmt.Sprintf(
		`SELECT DISTINCT CASE WHEN blocker_id = %s THEN blocked_id ELSE blocker_id END
		 FROM blocked_users
		 WHERE (blocker_id = %s AND blocked_id IN (%s))
		    OR (blocked_id = %s AND blocker_id IN (%s))`,
		uidParam, uidParam, placeholders, uidParam, placeholders,
	)

	rows, err := postgress.GetRawDB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			result[id] = true
		}
	}
	return result
}

// buildINClause creates a ($1, $2, $3, ...) placeholder string and args
// for use in SQL IN clauses. startIdx is the first parameter number.
func buildINClause(ids []string, startIdx int) (string, []interface{}) {
	var placeholders string
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		if i > 0 {
			placeholders += ","
		}
		placeholders += fmt.Sprintf("$%d", startIdx+i)
		args[i] = id
	}
	return placeholders, args
}

// broadcastGroupEvent sends a WebSocket event to a list of users via Redis Pub/Sub.
// For group lifecycle events (group_created, member_added, etc.)
func broadcastGroupEvent(ctx context.Context, roomID string, eventType string, data map[string]interface{}, targetUserIDs []string) {
	if len(targetUserIDs) == 0 {
		return
	}

	data[config.FieldType] = eventType

	envelope := map[string]interface{}{
		config.FieldType: config.MsgTypePrivate,
		config.FieldFrom: config.SystemSender,
		"targets":        targetUserIDs,
		config.FieldData: data,
	}

	envBytes, err := json.Marshal(envelope)
	if err != nil {
		return
	}

	pubCtx, cancel := context.WithTimeout(ctx, config.RedisOpTimeout)
	defer cancel()
	redis.Publish(pubCtx, config.CHAT_GLOBAL_CHANNEL, envBytes)
}

// generateInviteCode creates a random 8-byte hex invite code (16 chars).
func generateInviteCode() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use time-based value (shouldn't happen)
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))[:16]
	}
	return hex.EncodeToString(b)
}
