package chat

import (
	"context"
	"hash/fnv"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/middleware"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ---------------------------------------------------------------------------
// Package-level engine singleton (so handlers can access it)
// ---------------------------------------------------------------------------

var engineInstance *Engine

// GetEngine returns the singleton Engine.
// Call this from handlers after NewEngine() has been called in main.
func GetEngine() *Engine {
	return engineInstance
}

// ---------------------------------------------------------------------------
// Sharded Lock Design
//
// Instead of one giant mutex for the entire engine, we split the room map
// across 64 "shards". Each shard has its own RWMutex. When a message
// arrives for room "abc", we hash "abc" to pick ONE shard and only lock
// that shard. This means 64 rooms can be read/written simultaneously
// without blocking each other. Massive improvement at 10K+ users.
// ---------------------------------------------------------------------------

const roomShardCount = 64

// roomShard holds a subset of room subscriptions and its own lock.
type roomShard struct {
	mu    sync.RWMutex
	rooms map[string]map[*Client]bool
}

// Engine manages local WebSocket connections and Redis Pub/Sub routing.
type Engine struct {
	// userMu protects the users map (separate from room shards)
	userMu sync.RWMutex
	users  map[string]map[*Client]bool // UserID -> Set of Clients (multi-device)

	// roomShards splits room subscriptions across N independent locks
	roomShards [roomShardCount]roomShard

	// wsLimiter enforces per-user message rate limiting on WebSocket.
	// Keyed by userID (reuses go-starter-kit's IPRateLimiter with userID as key).
	wsLimiter *middleware.IPRateLimiter

	// done is closed to signal the Redis listener to stop (graceful shutdown)
	done chan struct{}

	// OnUserOffline is called when a user's last device disconnects.
	// Used by the bot service to cancel pending bot match timers.
	OnUserOffline func(userID string)

	// SendPushToUser sends a push notification to a single user's devices.
	// Wired up in main.go to services.SendPushToUser to break the import cycle.
	SendPushToUser func(ctx context.Context, userID string, data map[string]string, highPriority bool)

	// ShouldPushMessage checks debounce for a room+user combo.
	// Wired up in main.go to services.ShouldPushMessage.
	ShouldPushMessage func(ctx context.Context, roomID, userID string) bool
}

// NewEngine creates the engine and starts the Redis listener.
// Call this once during application startup.
func NewEngine() *Engine {
	e := &Engine{
		users:     make(map[string]map[*Client]bool),
		done:      make(chan struct{}),
		wsLimiter: middleware.NewIPRateLimiter(config.RateLimitRate, config.RateLimitBurst),
	}

	// Initialize each shard's map
	for i := range e.roomShards {
		e.roomShards[i].rooms = make(map[string]map[*Client]bool)
	}

	// Store the singleton so handlers can access the engine
	engineInstance = e

	// Start listening to the global Redis Pub/Sub channel
	go e.listenToRedis()

	// Start orphan call state scanner (cleans up after server crashes)
	startOrphanCallScanner(e.done)

	// Log connection metrics only when usage is critically high (>80% capacity)
	// or when messages/tasks are being dropped.
	go func() {
		ticker := time.NewTicker(120 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-e.done:
				return
			case <-ticker.C:
				conns := activeConns.Load()
				if conns > maxConnections*80/100 {
					log.Printf("[engine] ⚠ High connection usage: %d / %d (%.0f%%)", conns, maxConnections, float64(conns)/float64(maxConnections)*100)
				}
				if n := droppedMessages.Swap(0); n > 0 {
					log.Printf("[engine] Dropped %d messages (client buffers full) in last 30s", n)
				}
				if n := droppedBackgroundTasks.Swap(0); n > 0 {
					log.Printf("[engine] Dropped %d background tasks (pool exhausted) in last 30s", n)
				}
			}
		}
	}()

	return e
}

// Shutdown stops the Redis Pub/Sub listener and closes all client connections.
// Call this during graceful shutdown before closing Redis/Postgres.
func (e *Engine) Shutdown() {
	close(e.done)

	// Stop WS rate limiter sweeper goroutine
	e.wsLimiter.Close()

	// Close all connected clients
	e.userMu.Lock()
	for _, clients := range e.users {
		for c := range clients {
			c.Conn.Close()
		}
	}
	e.userMu.Unlock()
}

