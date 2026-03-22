package config

import "time"

// ---------------------------------------------------------------------------
// WebSocket Message Types
// These are the "type" field values in every JSON message sent over WebSocket.
// Using constants prevents typos and makes refactoring easy.
// ---------------------------------------------------------------------------

const (
	// Client -> Server: User wants to subscribe to a chat room
	MsgTypeJoinRoom = "join_room"

	// Client -> Server: User wants to unsubscribe from a chat room
	MsgTypeLeaveRoom = "leave_room"

	// Client -> Server: Keep-alive ping from the client
	MsgTypeHeartbeat = "heartbeat"

	// Client -> Server: User is sending a chat message
	MsgTypeSendMessage = "send_message"

	// Client -> Server: User started typing in a room
	MsgTypeTypingStart = "typing_start"

	// Client -> Server: User stopped typing in a room
	MsgTypeTypingEnd = "typing_end"

	// Server -> Client: Aggregated typing status for a room
	MsgTypeTypingStatus = "typing_status"

	// Server -> Client: Structured error event
	MsgTypeError = "error"

	// Server -> Client: Confirms the server received the message
	MsgTypeSentConfirm = "message_sent_confirm"

	// Server -> Client: Routed privately to a specific user (envelope type)
	MsgTypePrivate = "private"

	// Server -> Client: A stranger match was found
	MsgTypeMatchFound = "match_found"

	// Server -> Client: The other stranger left the chat
	MsgTypeStrangerDisconnected = "stranger_disconnected"

	// Server -> All in room: Room has been closed (stranger skip/block)
	MsgTypeRoomClosed = "room_closed"

	// Server -> Client: Partner wants to be friends (waiting for your accept)
	MsgTypeFriendRequest = "friend_request"

	// Server -> Both: Mutual friendship accepted — new DM room created
	MsgTypeFriendAccepted = "friend_accepted"

	// Server -> Client: Someone sent you a DM request (private account)
	MsgTypeDMRequest = "dm_request"

	// Server -> Client: Your DM request was accepted
	MsgTypeDMAccepted = "dm_accepted"

	// Server -> Client: A friend request you received was withdrawn by the sender
	MsgTypeFriendRequestWithdrawn = "friend_request_withdrawn"

	// Client -> Server: Mark a room as read (resets unread count)
	MsgTypeMarkRead = "mark_read"

	// Client -> Server: Mark messages as delivered (recipient came online)
	MsgTypeMarkDelivered = "mark_delivered"

	// Server -> Client: Message was delivered to the recipient's device
	MsgTypeMessageDelivered = "message_delivered"

	// Server -> Client: Recipient has read your messages
	MsgTypeMessageRead = "message_read"

	// Server -> Client: A friend came online
	MsgTypePresenceOnline = "presence_online"

	// Server -> Client: A friend went offline
	MsgTypePresenceOffline = "presence_offline"

	// ---------------------------------------------------------------------------
	// Call Signaling Message Types (WebRTC)
	// ---------------------------------------------------------------------------

	// Caller -> Server -> Callee: Incoming call notification (ring)
	MsgTypeCallRing = "call_ring"

	// Callee -> Server -> Caller: Callee accepted the call
	MsgTypeCallAccept = "call_accept"

	// Callee -> Server -> Caller: Callee rejected / busy
	MsgTypeCallReject = "call_reject"

	// Peer -> Server -> Peer: WebRTC SDP offer
	MsgTypeCallOffer = "call_offer"

	// Peer -> Server -> Peer: WebRTC SDP answer
	MsgTypeCallAnswer = "call_answer"

	// Peer -> Server -> Peer: ICE candidate exchange
	MsgTypeICECandidate = "ice_candidate"

	// Either -> Server -> Other: Hang up
	MsgTypeCallEnd = "call_end"

	// Server -> Client: Callee was offline / didn't answer
	MsgTypeCallMissed = "call_missed"

	// Server -> Client: Callee is already on another call
	MsgTypeCallBusy = "call_busy"

	// Server -> Room: A group call has started (for late joiners)
	MsgTypeCallStarted = "call_started"

	// Server -> Client: Current participants in a call
	MsgTypeCallParticipants = "call_participants"

	// Server -> Client: Dismiss ringing on other devices (accepted/rejected/ended elsewhere)
	MsgTypeCallDismiss = "call_dismiss"

	// Participant -> Server: Leave a group call (without ending it)
	MsgTypeCallLeave = "call_leave"

	// Server -> Client: Redirect from P2P to SFU (when 3rd person joins)
	MsgTypeSFURedirect = "sfu_redirect"

	// -----------------------------------------------------------------------
	// Group Lifecycle Events
	// -----------------------------------------------------------------------

	// Server -> Client: A new group was created (sent to all initial members)
	MsgTypeGroupCreated = "group_created"

	// Server -> Client: A member was added to a group
	MsgTypeMemberAdded = "member_added"

	// Server -> Client: A member was removed from a group
	MsgTypeMemberRemoved = "member_removed"

	// Server -> Client: A member left a group voluntarily
	MsgTypeMemberLeft = "member_left"

	// Server -> Client: Group metadata was updated (name, avatar)
	MsgTypeGroupUpdated = "group_updated"

	// Server -> Client: A group call has started
	MsgTypeGroupCallStarted = "group_call_started"

	// Server -> Client: A participant joined the group call
	MsgTypeGroupCallParticipantJoined = "group_call_participant_joined"

	// Server -> Client: A participant left the group call
	MsgTypeGroupCallParticipantLeft = "group_call_participant_left"

	// Server -> Client: The group call has ended
	MsgTypeGroupCallEnded = "group_call_ended"

	// Server -> All members: A participant was muted by an admin
	MsgTypeGroupCallParticipantMuted = "group_call_participant_muted"

	// Server -> All members: A participant was unmuted by an admin
	MsgTypeGroupCallParticipantUnmuted = "group_call_participant_unmuted"

	// Server -> All members: A participant was kicked from the call
	MsgTypeGroupCallParticipantKicked = "group_call_participant_kicked"

	// Server -> All members: A participant was granted call admin
	MsgTypeGroupCallAdminGranted = "group_call_admin_granted"

	// Server -> All members: The call was force-ended by an admin
	MsgTypeGroupCallForceEnded = "group_call_force_ended"

	// Server -> All members: A member was promoted to group admin
	MsgTypeMemberPromoted = "member_promoted"

	// Server -> All members: A user joined the group via self-join or invite link
	MsgTypeMemberJoined = "member_joined"

	// Server -> Target user: You have been invited to a group
	MsgTypeGroupInvite = "group_invite"

	// Server -> All members: A user accepted a group invite and joined
	MsgTypeGroupInviteAccepted = "group_invite_accepted"

	// -----------------------------------------------------------------------
	// Arena (Feed) Events
	// -----------------------------------------------------------------------

	// Server -> Client: A new post was created by someone you follow
	MsgTypePostCreated = "post_created"

	// Server -> Client: A post was deleted
	MsgTypePostDeleted = "post_deleted"

	// Server -> Client: Someone liked your post
	MsgTypePostLiked = "post_liked"

	// Server -> Client: Someone commented on your post
	MsgTypePostCommented = "post_commented"

	// Server -> Client: Someone replied to your comment
	MsgTypeCommentReplied = "comment_replied"

	// Server -> Client: A post is trending (HIGH HEAT)
	MsgTypePostTrending = "post_trending"
)

