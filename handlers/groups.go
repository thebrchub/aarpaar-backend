package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/helper"
	"github.com/shivanand-burli/go-starter-kit/middleware"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/models"
	"github.com/thebrchub/aarpaar/services"
)

// CreateGroupHandler creates a new group.
func CreateGroupHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var req models.CreateGroupRequest
	if err := helper.ReadJSON(r, &req); err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Name == "" {
		helper.Error(w, http.StatusBadRequest, "Group name is required")
		return
	}
	if len(req.Name) > 100 {
		helper.Error(w, http.StatusBadRequest, "Group name must be 100 characters or fewer")
		return
	}

	// Normalize visibility — default to public
	visibility := config.VisibilityPublic
	if req.Visibility == config.VisibilityPrivate {
		visibility = config.VisibilityPrivate
	}

	// Generate invite code for the group
	inviteCode, _ := helper.RandomHex(8)

	// Limit initial members (don't include creator in count)
	if len(req.MemberIDs) > 49 { // 49 + creator = 50
		helper.Error(w, http.StatusBadRequest, "Too many initial members (max 49 plus yourself)")
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
		rows, err := postgress.GetPool().Query(ctx, query, args...)
		if err != nil {
			log.Printf("[groups] validate members query failed group=%s: %v", req.Name, err)
			helper.Error(w, http.StatusInternalServerError, "Database error")
			return
		}
		defer rows.Close()
		validMembers := make(map[string]bool)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				validMembers[id] = true
			}
		}

		if len(validMembers) != len(uniqueMembers) {
			helper.Error(w, http.StatusBadRequest, "One or more member IDs are invalid")
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
		blockRows, err := postgress.GetPool().Query(ctx, blockQuery, blockArgs...)
		if err != nil {
			log.Printf("[groups] block check query failed: %v", err)
			helper.Error(w, http.StatusInternalServerError, "Database error")
			return
		}
		defer blockRows.Close()
		blocked := make(map[string]bool)
		for blockRows.Next() {
			var id string
			if err := blockRows.Scan(&id); err == nil {
				blocked[id] = true
			}
		}

		// Filter out blocked users
		for _, id := range uniqueMembers {
			if !blocked[id] {
				finalMembers = append(finalMembers, id)
			}
		}
	} // end if len(uniqueMembers) > 0

	// Create room + members in a transaction
	tx, err := postgress.GetPool().Begin(ctx)
	if err != nil {
		log.Printf("[groups] CreateGroup begin tx failed: %v", err)
		helper.Error(w, http.StatusInternalServerError, "Database error")
		return
	}
	defer tx.Rollback(ctx)

	var roomID string
	err = tx.QueryRow(ctx,
		`INSERT INTO rooms (name, type, avatar_url, created_by, max_members, visibility, invite_code)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id`,
		req.Name, config.RoomTypeGroup, req.AvatarURL, userID, getGroupCapacity(ctx), visibility, inviteCode,
	).Scan(&roomID)
	if err != nil {
		log.Printf("[groups] CreateGroup insert room failed: %v", err)
		helper.Error(w, http.StatusInternalServerError, "Failed to create group")
		return
	}

	// Insert creator as admin
	_, err = tx.Exec(ctx,
		`INSERT INTO room_members (room_id, user_id, status, role, joined_at)
		 VALUES ($1, $2, 'active', 'admin', NOW())`,
		roomID, userID,
	)
	if err != nil {
		log.Printf("[groups] CreateGroup insert creator failed room=%s: %v", roomID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to add creator to group")
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
		if _, err = tx.Exec(ctx, insertSQL, args...); err != nil {
			log.Printf("[groups] CreateGroup insert members failed room=%s: %v", roomID, err)
			helper.Error(w, http.StatusInternalServerError, "Failed to add members to group")
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("[groups] CreateGroup commit failed: %v", err)
		helper.Error(w, http.StatusInternalServerError, "Failed to create group")
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

	helper.JSON(w, http.StatusOK, map[string]interface{}{
		"roomId":     roomID,
		"name":       req.Name,
		"visibility": visibility,
		"inviteCode": inviteCode,
	})
}

// GetGroupHandler returns group info and member list.
func GetGroupHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		helper.Error(w, http.StatusBadRequest, "Missing group ID")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Verify requester is an active member
	if !isGroupMember(ctx, groupID, userID) {
		helper.Error(w, http.StatusForbidden, "Not a member of this group")
		return
	}

	// Fetch group info
	var name, avatarURL, createdBy, visibility string
	var inviteCode *string
	var maxMembers int
	err := postgress.GetPool().QueryRow(ctx,
		`SELECT COALESCE(name,''), COALESCE(avatar_url,''), COALESCE(created_by::text,''), max_members,
		        COALESCE(visibility,'public'), invite_code
		 FROM rooms WHERE id = $1 AND type = 'GROUP'`, groupID,
	).Scan(&name, &avatarURL, &createdBy, &maxMembers, &visibility, &inviteCode)
	if err != nil {
		log.Printf("[groups] GetGroup query failed group=%s: %v", groupID, err)
		helper.Error(w, http.StatusNotFound, "Group not found")
		return
	}

	memberRows, err := postgress.GetPool().Query(ctx,
		`SELECT u.id, u.username, u.name, COALESCE(u.avatar_url,''), rm.role
		 FROM room_members rm
		 JOIN users u ON u.id = rm.user_id
		 WHERE rm.room_id = $1 AND rm.status = 'active'
		 LIMIT 500`,
		groupID,
	)
	if err != nil {
		log.Printf("[groups] GetGroup members query failed group=%s: %v", groupID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to fetch members")
		return
	}
	defer memberRows.Close()

	e := chat.GetEngine()
	var members []models.GroupMember
	for memberRows.Next() {
		var m models.GroupMember
		if err := memberRows.Scan(&m.ID, &m.Username, &m.Name, &m.AvatarURL, &m.Role); err != nil {
			log.Printf("[groups] Scan member row failed group=%s: %v", groupID, err)
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

	helper.JSON(w, http.StatusOK, models.GroupResponse{
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

// UpdateGroupHandler updates a group's name, avatar, or visibility (admin only).
func UpdateGroupHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		helper.Error(w, http.StatusBadRequest, "Missing group ID")
		return
	}

	var req models.UpdateGroupRequest
	if err := helper.ReadJSON(r, &req); err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Check admin
	if !isGroupAdmin(ctx, groupID, userID) {
		helper.Error(w, http.StatusForbidden, "Only admins can update the group")
		return
	}

	if req.Name != nil && len(*req.Name) > 100 {
		helper.Error(w, http.StatusBadRequest, "Group name must be 100 characters or fewer")
		return
	}

	// Build a single dynamic UPDATE instead of N separate calls
	setClauses := []string{}
	args := []any{}
	argIdx := 1
	if req.Name != nil {
		setClauses = append(setClauses, fmt.Sprintf("name = $%d", argIdx))
		args = append(args, *req.Name)
		argIdx++
	}
	if req.AvatarURL != nil {
		setClauses = append(setClauses, fmt.Sprintf("avatar_url = $%d", argIdx))
		args = append(args, *req.AvatarURL)
		argIdx++
	}
	if req.Visibility != nil {
		v := *req.Visibility
		if v == config.VisibilityPublic || v == config.VisibilityPrivate {
			setClauses = append(setClauses, fmt.Sprintf("visibility = $%d", argIdx))
			args = append(args, v)
			argIdx++
		}
	}
	if len(setClauses) > 0 {
		args = append(args, groupID)
		query := fmt.Sprintf("UPDATE rooms SET %s WHERE id = $%d", strings.Join(setClauses, ", "), argIdx)
		if _, err := postgress.GetPool().Exec(ctx, query, args...); err != nil {
			log.Printf("[groups] UpdateGroup failed group=%s: %v", groupID, err)
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

	helper.JSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "Group updated"})
}

// AddGroupMembersHandler adds members to a group (admin only).
func AddGroupMembersHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		helper.Error(w, http.StatusBadRequest, "Missing group ID")
		return
	}

	var req models.AddMembersRequest
	if err := helper.ReadJSON(r, &req); err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if len(req.MemberIDs) == 0 {
		helper.Error(w, http.StatusBadRequest, "No members specified")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Check admin
	if !isGroupAdmin(ctx, groupID, userID) {
		helper.Error(w, http.StatusForbidden, "Only admins can add members")
		return
	}

	// Check max_members
	var maxMembers, currentCount int
	err := postgress.GetPool().QueryRow(ctx,
		`SELECT max_members, member_count FROM rooms WHERE id = $1 AND type = 'GROUP'`, groupID,
	).Scan(&maxMembers, &currentCount)
	if err != nil {
		log.Printf("[groups] AddGroupMembers count query failed group=%s: %v", groupID, err)
		helper.Error(w, http.StatusNotFound, "Group not found")
		return
	}

	if currentCount+len(req.MemberIDs) > maxMembers {
		helper.Error(w, http.StatusBadRequest, fmt.Sprintf("Adding these members would exceed the group limit of %d", maxMembers))
		return
	}

	// Validate members exist and are not banned
	placeholders, args := buildINClause(req.MemberIDs, 1)
	query := fmt.Sprintf(
		`SELECT id, is_private FROM users WHERE id IN (%s) AND is_banned = false`, placeholders,
	)
	rows, err := postgress.GetPool().Query(ctx, query, args...)
	if err != nil {
		log.Printf("[groups] AddGroupMembers validate query failed group=%s: %v", groupID, err)
		helper.Error(w, http.StatusInternalServerError, "Database error")
		return
	}
	defer rows.Close()
	validIDs := make(map[string]bool)
	privateIDs := make(map[string]bool)
	for rows.Next() {
		var id string
		var isPrivate bool
		if err := rows.Scan(&id, &isPrivate); err == nil {
			validIDs[id] = true
			if isPrivate {
				privateIDs[id] = true
			}
		}
	}

	// Check blocks between adder and new members
	blocked := getBlockedBetween(ctx, userID, req.MemberIDs)

	// Build friend set of the adder to check private non-friends
	friendSet := make(map[string]bool)
	if len(privateIDs) > 0 {
		friendRows, fErr := postgress.GetPool().Query(ctx,
			`SELECT user_id_2 FROM friendships WHERE user_id_1 = $1
			 UNION ALL
			 SELECT user_id_1 FROM friendships WHERE user_id_2 = $1`, userID,
		)
		if fErr == nil {
			defer friendRows.Close()
			for friendRows.Next() {
				var fid string
				if friendRows.Scan(&fid) == nil {
					friendSet[fid] = true
				}
			}
		}
	}

	added := make([]string, 0)
	invited := make([]string, 0)
	toAdd := make([]string, 0)
	toInvite := make([]string, 0)
	for _, memberID := range req.MemberIDs {
		if !validIDs[memberID] || blocked[memberID] {
			continue
		}
		if privateIDs[memberID] && !friendSet[memberID] {
			toInvite = append(toInvite, memberID)
		} else {
			toAdd = append(toAdd, memberID)
		}
	}

	// Batch upsert invited members
	if len(toInvite) > 0 {
		values := make([]string, len(toInvite))
		params := make([]any, 0, len(toInvite)+1)
		params = append(params, groupID)
		for i, memberID := range toInvite {
			params = append(params, memberID)
			values[i] = fmt.Sprintf("($1, $%d::uuid, 'invited', 'member', NOW())", i+2)
		}
		_, err := postgress.GetPool().Exec(ctx,
			fmt.Sprintf(`INSERT INTO room_members (room_id, user_id, status, role, joined_at)
			 VALUES %s
			 ON CONFLICT (room_id, user_id)
			 DO UPDATE SET status = 'invited', joined_at = NOW(), left_at = NULL`,
				joinStrings(values, ", ")),
			params...,
		)
		if err != nil {
			log.Printf("[groups] batch invite failed group=%s: %v", groupID, err)
		} else {
			invited = toInvite
		}
	}

	// Batch upsert directly added members
	if len(toAdd) > 0 {
		values := make([]string, len(toAdd))
		params := make([]any, 0, len(toAdd)+1)
		params = append(params, groupID)
		for i, memberID := range toAdd {
			params = append(params, memberID)
			values[i] = fmt.Sprintf("($1, $%d::uuid, 'active', 'member', NOW())", i+2)
		}
		_, err := postgress.GetPool().Exec(ctx,
			fmt.Sprintf(`INSERT INTO room_members (room_id, user_id, status, role, joined_at)
			 VALUES %s
			 ON CONFLICT (room_id, user_id)
			 DO UPDATE SET status = 'active', role = 'member', joined_at = NOW(), left_at = NULL`,
				joinStrings(values, ", ")),
			params...,
		)
		if err != nil {
			log.Printf("[groups] batch add failed group=%s: %v", groupID, err)
		} else {
			added = toAdd
		}
	}

	// Subscribe directly added members to the room
	if e := chat.GetEngine(); e != nil {
		for _, id := range added {
			e.JoinRoomForUser(id, groupID)
		}
	}

	// Notify all group members about directly added members
	allMembers := getActiveGroupMemberIDs(ctx, groupID)
	for _, addedID := range added {
		broadcastGroupEvent(ctx, groupID, config.MsgTypeMemberAdded, map[string]interface{}{
			config.FieldRoomID:  groupID,
			config.FieldUserID:  addedID,
			config.FieldAddedBy: userID,
		}, allMembers)
	}

	// Fetch group name for invite notifications
	if len(invited) > 0 {
		var groupName string
		postgress.GetPool().QueryRow(ctx,
			`SELECT COALESCE(name,'') FROM rooms WHERE id = $1`, groupID,
		).Scan(&groupName)
		for _, invitedID := range invited {
			if services.ShouldNotify(ctx, invitedID, services.NotifPrefGroupInvites) {
				notifyUser(ctx, invitedID, map[string]interface{}{
					config.FieldType:      config.MsgTypeGroupInvite,
					config.FieldRoomID:    groupID,
					config.FieldGroupName: groupName,
					config.FieldInvitedBy: userID,
				})
			}
		}
	}

	helper.JSON(w, http.StatusOK, map[string]interface{}{
		"added":   added,
		"invited": invited,
	})
}

// RemoveGroupMemberHandler removes a member from the group, or leaves (self).
func RemoveGroupMemberHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	groupID := r.PathValue("groupId")
	targetID := r.PathValue("userId")
	if groupID == "" || targetID == "" {
		helper.Error(w, http.StatusBadRequest, "Missing group or user ID")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	isSelf := userID == targetID

	if !isSelf {
		// Only admins can remove others
		if !isGroupAdmin(ctx, groupID, userID) {
			helper.Error(w, http.StatusForbidden, "Only admins can remove members")
			return
		}
	} else {
		// Self-leave: just verify membership
		if !isGroupMember(ctx, groupID, userID) {
			helper.Error(w, http.StatusForbidden, "Not a member of this group")
			return
		}
	}

	// Deactivate membership
	_, err := postgress.GetPool().Exec(ctx,
		`UPDATE room_members SET status = 'inactive', left_at = NOW()
		 WHERE room_id = $1 AND user_id = $2 AND status = 'active'`,
		groupID, targetID,
	)
	if err != nil {
		log.Printf("[groups] RemoveMember update failed group=%s user=%s: %v", groupID, targetID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to remove member")
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

	helper.JSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "Member removed"})
}

// PromoteAdminHandler promotes a member to group admin.
func PromoteAdminHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		helper.Error(w, http.StatusBadRequest, "Missing group ID")
		return
	}

	var req models.PromoteAdminRequest
	if err := helper.ReadJSON(r, &req); err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.UserID == "" {
		helper.Error(w, http.StatusBadRequest, "userId is required")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	if !isGroupAdmin(ctx, groupID, userID) {
		helper.Error(w, http.StatusForbidden, "Only admins can promote members")
		return
	}

	res, err := postgress.GetPool().Exec(ctx,
		`UPDATE room_members SET role = 'admin'
		 WHERE room_id = $1 AND user_id = $2 AND status = 'active'`,
		groupID, req.UserID,
	)
	if err != nil {
		log.Printf("[groups] PromoteAdmin update failed group=%s user=%s: %v", groupID, req.UserID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to promote member")
		return
	}
	affected := res.RowsAffected()
	if affected == 0 {
		helper.Error(w, http.StatusBadRequest, "User is not an active member of this group")
		return
	}

	// Broadcast promotion event to all group members
	memberIDs := getActiveGroupMemberIDs(ctx, groupID)
	broadcastGroupEvent(ctx, groupID, config.MsgTypeMemberPromoted, map[string]interface{}{
		config.FieldRoomID: groupID,
		config.FieldUserID: req.UserID,
		config.FieldRole:   config.RoleAdmin,
	}, memberIDs)

	helper.JSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "Member promoted to admin"})
}

// DeleteGroupHandler deletes a group (original creator only).
func DeleteGroupHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		helper.Error(w, http.StatusBadRequest, "Missing group ID")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Only the original creator can delete the group
	var createdBy string
	err := postgress.GetPool().QueryRow(ctx,
		`SELECT COALESCE(created_by::text, '') FROM rooms WHERE id = $1 AND type = 'GROUP'`, groupID,
	).Scan(&createdBy)
	if err != nil {
		helper.Error(w, http.StatusNotFound, "Group not found")
		return
	}
	if createdBy != userID {
		helper.Error(w, http.StatusForbidden, "Only the group creator can delete the group")
		return
	}

	// Clean up active call if one exists
	rdb := redis.GetRawClient()
	callState, callErr := loadGroupCallState(ctx, rdb, groupID)
	if callErr == nil && callState != nil {
		// End the LiveKit room
		if RTC != nil && RTC.IsConfigured() && callState.LKRoomName != "" {
			if err := RTC.DeleteRoom(ctx, callState.LKRoomName); err != nil {
				log.Printf("[groups] DeleteGroup RTC.DeleteRoom failed group=%s: %v", groupID, err)
			}
		}
		// Delete Redis key
		if err := rdb.Del(ctx, config.GROUP_CALL_COLON+groupID).Err(); err != nil {
			log.Printf("[groups] DeleteGroup Redis Del call state failed group=%s: %v", groupID, err)
		}
		// Update call_logs
		if callState.CallID != "" {
			duration := int(time.Since(callState.StartedAt).Seconds())
			if _, err := postgress.GetPool().Exec(ctx,
				`UPDATE call_logs SET ended_at = NOW(), duration_seconds = $2
				 WHERE call_id = $1 AND ended_at IS NULL`,
				callState.CallID, duration,
			); err != nil {
				log.Printf("[groups] DeleteGroup update call_logs failed call=%s: %v", callState.CallID, err)
			}
		}
	}

	// Get all members before deleting (for notification)
	memberIDs := getActiveGroupMemberIDs(ctx, groupID)

	// Close the room on the engine (unsubscribe everyone)
	if e := chat.GetEngine(); e != nil {
		closedPayload, err := json.Marshal(map[string]string{
			config.FieldType:   config.MsgTypeRoomClosed,
			config.FieldRoomID: groupID,
		})
		if err != nil {
			log.Printf("[groups] Marshal closedPayload failed group=%s: %v", groupID, err)
		}
		e.CloseRoom(groupID, closedPayload)
	}

	// Delete the room (CASCADE will remove room_members and messages)
	_, err = postgress.GetPool().Exec(ctx,
		`DELETE FROM rooms WHERE id = $1`, groupID,
	)
	if err != nil {
		log.Printf("[groups] DeleteGroup delete room failed group=%s: %v", groupID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to delete group")
		return
	}

	// Notify members that the room was closed
	_ = memberIDs // Already notified via CloseRoom above

	helper.JSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "Group deleted"})
}

