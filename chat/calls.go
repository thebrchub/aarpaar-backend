package chat

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/goccy/go-json"
	goredis "github.com/redis/go-redis/v9"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/config"
	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// Call State Management
//
// Tracks active calls in-memory (per server instance) with Redis-backed
// cross-instance state. Each user can only be in ONE call at a time.
//
// Features:
//   - Call state tracking (who is in a call with whom)
//   - Call timeout → call_missed after CallRingTimeout
//   - call_busy detection when target is already in a call
//   - Auto call_end on WebSocket disconnect
//   - Authorization checks (friendship/room membership required)
//   - Call history logging to Postgres (call_logs table)
//   - Push notification stubs for incoming calls
// ---------------------------------------------------------------------------

const (
	// CallRingTimeout is how long we wait for the callee to answer before
	// sending call_missed to the caller. 35 seconds is standard for most apps.
	CallRingTimeout = 35 * time.Second

	// callActiveTTL is the auto-cleanup TTL for stuck call state entries.
	callActiveTTL = 2 * time.Hour
)

// activeCall represents a user's current call state.
type activeCall struct {
	CallID    string    `json:"callId"`
	PeerID    string    `json:"peerId"`
	Role      string    `json:"role"` // "caller" or "callee"
	HasVideo  bool      `json:"hasVideo"`
	StartedAt time.Time `json:"startedAt"` // when the call was initiated (ring time)
	Answered  bool      `json:"answered"`  // true once call_accept is received
}

// callTimers stores pending ring timeout timers per callID.
// When a call_ring is sent, we start a timer. If no accept/reject
// arrives within CallRingTimeout, we auto-send call_missed.
var (
	callTimersMu sync.Mutex
	callTimers   = make(map[string]*time.Timer) // callID → timer
)

// ---------------------------------------------------------------------------
// Call State CRUD (Redis-backed for multi-instance support)
// ---------------------------------------------------------------------------

