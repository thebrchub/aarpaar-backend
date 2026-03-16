// Package models provides shared data types used across handlers and services.
package models

import "time"

// ---------------------------------------------------------------------------
// Group Chat Types
// ---------------------------------------------------------------------------

// CreateGroupRequest is the JSON body for POST /api/v1/groups.
type CreateGroupRequest struct {
	Name       string   `json:"name"`
	AvatarURL  string   `json:"avatarUrl,omitempty"`
	MemberIDs  []string `json:"memberIds,omitempty"`  // Optional initial member UUIDs
	Visibility string   `json:"visibility,omitempty"` // "public" (default) or "private"
}

// UpdateGroupRequest is the JSON body for PATCH /api/v1/groups/{groupId}.
type UpdateGroupRequest struct {
	Name       *string `json:"name,omitempty"`
	AvatarURL  *string `json:"avatarUrl,omitempty"`
	Visibility *string `json:"visibility,omitempty"` // "public" or "private"
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
	RoomID      string        `json:"roomId"`
	Name        string        `json:"name"`
	AvatarURL   string        `json:"avatarUrl"`
	Type        string        `json:"type"`
	CreatedBy   string        `json:"createdBy"`
	MaxMembers  int           `json:"maxMembers"`
	Visibility  string        `json:"visibility"`
	InviteCode  string        `json:"inviteCode,omitempty"`
	MemberCount int           `json:"memberCount"`
	Members     []GroupMember `json:"members,omitempty"`
}

// GroupListItem is a lightweight shape for listing/searching public groups.
type GroupListItem struct {
	RoomID      string `json:"roomId"`
	Name        string `json:"name"`
	AvatarURL   string `json:"avatarUrl"`
	Visibility  string `json:"visibility"`
	MemberCount int    `json:"memberCount"`
	CreatedBy   string `json:"createdBy"`
	IsMember    bool   `json:"isMember"`
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

// StartGroupCallResponse is the JSON shape returned when starting a group call.
type StartGroupCallResponse struct {
	CallID     string `json:"callId"`
	Token      string `json:"token"`
	LiveKitURL string `json:"livekitUrl"`
}

// StartGroupCallRequest is the JSON body for POST /api/v1/groups/{groupId}/calls.
type StartGroupCallRequest struct {
	CallType string `json:"callType"` // "audio" or "video"
}

// ---------------------------------------------------------------------------
// Arena (Feed) Types
// ---------------------------------------------------------------------------

// ArenaLimits holds admin-configurable limits for The Arena.
// Loaded from app_settings.arena_limits; cached in Redis.
type ArenaLimits struct {
	MaxPostsPerUser   int `json:"max_posts_per_user"`
	MaxMediaPerPost   int `json:"max_media_per_post"`
	MaxImageSizeKB    int `json:"max_image_size_kb"`
	MaxVideoSizeKB    int `json:"max_video_size_kb"`
	MaxCaptionLength  int `json:"max_caption_length"`  // Paid/donated users
	MaxCommentLength  int `json:"max_comment_length"`  // Paid/donated users
	FreeCaptionLength int `json:"free_caption_length"` // Free users
	FreeCommentLength int `json:"free_comment_length"` // Free users
	TrendingThreshold int `json:"trending_threshold"`
	PresignPutMins    int `json:"presign_put_mins"` // Upload URL validity in minutes
	PresignGetMins    int `json:"presign_get_mins"` // Download URL validity in minutes
}

// CreatePostRequest is the JSON body for POST /api/v1/arena/posts.
type CreatePostRequest struct {
	Caption    string      `json:"caption"`
	Visibility string      `json:"visibility,omitempty"` // "public" (default) or "friends"
	Media      []PostMedia `json:"media"`
}

// PostMedia is a single media item within a post.
type PostMedia struct {
	ObjectKey   string `json:"objectKey"`
	MediaType   string `json:"mediaType"` // "image" or "video"
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	DurationMs  int    `json:"durationMs,omitempty"`
	PreviewHash string `json:"previewHash,omitempty"` // BlurHash string
	SortOrder   int    `json:"sortOrder"`
}

// PostResponse is the JSON shape returned for a single post.
type PostResponse struct {
	ID             int64               `json:"id"`
	UserID         string              `json:"userId"`
	Username       string              `json:"username,omitempty"`
	DisplayName    string              `json:"displayName,omitempty"`
	AvatarURL      string              `json:"avatarUrl,omitempty"`
	Caption        string              `json:"caption"`
	PostType       string              `json:"postType"`
	OriginalPostID *int64              `json:"originalPostId,omitempty"`
	OriginalPost   *PostResponse       `json:"originalPost,omitempty"`
	Visibility     string              `json:"visibility"`
	IsPinned       bool                `json:"isPinned"`
	LikeCount      int                 `json:"likeCount"`
	CommentCount   int                 `json:"commentCount"`
	RepostCount    int                 `json:"repostCount"`
	ViewCount      int                 `json:"viewCount"`
	BookmarkCount  int                 `json:"bookmarkCount"`
	HasLiked       bool                `json:"hasLiked"`
	HasBookmarked  bool                `json:"hasBookmarked"`
	Media          []PostMediaResponse `json:"media"`
	CreatedAt      time.Time           `json:"createdAt"`
}

// PostMediaResponse is the JSON shape for a media item with a presigned URL.
type PostMediaResponse struct {
	ID          int64  `json:"id"`
	MediaType   string `json:"mediaType"`
	URL         string `json:"url"` // presigned GET URL
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	DurationMs  int    `json:"durationMs,omitempty"`
	PreviewHash string `json:"previewHash,omitempty"`
	SortOrder   int    `json:"sortOrder"`
}

// PresignRequest is the JSON body for POST /api/v1/arena/media/presign.
type PresignRequest struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
}

// PresignResponse is returned by the presign endpoint.
type PresignResponse struct {
	URL       string            `json:"url"`
	Fields    map[string]string `json:"fields"`
	ObjectKey string            `json:"objectKey"`
}

// RepostRequest is the JSON body for POST /api/v1/arena/posts/{postId}/repost.
type RepostRequest struct {
	Caption string `json:"caption,omitempty"`
}

// CreateCommentRequest is the JSON body for POST /api/v1/arena/posts/{postId}/comments.
type CreateCommentRequest struct {
	Body      string `json:"body"`
	ParentID  *int64 `json:"parentId,omitempty"` // nil = top-level comment
	GifURL    string `json:"gifUrl,omitempty"`
	GifWidth  int    `json:"gifWidth,omitempty"`
	GifHeight int    `json:"gifHeight,omitempty"`
}

// CommentResponse is the JSON shape for a single comment.
type CommentResponse struct {
	ID         int64     `json:"id"`
	PostID     int64     `json:"postId"`
	UserID     string    `json:"userId"`
	Username   string    `json:"username,omitempty"`
	AvatarURL  string    `json:"avatarUrl,omitempty"`
	Body       string    `json:"body"`
	Depth      int       `json:"depth"`
	LikeCount  int       `json:"likeCount"`
	HasLiked   bool      `json:"hasLiked"`
	ReplyCount int       `json:"replyCount"`
	GifURL     string    `json:"gifUrl,omitempty"`
	GifWidth   int       `json:"gifWidth,omitempty"`
	GifHeight  int       `json:"gifHeight,omitempty"`
	ParentID   *int64    `json:"parentId,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// ReportRequest is the JSON body for reporting a post or comment.
type ReportRequest struct {
	Reason string `json:"reason"`
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
