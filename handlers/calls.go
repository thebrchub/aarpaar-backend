package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/goccy/go-json"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/shivanand-burli/go-starter-kit/rtc"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/models"
)

// RTC is the LiveKit client, initialised once at startup in main.go.
// A single *rtc.Client is safe for concurrent use across all goroutines.
var RTC rtc.RTCService

// ---------------------------------------------------------------------------
// GET /api/v1/calls/config
//
// Returns ICE server configuration for WebRTC P2P calls.
// Clients use this to initialize RTCPeerConnection with STUN/TURN servers.
//
// For group calls (3+ participants), LiveKit Cloud handles ICE internally,
// so this endpoint is only needed for 1:1 P2P calls.
// ---------------------------------------------------------------------------

// ICEServer matches the WebRTC RTCIceServer interface.
type ICEServer struct {
	URLs       any    `json:"urls"` // string or []string
	Username   string `json:"username,omitempty"`
	Credential string `json:"credential,omitempty"`
}

// CallConfig is the response shape for GET /calls/config.
type CallConfig struct {
	ICEServers []ICEServer `json:"iceServers"`
	LiveKit    *LKConfig   `json:"livekit,omitempty"`
}

// LKConfig exposes LiveKit Cloud URL (token generation is server-side only).
type LKConfig struct {
	URL string `json:"url"`
}

// GetCallConfigHandler returns ICE server configuration for WebRTC P2P calls.
//
// @Summary		Get call configuration
// @Description	Returns ICE (STUN/TURN) server config for WebRTC. Includes LiveKit URL if group calls are configured.
// @Tags		Calls
// @Produce		json
// @Success		200	{object}	CallConfig
// @Security	BearerAuth
// @Router		/calls/config [get]
func GetCallConfigHandler(w http.ResponseWriter, r *http.Request) {
	servers := []ICEServer{
		{URLs: "stun:stun.l.google.com:19302"},
		{URLs: "stun:stun.cloudflare.com:3478"},
	}

	// Add TURN server if configured
	if config.TURNURL != "" {
		servers = append(servers, ICEServer{
			URLs:       config.TURNURL,
			Username:   config.TURNUsername,
			Credential: config.TURNPassword,
		})
	}

	// Add secondary TURN (TCP/TLS fallback) if configured
	if config.TURNURL2 != "" {
		servers = append(servers, ICEServer{
			URLs:       config.TURNURL2,
			Username:   config.TURNUsername2,
			Credential: config.TURNPassword2,
		})
	}

	resp := CallConfig{
		ICEServers: servers,
	}

	// Expose LiveKit URL if configured (so clients know group calls are available)
	if RTC != nil && RTC.IsConfigured() {
		resp.LiveKit = &LKConfig{URL: RTC.GetURL()}
	}

	JSONSuccess(w, resp)
}

// ---------------------------------------------------------------------------
// GET /api/v1/calls/history?limit=20&offset=0
//
// Returns the user's call history from the call_logs table.
// Includes caller/callee info and call metadata.
// ---------------------------------------------------------------------------