// setActiveCall marks a user as being in a call (stored in Redis).
func setActiveCall(userID string, call *activeCall) {
	data, err := json.Marshal(call)
	if err != nil {
		log.Printf("[calls] marshal activeCall failed user=%s: %v", userID, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
	defer cancel()
	if err := redis.GetRawClient().Set(ctx, config.CALL_ACTIVE_COLON+userID, data, callActiveTTL).Err(); err != nil {
		log.Printf("[calls] Redis Set activeCall failed user=%s: %v", userID, err)
	}
}

// getActiveCall retrieves a user's current call state, or nil if not in a call.
func getActiveCall(userID string) *activeCall {
	ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
	defer cancel()
	val, err := redis.GetRawClient().Get(ctx, config.CALL_ACTIVE_COLON+userID).Result()
	if err != nil {
		return nil
	}
	var call activeCall
	if err := json.Unmarshal([]byte(val), &call); err != nil {
		log.Printf("[calls] Unmarshal activeCall failed user=%s: %v", userID, err)
		return nil
	}
	return &call
}

// clearActiveCall removes a user's active call state.
func clearActiveCall(userID string) {
	ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
	defer cancel()
	if err := redis.GetRawClient().Del(ctx, config.CALL_ACTIVE_COLON+userID).Err(); err != nil {
		log.Printf("[calls] Redis Del activeCall failed user=%s: %v", userID, err)
	}
}

// markCallAnsweredScript is a Lua script for atomic read-modify-write of call state.
// This prevents race conditions when multiple devices send call_accept simultaneously.
var markCallAnsweredScript = goredis.NewScript(`
	local val = redis.call('GET', KEYS[1])
	if not val then return nil end
	local call = cjson.decode(val)
	call.answered = true
	call.startedAt = ARGV[1]
	redis.call('SET', KEYS[1], cjson.encode(call), 'KEEPTTL')
	return 1
`)

// setActiveCallPairScript atomically sets call state for BOTH caller and callee
// only if NEITHER currently has an active call. Returns:
//
//	0 = success (both keys set)
//	1 = caller already busy
//	2 = callee already busy
var setActiveCallPairScript = goredis.NewScript(`
	if redis.call('EXISTS', KEYS[1]) == 1 then return 1 end
	if redis.call('EXISTS', KEYS[2]) == 1 then return 2 end
	redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[3])
	redis.call('SET', KEYS[2], ARGV[2], 'EX', ARGV[3])
	return 0
`)

// clearCallIfMatchScript atomically clears a call state key only if the stored
// callId matches the expected one and the call is unanswered. Used by ring timeout
// to prevent race with concurrent call_accept/call_end.
// Returns 1 if cleared, 0 if no match (already accepted/cleared).
var clearCallIfMatchScript = goredis.NewScript(`
	local val = redis.call('GET', KEYS[1])
	if not val then return 0 end
	local call = cjson.decode(val)
	if call.callId ~= ARGV[1] then return 0 end
	if call.answered then return 0 end
	redis.call('DEL', KEYS[1])
	return 1
`)

// markCallAnswered atomically updates the call state to reflect that the call
// was accepted. Uses a Lua script to prevent GET-then-SET race conditions.
func markCallAnswered(userID string) {
	ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
	defer cancel()
	key := config.CALL_ACTIVE_COLON + userID
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := markCallAnsweredScript.Run(ctx, redis.GetRawClient(), []string{key}, now).Err(); err != nil && err != goredis.Nil {
		log.Printf("[calls] markCallAnswered Lua failed user=%s: %v", userID, err)
	}
}

// ---------------------------------------------------------------------------
// Ring Timeout Management
// ---------------------------------------------------------------------------

// startRingTimeout starts a timer that fires call_missed if the callee
// doesn't answer within CallRingTimeout.
func (e *Engine) startRingTimeout(callID, callerID, calleeID string, hasVideo bool) {
	callTimersMu.Lock()
	defer callTimersMu.Unlock()

	// Cancel any existing timer for this call
	if t, ok := callTimers[callID]; ok {
		t.Stop()
	}

	callTimers[callID] = time.AfterFunc(CallRingTimeout, func() {
		// Clean up timer reference
		callTimersMu.Lock()
		delete(callTimers, callID)
		callTimersMu.Unlock()

		// Atomically clear both users' call state only if the callId still
		// matches and the call is unanswered. This prevents the race between
		// ring timeout and concurrent call_accept/call_end.
		ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
		defer cancel()
		rdb := redis.GetRawClient()
		callerCleared, _ := clearCallIfMatchScript.Run(ctx, rdb,
			[]string{config.CALL_ACTIVE_COLON + callerID}, callID).Int()
		clearCallIfMatchScript.Run(ctx, rdb,
			[]string{config.CALL_ACTIVE_COLON + calleeID}, callID)

		// Only send call_missed if we actually cleared the caller's state
		// (meaning nobody else handled this call before us)
		if callerCleared == 1 {
			missedMsg, _ := json.Marshal(map[string]string{
				config.FieldType:   config.MsgTypeCallMissed,
				config.FieldCallID: callID,
				config.FieldFrom:   calleeID,
				config.FieldTo:     callerID,
			})
			pubCtx, pubCancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
			redis.Publish(pubCtx, config.CHAT_GLOBAL_CHANNEL, missedMsg)
			pubCancel()

			logCall(callID, "", callerID, calleeID, hasVideo, "missed", 0)
			RunBackground(func() { sendMissedCallPush(calleeID, callerID, callID) })
		}
	})
}

// cancelRingTimeout stops the ring timeout timer for a call.
func cancelRingTimeout(callID string) {
	callTimersMu.Lock()
	defer callTimersMu.Unlock()
	if t, ok := callTimers[callID]; ok {
		t.Stop()
		delete(callTimers, callID)
	}
}

// ---------------------------------------------------------------------------
// Authorization Check
// ---------------------------------------------------------------------------

// canUserCall checks if the caller is allowed to call the target user.
// Returns true only if they share a friendship. Room membership alone is
// insufficient — non-friends in shared DM rooms must not be able to call.
func canUserCall(callerID, targetID string) bool {
	var exists bool
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM friendships
			WHERE (user_id_1 = $1 AND user_id_2 = $2)
			   OR (user_id_1 = $2 AND user_id_2 = $1)
		)`, callerID, targetID,
	).Scan(&exists)
	if err != nil {
		log.Printf("[calls] canUserCall query failed caller=%s target=%s: %v", callerID, targetID, err)
	}
	return err == nil && exists
}

// isUserBlocked checks if target has blocked the caller (or vice versa).
func isUserBlocked(callerID, targetID string) bool {
	var exists bool
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM blocked_users
			WHERE (blocker_id = $1 AND blocked_id = $2)
			   OR (blocker_id = $2 AND blocked_id = $1)
		)`, callerID, targetID,
	).Scan(&exists)
	if err != nil {
		log.Printf("[calls] isUserBlocked query failed caller=%s target=%s: %v", callerID, targetID, err)
	}
	return err == nil && exists
}