// ListGroupsHandler lists or searches public groups.
func ListGroupsHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	search := strings.TrimSpace(r.URL.Query().Get("search"))

	baseQuery := `SELECT r.id, COALESCE(r.name,''), COALESCE(r.avatar_url,''),
		        COALESCE(r.visibility,'public'), COALESCE(r.created_by::text,''),
		        r.member_count,
		        my_rm.user_id IS NOT NULL AS is_member
		 FROM rooms r
		 LEFT JOIN room_members my_rm ON my_rm.room_id = r.id AND my_rm.user_id = $1 AND my_rm.status = 'active'
		 WHERE r.type = 'GROUP' AND r.visibility = 'public'`

	limit, offset := parsePagination(r)
	var args []interface{}
	args = append(args, userID)

	if search != "" {
		baseQuery += ` AND LOWER(r.name) LIKE LOWER('%' || $2 || '%')`
		args = append(args, search)
	}
	args = append(args, limit, offset)
	baseQuery += fmt.Sprintf(` ORDER BY member_count DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args))

	rows, err := postgress.GetPool().Query(ctx, baseQuery, args...)
	if err != nil {
		log.Printf("[groups] ListGroups query failed: %v", err)
		helper.Error(w, http.StatusInternalServerError, "Database error")
		return
	}
	defer rows.Close()

	var groups []models.GroupListItem
	for rows.Next() {
		var g models.GroupListItem
		if err := rows.Scan(&g.RoomID, &g.Name, &g.AvatarURL, &g.Visibility, &g.CreatedBy, &g.MemberCount, &g.IsMember); err != nil {
			log.Printf("[groups] Scan group list row failed: %v", err)
			continue
		}
		groups = append(groups, g)
	}
	if groups == nil {
		groups = []models.GroupListItem{}
	}

	helper.JSON(w, http.StatusOK, groups)
}

// JoinGroupHandler self-joins a public group.
func JoinGroupHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		helper.Error(w, http.StatusBadRequest, "Missing group ID")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Check the group exists, is a GROUP, and is public
	var visibility string
	var maxMembers, currentCount int
	err := postgress.GetPool().QueryRow(ctx,
		`SELECT COALESCE(r.visibility,'public'), r.max_members, r.member_count
		 FROM rooms r WHERE r.id = $1 AND r.type = 'GROUP'`, groupID,
	).Scan(&visibility, &maxMembers, &currentCount)
	if err != nil {
		log.Printf("[groups] JoinGroup query failed group=%s: %v", groupID, err)
		helper.Error(w, http.StatusNotFound, "Group not found")
		return
	}

	if visibility != config.VisibilityPublic {
		helper.Error(w, http.StatusForbidden, "This group is private — use an invite link to join")
		return
	}

	// Check if already a member
	if isGroupMember(ctx, groupID, userID) {
		helper.Error(w, http.StatusConflict, "You are already a member of this group")
		return
	}

	if currentCount >= maxMembers {
		helper.Error(w, http.StatusBadRequest, "Group is full")
		return
	}

	// Check if user is banned
	var isBanned bool
	postgress.GetPool().QueryRow(ctx,
		`SELECT is_banned FROM users WHERE id = $1`, userID,
	).Scan(&isBanned)
	if isBanned {
		helper.Error(w, http.StatusForbidden, "Account is banned")
		return
	}

	// Upsert membership (handles re-joining after leaving)
	_, err = postgress.GetPool().Exec(ctx,
		`INSERT INTO room_members (room_id, user_id, status, role, joined_at)
		 VALUES ($1, $2, 'active', 'member', NOW())
		 ON CONFLICT (room_id, user_id)
		 DO UPDATE SET status = 'active', role = 'member', joined_at = NOW(), left_at = NULL`,
		groupID, userID,
	)
	if err != nil {
		log.Printf("[groups] JoinGroup upsert failed group=%s user=%s: %v", groupID, userID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to join group")
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

	helper.JSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "Joined group successfully"})
}

// JoinGroupByInviteHandler joins a group via invite link.
func JoinGroupByInviteHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	code := r.PathValue("inviteCode")
	if code == "" {
		helper.Error(w, http.StatusBadRequest, "Missing invite code")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Look up the group by invite code
	var groupID string
	var maxMembers, currentCount int
	err := postgress.GetPool().QueryRow(ctx,
		`SELECT r.id, r.max_members, r.member_count
		 FROM rooms r WHERE r.invite_code = $1 AND r.type = 'GROUP'`, code,
	).Scan(&groupID, &maxMembers, &currentCount)
	if err != nil {
		log.Printf("[groups] JoinGroupByInvite lookup failed code=%s: %v", code, err)
		helper.Error(w, http.StatusNotFound, "Invalid or expired invite code")
		return
	}

	// Check if already a member
	if isGroupMember(ctx, groupID, userID) {
		helper.Error(w, http.StatusConflict, "You are already a member of this group")
		return
	}

	if currentCount >= maxMembers {
		helper.Error(w, http.StatusBadRequest, "Group is full")
		return
	}

	// Upsert membership
	_, err = postgress.GetPool().Exec(ctx,
		`INSERT INTO room_members (room_id, user_id, status, role, joined_at)
		 VALUES ($1, $2, 'active', 'member', NOW())
		 ON CONFLICT (room_id, user_id)
		 DO UPDATE SET status = 'active', role = 'member', joined_at = NOW(), left_at = NULL`,
		groupID, userID,
	)
	if err != nil {
		log.Printf("[groups] JoinGroupByInvite upsert failed group=%s user=%s: %v", groupID, userID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to join group")
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

	helper.JSON(w, http.StatusOK, map[string]interface{}{
		"roomId":  groupID,
		"message": "Joined group via invite link",
	})
}

// GenerateInviteHandler generates or regenerates an invite code (admin only).
func GenerateInviteHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		helper.Error(w, http.StatusBadRequest, "Missing group ID")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	if !isGroupAdmin(ctx, groupID, userID) {
		helper.Error(w, http.StatusForbidden, "Only admins can generate invite codes")
		return
	}

	newCode, _ := helper.RandomHex(8)
	_, err := postgress.GetPool().Exec(ctx,
		`UPDATE rooms SET invite_code = $1 WHERE id = $2 AND type = 'GROUP'`, newCode, groupID,
	)
	if err != nil {
		log.Printf("[groups] GenerateInvite update failed group=%s: %v", groupID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to generate invite code")
		return
	}

	helper.JSON(w, http.StatusOK, map[string]interface{}{
		"inviteCode": newCode,
	})
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

// getGroupCapacity reads the group_capacity setting from app_settings.
// Falls back to 50 if not configured.
func getGroupCapacity(ctx context.Context) int {
	var cfg struct {
		MaxMembers int `json:"max_members"`
	}
	cfg.MaxMembers = 50 // default
	_ = GetAppSetting(ctx, "group_capacity", &cfg)
	if cfg.MaxMembers <= 0 {
		return 50
	}
	return cfg.MaxMembers
}

// SetVanitySlugHandler sets a vanity slug for a group (admin + VIP only).
func SetVanitySlugHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		helper.Error(w, http.StatusBadRequest, "Missing group ID")
		return
	}

	var body struct {
		Slug string `json:"slug"`
	}
	if err := helper.ReadJSON(r, &body); err != nil || body.Slug == "" {
		helper.Error(w, http.StatusBadRequest, "slug is required")
		return
	}

	// Validate slug format: 3-50 chars, alphanumeric + hyphens, no leading/trailing hyphens
	slug := strings.ToLower(strings.TrimSpace(body.Slug))
	if len(slug) < 3 || len(slug) > 50 {
		helper.Error(w, http.StatusBadRequest, "Slug must be 3-50 characters")
		return
	}
	for _, ch := range slug {
		if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-') {
			helper.Error(w, http.StatusBadRequest, "Slug can only contain lowercase letters, numbers, and hyphens")
			return
		}
	}
	if slug[0] == '-' || slug[len(slug)-1] == '-' {
		helper.Error(w, http.StatusBadRequest, "Slug cannot start or end with a hyphen")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Must be group admin
	if !isGroupAdmin(ctx, groupID, userID) {
		helper.Error(w, http.StatusForbidden, "Only admins can set vanity slug")
		return
	}

	// Must be VIP (donor)
	if !IsUserVIP(ctx, userID) {
		helper.Error(w, http.StatusForbidden, "Only VIP donors can set vanity slugs")
		return
	}

	// Try to set the slug (unique constraint will catch duplicates)
	res, err := postgress.GetPool().Exec(ctx,
		`UPDATE rooms SET vanity_slug = $1 WHERE id = $2 AND type = 'GROUP'`, slug, groupID,
	)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			helper.Error(w, http.StatusConflict, "This slug is already taken")
			return
		}
		helper.Error(w, http.StatusInternalServerError, "Failed to set vanity slug")
		return
	}
	affected := res.RowsAffected()
	if affected == 0 {
		helper.Error(w, http.StatusNotFound, "Group not found")
		return
	}

	helper.JSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "Vanity slug set to: " + slug})
}

// JoinGroupByVanityHandler joins a group via its vanity slug.
func JoinGroupByVanityHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	slug := r.PathValue("slug")
	if slug == "" {
		helper.Error(w, http.StatusBadRequest, "Missing vanity slug")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Look up group by vanity_slug
	var groupID, visibility string
	var maxMembers, currentCount int
	err := postgress.GetPool().QueryRow(ctx,
		`SELECT r.id, COALESCE(r.visibility,'public'), r.max_members, r.member_count
		 FROM rooms r WHERE r.vanity_slug = $1 AND r.type = 'GROUP'`, strings.ToLower(slug),
	).Scan(&groupID, &visibility, &maxMembers, &currentCount)
	if err != nil {
		log.Printf("[groups] JoinGroupByVanity lookup failed slug=%s: %v", slug, err)
		helper.Error(w, http.StatusNotFound, "Group not found")
		return
	}

	// Private groups cannot be joined via vanity slug (must use invite code)
	if visibility != config.VisibilityPublic {
		helper.Error(w, http.StatusForbidden, "This group is private — use an invite link to join")
		return
	}

	if isGroupMember(ctx, groupID, userID) {
		helper.Error(w, http.StatusConflict, "You are already a member of this group")
		return
	}

	if currentCount >= maxMembers {
		helper.Error(w, http.StatusBadRequest, "Group is full")
		return
	}

	// Upsert membership
	_, err = postgress.GetPool().Exec(ctx,
		`INSERT INTO room_members (room_id, user_id, status, role, joined_at)
		 VALUES ($1, $2, 'active', 'member', NOW())
		 ON CONFLICT (room_id, user_id)
		 DO UPDATE SET status = 'active', role = 'member', joined_at = NOW(), left_at = NULL`,
		groupID, userID,
	)
	if err != nil {
		log.Printf("[groups] JoinGroupByVanity upsert failed group=%s user=%s: %v", groupID, userID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to join group")
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

	helper.JSON(w, http.StatusOK, map[string]interface{}{
		"roomId":  groupID,
		"message": "Joined group via vanity link",
	})
}

// ---------------------------------------------------------------------------
// Group Invite Handlers (accept / decline / list)
// ---------------------------------------------------------------------------

// GetGroupInvitesHandler returns pending group invites for the authenticated user.
func GetGroupInvitesHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	query := `
		SELECT COALESCE(json_agg(t), '[]')::text
		FROM (
			SELECT
				r.id AS "roomId",
				COALESCE(r.name,'') AS "groupName",
				COALESCE(r.avatar_url,'') AS "avatarUrl",
				COALESCE(r.created_by::text,'') AS "invitedBy",
				COALESCE(u.name,'') AS "inviterName",
				rm.joined_at AS "invitedAt"
			FROM room_members rm
			JOIN rooms r ON rm.room_id = r.id
			LEFT JOIN users u ON u.id = r.created_by
			WHERE rm.user_id = $1 AND rm.status = 'invited' AND r.type = 'GROUP'
			ORDER BY rm.joined_at DESC
		) t
	`

	var raw []byte
	err := postgress.GetPool().QueryRow(ctx, query, userID).Scan(&raw)
	if err != nil {
		log.Printf("[groups] GetGroupInvites query failed user=%s: %v", userID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to fetch group invites")
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
}

// AcceptGroupInviteHandler accepts a pending group invite.
func AcceptGroupInviteHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		helper.Error(w, http.StatusBadRequest, "Missing group ID")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Check group is not full
	var maxMembers, currentCount int
	err := postgress.GetPool().QueryRow(ctx,
		`SELECT r.max_members, r.member_count
		 FROM rooms r WHERE r.id = $1 AND r.type = 'GROUP'`, groupID,
	).Scan(&maxMembers, &currentCount)
	if err != nil {
		helper.Error(w, http.StatusNotFound, "Group not found")
		return
	}
	if currentCount >= maxMembers {
		helper.Error(w, http.StatusBadRequest, "Group is full")
		return
	}

	// Activate the invited membership
	res, err := postgress.GetPool().Exec(ctx,
		`UPDATE room_members SET status = 'active', joined_at = NOW()
		 WHERE room_id = $1 AND user_id = $2 AND status = 'invited'`,
		groupID, userID,
	)
	if err != nil {
		log.Printf("[groups] AcceptGroupInvite update failed group=%s user=%s: %v", groupID, userID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to accept invite")
		return
	}
	affected := res.RowsAffected()
	if affected == 0 {
		helper.Error(w, http.StatusNotFound, "No pending invite for this group")
		return
	}

	// Subscribe to room
	if e := chat.GetEngine(); e != nil {
		e.JoinRoomForUser(userID, groupID)
	}

	// Notify all members
	allMembers := getActiveGroupMemberIDs(ctx, groupID)
	broadcastGroupEvent(ctx, groupID, config.MsgTypeGroupInviteAccepted, map[string]interface{}{
		config.FieldRoomID: groupID,
		config.FieldUserID: userID,
	}, allMembers)

	helper.JSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "Group invite accepted"})
}

// DeclineGroupInviteHandler declines a pending group invite.
func DeclineGroupInviteHandler(w http.ResponseWriter, r *http.Request) {
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	groupID := r.PathValue("groupId")
	if groupID == "" {
		helper.Error(w, http.StatusBadRequest, "Missing group ID")
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	res, err := postgress.GetPool().Exec(ctx,
		`DELETE FROM room_members WHERE room_id = $1 AND user_id = $2 AND status = 'invited'`,
		groupID, userID,
	)
	if err != nil {
		log.Printf("[groups] DeclineGroupInvite delete failed group=%s user=%s: %v", groupID, userID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to decline invite")
		return
	}
	affected := res.RowsAffected()
	if affected == 0 {
		helper.Error(w, http.StatusNotFound, "No pending invite for this group")
		return
	}

	helper.JSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "Group invite declined"})
}

// isGroupMember checks if a user is an active member of a group room.
func isGroupMember(ctx context.Context, groupID, userID string) bool {
	var exists bool
	postgress.GetPool().QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM room_members
			WHERE room_id = $1 AND user_id = $2 AND status = 'active'
		)`, groupID, userID,
	).Scan(&exists)
	return exists
}

// isGroupAdmin checks if a user is an admin of a group room.
func isGroupAdmin(ctx context.Context, groupID, userID string) bool {
	var exists bool
	postgress.GetPool().QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM room_members
			WHERE room_id = $1 AND user_id = $2 AND status = 'active' AND role = 'admin'
		)`, groupID, userID,
	).Scan(&exists)
	return exists
}

// getActiveGroupMemberIDs returns all active member user IDs for a group.
func getActiveGroupMemberIDs(ctx context.Context, groupID string) []string {
	rows, err := postgress.GetPool().Query(ctx,
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

	rows, err := postgress.GetPool().Query(ctx, query, args...)
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
