package chat

import (
	"context"
	"fmt"
	"log"
	"strings"
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
	redis.GetRawClient().Set(ctx, config.CALL_ACTIVE_COLON+userID, data, callActiveTTL)
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
		return nil
	}
	return &call
}

// clearActiveCall removes a user's active call state.
func clearActiveCall(userID string) {
	ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
	defer cancel()
	redis.GetRawClient().Del(ctx, config.CALL_ACTIVE_COLON+userID)
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

// markCallAnswered atomically updates the call state to reflect that the call
// was accepted. Uses a Lua script to prevent GET-then-SET race conditions.
func markCallAnswered(userID string) {
	ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
	defer cancel()
	key := config.CALL_ACTIVE_COLON + userID
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := markCallAnsweredScript.Run(ctx, redis.GetRawClient(), []string{key}, now).Err(); err != nil {
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
		log.Printf("[calls] Ring timeout for call=%s caller=%s callee=%s", callID, callerID, calleeID)

		// Clean up timer reference
		callTimersMu.Lock()
		delete(callTimers, callID)
		callTimersMu.Unlock()

		// Only fire if both users still have this call active (not already accepted/rejected)
		callerCall := getActiveCall(callerID)
		_ = getActiveCall(calleeID) // verify callee state exists

		if callerCall != nil && callerCall.CallID == callID && !callerCall.Answered {
			// Send call_missed to the caller
			missedMsg, _ := json.Marshal(map[string]string{
				config.FieldType:   config.MsgTypeCallMissed,
				config.FieldCallID: callID,
				config.FieldFrom:   calleeID,
				config.FieldTo:     callerID,
			})
			ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
			redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, missedMsg)
			cancel()

			// Clear both users' call state
			clearActiveCall(callerID)
			clearActiveCall(calleeID)

			// Log the missed call
			logCall(callID, "", callerID, hasVideo, "missed", 0)
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
// Returns true if they share a friendship or an active room membership.
// Combined into a single query with UNION ALL for short-circuit behavior (P2-7 fix).
func canUserCall(callerID, targetID string) bool {
	var exists bool
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM friendships
			WHERE (user_id_1 = $1 AND user_id_2 = $2)
			   OR (user_id_1 = $2 AND user_id_2 = $1)
			UNION ALL
			SELECT 1 FROM room_members rm1
			JOIN room_members rm2 ON rm1.room_id = rm2.room_id
			WHERE rm1.user_id = $1 AND rm2.user_id = $2
			  AND rm1.status = 'active' AND rm2.status = 'active'
			LIMIT 1
		)`, callerID, targetID,
	).Scan(&exists)
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
	return err == nil && exists
}

// ---------------------------------------------------------------------------
// Call History Logging (Postgres)
// ---------------------------------------------------------------------------

// logCall inserts a call record into the call_logs table.
// status: "completed", "missed", "rejected", "cancelled"
func logCall(callID, roomID, initiatedBy string, hasVideo bool, status string, durationSecs int) {
	callType := "audio"
	if hasVideo {
		callType = "video"
	}

	// Find room_id from the shared room between the two users if not provided
	var roomIDPtr *string
	if roomID != "" {
		roomIDPtr = &roomID
	}

	var endedAt *time.Time
	var durationPtr *int
	if status == "completed" && durationSecs > 0 {
		now := time.Now().UTC()
		endedAt = &now
		durationPtr = &durationSecs
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()
	_, err := postgress.GetRawDB().ExecContext(ctx,
		`INSERT INTO call_logs (call_id, room_id, initiated_by, call_type, tier, max_participants, ended_at, duration_seconds)
		 VALUES ($1, $2, $3::uuid, $4, 'p2p', 2, $5, $6)
		 ON CONFLICT (call_id) DO UPDATE SET ended_at = COALESCE($5, call_logs.ended_at), duration_seconds = COALESCE($6, call_logs.duration_seconds)`,
		callID, roomIDPtr, initiatedBy, callType, endedAt, durationPtr,
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()

	// Query device tokens for the callee
	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT token, device_type FROM device_tokens WHERE user_id = $1`, calleeID,
	)
	if err != nil {
		log.Printf("[calls] Failed to query device tokens for push user=%s: %v", calleeID, err)
		return
	}
	defer rows.Close()

	// Get caller's name for the notification
	var callerName string
	err = postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT COALESCE(name, 'Unknown') FROM users WHERE id = $1`, callerID,
	).Scan(&callerName)
	if err != nil {
		callerName = "Unknown"
	}

	callType := "Audio"
	if hasVideo {
		callType = "Video"
	}

	for rows.Next() {
		var token, deviceType string
		if err := rows.Scan(&token, &deviceType); err != nil {
			continue
		}

		// TODO: Integrate with FCM/APNs push service
		// For now, log the push notification intent
		log.Printf("[calls] PUSH: %s call from %s to device=%s type=%s token=%s...",
			callType, callerName, calleeID, deviceType, token[:min(10, len(token))])

		// When integrating FCM, the payload should include:
		// - High priority (for immediate delivery)
		// - callId, callerID, callerName, hasVideo
		// - data-only message (so the app can show a full-screen call UI)
		_ = fmt.Sprintf(`{"to":"%s","priority":"high","data":{"type":"incoming_call","callId":"%s","callerId":"%s","callerName":"%s","hasVideo":%v}}`,
			token, callID, callerID, callerName, hasVideo)
	}
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
			sendError(c, "NOT_ALLOWED", "You can only call friends or active room members")
			return true
		}

		// --- Busy Detection ---
		// Check if caller is already in a call
		if existing := getActiveCall(c.UserID); existing != nil {
			sendError(c, "ALREADY_IN_CALL", "You are already in a call")
			return true
		}
		// Check if callee is already in a call
		if existing := getActiveCall(targetUser); existing != nil {
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

		// --- Check if callee is online ---
		if !e.IsUserOnline(targetUser) {
			// Callee is offline — send call_missed immediately
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

			// Send push notification so callee sees the missed call
			runBackground(func() { sendCallPushNotification(targetUser, c.UserID, callID, hasVideo) })

			// Log as missed call
			runBackground(func() { logCall(callID, "", c.UserID, hasVideo, "missed", 0) })
			return true
		}

		// --- Set call state for both users ---
		setActiveCall(c.UserID, &activeCall{
			CallID:    callID,
			PeerID:    targetUser,
			Role:      "caller",
			HasVideo:  hasVideo,
			StartedAt: time.Now().UTC(),
		})
		setActiveCall(targetUser, &activeCall{
			CallID:    callID,
			PeerID:    c.UserID,
			Role:      "callee",
			HasVideo:  hasVideo,
			StartedAt: time.Now().UTC(),
		})

		// --- Start ring timeout ---
		e.startRingTimeout(callID, c.UserID, targetUser, hasVideo)

		// --- Send push notification to callee ---
		runBackground(func() { sendCallPushNotification(targetUser, c.UserID, callID, hasVideo) })

		// --- Log call initiation ---
		runBackground(func() { logCall(callID, "", c.UserID, hasVideo, "ringing", 0) })

	case config.MsgTypeCallAccept:
		// Cancel the ring timeout
		cancelRingTimeout(callID)

		// Mark both users' calls as answered
		markCallAnswered(c.UserID)
		markCallAnswered(targetUser)

		// Update call log
		runBackground(func() {
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

		// Clear both users' call state
		clearActiveCall(c.UserID)
		clearActiveCall(targetUser)

		// Log rejected call
		runBackground(func() { logCall(callID, "", targetUser, hasVideo, "rejected", 0) })

	case config.MsgTypeCallEnd:
		// Cancel any pending ring timeout
		cancelRingTimeout(callID)

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
		runBackground(func() { logCall(callID, "", c.UserID, hasVideo, status, duration) })

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

	log.Printf("[calls] User %s disconnected during call=%s, auto-ending", userID, call.CallID)

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
	logCall(call.CallID, "", call.PeerID, call.HasVideo, status, duration)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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
				userID := strings.TrimPrefix(key, config.CALL_ACTIVE_COLON)
				log.Printf("[calls] Cleaning orphan call state: user=%s call=%s (ringing for %v)",
					userID, call.CallID, time.Since(call.StartedAt).Round(time.Second))
				rdb.Del(ctx, key)
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
}
