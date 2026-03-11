package chat

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goccy/go-json"
	"github.com/gorilla/websocket"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ---------------------------------------------------------------------------
// WebSocket Timing Constants
// ---------------------------------------------------------------------------

const (
	writeWait  = 10 * time.Second    // Max time to write a message to the client
	pongWait   = 60 * time.Second    // Max time to wait for a pong from the client
	pingPeriod = (pongWait * 9) / 10 // How often we ping (slightly less than pongWait)
	maxMsgSize = 16384               // 16KB — accommodates large WebRTC SDP offers + ICE candidates
)

// wsWriteBufferPool reuses WebSocket write buffers across connections.
// Without this, each of the 10K connections allocates its own 4KB write buffer
// that lives for the connection lifetime (~80MB total). With pooling, only
// concurrently-writing connections need buffers, reducing memory by ~90%.
var wsWriteBufferPool = &sync.Pool{}

// activeConns tracks the current number of WebSocket connections.
// Used to enforce maxConnections and prevent OOM under spike load.
var activeConns atomic.Int64

// maxConnections is the hard limit on concurrent WebSocket connections.
// Set to 12000 to support 10K+ concurrent users with headroom.
const maxConnections = 12000

// upgrader promotes an HTTP connection to a WebSocket connection.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  config.WSReadBufferSize,
	WriteBufferSize: config.WSWriteBufferSize,
	WriteBufferPool: wsWriteBufferPool,
	// Restrict WebSocket origins to the configured CORS_ORIGIN
	CheckOrigin: func(r *http.Request) bool {
		if config.CORSOrigin == "*" {
			return true // Development mode — allow all
		}
		origin := r.Header.Get("Origin")
		return origin == config.CORSOrigin
	},
}

// Client represents a single WebSocket connection from a user.
// One user can have multiple Clients (e.g. phone + laptop).
type Client struct {
	Engine      *Engine         // Reference to the central engine
	UserID      string          // The authenticated user who owns this connection
	Conn        *websocket.Conn // The underlying WebSocket connection
	Send        chan []byte     // Outbound message queue (buffered channel)
	JoinedRooms sync.Map        // Set of room IDs this client is subscribed to (concurrent-safe)
	closeOnce   sync.Once       // Ensures c.Send is closed exactly once
	fromPrefix  []byte          // Pre-computed `{"from":"<userID>",` prefix (zero-alloc per message)
	nameOnce    sync.Once       // Ensures cachedName DB lookup happens exactly once
	cachedName  string          // User's display name (cached after first lookup)
	ready       chan struct{}   // Closed after Register completes; gates readPump
}

// closeSend safely closes the Send channel exactly once, preventing double-close panics.
func (c *Client) closeSend() {
	c.closeOnce.Do(func() { close(c.Send) })
}

// getFromName queries the user's display name from the database.
// Cached in-memory after first lookup for the lifetime of the connection.
// Thread-safe via sync.Once.
func (c *Client) getFromName() string {
	c.nameOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var name string
		err := postgress.GetRawDB().QueryRowContext(ctx,
			`SELECT COALESCE(name, '') FROM users WHERE id = $1`, c.UserID,
		).Scan(&name)
		if err != nil {
			log.Printf("[client] getFromName query failed user=%s: %v", c.UserID, err)
		} else if name != "" {
			c.cachedName = name
		}
	})
	return c.cachedName
}

// confirmMessage is the JSON shape for delivery confirmations.
// Using a struct + json.Marshal avoids broken JSON from string formatting.
type confirmMessage struct {
	Type   string `json:"type"`
	TempID string `json:"tempId"`
}

// wsError is the JSON shape for WebSocket error events.
type wsError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// sendError sends a structured error event to the client over WebSocket.
func sendError(c *Client, code string, message string) {
	errMsg, err := json.Marshal(wsError{
		Type:    config.MsgTypeError,
		Code:    code,
		Message: message,
	})
	if err != nil {
		return
	}
	select {
	case c.Send <- errMsg:
	default:
		droppedMessages.Add(1)
	}
}

// ---------------------------------------------------------------------------
// UPSTREAM: WebSocket -> Server / Redis
//
// readPump reads messages from the WebSocket and routes them:
//   - join_room / leave_room / heartbeat → handled locally
//   - send_message / typing_start → published to Redis for all servers
//   - send_message also sanitized and buffered for Postgres persistence
//
// This function runs in its own goroutine per client.
// ---------------------------------------------------------------------------

