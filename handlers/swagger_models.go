package handlers

// ---------------------------------------------------------------------------
// Swagger Documentation-Only Models
//
// These structs mirror the JSON shapes returned by handlers that stream
// raw Postgres JSON (row_to_json / json_agg). They are NOT used in actual
// marshaling — they exist solely so swaggo/swag can generate accurate
// OpenAPI schema definitions.
// ---------------------------------------------------------------------------

// UserProfile is the doc-only response shape for GET /api/v1/users/me.
type UserProfile struct {
	ID           string  `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Email        string  `json:"email" example:"user@example.com"`
	Name         string  `json:"name" example:"John Doe"`
	Username     *string `json:"username" example:"johndoe"`
	AvatarURL    *string `json:"avatar_url" example:"https://lh3.googleusercontent.com/photo.jpg"`
	Mobile       *string `json:"mobile" example:"+919876543210"`
	Gender       *string `json:"gender" example:"Male"`
	IsPrivate    bool    `json:"is_private" example:"false"`
	ShowLastSeen bool    `json:"show_last_seen" example:"true"`
	CreatedAt    string  `json:"created_at" example:"2025-01-01T00:00:00Z"`
}

// UpdateMeRequest is the doc-only request body for PATCH /api/v1/users/me.
type UpdateMeRequest struct {
	Username     *string `json:"username,omitempty" example:"ninja"`
	Name         *string `json:"name,omitempty" example:"New Name"`
	Mobile       *string `json:"mobile,omitempty" example:"+919876543210"`
	Gender       *string `json:"gender,omitempty" example:"Male"`
	AvatarURL    *string `json:"avatar_url,omitempty" example:"https://example.com/avatar.jpg"`
	IsPrivate    *bool   `json:"is_private,omitempty" example:"false"`
	ShowLastSeen *bool   `json:"show_last_seen,omitempty" example:"true"`
}

// PutMeRequest is the doc-only request body for PUT /api/v1/users/me.
type PutMeRequest struct {
	Username     string  `json:"username" example:"ninja"`
	Name         string  `json:"name" example:"Ninja Coder"`
	Mobile       *string `json:"mobile,omitempty" example:"+919876543210"`
	Gender       *string `json:"gender,omitempty" example:"Male"`
	IsPrivate    *bool   `json:"is_private,omitempty" example:"false"`
	ShowLastSeen *bool   `json:"show_last_seen,omitempty" example:"true"`
}

// UserSearchResult is the doc-only item in the search response array.
type UserSearchResult struct {
	ID        string  `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Name      string  `json:"name" example:"John Doe"`
	Username  *string `json:"username" example:"johndoe"`
	AvatarURL *string `json:"avatar_url" example:"https://lh3.googleusercontent.com/photo.jpg"`
}

// RoomListItem is the doc-only item in the rooms list response array.
type RoomListItem struct {
	RoomID             string       `json:"room_id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Name               *string      `json:"name"`
	Type               string       `json:"type" example:"DM"`
	GroupAvatar        *string      `json:"group_avatar"`
	CreatedBy          string       `json:"created_by"`
	LastMessagePreview *string      `json:"last_message_preview" example:"Hello!"`
	LastMessageAt      *string      `json:"last_message_at" example:"2025-01-01T12:00:00Z"`
	UnreadCount        int          `json:"unread_count" example:"3"`
	MemberCount        int          `json:"member_count" example:"2"`
	Members            []RoomMember `json:"members"`
}

// RoomMember is a member within a room response.
type RoomMember struct {
	ID        string  `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Username  *string `json:"username" example:"johndoe"`
	Name      *string `json:"name" example:"John Doe"`
	AvatarURL *string `json:"avatar_url"`
	LastSeen  *string `json:"last_seen_at,omitempty"`
	IsOnline  bool    `json:"is_online" example:"true"`
}

