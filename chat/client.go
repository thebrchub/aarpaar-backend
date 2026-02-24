package chat

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/gorilla/websocket"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/config"
	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// WebSocket Timing Constants
// ---------------------------------------------------------------------------

const (
	writeWait  = 10 * time.Second    // Max time to write a message to the client
	pongWait   = 60 * time.Second    // Max time to wait for a pong from the client
	pingPeriod = (pongWait * 9) / 10 // How often we ping (slightly less than pongWait)
	maxMsgSize = 4096                // 4KB — prevents clients from sending huge payloads
)

// upgrader promotes an HTTP connection to a WebSocket connection.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  config.WSReadBufferSize,
	WriteBufferSize: config.WSWriteBufferSize,
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
	JoinedRooms map[string]bool // Set of room IDs this client is subscribed to
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
	}
}

// ---------------------------------------------------------------------------
// UPSTREAM: WebSocket -> Server / Redis
//
// readPump reads messages from the WebSocket and routes them:
//   - join_room / leave_room / heartbeat → handled locally
//   - send_message / typing_start / typing_end → published to Redis for all servers
//   - send_message also sanitized and buffered for Postgres persistence
//
// This function runs in its own goroutine per client.
// ---------------------------------------------------------------------------

func (c *Client) readPump() {
	defer func() {
		c.Engine.Unregister(c)
		c.Conn.Close()
	}()

	c.Conn.SetReadLimit(maxMsgSize)
	c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, payload, err := c.Conn.ReadMessage()
		if err != nil {
			break // Connection closed or error — exit the loop
		}

		// Single-pass extraction of the two fields every message needs.
		// gjson.GetManyBytes scans the JSON once instead of once per field.
		common := gjson.GetManyBytes(payload, config.FieldType, config.FieldRoomID)
		msgType := common[0].String()
		roomID := common[1].String()

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

		// --- Mark Read: update last_read_at in Postgres ---
		// Client sends: {"type":"mark_read","roomId":"<uuid>"}
		if msgType == config.MsgTypeMarkRead {
			if roomID == "" {
				sendError(c, "INVALID_PAYLOAD", "Missing roomId")
				continue
			}
			if !c.JoinedRooms[roomID] {
				sendError(c, "NOT_A_MEMBER", "You are not a member of this room")
				continue
			}
			go func(uid, rid string) {
				_, err := postgress.GetRawDB().Exec(
					`UPDATE room_members SET last_read_at = NOW() WHERE room_id = $1 AND user_id = $2`,
					rid, uid,
				)
				if err != nil {
					log.Printf("[client] mark_read failed for user=%s room=%s: %v", uid, rid, err)
					return
				}

				// Broadcast a read receipt to all room members via Redis Pub/Sub
				// so the sender sees blue ticks on their messages.
				readReceipt, err := json.Marshal(map[string]string{
					config.FieldType:   config.MsgTypeMessageRead,
					config.FieldRoomID: rid,
					config.FieldUserID: uid,
					config.FieldReadAt: time.Now().UTC().Format(time.RFC3339),
				})
				if err == nil {
					ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
					redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, readReceipt)
					cancel()
				}
			}(c.UserID, roomID)
			continue
		}

		// --- Chat Messages & Typing (forwarded to Redis) ---

		if msgType == config.MsgTypeSendMessage || msgType == config.MsgTypeTypingStart || msgType == config.MsgTypeTypingEnd {
			if roomID == "" {
				sendError(c, "INVALID_PAYLOAD", "Missing roomId")
				continue
			}

			// Membership check: reject if client is not subscribed to this room.
			// Since rooms are auto-subscribed on connect and dynamically managed
			// by the server, JoinedRooms is the source of truth.
			if !c.JoinedRooms[roomID] {
				sendError(c, "NOT_A_MEMBER", "You are not a member of this room")
				continue
			}

			// Block messages to closed stranger rooms
			if strings.HasPrefix(roomID, config.STRANGER_PREFIX) {
				closed, _ := redis.GetRawClient().Exists(
					context.Background(), config.CHAT_CLOSED_COLON+roomID,
				).Result()
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
				if sanitized != rawText {
					// Rebuild JSON with sanitized text
					parsed := gjson.ParseBytes(payload)
					fields := make(map[string]interface{})
					parsed.ForEach(func(key, value gjson.Result) bool {
						if key.String() == config.FieldText {
							fields[config.FieldText] = sanitized
						} else {
							fields[key.String()] = value.Value()
						}
						return true
					})
					if rebuilt, err := json.Marshal(fields); err == nil {
						payload = rebuilt
					}
				}
			}

			// Stamp the authenticated sender onto the payload (zero-alloc byte splice).
			// Turns {"type":...} into {"from":"<userID>","type":...}
			fromPrefix := []byte(`{"from":"` + c.UserID + `",`)
			payload = append(fromPrefix, payload[1:]...) // skip the leading '{'

			// Broadcast to all servers via Redis Pub/Sub (with timeout)
			ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
			redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, payload)
			cancel()

			// Only persist actual messages (skip typing indicators)
			if msgType == config.MsgTypeSendMessage {
				// Send an instant delivery confirmation back to the sender
				tempID := gjson.GetBytes(payload, config.FieldTempID).String()
				if tempID != "" {
					confirm, err := json.Marshal(confirmMessage{
						Type:   config.MsgTypeSentConfirm,
						TempID: tempID,
					})
					if err == nil {
						c.Send <- confirm
					}
				}

				// Buffer the message in Redis for the flusher to persist to Postgres
				ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
				pipe := redis.GetRawClient().Pipeline()
				pipe.RPush(ctx, config.CHAT_BUFFER_COLON+roomID, payload)
				pipe.SAdd(ctx, config.CHAT_DIRTY_TARGETS, roomID)

				if _, err := pipe.Exec(ctx); err != nil {
					log.Printf("[client] Failed to buffer message: %v", err)
				}
				cancel()
			}
		}
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
				return
			}
			w.Write(payload)

			// Batch: if more messages are queued, write them in the same frame
			n := len(c.Send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.Send)
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			// Send a WebSocket ping to check if the client is still alive
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
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
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("[ws] Upgrade error:", err)
		return
	}

	client := &Client{
		Engine:      engine,
		UserID:      userID,
		Conn:        conn,
		Send:        make(chan []byte, config.ClientSendBuffer),
		JoinedRooms: make(map[string]bool),
	}

	engine.Register(client)

	go client.writePump()
	go client.readPump()
}
