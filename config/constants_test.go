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

func TestRedisKeyConstants(t *testing.T) {
	assert.Equal(t, "stranger_", STRANGER_PREFIX)
	assert.Equal(t, "chat:global", CHAT_GLOBAL_CHANNEL)
	assert.NotEmpty(t, CHAT_BUFFER_COLON)
	assert.NotEmpty(t, CHAT_DIRTY_TARGETS)
}