func (c *Client) readPump() {
	defer func() {
		activeConns.Add(-1)
		c.Engine.Unregister(c)
		c.Conn.Close()
	}()

	// Wait for Register to finish subscribing to all rooms before processing
	// messages. Without this gate, a message arriving for a room still being
	// joined would trigger a spurious NOT_A_MEMBER error.
	<-c.ready

	c.Conn.SetReadLimit(maxMsgSize)
	c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, payload, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[client] WebSocket read error user=%s: %v", c.UserID, err)
			}
			break // Connection closed or error — exit the loop
		}

		// Single-pass extraction of the two fields every message needs.
		// gjson.GetManyBytes scans the JSON once instead of once per field.
		common := gjson.GetManyBytes(payload, config.FieldType, config.FieldRoomID)
		msgType := common[0].String()
		roomID := common[1].String()

		// Per-user message rate limiting — drop messages that exceed the limit.
		// Heartbeats are exempt (they're keep-alives, not user actions).
		// Uses the engine's shared IPRateLimiter keyed by userID.
		if msgType != config.MsgTypeHeartbeat && !c.Engine.AllowMessage(c.UserID) {
			sendError(c, "RATE_LIMITED", "Too many messages, slow down")
			continue
		}

		// --- Deprecated: join_room / leave_room ---
		// Rooms are now auto-managed server-side. If old clients still send
		// these, we respond with an error and move on.

		if msgType == config.MsgTypeJoinRoom || msgType == config.MsgTypeLeaveRoom {
			sendError(c, "DEPRECATED", "Room subscriptions are managed automatically by the server")
			continue
		}

		if msgType == config.MsgTypeHeartbeat {
			// The read deadline was already reset by receiving this message
			continue
		}

		// --- Mark Read: buffer in Redis, flush to Postgres periodically ---
		// Client sends: {"type":"mark_read","roomId":"<uuid>"}
		if msgType == config.MsgTypeMarkRead {
			if roomID == "" {
				sendError(c, "INVALID_PAYLOAD", "Missing roomId")
				continue
			}
			if _, ok := c.JoinedRooms.Load(roomID); !ok {
				sendError(c, "NOT_A_MEMBER", "You are not a member of this room")
				continue
			}
			now := time.Now().UTC().Format(time.RFC3339)
			ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
			pipe := redis.GetRawClient().Pipeline()
			// Stranger chats are ephemeral — only broadcast, don't persist to Postgres
			if !strings.HasPrefix(roomID, config.STRANGER_PREFIX) {
				pipe.HSet(ctx, config.CHAT_READ_RECEIPTS, roomID+":"+c.UserID, now)
			}
			// Broadcast read receipt to room members for real-time blue ticks
			readReceipt, err := json.Marshal(map[string]string{
				config.FieldType:   config.MsgTypeMessageRead,
				config.FieldRoomID: roomID,
				config.FieldUserID: c.UserID,
				config.FieldReadAt: now,
			})
			if err != nil {
				log.Printf("[client] mark_read marshal failed user=%s room=%s: %v", c.UserID, roomID, err)
				cancel()
				continue
			}
			pipe.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, readReceipt)
			if _, err := pipe.Exec(ctx); err != nil {
				log.Printf("[client] mark_read buffer failed for user=%s room=%s: %v", c.UserID, roomID, err)
			}
			cancel()
			continue
		}

		// --- Mark Delivered: buffer in Redis, flush to Postgres periodically ---
		// Client sends: {"type":"mark_delivered","roomId":"<uuid>"}
		// Used when a client reconnects after being offline to acknowledge
		// that messages in this room have reached the device.
		if msgType == config.MsgTypeMarkDelivered {
			if roomID == "" {
				sendError(c, "INVALID_PAYLOAD", "Missing roomId")
				continue
			}
			if _, ok := c.JoinedRooms.Load(roomID); !ok {
				sendError(c, "NOT_A_MEMBER", "You are not a member of this room")
				continue
			}
			now := time.Now().UTC().Format(time.RFC3339)
			ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
			pipe := redis.GetRawClient().Pipeline()
			// Stranger chats are ephemeral — only broadcast, don't persist to Postgres
			if !strings.HasPrefix(roomID, config.STRANGER_PREFIX) {
				pipe.HSet(ctx, config.CHAT_DELIVERY_RECEIPTS, roomID+":"+c.UserID, now)
			}
			// Broadcast delivery receipt so the sender sees double ticks
			deliveryReceipt, err := json.Marshal(map[string]string{
				config.FieldType:        config.MsgTypeMessageDelivered,
				config.FieldRoomID:      roomID,
				config.FieldDeliveredAt: now,
			})
			if err != nil {
				log.Printf("[client] mark_delivered marshal failed user=%s room=%s: %v", c.UserID, roomID, err)
				cancel()
				continue
			}
			pipe.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, deliveryReceipt)
			if _, err := pipe.Exec(ctx); err != nil {
				log.Printf("[client] mark_delivered buffer failed for user=%s room=%s: %v", c.UserID, roomID, err)
			}
			cancel()
			continue
		}

		// --- Call Signaling (with server-side state management) ---
		// Authorization, busy detection, timeout, and call logging are handled
		// by processCallSignaling before relaying via Redis Pub/Sub.
		if msgType == config.MsgTypeCallRing ||
			msgType == config.MsgTypeCallAccept ||
			msgType == config.MsgTypeCallReject ||
			msgType == config.MsgTypeCallOffer ||
			msgType == config.MsgTypeCallAnswer ||
			msgType == config.MsgTypeICECandidate ||
			msgType == config.MsgTypeCallEnd ||
			msgType == config.MsgTypeCallLeave {

			if c.Engine.processCallSignaling(c, msgType, payload) {
				continue
			}
		}

		// --- Chat Messages & Typing (forwarded to Redis) ---

		if msgType == config.MsgTypeSendMessage || msgType == config.MsgTypeTypingStart {
			if roomID == "" {
				sendError(c, "INVALID_PAYLOAD", "Missing roomId")
				continue
			}

			// Membership check: reject if client is not subscribed to this room.
			// Since rooms are auto-subscribed on connect and dynamically managed
			// by the server, JoinedRooms is the source of truth.
			if _, ok := c.JoinedRooms.Load(roomID); !ok {
				sendError(c, "NOT_A_MEMBER", "You are not a member of this room")
				continue
			}

			// Block messages to closed stranger rooms
			if strings.HasPrefix(roomID, config.STRANGER_PREFIX) {
				sctx, scancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
				closed, _ := redis.GetRawClient().Exists(
					sctx, config.CHAT_CLOSED_COLON+roomID,
				).Result()
				scancel()
				if closed > 0 {
					sendError(c, "ROOM_CLOSED", "This stranger chat has ended")
					continue
				}
			}

			// For actual messages, sanitize content before broadcasting
			if msgType == config.MsgTypeSendMessage {
				rawText := gjson.GetBytes(payload, config.FieldText).String()
				sanitized := SanitizeMessage(rawText)
				if sanitized == "" {
					sendError(c, "EMPTY_MESSAGE", "Message content is empty after sanitization")
					continue
				}

				// Optional profanity filter for stranger match rooms
				if strings.HasPrefix(roomID, config.STRANGER_PREFIX) && ContainsProfanity(sanitized) {
					sendError(c, "PROFANITY", "Message contains inappropriate language")
					continue
				}

				// Replace the text field in the payload with sanitized content
				// Uses sjson for surgical replacement — avoids full JSON rebuild (P2-4 fix)
				if sanitized != rawText {
					if patched, err := sjson.SetBytes(payload, config.FieldText, sanitized); err == nil {
						payload = patched
					}
				}

				// Attach sender's display name for group chats (so recipients can identify sender)
				if fromName := c.getFromName(); fromName != "" {
					if patched, err := sjson.SetBytes(payload, config.FieldFromName, fromName); err == nil {
						payload = patched
					}
				}

				// Extract @mentions from message text
				if mentions := ExtractMentions(sanitized); len(mentions) > 0 {
					if patched, err := sjson.SetBytes(payload, config.FieldMentions, mentions); err == nil {
						payload = patched
					}
				}

				// Preserve replyTo if present (for threaded replies)
				// No modification needed — the field passes through as-is
			}

			// Stamp the authenticated sender onto the payload (pre-computed prefix).
			// Turns {"type":...} into {"from":"<userID>","type":...}
			// NOTE: We must allocate a new slice — append(c.fromPrefix, ...) can
			// mutate the fromPrefix backing array if it has spare capacity.
			if len(payload) == 0 || payload[0] != '{' {
				sendError(c, "INVALID_PAYLOAD", "Malformed JSON")
				continue
			}
			stamped := make([]byte, 0, len(c.fromPrefix)+len(payload)-1)
			stamped = append(stamped, c.fromPrefix...)
			stamped = append(stamped, payload[1:]...) // skip the leading '{'
			payload = stamped

			// Broadcast to all servers via Redis Pub/Sub (with timeout)
			ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
			redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, payload)
			cancel()

			// Only persist actual messages (skip typing indicators)
			if msgType == config.MsgTypeSendMessage {
				// Send an instant delivery confirmation back to the sender (non-blocking)
				tempID := gjson.GetBytes(payload, config.FieldTempID).String()
				if tempID != "" {
					confirm, err := json.Marshal(confirmMessage{
						Type:   config.MsgTypeSentConfirm,
						TempID: tempID,
					})
					if err == nil {
						select {
						case c.Send <- confirm:
						default:
							// Client buffer full — drop confirmation to protect server
							droppedMessages.Add(1)
						}
					}
				}

				// Buffer the message in Redis for the flusher to persist to Postgres
				c.bufferMessage(roomID, payload)
			}
		}
	}
}