// ---------------------------------------------------------------------------
// Gender & Matchmaking Constants
// ---------------------------------------------------------------------------

const (
	GenderAny    = "Any"
	GenderMale   = "M"
	GenderFemale = "F"
)

// MatchQueueFormat is the Redis key pattern for matchmaking queues.
// Usage: fmt.Sprintf(MatchQueueFormat, gender, seekingGender)
const MatchQueueFormat = "match_queue:%s_seeking_%s"

// DefaultMatchQueue is the single matchmaking queue (no gender preference).
const DefaultMatchQueue = "match_queue:Any_seeking_Any"

// ---------------------------------------------------------------------------
// Match Action Types
// ---------------------------------------------------------------------------

const (
	ActionSkip   = "skip"
	ActionBlock  = "block"
	ActionFriend = "friend"
)

// ---------------------------------------------------------------------------
// Friend Request Statuses
// ---------------------------------------------------------------------------

const (
	FriendReqPending  = "pending"
	FriendReqAccepted = "accepted"
	FriendReqRejected = "rejected"
)

// ---------------------------------------------------------------------------
// Room Member Statuses
// ---------------------------------------------------------------------------

const (
	RoomMemberActive  = "active"  // Normal — messages visible in inbox
	RoomMemberPending = "pending" // DM request — hidden until accepted
	RoomMemberInvited = "invited" // Group invite — awaiting accept/decline
)

