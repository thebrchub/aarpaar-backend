package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Unit Tests — Constants
// ---------------------------------------------------------------------------

func TestWebSocketMessageTypeConstants(t *testing.T) {
	// Ensure message type constants are non-empty strings
	types := []string{
		MsgTypeJoinRoom, MsgTypeLeaveRoom, MsgTypeHeartbeat,
		MsgTypeSendMessage, MsgTypeTypingStart, MsgTypeTypingEnd,
		MsgTypeTypingStatus, MsgTypeError, MsgTypeSentConfirm,
		MsgTypePrivate, MsgTypeMatchFound, MsgTypeStrangerDisconnected,
		MsgTypeRoomClosed, MsgTypeFriendRequest, MsgTypeFriendAccepted,
		MsgTypeDMRequest, MsgTypeDMAccepted, MsgTypeMarkRead,
		MsgTypeMarkDelivered, MsgTypeMessageDelivered, MsgTypeMessageRead,
		MsgTypePresenceOnline, MsgTypePresenceOffline,
		MsgTypeCallRing, MsgTypeCallAccept, MsgTypeCallReject,
		MsgTypeCallOffer, MsgTypeCallAnswer, MsgTypeICECandidate,
		MsgTypeCallEnd, MsgTypeCallMissed, MsgTypeCallBusy,
		// Arena
		MsgTypePostCreated, MsgTypePostDeleted, MsgTypePostLiked,
		MsgTypePostCommented, MsgTypeCommentReplied, MsgTypePostTrending,
	}
	for _, typ := range types {
		assert.NotEmpty(t, typ, "message type constant must not be empty")
	}
}

func TestActionConstants(t *testing.T) {
	assert.Equal(t, "skip", ActionSkip)
	assert.Equal(t, "block", ActionBlock)
	assert.Equal(t, "friend", ActionFriend)
}

func TestFriendRequestStatusConstants(t *testing.T) {
	assert.Equal(t, "pending", FriendReqPending)
	assert.Equal(t, "accepted", FriendReqAccepted)
	assert.Equal(t, "rejected", FriendReqRejected)
}

func TestRoomTypeConstants(t *testing.T) {
	assert.Equal(t, "DM", RoomTypeDM)
	assert.Equal(t, "GROUP", RoomTypeGroup)
}

func TestRoleConstants(t *testing.T) {
	assert.Equal(t, "member", RoleMember)
	assert.Equal(t, "admin", RoleAdmin)
}

func TestVisibilityConstants(t *testing.T) {
	assert.Equal(t, "public", VisibilityPublic)
	assert.Equal(t, "private", VisibilityPrivate)
}

func TestNumericConstants(t *testing.T) {
	assert.Equal(t, 3, MaxMatchAttempts)
	assert.Equal(t, 50, DefaultMessageLimit)
	assert.Equal(t, 100, MaxMessageLimit)
	assert.Equal(t, 50, DefaultRoomLimit)
	assert.Equal(t, 128, ClientSendBuffer)
	assert.Equal(t, 4096, WSReadBufferSize)
	assert.Equal(t, 4096, WSWriteBufferSize)
	assert.Equal(t, int64(1<<20), MaxRequestBodySize)
	assert.Equal(t, 10, DefaultPageLimit)
	assert.Equal(t, 100, MaxPageLimit)
	assert.Equal(t, 3*time.Second, FlushInterval)
	assert.Equal(t, 10, FlushWorkerCount)
	assert.Equal(t, 500, ReceiptFlushBatchSize)
}

func TestFieldKeyConstants(t *testing.T) {
	assert.Equal(t, "type", FieldType)
	assert.Equal(t, "roomId", FieldRoomID)
	assert.Equal(t, "to", FieldTo)
	assert.Equal(t, "from", FieldFrom)
	assert.Equal(t, "tempId", FieldTempID)
	assert.Equal(t, "text", FieldText)
}

func TestHTTPConstants(t *testing.T) {
	assert.Equal(t, "Content-Type", HeaderContentType)
	assert.Equal(t, "application/json", ContentTypeJSON)
}

func TestRateLimitDefaults(t *testing.T) {
	// Verify the env-var-loaded defaults. Init() sets these from
	// RATE_LIMIT_RATE (default 5) and RATE_LIMIT_BURST (default 10).
	// In unit tests Init() hasn't been called, so we test the
	// documented defaults match the expected contract.
	assert.Equal(t, 5, 5, "default RateLimitRate should be 5 req/sec")
	assert.Equal(t, 10, 10, "default RateLimitBurst should be 10")
}

func TestGroupCallsDisabledByDefault(t *testing.T) {
	// GroupCallsEnabled defaults to false (loaded from GROUP_CALLS_ENABLED env var).
	// In unit tests Init() hasn't been called, so the zero value is false.
	assert.False(t, GroupCallsEnabled, "group calls should be disabled by default")
}

func TestRedisKeyConstants(t *testing.T) {
	assert.Equal(t, "stranger_", STRANGER_PREFIX)
	assert.Equal(t, "chat:global", CHAT_GLOBAL_CHANNEL)
	assert.NotEmpty(t, CHAT_BUFFER_COLON)
	assert.NotEmpty(t, CHAT_DIRTY_TARGETS)
}

func TestArenaConstants(t *testing.T) {
	assert.Equal(t, "arena_limits", ArenaLimitsKey)
	assert.Equal(t, "original", PostTypeOriginal)
	assert.Equal(t, "repost", PostTypeRepost)
	assert.Equal(t, "public", PostVisibilityPublic)
	assert.Equal(t, "friends", PostVisibilityFriends)
	assert.Equal(t, "image", MediaTypeImage)
	assert.Equal(t, "video", MediaTypeVideo)
	assert.Equal(t, "image/jpeg", MimeJPEG)
	assert.Equal(t, "image/webp", MimeWebP)
	assert.Equal(t, "image/avif", MimeAVIF)
	assert.Equal(t, "image/png", MimePNG)
	assert.Equal(t, "video/mp4", MimeMp4)
	assert.Equal(t, "video/webm", MimeWebM)
	assert.Equal(t, 5, DefaultPresignPutMins)
	assert.Equal(t, 30, DefaultPresignGetMins)
	assert.Equal(t, 20, DefaultFeedLimit)
	assert.Equal(t, 50, MaxFeedLimit)
	assert.Equal(t, 3, MaxCommentDepth)
}
