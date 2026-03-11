package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"
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
func GetCallHistoryHandler(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(config.UserIDKey).(string)

	limit, offset := parsePagination(r)

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
			COALESCE(u.avatar_url, '') AS caller_avatar,
			COALESCE(peer.name, '') AS peer_name,
			COALESCE(peer.avatar_url, '') AS peer_avatar
		FROM call_logs cl
		LEFT JOIN users u ON u.id = cl.initiated_by
		LEFT JOIN LATERAL (
			SELECT u2.name, u2.avatar_url
			FROM room_members rm2
			JOIN users u2 ON u2.id = rm2.user_id
			WHERE rm2.room_id = cl.room_id
			  AND rm2.user_id != $1
			LIMIT 1
		) peer ON true
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
		log.Printf("[calls] GetCallHistory query failed user=%s: %v", userID, err)
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
		PeerName     string  `json:"peerName"`
		PeerAvatar   string  `json:"peerAvatar"`
	}

	var calls []callEntry
	for rows.Next() {
		var c callEntry
		if err := rows.Scan(
			&c.CallID, &c.CallType, &c.Tier,
			&c.StartedAt, &c.EndedAt, &c.Duration,
			&c.InitiatedBy, &c.CallerName, &c.CallerAvatar,
			&c.PeerName, &c.PeerAvatar,
		); err != nil {
			log.Printf("[calls] Scan call history row failed: %v", err)
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
	if err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT max_members FROM rooms WHERE id = $1`, groupID,
	).Scan(&maxMembers); err != nil {
		log.Printf("[calls] maxMembers query failed group=%s: %v", groupID, err)
	}
	if maxMembers <= 0 {
		maxMembers = 50
	}

	_, err := RTC.CreateRoom(ctx, lkRoomName, maxMembers)
	if err != nil {
		log.Printf("[calls] RTC.CreateRoom failed group=%s: %v", groupID, err)
		JSONError(w, "Failed to create call room", http.StatusInternalServerError)
		return
	}

	// Get initiator's name for token
	var userName string
	if err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT COALESCE(name, '') FROM users WHERE id = $1`, userID,
	).Scan(&userName); err != nil {
		log.Printf("[calls] userName query failed user=%s: %v", userID, err)
	}

	// Generate token for the initiator (CPU-only, ~1-2μs, no network call)
	token, err := RTC.GenerateToken(lkRoomName, userID, userName, true, true)
	if err != nil {
		log.Printf("[calls] RTC.GenerateToken failed user=%s: %v", userID, err)
		JSONError(w, "Failed to create call room", http.StatusInternalServerError)
		return
	}

	// Atomically claim this group's call slot using SetNX.
	// This prevents the TOCTOU race where two concurrent requests both pass
	// an Exists check before either writes the state.
	state := models.GroupCallState{
		CallID:       callID,
		InitiatedBy:  userID,
		StartedAt:    time.Now().UTC(),
		CallType:     req.CallType,
		LKRoomName:   lkRoomName,
		Participants: []string{userID},
		Admins:       []string{userID}, // initiator is first call admin
	}
	callState, err := json.Marshal(state)
	if err != nil {
		log.Printf("[calls] Marshal call state failed group=%s: %v", groupID, err)
		JSONError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	rdb := redisClient()
	acquired, err := rdb.SetNX(ctx, config.GROUP_CALL_COLON+groupID, callState, 24*time.Hour).Result()
	if err != nil {
		log.Printf("[calls] Redis SetNX failed group=%s: %v", groupID, err)
		JSONError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if !acquired {
		// Another request won the race — clean up LiveKit room we just created
		if delErr := RTC.DeleteRoom(ctx, lkRoomName); delErr != nil {
			log.Printf("[calls] RTC.DeleteRoom cleanup failed group=%s room=%s: %v", groupID, lkRoomName, delErr)
		}
		JSONError(w, "A call is already active in this group", http.StatusConflict)
		return
	}

	// Log the call to Postgres — clean up Redis + LiveKit on failure
	if _, dbErr := postgress.GetRawDB().ExecContext(ctx,
		`INSERT INTO call_logs (call_id, room_id, initiated_by, call_type, tier, max_participants, participants)
		 VALUES ($1, $2, $3, $4, 'sfu', $5, ARRAY[$3])`,
		callID, groupID, userID, req.CallType, maxMembers,
	); dbErr != nil {
		log.Printf("[calls] Insert call_logs failed call=%s: %v — cleaning up", callID, dbErr)
		rdb.Del(ctx, config.GROUP_CALL_COLON+groupID)
		if delErr := RTC.DeleteRoom(ctx, lkRoomName); delErr != nil {
			log.Printf("[calls] RTC.DeleteRoom cleanup failed group=%s room=%s: %v", groupID, lkRoomName, delErr)
		}
		JSONError(w, "Failed to start group call", http.StatusInternalServerError)
		return
	}

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
	if err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT COALESCE(name, '') FROM users WHERE id = $1`, userID,
	).Scan(&userName); err != nil {
		log.Printf("[calls] userName query failed user=%s: %v", userID, err)
	}

	// Generate token (CPU-only, ~1-2μs, no network call)
	token, err := RTC.GenerateToken(lkRoomName, userID, userName, true, true)
	if err != nil {
		log.Printf("[calls] RTC.GenerateToken failed user=%s: %v", userID, err)
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
	if _, err := postgress.GetRawDB().ExecContext(ctx,
		`UPDATE call_logs SET participants = array_append(
			CASE WHEN $2 = ANY(participants) THEN participants
			     ELSE participants END, $2)
		 WHERE call_id = $1 AND NOT ($2 = ANY(participants))`,
		callID, userID,
	); err != nil {
		log.Printf("[calls] Update call_logs participants failed call=%s: %v", callID, err)
	}

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
		if err := RTC.RemoveParticipant(ctx, lkRoomName, userID); err != nil {
			log.Printf("[calls] RTC.RemoveParticipant failed user=%s room=%s: %v", userID, lkRoomName, err)
		}
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
		if _, err := postgress.GetRawDB().ExecContext(ctx,
			`UPDATE call_logs SET ended_at = NOW(), duration_seconds = $2
			 WHERE call_id = $1`, callID, duration,
		); err != nil {
			log.Printf("[calls] Update call_logs ended_at failed call=%s: %v", callID, err)
		}

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
	data, err := json.Marshal(state)
	if err != nil {
		log.Printf("[calls] Marshal call state failed group=%s: %v", groupID, err)
		return
	}
	if err := rdb.Set(ctx, config.GROUP_CALL_COLON+groupID, data, 24*time.Hour).Err(); err != nil {
		log.Printf("[calls] Redis Set call state failed group=%s: %v", groupID, err)
	}
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
	if err := RTC.RemoveParticipant(ctx, state.LKRoomName, req.UserID); err != nil {
		log.Printf("[calls] RTC.RemoveParticipant failed user=%s room=%s: %v", req.UserID, state.LKRoomName, err)
	}

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
		if _, err := postgress.GetRawDB().ExecContext(ctx,
			`UPDATE call_logs SET ended_at = NOW(), duration_seconds = $2
			 WHERE call_id = $1`, callID, duration,
		); err != nil {
			log.Printf("[calls] Update call_logs ended_at failed call=%s: %v", callID, err)
		}

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
	if err := RTC.DeleteRoom(ctx, state.LKRoomName); err != nil {
		log.Printf("[calls] RTC.DeleteRoom failed room=%s: %v", state.LKRoomName, err)
	}

	// Delete Redis key
	if err := rdb.Del(ctx, config.GROUP_CALL_COLON+groupID).Err(); err != nil {
		log.Printf("[calls] Redis Del call state failed group=%s: %v", groupID, err)
	}

	// Calculate duration
	duration := int(time.Since(state.StartedAt).Seconds())

	// Update call_logs
	if _, err := postgress.GetRawDB().ExecContext(ctx,
		`UPDATE call_logs SET ended_at = NOW(), duration_seconds = $2
		 WHERE call_id = $1`, callID, duration,
	); err != nil {
		log.Printf("[calls] Update call_logs ended_at failed call=%s: %v", callID, err)
	}

	// Broadcast force-ended event
	memberIDs := getActiveGroupMemberIDs(ctx, groupID)
	broadcastGroupEvent(ctx, groupID, config.MsgTypeGroupCallForceEnded, map[string]interface{}{
		config.FieldRoomID:  groupID,
		config.FieldCallID:  callID,
		config.FieldEndedBy: userID,
	}, memberIDs)

	JSONMessage(w, "ok", "Call ended")
}