// GetCallHistoryHandler returns the user's call history.
//
// @Summary		Get call history
// @Description	Returns paginated call history for the authenticated user.
// @Tags		Calls
// @Produce		json
// @Param		limit	query	int	false	"Max results (default 20, max 50)"
// @Param		offset	query	int	false	"Offset for pagination (default 0)"
// @Success		200	{array}	CallHistoryEntry
// @Failure		401	{object}	StatusMessage
// @Failure		500	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/calls/history [get]
func GetCallHistoryHandler(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(config.UserIDKey).(string)

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()
	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT
			cl.call_id,
			cl.call_type,
			cl.tier,
			cl.started_at,
			cl.ended_at,
			cl.duration_seconds,
			cl.initiated_by,
			COALESCE(u.name, '') AS caller_name,
			COALESCE(u.avatar_url, '') AS caller_avatar
		FROM call_logs cl
		LEFT JOIN users u ON u.id = cl.initiated_by
		WHERE cl.initiated_by = $1
		   OR EXISTS (
				SELECT 1 FROM room_members rm
				WHERE rm.room_id = cl.room_id
				AND rm.user_id = $1
				AND rm.status = 'active'
		   )
		ORDER BY cl.started_at DESC
		LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		JSONError(w, "Failed to fetch call history", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type callEntry struct {
		CallID       string  `json:"callId"`
		CallType     string  `json:"callType"`
		Tier         string  `json:"tier"`
		StartedAt    string  `json:"startedAt"`
		EndedAt      *string `json:"endedAt"`
		Duration     *int    `json:"durationSeconds"`
		InitiatedBy  string  `json:"initiatedBy"`
		CallerName   string  `json:"callerName"`
		CallerAvatar string  `json:"callerAvatar"`
	}

	var calls []callEntry
	for rows.Next() {
		var c callEntry
		if err := rows.Scan(
			&c.CallID, &c.CallType, &c.Tier,
			&c.StartedAt, &c.EndedAt, &c.Duration,
			&c.InitiatedBy, &c.CallerName, &c.CallerAvatar,
		); err != nil {
			continue
		}
		calls = append(calls, c)
	}

	if calls == nil {
		calls = []callEntry{}
	}

	JSONSuccess(w, calls)
}

// ---------------------------------------------------------------------------
// GROUP CALL ENDPOINTS
// ---------------------------------------------------------------------------

// StartGroupCallHandler creates a new group call.
//
// @Summary		Start group call
// @Description	Starts a new group call via LiveKit SFU. Returns a token and LiveKit URL.
// @Tags		Group Calls
// @Accept		json
// @Produce		json
// @Param		groupId	path	string	true	"Group room UUID"
// @Param		body	body	models.StartGroupCallRequest	true	"Call type (audio/video)"
// @Success		200	{object}	models.StartGroupCallResponse
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Failure		403	{object}	StatusMessage
// @Failure		409	{object}	StatusMessage	"Call already active"
// @Failure		500	{object}	StatusMessage
// @Failure		503	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/groups/{groupId}/calls [post]
func StartGroupCallHandler(w http.ResponseWriter, r *http.Request) {
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

	if RTC == nil || !RTC.IsConfigured() {
		JSONError(w, "Group calls are not available", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Verify requester is an active member
	if !isGroupMember(ctx, groupID, userID) {
		JSONError(w, "Not a member of this group", http.StatusForbidden)
		return
	}

	// Check if there's already an active call for this group
	rdb := redisClient()
	existing, _ := rdb.Exists(ctx, config.GROUP_CALL_COLON+groupID).Result()
	if existing > 0 {
		JSONError(w, "A call is already active in this group", http.StatusConflict)
		return
	}

	// Parse request body
	var req models.StartGroupCallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.CallType = "video" // default
	}
	if req.CallType != "audio" && req.CallType != "video" {
		req.CallType = "video"
	}

	// Generate a unique call ID
	callID := uuid.New().String()
	lkRoomName := fmt.Sprintf("group_%s_%s", groupID, callID)

	// Create LiveKit room (single shared client — no per-request allocation)
	var maxMembers int
	postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT max_members FROM rooms WHERE id = $1`, groupID,
	).Scan(&maxMembers)
	if maxMembers <= 0 {
		maxMembers = 50
	}

	_, err := RTC.CreateRoom(ctx, lkRoomName, maxMembers)
	if err != nil {
		JSONError(w, "Failed to create call room", http.StatusInternalServerError)
		return
	}

	// Get initiator's name for token
	var userName string
	postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT COALESCE(name, '') FROM users WHERE id = $1`, userID,
	).Scan(&userName)

	// Generate token for the initiator (CPU-only, ~1-2μs, no network call)
	token, err := RTC.GenerateToken(lkRoomName, userID, userName, true, true)
	if err != nil {
		JSONError(w, "Failed to generate call token", http.StatusInternalServerError)
		return
	}

	// Store active call state in Redis (typed struct — no map[string]interface{})
	state := models.GroupCallState{
		CallID:       callID,
		InitiatedBy:  userID,
		StartedAt:    time.Now().UTC(),
		CallType:     req.CallType,
		LKRoomName:   lkRoomName,
		Participants: []string{userID},
		Admins:       []string{userID}, // initiator is first call admin
	}
	callState, _ := json.Marshal(state)
	rdb.Set(ctx, config.GROUP_CALL_COLON+groupID, callState, 24*time.Hour)

	// Log the call to Postgres
	postgress.GetRawDB().ExecContext(ctx,
		`INSERT INTO call_logs (call_id, room_id, initiated_by, call_type, tier, max_participants, participants)
		 VALUES ($1, $2, $3, $4, 'sfu', $5, ARRAY[$3])`,
		callID, groupID, userID, req.CallType, maxMembers,
	)

	// Broadcast group_call_started to all group members
	memberIDs := getActiveGroupMemberIDs(ctx, groupID)
	broadcastGroupEvent(ctx, groupID, config.MsgTypeGroupCallStarted, map[string]interface{}{
		config.FieldRoomID:      groupID,
		config.FieldCallID:      callID,
		config.FieldInitiatedBy: userID,
		config.FieldCallType:    req.CallType,
	}, memberIDs)

	JSONSuccess(w, models.StartGroupCallResponse{
		CallID:     callID,
		Token:      token,
		LiveKitURL: RTC.GetURL(),
	})
}

// JoinGroupCallHandler joins an ongoing group call.
//
// @Summary		Join group call
// @Description	Joins an active group call and returns a LiveKit token.
// @Tags		Group Calls
// @Produce		json
// @Param		groupId	path	string	true	"Group room UUID"
// @Param		callId	path	string	true	"Call UUID"
// @Success		200	{object}	models.LiveKitTokenResponse
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Failure		403	{object}	StatusMessage
// @Failure		404	{object}	StatusMessage
// @Failure		500	{object}	StatusMessage
// @Failure		503	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/groups/{groupId}/calls/{callId}/join [post]
func JoinGroupCallHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	groupID := r.PathValue("groupId")
	callID := r.PathValue("callId")
	if groupID == "" || callID == "" {
		JSONError(w, "Missing group or call ID", http.StatusBadRequest)
		return
	}

	if RTC == nil || !RTC.IsConfigured() {
		JSONError(w, "Group calls are not available", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Verify membership
	if !isGroupMember(ctx, groupID, userID) {
		JSONError(w, "Not a member of this group", http.StatusForbidden)
		return
	}

	// Check if call is still active
	rdb := redisClient()
	state, err := loadGroupCallState(ctx, rdb, groupID)
	if err != nil {
		JSONError(w, "No active call in this group", http.StatusNotFound)
		return
	}

	// Verify callId matches
	if state.CallID != callID {
		JSONError(w, "Call ID mismatch", http.StatusBadRequest)
		return
	}

	lkRoomName := state.LKRoomName
	if lkRoomName == "" {
		lkRoomName = fmt.Sprintf("group_%s_%s", groupID, callID)
	}

	// Get user's name for token
	var userName string
	postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT COALESCE(name, '') FROM users WHERE id = $1`, userID,
	).Scan(&userName)

	// Generate token (CPU-only, ~1-2μs, no network call)
	token, err := RTC.GenerateToken(lkRoomName, userID, userName, true, true)
	if err != nil {
		JSONError(w, "Failed to generate call token", http.StatusInternalServerError)
		return
	}

	// Update participants in Redis (typed slice, no type assertions)
	pidSet := make(map[string]bool, len(state.Participants)+1)
	for _, p := range state.Participants {
		pidSet[p] = true
	}
	pidSet[userID] = true
	newParticipants := make([]string, 0, len(pidSet))
	for p := range pidSet {
		newParticipants = append(newParticipants, p)
	}
	state.Participants = newParticipants
	saveGroupCallState(ctx, rdb, groupID, state)

	// Update participants array in call_logs
	postgress.GetRawDB().ExecContext(ctx,
		`UPDATE call_logs SET participants = array_append(
			CASE WHEN $2 = ANY(participants) THEN participants
			     ELSE participants END, $2)
		 WHERE call_id = $1 AND NOT ($2 = ANY(participants))`,
		callID, userID,
	)

	// Broadcast participant joined
	memberIDs := getActiveGroupMemberIDs(ctx, groupID)
	broadcastGroupEvent(ctx, groupID, config.MsgTypeGroupCallParticipantJoined, map[string]interface{}{
		config.FieldRoomID: groupID,
		config.FieldCallID: callID,
		config.FieldUserID: userID,
	}, memberIDs)

	JSONSuccess(w, models.LiveKitTokenResponse{
		Token:      token,
		LiveKitURL: RTC.GetURL(),
	})
}

// LeaveGroupCallHandler leaves a group call.
//
// @Summary		Leave group call
// @Description	Leaves the active group call. If last participant, the call is ended.
// @Tags		Group Calls
// @Produce		json
// @Param		groupId	path	string	true	"Group room UUID"
// @Param		callId	path	string	true	"Call UUID"
// @Success		200	{object}	StatusMessage
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Failure		404	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/groups/{groupId}/calls/{callId}/leave [post]
func LeaveGroupCallHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	groupID := r.PathValue("groupId")
	callID := r.PathValue("callId")
	if groupID == "" || callID == "" {
		JSONError(w, "Missing group or call ID", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	rdb := redisClient()
	state, err := loadGroupCallState(ctx, rdb, groupID)
	if err != nil {
		JSONError(w, "No active call in this group", http.StatusNotFound)
		return
	}

	if state.CallID != callID {
		JSONError(w, "Call ID mismatch", http.StatusBadRequest)
		return
	}

	lkRoomName := state.LKRoomName
	if lkRoomName == "" {
		lkRoomName = fmt.Sprintf("group_%s_%s", groupID, callID)
	}

	// Remove participant from LiveKit
	if RTC != nil && RTC.IsConfigured() {
		_ = RTC.RemoveParticipant(ctx, lkRoomName, userID)
	}

	// Remove user from participants (typed slice filtering)
	newParticipants := make([]string, 0, len(state.Participants))
	for _, p := range state.Participants {
		if p != userID {
			newParticipants = append(newParticipants, p)
		}
	}

	// Also remove from admins if present
	newAdmins := make([]string, 0, len(state.Admins))
	for _, a := range state.Admins {
		if a != userID {
			newAdmins = append(newAdmins, a)
		}
	}

	memberIDs := getActiveGroupMemberIDs(ctx, groupID)

	// Broadcast participant left
	broadcastGroupEvent(ctx, groupID, config.MsgTypeGroupCallParticipantLeft, map[string]interface{}{
		config.FieldRoomID: groupID,
		config.FieldCallID: callID,
		config.FieldUserID: userID,
	}, memberIDs)

	if len(newParticipants) == 0 {
		// Last participant left — end the call
		rdb.Del(ctx, config.GROUP_CALL_COLON+groupID)

		// Calculate duration (typed time.Time, no string parsing needed)
		duration := int(time.Since(state.StartedAt).Seconds())

		// Update call log
		postgress.GetRawDB().ExecContext(ctx,
			`UPDATE call_logs SET ended_at = NOW(), duration_seconds = $2
			 WHERE call_id = $1`, callID, duration,
		)

		// Broadcast call ended
		broadcastGroupEvent(ctx, groupID, config.MsgTypeGroupCallEnded, map[string]interface{}{
			config.FieldRoomID: groupID,
			config.FieldCallID: callID,
		}, memberIDs)
	} else {
		// Update state with remaining participants
		state.Participants = newParticipants
		state.Admins = newAdmins
		saveGroupCallState(ctx, rdb, groupID, state)
	}

	JSONMessage(w, "ok", "Left the call")
}

// GetGroupCallStatusHandler returns the status of an active group call.
//
// @Summary		Get group call status
// @Description	Returns participants, admins, and call duration for an active group call.
// @Tags		Group Calls
// @Produce		json
// @Param		groupId	path	string	true	"Group room UUID"
// @Param		callId	path	string	true	"Call UUID"
// @Success		200	{object}	models.GroupCallStatusResponse
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Failure		403	{object}	StatusMessage
// @Failure		404	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/groups/{groupId}/calls/{callId} [get]
func GetGroupCallStatusHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	groupID := r.PathValue("groupId")
	callID := r.PathValue("callId")
	if groupID == "" || callID == "" {
		JSONError(w, "Missing group or call ID", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Verify membership
	if !isGroupMember(ctx, groupID, userID) {
		JSONError(w, "Not a member of this group", http.StatusForbidden)
		return
	}

	rdb := redisClient()
	state, err := loadGroupCallState(ctx, rdb, groupID)
	if err != nil {
		JSONError(w, "No active call in this group", http.StatusNotFound)
		return
	}

	if state.CallID != callID {
		JSONError(w, "Call ID mismatch", http.StatusBadRequest)
		return
	}

	// Calculate duration (typed time.Time)
	durationSecs := int(time.Since(state.StartedAt).Seconds())

	// Build enriched participant list from LiveKit's real-time state
	participants := make([]models.GroupCallParticipant, 0, len(state.Participants))
	if RTC != nil && RTC.IsConfigured() && state.LKRoomName != "" {
		lkParticipants, lkErr := RTC.ListParticipants(ctx, state.LKRoomName)
		if lkErr == nil {
			lkMap := make(map[string]*models.GroupCallParticipant, len(lkParticipants))
			for _, lkp := range lkParticipants {
				detail := rtc.ToParticipantDetail(lkp)
				gp := &models.GroupCallParticipant{
					UserID:   detail.Identity,
					JoinedAt: time.Unix(detail.JoinedAt, 0),
				}
				// Detect audio/video/screen from tracks
				for _, t := range lkp.GetTracks() {
					if t.GetMuted() {
						continue
					}
					switch {
					case t.GetType() == 0 && t.GetSource() == 1: // AUDIO + MICROPHONE
						gp.Audio = true
					case t.GetType() == 1 && t.GetSource() == 2: // VIDEO + CAMERA
						gp.Video = true
					case t.GetType() == 1 && t.GetSource() == 3: // VIDEO + SCREEN_SHARE
						gp.Screen = true
					}
				}
				lkMap[detail.Identity] = gp
			}
			for _, uid := range state.Participants {
				if gp, ok := lkMap[uid]; ok {
					participants = append(participants, *gp)
				} else {
					participants = append(participants, models.GroupCallParticipant{UserID: uid})
				}
			}
		}
	}

	// Fallback if LiveKit didn't return data
	if len(participants) == 0 {
		for _, uid := range state.Participants {
			participants = append(participants, models.GroupCallParticipant{UserID: uid})
		}
	}

	admins := state.Admins
	if admins == nil {
		admins = []string{}
	}

	JSONSuccess(w, models.GroupCallStatusResponse{
		CallID:       state.CallID,
		InitiatedBy:  state.InitiatedBy,
		CallType:     state.CallType,
		Participants: participants,
		Admins:       admins,
		DurationSecs: durationSecs,
	})
}

// redisClient is a convenience helper to get the Redis client.
func redisClient() *goredis.Client {
	return redis.GetRawClient()
}

// ---------------------------------------------------------------------------
// Typed State Helpers
// ---------------------------------------------------------------------------

// loadGroupCallState loads and unmarshals a GroupCallState from Redis.
func loadGroupCallState(ctx context.Context, rdb *goredis.Client, groupID string) (*models.GroupCallState, error) {
	data, err := rdb.Get(ctx, config.GROUP_CALL_COLON+groupID).Result()
	if err != nil {
		return nil, err
	}
	var state models.GroupCallState
	if err := json.Unmarshal([]byte(data), &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// saveGroupCallState marshals and saves a GroupCallState to Redis with 24h TTL.
func saveGroupCallState(ctx context.Context, rdb *goredis.Client, groupID string, state *models.GroupCallState) {
	data, _ := json.Marshal(state)
	rdb.Set(ctx, config.GROUP_CALL_COLON+groupID, data, 24*time.Hour)
}

// isCallAdmin checks if a user is a call admin or a group admin (fallback).
// Dual-layer: explicit call admins + implicit group admin fallback.
func isCallAdmin(state *models.GroupCallState, userID string, ctx context.Context, groupID string) bool {
	for _, a := range state.Admins {
		if a == userID {
			return true
		}
	}
	return isGroupAdmin(ctx, groupID, userID)
}

// containsString checks if a string exists in a slice.
func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// removeString returns a new slice with the given string removed.
func removeString(slice []string, s string) []string {
	out := make([]string, 0, len(slice))
	for _, v := range slice {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// MuteParticipantHandler mutes or unmutes a participant's track.
//
// @Summary		Mute/unmute participant
// @Description	Mutes or unmutes a participant's audio, video, or screen track. Requires call admin.
// @Tags		Group Calls
// @Accept		json
// @Produce		json
// @Param		groupId	path	string						true	"Group room UUID"
// @Param		callId	path	string						true	"Call UUID"
// @Param		body	body	models.MuteParticipantRequest	true	"Mute details"
// @Success		200	{object}	StatusMessage
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Failure		403	{object}	StatusMessage
// @Failure		404	{object}	StatusMessage
// @Failure		503	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/groups/{groupId}/calls/{callId}/mute [post]
func MuteParticipantHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	groupID := r.PathValue("groupId")
	callID := r.PathValue("callId")
	if groupID == "" || callID == "" {
		JSONError(w, "Missing group or call ID", http.StatusBadRequest)
		return
	}

	var req models.MuteParticipantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.UserID == "" {
		JSONError(w, "userId is required", http.StatusBadRequest)
		return
	}
	if req.TrackType != "audio" && req.TrackType != "video" && req.TrackType != "screen" {
		JSONError(w, "trackType must be one of: audio, video, screen", http.StatusBadRequest)
		return
	}

	if RTC == nil || !RTC.IsConfigured() {
		JSONError(w, "Group calls are not available", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	if !isGroupMember(ctx, groupID, userID) {
		JSONError(w, "Not a member of this group", http.StatusForbidden)
		return
	}

	rdb := redisClient()
	state, err := loadGroupCallState(ctx, rdb, groupID)
	if err != nil {
		JSONError(w, "No active call in this group", http.StatusNotFound)
		return
	}

	if state.CallID != callID {
		JSONError(w, "Call ID mismatch", http.StatusBadRequest)
		return
	}

	if !isCallAdmin(state, userID, ctx, groupID) {
		JSONError(w, "Only call admins can mute participants", http.StatusForbidden)
		return
	}

	if !containsString(state.Participants, req.UserID) {
		JSONError(w, "Target user is not in the call", http.StatusBadRequest)
		return
	}

	if req.UserID == userID {
		JSONError(w, "Cannot mute yourself via this endpoint, use client-side toggle", http.StatusBadRequest)
		return
	}

	// Option B (track-level, preferred): resolve track SID from LiveKit
	muted := false
	participant, getErr := RTC.GetParticipant(ctx, state.LKRoomName, req.UserID)
	if getErr == nil && participant != nil {
		for _, t := range participant.GetTracks() {
			trackMatch := false
			switch req.TrackType {
			case "audio":
				trackMatch = t.GetType() == 0 && t.GetSource() == 1 // AUDIO + MICROPHONE
			case "video":
				trackMatch = t.GetType() == 1 && t.GetSource() == 2 // VIDEO + CAMERA
			case "screen":
				trackMatch = t.GetType() == 1 && t.GetSource() == 3 // VIDEO + SCREEN_SHARE
			}
			if trackMatch {
				_, muteErr := RTC.MutePublishedTrack(ctx, state.LKRoomName, req.UserID, t.GetSid(), req.Muted)
				if muteErr == nil {
					muted = true
				}
				break
			}
		}
	}

	// Option A (fallback — permission-level): revoke/restore publishing rights
	if !muted {
		perm := &rtc.ParticipantPermission{
			CanPublish:     !req.Muted,
			CanSubscribe:   true,
			CanPublishData: true,
		}
		_, _ = RTC.UpdateParticipant(ctx, state.LKRoomName, req.UserID, nil, perm)
	}

	// Broadcast mute/unmute event
	memberIDs := getActiveGroupMemberIDs(ctx, groupID)
	eventType := config.MsgTypeGroupCallParticipantMuted
	if !req.Muted {
		eventType = config.MsgTypeGroupCallParticipantUnmuted
	}
	broadcastGroupEvent(ctx, groupID, eventType, map[string]interface{}{
		config.FieldRoomID:    groupID,
		config.FieldCallID:    callID,
		config.FieldUserID:    req.UserID,
		config.FieldMutedBy:   userID,
		config.FieldTrackType: req.TrackType,
		config.FieldMuted:     req.Muted,
	}, memberIDs)

	JSONMessage(w, "ok", "Participant muted")
}

// KickParticipantHandler kicks a participant from the call.
//
// @Summary		Kick participant
// @Description	Removes a participant from the active group call. Requires call admin.
// @Tags		Group Calls
// @Accept		json
// @Produce		json
// @Param		groupId	path	string						true	"Group room UUID"
// @Param		callId	path	string						true	"Call UUID"
// @Param		body	body	models.KickParticipantRequest	true	"User to kick"
// @Success		200	{object}	StatusMessage
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Failure		403	{object}	StatusMessage
// @Failure		404	{object}	StatusMessage
// @Failure		503	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/groups/{groupId}/calls/{callId}/kick [post]
func KickParticipantHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	groupID := r.PathValue("groupId")
	callID := r.PathValue("callId")
	if groupID == "" || callID == "" {
		JSONError(w, "Missing group or call ID", http.StatusBadRequest)
		return
	}

	var req models.KickParticipantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.UserID == "" {
		JSONError(w, "userId is required", http.StatusBadRequest)
		return
	}

	if RTC == nil || !RTC.IsConfigured() {
		JSONError(w, "Group calls are not available", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	if !isGroupMember(ctx, groupID, userID) {
		JSONError(w, "Not a member of this group", http.StatusForbidden)
		return
	}

	rdb := redisClient()
	state, err := loadGroupCallState(ctx, rdb, groupID)
	if err != nil {
		JSONError(w, "No active call in this group", http.StatusNotFound)
		return
	}

	if state.CallID != callID {
		JSONError(w, "Call ID mismatch", http.StatusBadRequest)
		return
	}

	if !isCallAdmin(state, userID, ctx, groupID) {
		JSONError(w, "Only call admins can kick participants", http.StatusForbidden)
		return
	}

	if !containsString(state.Participants, req.UserID) {
		JSONError(w, "Target user is not in the call", http.StatusBadRequest)
		return
	}

	if req.UserID == userID {
		JSONError(w, "Cannot kick yourself, use the leave endpoint", http.StatusBadRequest)
		return
	}

	// Remove from LiveKit
	_ = RTC.RemoveParticipant(ctx, state.LKRoomName, req.UserID)

	// Remove from state
	state.Participants = removeString(state.Participants, req.UserID)
	state.Admins = removeString(state.Admins, req.UserID)

	memberIDs := getActiveGroupMemberIDs(ctx, groupID)

	// Broadcast kick event
	broadcastGroupEvent(ctx, groupID, config.MsgTypeGroupCallParticipantKicked, map[string]interface{}{
		config.FieldRoomID:   groupID,
		config.FieldCallID:   callID,
		config.FieldUserID:   req.UserID,
		config.FieldKickedBy: userID,
	}, memberIDs)

	if len(state.Participants) == 0 {
		// Last participant kicked — end the call
		rdb.Del(ctx, config.GROUP_CALL_COLON+groupID)

		duration := int(time.Since(state.StartedAt).Seconds())
		postgress.GetRawDB().ExecContext(ctx,
			`UPDATE call_logs SET ended_at = NOW(), duration_seconds = $2
			 WHERE call_id = $1`, callID, duration,
		)

		broadcastGroupEvent(ctx, groupID, config.MsgTypeGroupCallEnded, map[string]interface{}{
			config.FieldRoomID: groupID,
			config.FieldCallID: callID,
		}, memberIDs)
	} else {
		saveGroupCallState(ctx, rdb, groupID, state)
	}

	JSONMessage(w, "ok", "Participant removed from call")
}

// PromoteCallAdminHandler promotes a call participant to call admin.
//
// @Summary		Promote call admin
// @Description	Promotes a participant to call-level admin. Requires existing call admin.
// @Tags		Group Calls
// @Accept		json
// @Produce		json
// @Param		groupId	path	string					true	"Group room UUID"
// @Param		callId	path	string					true	"Call UUID"
// @Param		body	body	models.CallAdminRequest	true	"User to promote"
// @Success		200	{object}	StatusMessage
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Failure		403	{object}	StatusMessage
// @Failure		404	{object}	StatusMessage
// @Failure		409	{object}	StatusMessage	"Already admin"
// @Failure		503	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/groups/{groupId}/calls/{callId}/admins [post]
func PromoteCallAdminHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	groupID := r.PathValue("groupId")
	callID := r.PathValue("callId")
	if groupID == "" || callID == "" {
		JSONError(w, "Missing group or call ID", http.StatusBadRequest)
		return
	}

	var req models.CallAdminRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.UserID == "" {
		JSONError(w, "userId is required", http.StatusBadRequest)
		return
	}

	if RTC == nil || !RTC.IsConfigured() {
		JSONError(w, "Group calls are not available", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	if !isGroupMember(ctx, groupID, userID) {
		JSONError(w, "Not a member of this group", http.StatusForbidden)
		return
	}

	rdb := redisClient()
	state, err := loadGroupCallState(ctx, rdb, groupID)
	if err != nil {
		JSONError(w, "No active call in this group", http.StatusNotFound)
		return
	}

	if state.CallID != callID {
		JSONError(w, "Call ID mismatch", http.StatusBadRequest)
		return
	}

	if !isCallAdmin(state, userID, ctx, groupID) {
		JSONError(w, "Only call admins can promote participants", http.StatusForbidden)
		return
	}

	if !containsString(state.Participants, req.UserID) {
		JSONError(w, "Target user is not in the call", http.StatusBadRequest)
		return
	}

	if containsString(state.Admins, req.UserID) {
		JSONError(w, "User is already a call admin", http.StatusConflict)
		return
	}

	// Promote
	state.Admins = append(state.Admins, req.UserID)
	saveGroupCallState(ctx, rdb, groupID, state)

	// Optionally set metadata on LiveKit participant
	adminMeta := `{"role":"admin"}`
	_, _ = RTC.UpdateParticipant(ctx, state.LKRoomName, req.UserID, &adminMeta, nil)

	// Broadcast admin granted event
	memberIDs := getActiveGroupMemberIDs(ctx, groupID)
	broadcastGroupEvent(ctx, groupID, config.MsgTypeGroupCallAdminGranted, map[string]interface{}{
		config.FieldRoomID:    groupID,
		config.FieldCallID:    callID,
		config.FieldUserID:    req.UserID,
		config.FieldGrantedBy: userID,
		config.FieldRole:      config.RoleAdmin,
	}, memberIDs)

	JSONMessage(w, "ok", "Participant promoted to call admin")
}

// ForceEndCallHandler force-ends a group call.
//
// @Summary		Force end call
// @Description	Force-ends an active group call. Kicks all participants. Requires call admin.
// @Tags		Group Calls
// @Produce		json
// @Param		groupId	path	string	true	"Group room UUID"
// @Param		callId	path	string	true	"Call UUID"
// @Success		200	{object}	StatusMessage
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Failure		403	{object}	StatusMessage
// @Failure		404	{object}	StatusMessage
// @Failure		503	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/groups/{groupId}/calls/{callId}/end [post]
func ForceEndCallHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	groupID := r.PathValue("groupId")
	callID := r.PathValue("callId")
	if groupID == "" || callID == "" {
		JSONError(w, "Missing group or call ID", http.StatusBadRequest)
		return
	}

	if RTC == nil || !RTC.IsConfigured() {
		JSONError(w, "Group calls are not available", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	if !isGroupMember(ctx, groupID, userID) {
		JSONError(w, "Not a member of this group", http.StatusForbidden)
		return
	}

	rdb := redisClient()
	state, err := loadGroupCallState(ctx, rdb, groupID)
	if err != nil {
		JSONError(w, "No active call in this group", http.StatusNotFound)
		return
	}

	if state.CallID != callID {
		JSONError(w, "Call ID mismatch", http.StatusBadRequest)
		return
	}

	if !isCallAdmin(state, userID, ctx, groupID) {
		JSONError(w, "Only call admins can force-end a call", http.StatusForbidden)
		return
	}

	// Delete LiveKit room (kicks all participants at once — more efficient than looping)
	_ = RTC.DeleteRoom(ctx, state.LKRoomName)

	// Delete Redis key
	rdb.Del(ctx, config.GROUP_CALL_COLON+groupID)

	// Calculate duration
	duration := int(time.Since(state.StartedAt).Seconds())

	// Update call_logs
	postgress.GetRawDB().ExecContext(ctx,
		`UPDATE call_logs SET ended_at = NOW(), duration_seconds = $2
		 WHERE call_id = $1`, callID, duration,
	)

	// Broadcast force-ended event
	memberIDs := getActiveGroupMemberIDs(ctx, groupID)
	broadcastGroupEvent(ctx, groupID, config.MsgTypeGroupCallForceEnded, map[string]interface{}{
		config.FieldRoomID:  groupID,
		config.FieldCallID:  callID,
		config.FieldEndedBy: userID,
	}, memberIDs)

	JSONMessage(w, "ok", "Call ended")
}