// AllowMessage checks whether the given userID is within their WS message
// rate limit. Uses the go-starter-kit IPRateLimiter keyed by userID.
func (e *Engine) AllowMessage(userID string) bool {
	return e.wsLimiter.Allow(userID)
}

// getShard picks the correct shard for a room ID using a fast hash.
func (e *Engine) getShard(roomID string) *roomShard {
	h := fnv.New32a()
	h.Write([]byte(roomID))
	return &e.roomShards[h.Sum32()%roomShardCount]
}

// ---------------------------------------------------------------------------
// Connection Lifecycle
// ---------------------------------------------------------------------------

// Register adds a client to the engine when they connect via WebSocket.
// After registration, it auto-subscribes the client to all their active
// rooms from the database so the frontend doesn't need to send join_room.
func (e *Engine) Register(c *Client) {
	e.userMu.Lock()
	firstDevice := len(e.users[c.UserID]) == 0
	if e.users[c.UserID] == nil {
		e.users[c.UserID] = make(map[*Client]bool)
	}
	e.users[c.UserID][c] = true
	e.userMu.Unlock()

	// Auto-subscribe to all active rooms from Postgres
	roomIDs := getUserActiveRoomIDs(c.UserID)
	for _, roomID := range roomIDs {
		e.JoinRoom(c, roomID)
	}

	// If this is the user's first device, broadcast "online" to friends
	if firstDevice {
		RunBackground(func() { e.broadcastPresence(c.UserID, true) })
	}

	// Deliver pending incoming call if user has an unanswered call as callee
	RunBackground(func() { e.deliverPendingCall(c) })
}

// Unregister removes a client when they disconnect (close tab / app).
// It cleans up the user map and all room subscriptions.
func (e *Engine) Unregister(c *Client) {
	// Remove from users map
	e.userMu.Lock()
	lastDevice := false
	if clients, ok := e.users[c.UserID]; ok {
		delete(clients, c)
		if len(clients) == 0 {
			delete(e.users, c.UserID)
			lastDevice = true
		}
	}
	e.userMu.Unlock()

	// Remove from every room they joined (using the correct shard for each)
	c.JoinedRooms.Range(func(key, _ any) bool {
		roomID := key.(string)
		shard := e.getShard(roomID)
		shard.mu.Lock()
		if roomClients, ok := shard.rooms[roomID]; ok {
			delete(roomClients, c)
			if len(roomClients) == 0 {
				delete(shard.rooms, roomID)
			}
		}
		shard.mu.Unlock()
		return true
	})

	// Close the outbound channel so writePump exits cleanly (exactly once)
	c.closeSend()

	// If this was the user's last device, update last_seen_at, broadcast "offline",
	// auto-end any active call, and remove from matchmaking queue.
	// Split into separate background tasks for better parallelism during mass-disconnect.
	if lastDevice {
		RunBackground(func() {
			e.handleCallDisconnect(c.UserID)
		})
		RunBackground(func() {
			e.handleUserWentOffline(c.UserID)
		})
		RunBackground(func() {
			// Clean up match queue so offline users don't get matched
			ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
			redis.GetRawClient().SRem(ctx, config.DefaultMatchQueue, c.UserID)
			cancel()
		})
		// Notify bot service to cancel any pending bot match timer
		if e.OnUserOffline != nil {
			RunBackground(func() { e.OnUserOffline(c.UserID) })
		}
	}
}

// ---------------------------------------------------------------------------
// Room Management
// ---------------------------------------------------------------------------

// JoinRoom subscribes a client to a specific chat room on this server.
func (e *Engine) JoinRoom(c *Client, roomID string) {
	shard := e.getShard(roomID)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	c.JoinedRooms.Store(roomID, true)
	if shard.rooms[roomID] == nil {
		shard.rooms[roomID] = make(map[*Client]bool)
	}
	shard.rooms[roomID][c] = true
}