// bufferMessage pushes a message into the Redis buffer for later flushing to Postgres.
// Extracted to its own function so that defer cancel() runs immediately after the
// Redis pipeline completes, instead of leaking until readPump returns (P0-1 fix).
func (c *Client) bufferMessage(roomID string, payload []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
	defer cancel()
	pipe := redis.GetRawClient().Pipeline()
	pipe.RPush(ctx, config.CHAT_BUFFER_COLON+roomID, payload)
	pipe.SAdd(ctx, config.CHAT_DIRTY_TARGETS, roomID)
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("[client] Failed to buffer message: %v", err)
	}
}

// ---------------------------------------------------------------------------
// DOWNSTREAM: Server -> WebSocket
//
// writePump sends messages from the Send channel to the WebSocket.
// It also sends periodic pings to detect dead connections.
//
// NOTE: Multiple queued messages are batched into a single WebSocket frame
// separated by newlines. The frontend must split on '\n' to parse them.
//
// This function runs in its own goroutine per client.
// ---------------------------------------------------------------------------

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()

	for {
		select {
		case payload, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Channel was closed — send a clean WebSocket close frame
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.Conn.NextWriter(websocket.TextMessage)
			if err != nil {
				log.Printf("[client] WebSocket NextWriter error user=%s: %v", c.UserID, err)
				return
			}
			w.Write(payload)

			// Batch: if more messages are queued, write them in the same frame.
			// Cap at 64 to bound write latency under burst load.
			n := len(c.Send)
			if n > 64 {
				n = 64
			}
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.Send)
			}

			if err := w.Close(); err != nil {
				log.Printf("[client] WebSocket write close error user=%s: %v", c.UserID, err)
				return
			}

		case <-ticker.C:
			// Send a WebSocket ping to check if the client is still alive
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("[client] WebSocket ping failed user=%s: %v", c.UserID, err)
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// ServeWs upgrades an HTTP request to a WebSocket and registers the client.
// Bind this to your router: GET /ws
// ---------------------------------------------------------------------------

func ServeWs(engine *Engine, w http.ResponseWriter, r *http.Request, userID string) {
	// Enforce connection limit to prevent OOM under spike load
	if activeConns.Load() >= maxConnections {
		http.Error(w, "Server at capacity", http.StatusServiceUnavailable)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("[ws] Upgrade error:", err)
		return
	}

	activeConns.Add(1)

	client := &Client{
		Engine:     engine,
		UserID:     userID,
		Conn:       conn,
		Send:       make(chan []byte, config.ClientSendBuffer),
		fromPrefix: []byte(`{"from":"` + userID + `",`),
		ready:      make(chan struct{}),
	}

	// writePump can start immediately (queued messages are harmless).
	// readPump waits on client.ready which is closed after Register completes.
	go client.writePump()

	engine.Register(client)
	close(client.ready) // Signal readPump that all rooms are subscribed

	go client.readPump()
}