// ---------------------------------------------------------------------------
// Call History Logging (Postgres)
// ---------------------------------------------------------------------------

// logCall inserts a call record into the call_logs table.
// status: "completed", "missed", "rejected", "cancelled"
func logCall(callID, roomID, initiatedBy, peerID string, hasVideo bool, status string, durationSecs int) {
	callType := "audio"
	if hasVideo {
		callType = "video"
	}

	var roomIDPtr *string
	if roomID != "" {
		roomIDPtr = &roomID
	}

	var peerIDPtr *string
	if peerID != "" {
		peerIDPtr = &peerID
	}

	// Set endedAt for all terminal statuses (completed, cancelled, missed, rejected)
	var endedAt *time.Time
	var durationPtr *int
	switch status {
	case "completed", "cancelled", "missed", "rejected":
		now := time.Now().UTC()
		endedAt = &now
		if durationSecs > 0 {
			durationPtr = &durationSecs
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()
	_, err := postgress.GetRawDB().ExecContext(ctx,
		`INSERT INTO call_logs (call_id, room_id, initiated_by, peer_id, call_type, tier, max_participants, ended_at, duration_seconds)
		 VALUES ($1, $2, $3::uuid, $4, $5, 'p2p', 2, $6, $7)
		 ON CONFLICT (call_id) DO UPDATE SET
		   peer_id = COALESCE(call_logs.peer_id, EXCLUDED.peer_id),
		   ended_at = COALESCE(EXCLUDED.ended_at, call_logs.ended_at),
		   duration_seconds = COALESCE(EXCLUDED.duration_seconds, call_logs.duration_seconds)`,
		callID, roomIDPtr, initiatedBy, peerIDPtr, callType, endedAt, durationPtr,
	)
	if err != nil {
		log.Printf("[calls] Failed to log call=%s: %v", callID, err)
	}
}

// ---------------------------------------------------------------------------
// Push Notification for Incoming Calls
// ---------------------------------------------------------------------------

// sendCallPushNotification sends a push notification to the callee's devices
// when they receive an incoming call. This ensures the call rings even if
// the app is in the background.
func sendCallPushNotification(calleeID, callerID, callID string, hasVideo bool) {
	e := GetEngine()
	if e == nil || e.SendPushToUser == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()

	// Get caller's name for the notification
	var callerName string
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT COALESCE(name, 'Unknown') FROM users WHERE id = $1`, callerID,
	).Scan(&callerName)
	if err != nil {
		log.Printf("[calls] sendCallPush caller name query failed caller=%s: %v", callerID, err)
		callerName = "Unknown"
	}

	callType := "audio"
	if hasVideo {
		callType = "video"
	}

	title := callerName + " is calling you"
	body := "Incoming " + callType + " call"
	e.SendPushToUser(ctx, calleeID, map[string]string{
		"type":       "incoming_call",
		"callId":     callID,
		"callerId":   callerID,
		"callerName": callerName,
		"hasVideo":   callType,
		"title":      title,
		"body":       body,
	}, true)
}

// sendMissedCallPush sends a push notification to the callee about a missed call.
func sendMissedCallPush(calleeID, callerID, callID string) {
	e := GetEngine()
	if e == nil || e.SendPushToUser == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()

	var callerName string
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT COALESCE(name, 'Unknown') FROM users WHERE id = $1`, callerID,
	).Scan(&callerName)
	if err != nil {
		log.Printf("[calls] sendMissedCallPush caller name query failed caller=%s: %v", callerID, err)
		callerName = "Unknown"
	}

	e.SendPushToUser(ctx, calleeID, map[string]string{
		"type":       "missed_call",
		"callId":     callID,
		"callerName": callerName,
		"title":      "Missed call",
		"body":       "from " + callerName,
	}, true)
}

// ---------------------------------------------------------------------------
// Server-Side Call Signaling Handler
//
// processCallSignaling is called from readPump when a call-related message
// arrives. It applies authorization, busy detection, state tracking,
// timeout management, and then relays the message via Redis Pub/Sub.
//
// Returns true if the message was handled (caller should continue to next msg).
// ---------------------------------------------------------------------------

func (e *Engine) processCallSignaling(c *Client, msgType string, payload []byte) bool {
	callID := extractField(payload, config.FieldCallID)
	targetUser := extractField(payload, config.FieldTo)
	hasVideo := extractFieldBool(payload, config.FieldHasVideo)

	if targetUser == "" {
		sendError(c, "INVALID_PAYLOAD", "Missing 'to' field for call signaling")
		return true
	}

	switch msgType {

	case config.MsgTypeCallRing:
		// --- Authorization ---
		if isUserBlocked(c.UserID, targetUser) {
			sendError(c, "BLOCKED", "Cannot call this user")
			return true
		}
		if !canUserCall(c.UserID, targetUser) {
			sendError(c, "NOT_ALLOWED", "You can only call friends")
			return true
		}

		// --- Atomically set call state for both users ---
		// Uses a Lua script to SET both keys only if neither exists,
		// preventing the TOCTOU race where two concurrent call_ring
		// messages could put a user in two simultaneous calls.
		now := time.Now().UTC()
		callerState, _ := json.Marshal(&activeCall{
			CallID:    callID,
			PeerID:    targetUser,
			Role:      "caller",
			HasVideo:  hasVideo,
			StartedAt: now,
		})
		calleeState, _ := json.Marshal(&activeCall{
			CallID:    callID,
			PeerID:    c.UserID,
			Role:      "callee",
			HasVideo:  hasVideo,
			StartedAt: now,
		})
		{
			ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
			result, err := setActiveCallPairScript.Run(ctx, redis.GetRawClient(),
				[]string{config.CALL_ACTIVE_COLON + c.UserID, config.CALL_ACTIVE_COLON + targetUser},
				callerState, calleeState, int(callActiveTTL.Seconds()),
			).Int()
			cancel()
			if err != nil {
				log.Printf("[calls] setActiveCallPair Lua failed caller=%s callee=%s: %v", c.UserID, targetUser, err)
				sendError(c, "INTERNAL_ERROR", "Failed to initiate call")
				return true
			}
			if result == 1 {
				sendError(c, "ALREADY_IN_CALL", "You are already in a call")
				return true
			}
			if result == 2 {
				busyMsg, _ := json.Marshal(map[string]string{
					config.FieldType:   config.MsgTypeCallBusy,
					config.FieldCallID: callID,
					config.FieldFrom:   targetUser,
					config.FieldTo:     c.UserID,
				})
				select {
				case c.Send <- busyMsg:
				default:
					droppedMessages.Add(1)
				}
				return true
			}
		}

		// --- Check if callee is online ---
		calleeOnline := e.IsUserOnline(targetUser)

		// If callee is offline and push is not configured, auto-miss
		if !calleeOnline && e.SendPushToUser == nil {
			// Clean up the state we just atomically set
			clearActiveCall(c.UserID)
			clearActiveCall(targetUser)
			missedMsg, _ := json.Marshal(map[string]string{
				config.FieldType:   config.MsgTypeCallMissed,
				config.FieldCallID: callID,
				config.FieldFrom:   targetUser,
				config.FieldTo:     c.UserID,
			})
			select {
			case c.Send <- missedMsg:
			default:
				droppedMessages.Add(1)
			}
			RunBackground(func() { logCall(callID, "", c.UserID, targetUser, hasVideo, "missed", 0) })
			return true
		}

		// --- Start ring timeout ---
		e.startRingTimeout(callID, c.UserID, targetUser, hasVideo)

		// --- Send push notification only when callee is offline ---
		// When online, call_ring is delivered via WebSocket (no push needed).
		if !calleeOnline {
			RunBackground(func() { sendCallPushNotification(targetUser, c.UserID, callID, hasVideo) })
		}

		// --- Log call initiation ---
		RunBackground(func() { logCall(callID, "", c.UserID, targetUser, hasVideo, "ringing", 0) })

	case config.MsgTypeCallAccept:
		// Cancel the ring timeout
		cancelRingTimeout(callID)

		// Dismiss ringing on the callee's OTHER devices
		e.dismissCallOnOtherDevices(c, callID)

		// Mark both users' calls as answered
		markCallAnswered(c.UserID)
		markCallAnswered(targetUser)

		// Update call log
		RunBackground(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
			defer cancel()
			_, err := postgress.GetRawDB().ExecContext(ctx,
				`UPDATE call_logs SET started_at = NOW() WHERE call_id = $1`, callID,
			)
			if err != nil {
				log.Printf("[calls] Failed to update call start time call=%s: %v", callID, err)
			}
		})

	case config.MsgTypeCallReject:
		// Cancel the ring timeout
		cancelRingTimeout(callID)

		// Dismiss ringing on the callee's OTHER devices
		e.dismissCallOnOtherDevices(c, callID)

		// Clear both users' call state
		clearActiveCall(c.UserID)
		clearActiveCall(targetUser)

		// Log rejected call
		RunBackground(func() { logCall(callID, "", targetUser, c.UserID, hasVideo, "rejected", 0) })

	case config.MsgTypeCallEnd:
		// Cancel any pending ring timeout
		cancelRingTimeout(callID)

		// Dismiss call UI on the sender's OTHER devices
		e.dismissCallOnOtherDevices(c, callID)

		// Calculate duration if call was answered
		callerCall := getActiveCall(c.UserID)
		duration := 0
		if callerCall != nil && callerCall.Answered {
			duration = int(time.Since(callerCall.StartedAt).Seconds())
		}

		// Clear both users' call state
		clearActiveCall(c.UserID)
		clearActiveCall(targetUser)

		// Log completed call with duration
		status := "completed"
		if callerCall != nil && !callerCall.Answered {
			status = "cancelled"
		}
		RunBackground(func() { logCall(callID, "", c.UserID, targetUser, hasVideo, status, duration) })

	case config.MsgTypeCallLeave:
		// For future group calls — treat like call_end for 1:1
		cancelRingTimeout(callID)
		clearActiveCall(c.UserID)
		clearActiveCall(targetUser)
	}

	// --- Relay the message via Redis Pub/Sub (stamp sender) ---
	// NOTE: We must allocate a new slice — append(c.fromPrefix, ...) can
	// mutate the fromPrefix backing array if it has spare capacity.
	stamped := make([]byte, 0, len(c.fromPrefix)+len(payload)-1)
	stamped = append(stamped, c.fromPrefix...)
	stamped = append(stamped, payload[1:]...)
	ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
	redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, stamped)
	cancel()

	return true
}