// LeaveRoom unsubscribes a client from a specific chat room.
func (e *Engine) LeaveRoom(c *Client, roomID string) {
	shard := e.getShard(roomID)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	c.JoinedRooms.Delete(roomID)
	if roomClients, ok := shard.rooms[roomID]; ok {
		delete(roomClients, c)
		if len(roomClients) == 0 {
			delete(shard.rooms, roomID)
		}
	}
}

// ---------------------------------------------------------------------------
// Cross-handler Room Management
//
// These methods let HTTP handlers subscribe/unsubscribe online users to rooms
// without the client needing to send join_room over the WebSocket.
// ---------------------------------------------------------------------------

// JoinRoomForUser subscribes all of a user's connected devices to a room.
// Safe to call if the user is offline (no-op). Called from HTTP handlers
// when a room is created, a DM is accepted, or a stranger match is made.
func (e *Engine) JoinRoomForUser(userID string, roomID string) {
	e.userMu.RLock()
	clients, ok := e.users[userID]
	if !ok {
		e.userMu.RUnlock()
		return
	}
	targets := make([]*Client, 0, len(clients))
	for c := range clients {
		targets = append(targets, c)
	}
	e.userMu.RUnlock()

	for _, c := range targets {
		e.JoinRoom(c, roomID)
	}
}

// LeaveRoomForUser unsubscribes all of a user's connected devices from a room.
// Safe to call if the user is offline (no-op). Called from HTTP handlers
// when a DM is rejected or a room is deleted.
func (e *Engine) LeaveRoomForUser(userID string, roomID string) {
	e.userMu.RLock()
	clients, ok := e.users[userID]
	if !ok {
		e.userMu.RUnlock()
		return
	}
	targets := make([]*Client, 0, len(clients))
	for c := range clients {
		targets = append(targets, c)
	}
	e.userMu.RUnlock()

	for _, c := range targets {
		e.LeaveRoom(c, roomID)
	}
}