// ---------------------------------------------------------------------------
// Room Types
// ---------------------------------------------------------------------------

const (
	RoomTypeDM    = "DM"
	RoomTypeGroup = "GROUP"
)

// ---------------------------------------------------------------------------
// Room Member Roles
// ---------------------------------------------------------------------------

const (
	RoleMember = "member"
	RoleAdmin  = "admin"
)

// ---------------------------------------------------------------------------
// Group Visibility
// ---------------------------------------------------------------------------

const (
	VisibilityPublic  = "public"  // Anyone can discover & join
	VisibilityPrivate = "private" // Invite-only
)

// ---------------------------------------------------------------------------
// System / Identity Constants
// ---------------------------------------------------------------------------

const (
	SystemSender        = "system"   // "from" field in system-generated messages
	DefaultStrangerName = "Stranger" // Placeholder name for anonymous matches
)

// ---------------------------------------------------------------------------
// JSON Field Keys
// These are the map keys / gjson lookup paths used in WebSocket messages.
// Centralizing them here prevents typos and makes refactoring safe.
// ---------------------------------------------------------------------------

const (
	FieldType            = "type"           // Message type discriminator
	FieldRoomID          = "roomId"         // Target room identifier
	FieldTo              = "to"             // Target user for private messages
	FieldFrom            = "from"           // Sender identifier
	FieldData            = "data"           // Nested event payload
	FieldTempID          = "tempId"         // Client-generated temp ID for delivery confirmation
	FieldText            = "text"           // Message content body
	FieldPartnerID       = "partnerId"      // Real user ID of the matched partner
	FieldPartnerFakeName = "displayName"    // Display name for stranger matches
	FieldPartnerAvatar   = "partner_avatar" // Avatar URL (empty for strangers)
	FieldCode            = "code"           // Error code for error events
	FieldMessage         = "message"        // Human-readable error message
	FieldUserIDs         = "userIds"        // Array of user IDs (typing status)
	FieldUserID          = "userId"         // Single user ID (receipts)
	FieldDeliveredAt     = "deliveredAt"    // Delivery receipt timestamp
	FieldReadAt          = "readAt"         // Read receipt timestamp
	FieldLastSeenAt      = "lastSeenAt"     // Last-seen timestamp for presence
	FieldIsOnline        = "isOnline"       // Whether the user is currently online
	FieldCallID          = "callId"         // Unique call identifier
	FieldSDP             = "sdp"            // WebRTC SDP offer/answer
	FieldCandidate       = "candidate"      // ICE candidate
	FieldCallType        = "callType"       // "audio" or "video"
	FieldHasVideo        = "hasVideo"       // Whether video is enabled
	FieldName            = "name"           // Display name (group name, sender name)
	FieldAvatarURL       = "avatarUrl"      // Avatar URL
	FieldMembers         = "members"        // Members array
	FieldAddedBy         = "addedBy"        // Who added a member
	FieldRemovedBy       = "removedBy"      // Who removed a member
	FieldInitiatedBy     = "initiatedBy"    // Who started a call
	FieldFromName        = "fromName"       // Sender display name on messages
	FieldReplyTo         = "replyTo"        // ID of the message being replied to
	FieldMentions        = "mentions"       // Array of mentioned user IDs
	FieldMutedBy         = "mutedBy"        // Who muted a participant
	FieldKickedBy        = "kickedBy"       // Who kicked a participant
	FieldGrantedBy       = "grantedBy"      // Who granted admin
	FieldEndedBy         = "endedBy"        // Who force-ended the call
	FieldTrackType       = "trackType"      // Track type: audio, video, screen
	FieldMuted           = "muted"          // Whether the track is muted
	FieldRole            = "role"           // Role (admin, member)
	FieldVisibility      = "visibility"     // Group visibility (public/private)
	FieldInvitedBy       = "invitedBy"      // Who invited a member
	FieldGroupName       = "groupName"      // Group name (for invite notifications)
	FieldInviteCode      = "inviteCode"     // Group invite code for join links
)