// ---------------------------------------------------------------------------
// Auto-Hangup on Disconnect
//
// Called from Engine.Unregister when a user's last device disconnects.
// If they were in an active call, this sends call_end to the peer.
// ---------------------------------------------------------------------------

func (e *Engine) handleCallDisconnect(userID string) {
	call := getActiveCall(userID)
	if call == nil {
		return // Not in a call
	}

	// log.Printf("[calls] User %s disconnected during call=%s, auto-ending", userID, call.CallID)

	// Cancel ring timeout if still ringing
	cancelRingTimeout(call.CallID)

	// Calculate duration
	duration := 0
	if call.Answered {
		duration = int(time.Since(call.StartedAt).Seconds())
	}

	// Send call_end to the peer
	endMsg, _ := json.Marshal(map[string]string{
		config.FieldType:   config.MsgTypeCallEnd,
		config.FieldCallID: call.CallID,
		config.FieldFrom:   userID,
		config.FieldTo:     call.PeerID,
	})
	ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
	redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, endMsg)
	cancel()

	// Clear both users' call state
	clearActiveCall(userID)
	clearActiveCall(call.PeerID)

	// Log the call
	status := "completed"
	if !call.Answered {
		status = "cancelled"
	}
	logCall(call.CallID, "", userID, call.PeerID, call.HasVideo, status, duration)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// dismissCallOnOtherDevices sends a call_dismiss message to ALL of the