// getUserActiveRoomIDs queries Postgres for all room IDs where the user
// has an active membership. Used during WebSocket registration.
func getUserActiveRoomIDs(userID string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()
	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT room_id FROM room_members WHERE user_id = $1 AND status = 'active'`,
		userID,
	)
	if err != nil {
		log.Printf("[engine] Failed to query rooms for user %s: %v", userID, err)
		return nil
	}
	defer rows.Close()

	var roomIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			log.Printf("[engine] Scan room ID failed for user=%s: %v", userID, err)
			continue
		}
		roomIDs = append(roomIDs, id)
	}
	return roomIDs
}

// CloseRoom sends a room_closed event to all clients in the room
// and then removes every client from it.
func (e *Engine) CloseRoom(roomID string, payload []byte) {
	shard := e.getShard(roomID)
	shard.mu.Lock()

	clients, ok := shard.rooms[roomID]
	if !ok {
		shard.mu.Unlock()
		return
	}

	// Collect all clients to notify
	targets := make([]*Client, 0, len(clients))
	for c := range clients {
		targets = append(targets, c)
	}

	// Notify each client WHILE the room still exists in the shard.
	// This prevents a race where a concurrent message for this room
	// could arrive after delete but before notification.
	for _, c := range targets {
		c.JoinedRooms.Delete(roomID)
		select {
		case c.Send <- payload:
		default:
			droppedMessages.Add(1)
		}
	}

	// Remove the room entirely AFTER notifying clients
	delete(shard.rooms, roomID)
	shard.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Redis Pub/Sub Listener (with auto-reconnect)
//
// This goroutine subscribes to the global Redis channel. Every message
// published by ANY server in the cluster arrives here. We peek at the
// JSON to decide whether it's a private event (route to user) or a room
// event (route to room subscribers).
//
// If Redis disconnects, we log the error, wait, and reconnect automatically
// so the system self-heals without a restart.
// ---------------------------------------------------------------------------

func (e *Engine) listenToRedis() {
	backoff := 1 * time.Second
	const maxBackoff = 30 * time.Second
	for {
		select {
		case <-e.done:
			log.Println("[engine] Shutdown signal received, stopping Redis listener")
			return
		default:
		}

		e.subscribeAndListen()

		// If we reach here, the subscription broke. Exponential backoff.
		log.Printf("[engine] Redis Pub/Sub disconnected. Reconnecting in %v...", backoff)

		select {
		case <-e.done:
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// subscribeAndListen is the inner loop that processes messages.
// It returns when the Redis subscription drops, triggering a reconnect.
func (e *Engine) subscribeAndListen() {
	ctx := context.Background()
	pubsub := redis.Subscribe(ctx, config.CHAT_GLOBAL_CHANNEL)
	if pubsub == nil {
		log.Println("[engine] Redis Subscribe returned nil, will retry...")
		return
	}
	defer pubsub.Close()

	ch := pubsub.Channel()

	for {
		select {
		case <-e.done:
			return
		case msg, ok := <-ch:
			if !ok {
				return // Channel closed — reconnect
			}

			payload := []byte(msg.Payload)

			// Single-pass extraction of all commonly needed fields.
			// gjson.GetManyBytes scans the JSON once and returns all values,
			// instead of rescanning per field.
			fields := gjson.GetManyBytes(payload,
				config.FieldType,   // [0] type
				config.FieldRoomID, // [1] roomId
				config.FieldFrom,   // [2] from
				config.FieldTo,     // [3] to
			)
			msgType := fields[0].String()
			roomID := fields[1].String()
			senderID := fields[2].String()
			targetUser := fields[3].String()

			switch msgType {
			// ---------------------------------------------------------
			// Call signaling: relay to specific user by "to" field
			// ---------------------------------------------------------
			case config.MsgTypeCallRing, config.MsgTypeCallAccept, config.MsgTypeCallReject,
				config.MsgTypeCallOffer, config.MsgTypeCallAnswer, config.MsgTypeICECandidate,
				config.MsgTypeCallEnd, config.MsgTypeCallMissed, config.MsgTypeCallBusy,
				config.MsgTypeCallLeave, config.MsgTypeCallDismiss:
				if targetUser != "" {
					// Block enforcement for 1:1 calls
					if senderID != "" && isBlockedPair(senderID, targetUser) {
						continue
					}
					e.deliverToUser(targetUser, payload)
				}

			// Call events broadcast to entire room (group call started, participants list)
			case config.MsgTypeCallStarted, config.MsgTypeCallParticipants, config.MsgTypeSFURedirect,
				config.MsgTypeGroupCallStarted, config.MsgTypeGroupCallParticipantJoined,
				config.MsgTypeGroupCallParticipantLeft, config.MsgTypeGroupCallEnded:
				if targetUser != "" {
					e.deliverToUser(targetUser, payload)
				} else if roomID != "" {
					e.deliverToRoom(roomID, payload)
				}

			case config.MsgTypePrivate, config.MsgTypeMatchFound, config.MsgTypeStrangerDisconnected:
				// Private/system events targeted at a specific user or a list of targets (P1-1)
				if targetUser != "" {
					e.deliverToUser(targetUser, payload)
				} else {
					// Presence events use a "targets" list — deliver to each locally-connected target
					targets := gjson.GetBytes(payload, "targets")
					if targets.Exists() && targets.IsArray() {
						// Build per-user payload without the targets array (strip it for the client)
						cleanPayload, err := sjson.DeleteBytes(payload, "targets")
						if err != nil {
							log.Printf("[engine] Failed to strip targets from payload: %v", err)
							cleanPayload = payload
						}
						targets.ForEach(func(_, target gjson.Result) bool {
							e.deliverToUser(target.String(), cleanPayload)
							return true
						})
					}
				}

				// If this is a stranger_disconnected wrapped inside a private envelope,
				// also extract the inner data for the room_closed handler.
				// These nested fields are rare, so a separate lookup is acceptable.
				innerFields := gjson.GetManyBytes(payload, "data.type", "data.roomId")
				if innerFields[0].String() == config.MsgTypeStrangerDisconnected {
					innerRoom := innerFields[1].String()
					if innerRoom != "" {
						closedMsg, _ := json.Marshal(map[string]string{
							config.FieldType:   config.MsgTypeRoomClosed,
							config.FieldRoomID: innerRoom,
						})
						e.CloseRoom(innerRoom, closedMsg)
					}
				}

			case config.MsgTypeRoomClosed:
				// Close the room on this server: kick everyone + notify
				if roomID != "" {
					e.CloseRoom(roomID, payload)
				}

			case config.MsgTypeSendMessage:
				// Room-level messages: deliver to everyone in the room on this server
				if roomID != "" {
					// Block enforcement for DM rooms: reject messages between blocked users
					if senderID != "" && !strings.HasPrefix(roomID, config.STRANGER_PREFIX) {
						if isBlockedInRoom(senderID, roomID) {
							// Silently drop — sender gets no delivery, no error event
							continue
						}
					}
					e.deliverToRoom(roomID, payload)
					// Generate delivery receipts: notify the sender that the message
					// was delivered to online recipient(s) in this room.
					if senderID != "" {
						e.sendDeliveryReceipts(roomID, senderID)
					}
					// Send push notifications to offline room members (except sender).
					// Online users already receive via WebSocket. Debounced per room+user.
					if senderID != "" && !strings.HasPrefix(roomID, config.STRANGER_PREFIX) {
						senderName := gjson.GetBytes(payload, config.FieldFromName).String()
						preview := gjson.GetBytes(payload, config.FieldText).String()
						if len(preview) > 100 {
							preview = preview[:100]
						}
						capturedRoom := roomID
						capturedSender := senderID
						RunBackground(func() {
							e.sendMessagePush(capturedRoom, capturedSender, senderName, preview)
						})
					}
				}

			case config.MsgTypeTypingStart:
				// Rewrite typing event to typing_status format with userIds array
				if roomID != "" && senderID != "" {
					typingStatus := map[string]interface{}{
						config.FieldType:    config.MsgTypeTypingStatus,
						config.FieldRoomID:  roomID,
						config.FieldUserIDs: []string{senderID},
					}
					if rewritten, err := json.Marshal(typingStatus); err == nil {
						e.deliverToRoom(roomID, rewritten)
					}
				}

			default:
				// All other room-scoped events (message_read, message_delivered, etc.):
				// pure pass-through broadcast to room members.
				if roomID != "" {
					e.deliverToRoom(roomID, payload)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Delivery Helpers
// ---------------------------------------------------------------------------

// deliverToUser sends raw bytes to all connected devices of a specific user.
func (e *Engine) deliverToUser(userID string, payload []byte) {
	e.userMu.RLock()
	clients, ok := e.users[userID]
	if !ok {
		e.userMu.RUnlock()
		return
	}
	// Copy the client set under the lock, then deliver outside the lock
	targets := make([]*Client, 0, len(clients))
	for c := range clients {
		targets = append(targets, c)
	}
	e.userMu.RUnlock()

	for _, c := range targets {
		select {
		case c.Send <- payload:
		default:
			// Client's buffer is full — drop to protect the server.
			// The client will catch up or reconnect.
			droppedMessages.Add(1)
		}
	}
}

// sendDeliveryReceipts checks if any non-sender clients are online in the room
// and sends a delivery receipt back to the sender. Also buffers the recipient's
// last_delivered_at in Redis for the flusher to batch-UPDATE to Postgres.
func (e *Engine) sendDeliveryReceipts(roomID string, senderID string) {
	shard := e.getShard(roomID)
	shard.mu.RLock()
	clients, ok := shard.rooms[roomID]
	if !ok {
		shard.mu.RUnlock()
		return
	}

	// Collect unique recipient user IDs who are online in the room
	recipientIDs := make(map[string]bool)
	for c := range clients {
		if c.UserID != senderID {
			recipientIDs[c.UserID] = true
		}
	}
	shard.mu.RUnlock()

	if len(recipientIDs) == 0 {
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Send a delivery receipt event to the sender
	receipt, err := json.Marshal(map[string]string{
		config.FieldType:        config.MsgTypeMessageDelivered,
		config.FieldRoomID:      roomID,
		config.FieldDeliveredAt: now,
	})
	if err != nil {
		log.Printf("[engine] delivery receipt marshal failed room=%s: %v", roomID, err)
	} else {
		e.deliverToUser(senderID, receipt)
	}

	// Buffer last_delivered_at for each online recipient in Redis hash
	// The flusher will batch-UPDATE them to Postgres every FlushInterval.
	// Stranger chats are ephemeral — skip persisting their receipts.
	if !strings.HasPrefix(roomID, config.STRANGER_PREFIX) {
		ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
		defer cancel()
		pipe := redis.GetRawClient().Pipeline()
		for uid := range recipientIDs {
			pipe.HSet(ctx, config.CHAT_DELIVERY_RECEIPTS, roomID+":"+uid, now)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			log.Printf("[engine] delivery receipt buffer failed room=%s: %v", roomID, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Block Enforcement
// ---------------------------------------------------------------------------

// isBlockedInRoom checks if the sender is blocked by (or has blocked) any
// other member in the room. Only enforced for DM rooms — blocked users can
// still message each other in group chats.
// Cached in Redis for 30 seconds to avoid per-message Postgres hits.
// DM room → other-user mapping is cached for 5 minutes (membership changes rarely).
func isBlockedInRoom(senderID, roomID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rdb := redis.GetRawClient()

	// Resolve the other user in the DM room. Cached in Redis to avoid a
	// Postgres hit on every single DM message.
	dmKey := "dm:other:" + roomID + ":" + senderID
	otherID, err := rdb.Get(ctx, dmKey).Result()
	if err != nil {
		// Cache miss — hit Postgres once and cache for 5 minutes
		err = postgress.GetRawDB().QueryRowContext(ctx,
			`SELECT rm.user_id FROM room_members rm
			 JOIN rooms r ON r.id = rm.room_id AND r.type = 'DM'
			 WHERE rm.room_id = $1 AND rm.user_id != $2 AND rm.status = 'active'
			 LIMIT 1`, roomID, senderID,
		).Scan(&otherID)
		if err != nil {
			return false // not a DM room or no other member
		}
		rdb.Set(ctx, dmKey, otherID, 5*time.Minute)
	}

	return isBlockedPairCached(ctx, senderID, otherID)
}

// isBlockedPairCached checks if either user has blocked the other, with
// Redis caching (30s TTL). Shared by isBlockedInRoom and isBlockedPair.
func isBlockedPairCached(ctx context.Context, userA, userB string) bool {
	a, b := userA, userB
	if a > b {
		a, b = b, a
	}
	cacheKey := "blockpair:" + a + ":" + b

	rdb := redis.GetRawClient()
	if cached, err := rdb.Get(ctx, cacheKey).Result(); err == nil {
		return cached == "1"
	}

	var blocked bool
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM blocked_users
			WHERE (blocker_id = $1 AND blocked_id = $2)
			   OR (blocker_id = $2 AND blocked_id = $1)
		)`, userA, userB,
	).Scan(&blocked)
	if err != nil {
		log.Printf("[engine] block check failed a=%s b=%s: %v", userA, userB, err)
		return false
	}

	val := "0"
	if blocked {
		val = "1"
	}
	rdb.Set(ctx, cacheKey, val, 30*time.Second)
	return blocked
}