// ---------------------------------------------------------------------------
// HTTP Response Constants
// ---------------------------------------------------------------------------

const (
	HeaderContentType = "Content-Type"
	ContentTypeJSON   = "application/json"
)

// ---------------------------------------------------------------------------
// Numeric Tuning Constants
// ---------------------------------------------------------------------------

const (
	// MaxMatchAttempts is how many times we try to find a non-blocked partner
	MaxMatchAttempts = 3

	// DefaultMessageLimit is the default number of messages returned per page
	DefaultMessageLimit = 50

	// MaxMessageLimit is the maximum number of messages a client can request
	MaxMessageLimit = 100

	// DefaultRoomLimit is the default (and max) number of rooms returned per page
	DefaultRoomLimit = 50

	// ClientSendBuffer is the size of each WebSocket client's outbound channel
	ClientSendBuffer = 128

	// WSReadBufferSize is the WebSocket read buffer in bytes
	WSReadBufferSize = 4096

	// WSWriteBufferSize is the WebSocket write buffer in bytes
	WSWriteBufferSize = 4096

	// MaxRequestBodySize is the maximum allowed HTTP request body (1 MB)
	MaxRequestBodySize int64 = 1 << 20

	// DefaultPageLimit is the default number of items returned by paginated endpoints
	DefaultPageLimit = 10

	// MaxPageLimit is the absolute maximum items a client can request via ?limit=
	MaxPageLimit = 100

	// FlushInterval is how often the flusher checks for dirty rooms
	FlushInterval = 3 * time.Second

	// FlushWorkerCount is the number of parallel flush goroutines
	FlushWorkerCount = 10

	// ReceiptFlushBatchSize is the max number of receipt updates per SQL statement
	ReceiptFlushBatchSize = 500
)

// ---------------------------------------------------------------------------
// Arena Constants
// ---------------------------------------------------------------------------

const (
	// ArenaLimitsKey is the app_settings key for admin-configurable arena limits
	ArenaLimitsKey = "arena_limits"

	// Post types
	PostTypeOriginal = "original"
	PostTypeRepost   = "repost"

	// Post visibility
	PostVisibilityPublic  = "public"
	PostVisibilityFriends = "friends"

	// Media types
	MediaTypeImage = "image"
	MediaTypeVideo = "video"

	// Allowed MIME types for upload
	MimeJPEG = "image/jpeg"
	MimeWebP = "image/webp"
	MimeAVIF = "image/avif"
	MimePNG  = "image/png"
	MimeMp4  = "video/mp4"
	MimeWebM = "video/webm"
	MimeMOV  = "video/quicktime"

	// Default presigned URL expiry (minutes); overridden by arena_limits at runtime
	DefaultPresignPutMins = 5
	DefaultPresignGetMins = 30

	// Feed defaults
	DefaultFeedLimit = 20
	MaxFeedLimit     = 50

	// Comment depth
	MaxCommentDepth = 3
)

// ---------------------------------------------------------------------------
// Call Types & Statuses
// ---------------------------------------------------------------------------

const (
	CallTypeAudio = "audio"
	CallTypeVideo = "video"

	TrackTypeAudio  = "audio"
	TrackTypeVideo  = "video"
	TrackTypeScreen = "screen"
)

const (
	CallStatusCompleted = "completed"
	CallStatusCancelled = "cancelled"
	CallStatusMissed    = "missed"
	CallStatusRejected  = "rejected"
)

// ---------------------------------------------------------------------------
// Room Member Inactive Status (set when removed/left)
// ---------------------------------------------------------------------------

