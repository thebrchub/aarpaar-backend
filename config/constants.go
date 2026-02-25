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

	// Participant -> Server: Leave a group call (without ending it)
	MsgTypeCallLeave = "call_leave"

	// Server -> Client: Redirect from P2P to SFU (when 3rd person joins)
	MsgTypeSFURedirect = "sfu_redirect"
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
	FieldType            = "type"              // Message type discriminator
	FieldRoomID          = "roomId"            // Target room identifier
	FieldTo              = "to"                // Target user for private messages
	FieldFrom            = "from"              // Sender identifier
	FieldData            = "data"              // Nested event payload
	FieldTempID          = "tempId"            // Client-generated temp ID for delivery confirmation
	FieldText            = "text"              // Message content body
	FieldPartnerFakeName = "partner_fake_name" // Fake display name for stranger matches
	FieldPartnerAvatar   = "partner_avatar"    // Avatar URL (empty for strangers)
	FieldCode            = "code"              // Error code for error events
	FieldMessage         = "message"           // Human-readable error message
	FieldUserIDs         = "userIds"           // Array of user IDs (typing status)
	FieldUserID          = "userId"            // Single user ID (receipts)
	FieldDeliveredAt     = "deliveredAt"       // Delivery receipt timestamp
	FieldReadAt          = "readAt"            // Read receipt timestamp
	FieldLastSeenAt      = "lastSeenAt"        // Last-seen timestamp for presence
	FieldIsOnline        = "isOnline"          // Whether the user is currently online
	FieldCallID          = "callId"            // Unique call identifier
	FieldSDP             = "sdp"               // WebRTC SDP offer/answer
	FieldCandidate       = "candidate"         // ICE candidate
	FieldCallType        = "callType"          // "audio" or "video"
	FieldHasVideo        = "hasVideo"          // Whether video is enabled
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

	// FlushInterval is how often the flusher checks for dirty rooms
	FlushInterval = 3 * time.Second

	// FlushWorkerCount is the number of parallel flush goroutines
	FlushWorkerCount = 10

	// ReceiptFlushBatchSize is the max number of receipt updates per SQL statement
	ReceiptFlushBatchSize = 500
)