// InvalidateBlockCache clears the cached block status for a user pair.
// Also clears DM room→user mapping caches that depend on this relationship.
func InvalidateBlockCache(userA, userB string) {
	a, b := userA, userB
	if a > b {
		a, b = b, a
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	redis.GetRawClient().Del(ctx, "blockpair:"+a+":"+b)
}

// isBlockedPair checks if either user has blocked the other.
// Uses Redis cache (30s TTL) to avoid per-call Postgres hits.
func isBlockedPair(userA, userB string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return isBlockedPairCached(ctx, userA, userB)
}

// ---------------------------------------------------------------------------
// Push Notifications for Chat Messages
// ---------------------------------------------------------------------------

// sendMessagePush sends push notifications to all offline room members
// (except the sender). Debounced per room+user to prevent spam on bursts.
// Skips users who have at least one active WebSocket connection, since they
// already receive the message in real-time (standard WhatsApp/Telegram pattern).
func (e *Engine) sendMessagePush(roomID, senderID, senderName, preview string) {
	if e.SendPushToUser == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()

	// Query room members except the sender (capped at 200 to prevent
	// unbounded result sets in large groups)
	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT user_id FROM room_members WHERE room_id = $1 AND user_id != $2 AND status = 'active' LIMIT 200`,
		roomID, senderID,
	)
	if err != nil {
		log.Printf("[engine] sendMessagePush query failed room=%s: %v", roomID, err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var memberID string
		if err := rows.Scan(&memberID); err != nil {
			log.Printf("[engine] sendMessagePush scan failed room=%s: %v", roomID, err)
			continue
		}

		// Skip users who are online — they already get the message via WebSocket
		if e.IsUserOnline(memberID) {
			continue
		}

		// Debounce: skip if we already pushed this room+user recently
		if e.ShouldPushMessage != nil && !e.ShouldPushMessage(ctx, roomID, memberID) {
			continue
		}

		e.SendPushToUser(ctx, memberID, map[string]string{
			"type":       "new_message",
			"roomId":     roomID,
			"senderId":   senderID,
			"senderName": senderName,
			"preview":    preview,
		}, false)
	}
}

// ---------------------------------------------------------------------------
// Presence Helpers
// ---------------------------------------------------------------------------

// IsUserOnline checks whether a user has at least one connected device.
func (e *Engine) IsUserOnline(userID string) bool {
	e.userMu.RLock()
	clients, ok := e.users[userID]
	online := ok && len(clients) > 0
	e.userMu.RUnlock()
	return online
}

// deliverPendingCall checks if a newly connected user has a pending incoming
// call (as callee, not yet answered) and delivers a synthetic call_ring so
// they can accept it. This handles the case where the callee was offline when
// the call was initiated and came online via a push notification.
func (e *Engine) deliverPendingCall(c *Client) {
	call := getActiveCall(c.UserID)
	if call == nil || call.Role != "callee" || call.Answered {
		return
	}
	// Only deliver if the call is still within the ring timeout window
	if time.Since(call.StartedAt) > CallRingTimeout {
		return
	}

	ringMsg, _ := json.Marshal(map[string]interface{}{
		config.FieldType:     config.MsgTypeCallRing,
		config.FieldFrom:     call.PeerID,
		config.FieldTo:       c.UserID,
		config.FieldCallID:   call.CallID,
		config.FieldHasVideo: call.HasVideo,
	})

	select {
	case c.Send <- ringMsg:
		// log.Printf("[calls] Delivered pending call_ring to user=%s call=%s", c.UserID, call.CallID)
	default:
		droppedMessages.Add(1)
	}
}

// OnlineUserCount returns the number of unique users with at least one WebSocket connection.
func (e *Engine) OnlineUserCount() int {
	e.userMu.RLock()
	n := len(e.users)
	e.userMu.RUnlock()
	return n
}

// ActiveConnectionCount returns the current number of WebSocket connections.
func ActiveConnectionCount() int64 {
	return activeConns.Load()
}

// DisconnectUser forcefully closes all WebSocket connections for a user.
// Used by the admin ban flow to immediately kick a banned user.
func (e *Engine) DisconnectUser(userID string) {
	e.userMu.RLock()
	clients := make([]*Client, 0, len(e.users[userID]))
	for c := range e.users[userID] {
		clients = append(clients, c)
	}
	e.userMu.RUnlock()

	for _, c := range clients {
		c.Conn.Close() // triggers readPump exit → Unregister → cleanup
	}
}

// getFriendIDs queries all friend user IDs for the given user.
// Uses UNION ALL to enable efficient index-only scans on both directions
// of the friendships table instead of a single OR query.
func getFriendIDs(userID string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()
	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT user_id_2 FROM friendships WHERE user_id_1 = $1
		 UNION ALL
		 SELECT user_id_1 FROM friendships WHERE user_id_2 = $1`,
		userID,
	)
	if err != nil {
		log.Printf("[engine] Failed to query friends for presence user=%s: %v", userID, err)
		return nil
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			log.Printf("[engine] getFriendIDs scan failed user=%s: %v", userID, err)
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// getBlockedSet returns a set of user IDs that are blocked by or have blocked
// the given user (bidirectional).
func getBlockedSet(userID string) map[string]bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()
	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT blocked_id FROM blocked_users WHERE blocker_id = $1
		 UNION ALL
		 SELECT blocker_id FROM blocked_users WHERE blocked_id = $1`,
		userID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	set := make(map[string]bool)
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			set[id] = true
		}
	}
	return set
}

// broadcastPresence publishes a presence event to all online friends via Redis.
// If the user has show_last_seen disabled or is a private account, we skip broadcasting.
func (e *Engine) broadcastPresence(userID string, online bool) {
	// Check if the user allows presence visibility
	var showLastSeen, isPrivate bool
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT show_last_seen, is_private FROM users WHERE id = $1`, userID,
	).Scan(&showLastSeen, &isPrivate)
	if err != nil {
		log.Printf("[engine] broadcastPresence query failed user=%s: %v", userID, err)
		return
	}
	if !showLastSeen || isPrivate {
		return // User has presence hidden or is private
	}

	friendIDs := getFriendIDs(userID)
	if len(friendIDs) == 0 {
		return
	}

	// Exclude blocked users (either direction) from presence targets
	blockedSet := getBlockedSet(userID)
	if len(blockedSet) > 0 {
		filtered := friendIDs[:0]
		for _, fid := range friendIDs {
			if !blockedSet[fid] {
				filtered = append(filtered, fid)
			}
		}
		friendIDs = filtered
		if len(friendIDs) == 0 {
			return
		}
	}

	msgType := config.MsgTypePresenceOnline
	payload := map[string]interface{}{
		config.FieldType:   msgType,
		config.FieldUserID: userID,
	}
	if !online {
		msgType = config.MsgTypePresenceOffline
		now := time.Now().UTC().Format(time.RFC3339)
		payload = map[string]interface{}{
			config.FieldType:       msgType,
			config.FieldUserID:     userID,
			config.FieldLastSeenAt: now,
		}
	}

	// Publish a SINGLE presence event with a target list instead of N separate
	// publishes. Each server filters locally to deliver only to connected targets.
	// This reduces O(N) marshal calls to O(1). (P1-1 fix)
	ctx, cancel = context.WithTimeout(context.Background(), config.RedisOpTimeout)
	defer cancel()

	envelope := map[string]interface{}{
		config.FieldType: config.MsgTypePrivate,
		config.FieldFrom: config.SystemSender,
		"targets":        friendIDs,
		config.FieldData: payload,
	}
	envBytes, err := json.Marshal(envelope)
	if err != nil {
		log.Printf("[engine] presence broadcast marshal failed for user=%s: %v", userID, err)
		return
	}
	if err := redis.GetRawClient().Publish(ctx, config.CHAT_GLOBAL_CHANNEL, envBytes).Err(); err != nil {
		log.Printf("[engine] presence broadcast failed for user=%s: %v", userID, err)
	}
}

// handleUserWentOffline updates last_seen_at in Postgres and broadcasts offline presence.
func (e *Engine) handleUserWentOffline(userID string) {
	// Update last_seen_at in the database
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()
	_, err := postgress.GetRawDB().ExecContext(ctx,
		`UPDATE users SET last_seen_at = NOW() WHERE id = $1`, userID,
	)
	if err != nil {
		log.Printf("[engine] Failed to update last_seen_at for user=%s: %v", userID, err)
	}

	// Broadcast offline status to friends
	e.broadcastPresence(userID, false)
}

// deliverToRoom sends raw bytes to everyone subscribed to a room on this server.
func (e *Engine) deliverToRoom(roomID string, payload []byte) {
	shard := e.getShard(roomID)
	shard.mu.RLock()
	clients, ok := shard.rooms[roomID]
	if !ok {
		shard.mu.RUnlock()
		return
	}
	// Copy the client set under the lock, then deliver outside the lock
	targets := make([]*Client, 0, len(clients))
	for c := range clients {
		targets = append(targets, c)
	}
	shard.mu.RUnlock()

	for _, c := range targets {
		select {
		case c.Send <- payload:
		default:
			// Client's buffer is full — drop to protect the server
			droppedMessages.Add(1)
		}
	}
}
