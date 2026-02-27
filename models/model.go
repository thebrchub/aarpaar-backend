// Package models provides shared data types used across handlers and services.
package models

import "time"

// ---------------------------------------------------------------------------
// Group Chat Types
// ---------------------------------------------------------------------------

// CreateGroupRequest is the JSON body for POST /api/v1/groups.
type CreateGroupRequest struct {
	Name      string   `json:"name"`
	AvatarURL string   `json:"avatarUrl,omitempty"`
	MemberIDs []string `json:"memberIds"` // Initial member UUIDs (excluding creator)
}

// UpdateGroupRequest is the JSON body for PATCH /api/v1/groups/{groupId}.
type UpdateGroupRequest struct {
	Name      *string `json:"name,omitempty"`
	AvatarURL *string `json:"avatarUrl,omitempty"`
}

// AddMembersRequest is the JSON body for POST /api/v1/groups/{groupId}/members.
type AddMembersRequest struct {
	MemberIDs []string `json:"memberIds"`
}

// PromoteAdminRequest is the JSON body for POST /api/v1/groups/{groupId}/admins.
type PromoteAdminRequest struct {
	UserID string `json:"userId"`
}

// GroupResponse is the JSON shape returned for group info endpoints.
type GroupResponse struct {
	RoomID     string        `json:"roomId"`
	Name       string        `json:"name"`
	AvatarURL  string        `json:"avatarUrl"`
	Type       string        `json:"type"`
	CreatedBy  string        `json:"createdBy"`
	MaxMembers int           `json:"maxMembers"`
	Members    []GroupMember `json:"members"`
}

// GroupMember represents a member in a group response.
type GroupMember struct {
	ID        string  `json:"id"`
	Username  *string `json:"username"`
	Name      string  `json:"name"`
	AvatarURL string  `json:"avatarUrl"`
	Role      string  `json:"role"`
	IsOnline  bool    `json:"isOnline"`
}

// ---------------------------------------------------------------------------
// Group Call Types
// ---------------------------------------------------------------------------

// GroupCallParticipant represents a user in an active group call.
type GroupCallParticipant struct {
	UserID   string    `json:"userId"`
	JoinedAt time.Time `json:"joinedAt"`
	Audio    bool      `json:"audio"`
	Video    bool      `json:"video"`
	Screen   bool      `json:"screen"`
}

// LiveKitTokenResponse is the JSON shape returned when generating a LiveKit token.
type LiveKitTokenResponse struct {
	Token      string `json:"token"`
	LiveKitURL string `json:"livekitUrl"`
}

// StartGroupCallRequest is the JSON body for POST /api/v1/groups/{groupId}/calls.
type StartGroupCallRequest struct {
	CallType string `json:"callType"` // "audio" or "video"
}

// ---------------------------------------------------------------------------
// Group Call State & Admin Types
// ---------------------------------------------------------------------------

// GroupCallState is the typed struct stored in Redis for active group calls.
// Replaces all map[string]interface{} for type safety and zero type-assertion overhead.
type GroupCallState struct {
	CallID       string    `json:"callId"`
	InitiatedBy  string    `json:"initiatedBy"`
	StartedAt    time.Time `json:"startedAt"`
	CallType     string    `json:"callType"`   // "audio" or "video"
	LKRoomName   string    `json:"lkRoomName"` // "group_{groupId}_{callId}"
	Participants []string  `json:"participants"`
	Admins       []string  `json:"admins"` // Call-level admin user IDs
}

// MuteParticipantRequest is the JSON body for POST .../calls/{callId}/mute.
type MuteParticipantRequest struct {
	UserID    string `json:"userId"`
	TrackType string `json:"trackType"` // "audio", "video", or "screen"
	Muted     bool   `json:"muted"`     // true = mute, false = unmute
}

// KickParticipantRequest is the JSON body for POST .../calls/{callId}/kick.
type KickParticipantRequest struct {
	UserID string `json:"userId"`
}

// CallAdminRequest is the JSON body for POST .../calls/{callId}/admins.
type CallAdminRequest struct {
	UserID string `json:"userId"`
}

// GroupCallStatusResponse is the enriched response for GET call status.
type GroupCallStatusResponse struct {
	CallID       string                 `json:"callId"`
	InitiatedBy  string                 `json:"initiatedBy"`
	CallType     string                 `json:"callType"`
	Participants []GroupCallParticipant `json:"participants"`
	Admins       []string               `json:"admins"`
	DurationSecs int                    `json:"durationSecs"`
}