// MessageItem is the doc-only item in the messages response array.
type MessageItem struct {
	ID           int64   `json:"id" example:"42"`
	SenderID     string  `json:"sender_id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Content      string  `json:"content" example:"Hello, world!"`
	CreatedAt    string  `json:"created_at" example:"2025-01-01T12:00:00Z"`
	SenderName   string  `json:"sender_name" example:"John Doe"`
	SenderAvatar string  `json:"sender_avatar"`
	Status       *string `json:"status" example:"read"`
}

// DMRequestItem is the doc-only item in the DM requests response array.
type DMRequestItem struct {
	RoomID             string  `json:"room_id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Type               string  `json:"type" example:"DM"`
	LastMessageAt      *string `json:"last_message_at"`
	SenderID           string  `json:"sender_id"`
	SenderName         string  `json:"sender_name" example:"Alice"`
	SenderUsername     string  `json:"sender_username" example:"alice"`
	SenderAvatar       string  `json:"sender_avatar"`
	LastMessagePreview *string `json:"last_message_preview" example:"Hey there!"`
}

// FriendItem is the doc-only item in the friends list response array.
type FriendItem struct {
	ID           string  `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Name         string  `json:"name" example:"Bob"`
	Username     *string `json:"username" example:"bob"`
	AvatarURL    *string `json:"avatar_url"`
	IsPrivate    bool    `json:"is_private" example:"false"`
	FriendsSince string  `json:"friends_since" example:"2025-01-01T00:00:00Z"`
	LastSeenAt   *string `json:"last_seen_at,omitempty"`
	IsOnline     bool    `json:"is_online" example:"true"`
}

// FriendRequestItem is the doc-only item in the friend requests response.
type FriendRequestItem struct {
	RequestID int64  `json:"request_id" example:"1"`
	Status    string `json:"status" example:"pending"`
	CreatedAt string `json:"created_at" example:"2025-01-01T00:00:00Z"`
	UserID    string `json:"user_id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Name      string `json:"name" example:"Alice"`
	Username  string `json:"username" example:"alice"`
	AvatarURL string `json:"avatar_url"`
}

// CallHistoryEntry is the doc-only item in the call history response array.
type CallHistoryEntry struct {
	CallID       string  `json:"callId" example:"550e8400-e29b-41d4-a716-446655440000"`
	CallType     string  `json:"callType" example:"video"`
	Tier         string  `json:"tier" example:"sfu"`
	StartedAt    string  `json:"startedAt" example:"2025-01-01T12:00:00Z"`
	EndedAt      *string `json:"endedAt" example:"2025-01-01T12:05:00Z"`
	Duration     *int    `json:"durationSeconds" example:"300"`
	InitiatedBy  string  `json:"initiatedBy" example:"550e8400-e29b-41d4-a716-446655440000"`
	CallerName   string  `json:"callerName" example:"John Doe"`
	CallerAvatar string  `json:"callerAvatar"`
}

// MatchResponse is the doc-only response when a match is found instantly.
type MatchResponse struct {
	Status  string `json:"status" example:"matched"`
	Message string `json:"message" example:"Match found instantly"`
	RoomID  string `json:"room_id" example:"stranger_550e8400-e29b-41d4-a716-446655440000"`
}

// CreateDMFullResponse is the actual full response for creating a DM (may include pending).
type CreateDMFullResponse struct {
	RoomID   string `json:"room_id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Existing bool   `json:"existing" example:"false"`
	Pending  bool   `json:"pending" example:"false"`
}

// GroupCreateResponse is the doc-only response for group creation.
type GroupCreateResponse struct {
	RoomID     string `json:"roomId" example:"550e8400-e29b-41d4-a716-446655440000"`
	Name       string `json:"name" example:"My Group"`
	Visibility string `json:"visibility" example:"public"`
	InviteCode string `json:"inviteCode" example:"a1b2c3d4e5f6g7h8"`
}

// MembersAddedResponse is the doc-only response for adding group members.
type MembersAddedResponse struct {
	Added []string `json:"added"`
}

// InviteCodeResponse is the doc-only response for generating an invite code.
type InviteCodeResponse struct {
	InviteCode string `json:"inviteCode" example:"a1b2c3d4e5f6g7h8"`
}

// JoinByInviteResponse is the doc-only response for joining via invite link.
type JoinByInviteResponse struct {
	RoomID  string `json:"roomId" example:"550e8400-e29b-41d4-a716-446655440000"`
	Message string `json:"message" example:"Joined group via invite link"`
}
