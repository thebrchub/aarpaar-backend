package handlers

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// GET /api/v1/calls/config
//
// Returns ICE server configuration for WebRTC P2P calls.
// Clients use this to initialize RTCPeerConnection with STUN/TURN servers.
//
// For group calls (3+ participants), LiveKit Cloud handles ICE internally,
// so this endpoint is only needed for 1:1 P2P calls.
// ---------------------------------------------------------------------------

// iceServer matches the WebRTC RTCIceServer interface.
type iceServer struct {
	URLs       any    `json:"urls"` // string or []string
	Username   string `json:"username,omitempty"`
	Credential string `json:"credential,omitempty"`
}

// callConfig is the response shape for GET /calls/config.
type callConfig struct {
	ICEServers []iceServer `json:"iceServers"`
	LiveKit    *lkConfig   `json:"livekit,omitempty"`
}

// lkConfig exposes LiveKit Cloud URL (token generation is server-side only).
type lkConfig struct {
	URL string `json:"url"`
}

func GetCallConfigHandler(w http.ResponseWriter, r *http.Request) {
	servers := []iceServer{
		{URLs: "stun:stun.l.google.com:19302"},
		{URLs: "stun:stun.cloudflare.com:3478"},
	}

	// Add TURN server if configured
	if config.TURNURL != "" {
		servers = append(servers, iceServer{
			URLs:       config.TURNURL,
			Username:   config.TURNUsername,
			Credential: config.TURNPassword,
		})
	}

	// Add secondary TURN (TCP/TLS fallback) if configured
	if config.TURNURL2 != "" {
		servers = append(servers, iceServer{
			URLs:       config.TURNURL2,
			Username:   config.TURNUsername2,
			Credential: config.TURNPassword2,
		})
	}

	resp := callConfig{
		ICEServers: servers,
	}

	// Expose LiveKit URL if configured (so clients know group calls are available)
	if config.LiveKitURL != "" {
		resp.LiveKit = &lkConfig{URL: config.LiveKitURL}
	}

	JSONSuccess(w, resp)
}

// ---------------------------------------------------------------------------
// GET /api/v1/calls/history?limit=20&offset=0
//
// Returns the user's call history from the call_logs table.
// Includes caller/callee info and call metadata.
// ---------------------------------------------------------------------------

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