// acting user's other connected devices (same user, different connections).
// This stops ringing / call UI on devices that didn't accept/reject/end.
func (e *Engine) dismissCallOnOtherDevices(sender *Client, callID string) {
	dismissMsg, _ := json.Marshal(map[string]string{
		config.FieldType:   config.MsgTypeCallDismiss,
		config.FieldCallID: callID,
	})

	e.userMu.RLock()
	clients, ok := e.users[sender.UserID]
	if !ok {
		e.userMu.RUnlock()
		return
	}
	targets := make([]*Client, 0, len(clients))
	for c := range clients {
		if c != sender {
			targets = append(targets, c)
		}
	}
	e.userMu.RUnlock()

	for _, c := range targets {
		select {
		case c.Send <- dismissMsg:
		default:
			droppedMessages.Add(1)
		}
	}
}

// extractField is a fast JSON field extractor using gjson.
func extractField(payload []byte, field string) string {
	return gjson.GetBytes(payload, field).String()
}

func extractFieldBool(payload []byte, field string) bool {
	return gjson.GetBytes(payload, field).Bool()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Orphan Call State Scanner
//
// Periodically scans Redis for call:active:* keys that have been ringing
// longer than CallRingTimeout without being answered. This handles the case
// where a server crashes and its local time.AfterFunc timers are lost,
// which would otherwise leave users stuck in "busy" state for up to 2 hours
// (the callActiveTTL). Runs every 60 seconds.
// ---------------------------------------------------------------------------

// startOrphanCallScanner starts the background scanner goroutine.
// Stops when the done channel is closed (engine shutdown).
func startOrphanCallScanner(done <-chan struct{}) {
	ticker := time.NewTicker(60 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				scanOrphanCalls()
			}
		}
	}()
}

