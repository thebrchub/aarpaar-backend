package chat

import (
	"context"
	"hash/fnv"
	"log"
	"sync"
	"time"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/config"
	"github.com/tidwall/gjson"
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

	// done is closed to signal the Redis listener to stop (graceful shutdown)
	done chan struct{}
}

// NewEngine creates the engine and starts the Redis listener.
// Call this once during application startup.
func NewEngine() *Engine {
	e := &Engine{
		users: make(map[string]map[*Client]bool),
		done:  make(chan struct{}),
	}

	// Initialize each shard's map
	for i := range e.roomShards {
		e.roomShards[i].rooms = make(map[string]map[*Client]bool)
	}

	// Store the singleton so handlers can access the engine
	engineInstance = e

	// Start listening to the global Redis Pub/Sub channel
	go e.listenToRedis()
	return e
}

// Shutdown stops the Redis Pub/Sub listener and closes all client connections.
// Call this during graceful shutdown before closing Redis/Postgres.
func (e *Engine) Shutdown() {
	close(e.done)

	// Close all connected clients
	e.userMu.Lock()
	for _, clients := range e.users {
		for c := range clients {
			c.Conn.Close()
		}
	}
	e.userMu.Unlock()
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
	firstDevice := e.users[c.UserID] == nil || len(e.users[c.UserID]) == 0
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
		go e.broadcastPresence(c.UserID, true)
	}
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
	for roomID := range c.JoinedRooms {
		shard := e.getShard(roomID)
		shard.mu.Lock()
		if roomClients, ok := shard.rooms[roomID]; ok {
			delete(roomClients, c)
			if len(roomClients) == 0 {
				delete(shard.rooms, roomID)
			}
		}
		shard.mu.Unlock()
	}

	// Close the outbound channel so writePump exits cleanly
	close(c.Send)

	// If this was the user's last device, update last_seen_at and broadcast "offline"
	if lastDevice {
		go e.handleUserWentOffline(c.UserID)
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

	c.JoinedRooms[roomID] = true
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

	delete(c.JoinedRooms, roomID)
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
	rows, err := postgress.GetRawDB().Query(
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
		if err := rows.Scan(&id); err == nil {
			roomIDs = append(roomIDs, id)
		}
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

	// Collect all clients to notify and remove
	targets := make([]*Client, 0, len(clients))
	for c := range clients {
		targets = append(targets, c)
	}

	// Remove the room entirely
	delete(shard.rooms, roomID)
	shard.mu.Unlock()

	// Notify each client and remove the room from their local set
	for _, c := range targets {
		delete(c.JoinedRooms, roomID)
		select {
		case c.Send <- payload:
		default:
		}
	}
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
	for {
		select {
		case <-e.done:
			log.Println("[engine] Shutdown signal received, stopping Redis listener")
			return
		default:
		}

		e.subscribeAndListen()

		// If we reach here, the subscription broke. Wait and retry.
		log.Println("[engine] Redis Pub/Sub disconnected. Reconnecting in 2s...")

		select {
		case <-e.done:
			return
		case <-time.After(2 * time.Second):
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
			case config.MsgTypePrivate, config.MsgTypeMatchFound, config.MsgTypeStrangerDisconnected:
				// Private/system events targeted at a specific user
				if targetUser != "" {
					e.deliverToUser(targetUser, payload)
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
					e.deliverToRoom(roomID, payload)
					// Generate delivery receipts: notify the sender that the message
					// was delivered to online recipient(s) in this room.
					if senderID != "" {
						e.sendDeliveryReceipts(roomID, senderID)
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
		}
	}
}

// sendDeliveryReceipts checks if any non-sender clients are online in the room
// and sends a delivery receipt back to the sender. Also updates the recipient's
// last_delivered_at in the database asynchronously.
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
	if err == nil {
		e.deliverToUser(senderID, receipt)
	}

	// Update last_delivered_at for each online recipient (async, non-blocking)
	for uid := range recipientIDs {
		go func(userID, room string) {
			_, err := postgress.GetRawDB().Exec(
				`UPDATE room_members SET last_delivered_at = NOW()
				 WHERE room_id = $1 AND user_id = $2 AND last_delivered_at < NOW()`,
				room, userID,
			)
			if err != nil {
				log.Printf("[engine] delivery receipt DB update failed user=%s room=%s: %v", userID, room, err)
			}
		}(uid, roomID)
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

// getFriendIDs queries all friend user IDs for the given user.
func getFriendIDs(userID string) []string {
	rows, err := postgress.GetRawDB().Query(
		`SELECT CASE WHEN user_id_1 = $1 THEN user_id_2 ELSE user_id_1 END
		 FROM friendships
		 WHERE user_id_1 = $1 OR user_id_2 = $1`,
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
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// broadcastPresence publishes a presence event to all online friends via Redis.
// If the user has show_last_seen disabled, we skip broadcasting.
func (e *Engine) broadcastPresence(userID string, online bool) {
	// Check if the user allows presence visibility
	var showLastSeen bool
	err := postgress.GetRawDB().QueryRow(
		`SELECT show_last_seen FROM users WHERE id = $1`, userID,
	).Scan(&showLastSeen)
	if err != nil || !showLastSeen {
		return // User has presence hidden
	}

	friendIDs := getFriendIDs(userID)
	if len(friendIDs) == 0 {
		return
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

	ctx := context.Background()
	for _, friendID := range friendIDs {
		// Wrap in a private envelope so cross-server routing works
		envelope := map[string]interface{}{
			config.FieldType: config.MsgTypePrivate,
			config.FieldTo:   friendID,
			config.FieldFrom: config.SystemSender,
			config.FieldData: payload,
		}
		envBytes, err := json.Marshal(envelope)
		if err != nil {
			continue
		}
		redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, envBytes)
	}
}

// handleUserWentOffline updates last_seen_at in Postgres and broadcasts offline presence.
func (e *Engine) handleUserWentOffline(userID string) {
	// Update last_seen_at in the database
	_, err := postgress.GetRawDB().Exec(
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
		}
	}
}