const RoomMemberInactive = "inactive"

// ---------------------------------------------------------------------------
// Payment Order Statuses
// ---------------------------------------------------------------------------

const (
	OrderStatusPending   = "pending"
	OrderStatusCompleted = "completed"
	OrderStatusFailed    = "failed"
)

// ---------------------------------------------------------------------------
// Redis Cache Key Prefixes
//
// All Redis keys used across the codebase are defined here for discoverability,
// typo prevention, and easy migration. Format comments show the full key shape.
// ---------------------------------------------------------------------------

const (
	CacheUserMe       = "user:me:"          // user:me:{userId}
	CacheUserNotifP   = "user:notifp:"      // user:notifp:{userId}
	CacheUserDonated  = "user:donated:"     // user:donated:{userId}
	CacheNotifPrefs   = "notif:prefs:"      // notif:prefs:{userId} (hash)
	CachePushTokens   = "push:tokens:"      // push:tokens:{userId}
	CachePushSent     = "push:sent:"        // push:sent:{roomId}:{userId}
	CacheFriends      = "friends:"          // friends:{userId}:{limit}:{offset}
	CacheFriendReqs   = "freq:"             // freq:{userId}:{type}:{limit}:{offset}
	CacheFriendSet    = "friendset:"        // friendset:{userId}
	CacheBlockedSet   = "blockedset:"       // blockedset:{userId}
	CacheDMRequests   = "dmreq:"            // dmreq:{userId}:{limit}:{offset}
	CacheComments     = "comments:"         // comments:{postId}:{parentId}:{limit}:{offset}
	CacheReposts      = "reposts:"          // reposts:{postId}:{limit}:{offset}
	CachePostLikers   = "post:likers:"      // post:likers:{postId}:{limit}:{offset}
	CachePost         = "post:"             // post:{postId}:{userId}
	CacheBookmarks    = "bookmarks:"        // bookmarks:{userId}:{limit}:{offset}
	CacheLeaderboard  = "leaderboard:"      // leaderboard:{scope}:{limit}:{offset}
	CacheRooms        = "rooms:"            // rooms:{userId}:{gen}:{cursor}:{limit}
	CacheRoomMembers  = "room:members:"     // room:members:{roomId} (set)
	CacheRoomMember   = "room:member:"      // room:member:{roomId}:{userId}
	CacheDMOther      = "dm:other:"         // dm:other:{roomId}:{senderId}
	CacheBlockPair    = "blockpair:"        // blockpair:{a}:{b}
	CacheBan          = "ban:"              // ban:{userId}
	CacheFeedGlobal   = "feed:global:"      // feed:global:{cursor}:{limit}
	CacheFeedNetwork  = "feed:network:"     // feed:network:{userId}:{gen}:{cursor}:{limit}
	CacheFeedUser     = "feed:user:"        // feed:user:{viewerId}:{targetId}:{limit}:{offset}
	CacheFeedTrending = "feed:trending:"    // feed:trending:{limit}:{offset}
	CacheNetworkGen   = "feed:network:gen:" // feed:network:gen:{userId}
	CacheRoomsGen     = "rooms:gen:"        // rooms:gen:{userId}
	CachePostOwner    = "post:owner:"       // post:owner:{postId}
	CachePostResolve  = "post:resolve:"     // post:resolve:{postId}
	CacheArenaLimits  = "arena:limits"      // arena:limits (single key)
)

// ---------------------------------------------------------------------------
// Cache TTL Constants
// ---------------------------------------------------------------------------

const (
	CacheTTLShort       = 15 * time.Second // DM requests, friend requests
	CacheTTLMedium      = 30 * time.Second // Friends list, comments, block pairs, notification prefs
	CacheTTLLong        = 2 * time.Minute  // Room members, arena limits, leaderboard stats, user donated
	CacheTTLDMOther     = 5 * time.Minute  // DM room → other-user mapping
	CacheTTLNotifPrefs  = 5 * time.Minute  // Notification preference hash
	CacheTTLPushTokens  = 5 * time.Minute  // Device token cache
	CacheTTLPostResolve = 10 * time.Minute // Resolve original post ID
)