// scanOrphanCalls checks all active call states and cleans up orphans.
func scanOrphanCalls() {
	// Scan P2P call states
	scanOrphanP2PCalls()

	// Scan group call states
	ScanOrphanGroupCalls()
}

// scanOrphanP2PCalls checks P2P (1:1) active call states.
func scanOrphanP2PCalls() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rdb := redis.GetRawClient()
	pattern := config.CALL_ACTIVE_COLON + "*"

	var cursor uint64
	for {
		keys, nextCursor, err := rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			log.Printf("[calls] Orphan call scan error: %v", err)
			return
		}

		for _, key := range keys {
			val, err := rdb.Get(ctx, key).Result()
			if err != nil {
				continue
			}
			var call activeCall
			if err := json.Unmarshal([]byte(val), &call); err != nil {
				continue
			}

			// Clean up calls that have been ringing longer than timeout + grace period
			if !call.Answered && time.Since(call.StartedAt) > CallRingTimeout+30*time.Second {
				// userID := strings.TrimPrefix(key, config.CALL_ACTIVE_COLON)
				// log.Printf("[calls] Cleaning orphan call state: user=%s call=%s (ringing for %v)",
				// 	userID, call.CallID, time.Since(call.StartedAt).Round(time.Second))
				rdb.Del(ctx, key)
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
}
