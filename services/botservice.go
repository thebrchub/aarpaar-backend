package services

import (
	"context"
	"log"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goccy/go-json"
	"github.com/google/uuid"
	"github.com/shivanand-burli/go-starter-kit/bot"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// Bot Service — Markov Chain Chatbot for Matchmaking Fallback
//
// When no human partner is available, a bot is matched with the user after
// a configurable delay. The bot identity is entirely in-memory — no database
// rows, no schema changes. The frontend receives identical payloads to a
// human match, making the bot indistinguishable from a real stranger.
// ---------------------------------------------------------------------------

// botFemaleClient is the female-persona Markov chain client (served to male users).
var botFemaleClient bot.BotService

// botMaleClient is the male-persona Markov chain client (served to female users).
var botMaleClient bot.BotService

// botConfigured is true when at least one bot client is ready.
var botConfigured bool

// sessions tracks active bot chat sessions (roomID → *BotSession).
var sessions sync.Map

// botUserIDs is a reverse lookup map (botUserID → roomID) for O(1) IsBotUser checks.
var botUserIDs sync.Map

// timers tracks pending bot match timers (userID → *time.Timer).
var timers sync.Map

// botDone is closed to signal all bot goroutines to stop (graceful shutdown).
var botDone chan struct{}

// ---------------------------------------------------------------------------
// Bot Session
// ---------------------------------------------------------------------------

// BotSession represents an active bot conversation in a stranger room.
type BotSession struct {
	RoomID      string
	BotUserID   string
	BotName     string
	UserID      string         // the real user in this session (for targeted disconnect notify)
	client      bot.BotService // gendered bot client for this session
	done        chan struct{}
	once        sync.Once    // ensures done is closed exactly once
	lastUserMsg atomic.Int64 // unix-nano timestamp of the last message from the real user
	replyQueue  chan string  // serialized reply queue — single worker goroutine drains this
}

// ---------------------------------------------------------------------------
// Name Pools — Random Indian Names (opposite gender)
// ---------------------------------------------------------------------------

var femaleNames = []string{
	"Ananya", "Priya", "Ishita", "Kavya", "Meera", "Riya", "Sneha", "Tanya",
	"Nisha", "Pooja", "Shreya", "Divya", "Aisha", "Simran", "Neha", "Sakshi",
	"Deepika", "Aarohi", "Kiara", "Avni", "Tanvi", "Rhea", "Sanya", "Zara",
	"Myra", "Ira", "Aditi", "Kritika", "Mahi", "Palak",
}

var maleNames = []string{
	"Aarav", "Vivaan", "Aditya", "Arjun", "Rohan", "Kabir", "Ishaan", "Dev",
	"Harsh", "Kunal", "Rahul", "Nikhil", "Sahil", "Varun", "Arnav", "Dhruv",
	"Karan", "Yash", "Shivam", "Aman", "Ayaan", "Reyansh", "Vihaan",
	"Siddharth", "Parth", "Aryan", "Rishi", "Atharv", "Rudra", "Krish",
}

// ---------------------------------------------------------------------------
// Initialization
// ---------------------------------------------------------------------------

// InitBot initializes the Markov chain chatbot clients (male + female persona).
// Call this once during application startup after Redis is connected.
//
// If BotEnabled is false, both clients are unconfigured and ScheduleBotMatch
// becomes a no-op. This mirrors the RTC optional client pattern.
func InitBot() {
	botDone = make(chan struct{})

	if !config.BotEnabled {
		log.Println("[bot] Bot not enabled — bot matching disabled")
		return
	}

	// Pick corpus text: env override > embedded default
	femaleCorpus := config.BotCorpusFemale
	if femaleCorpus == "" {
		femaleCorpus = corpusFemale
	}
	maleCorpus := config.BotCorpusMale
	if maleCorpus == "" {
		maleCorpus = corpusMale
	}

	botFemaleClient = bot.NewClientOptional(bot.Config{
		CorpusText: femaleCorpus,
	})
	botMaleClient = bot.NewClientOptional(bot.Config{
		CorpusText: maleCorpus,
	})

	if botFemaleClient.IsConfigured() && botMaleClient.IsConfigured() {
		botConfigured = true
		log.Println("[bot] Bot service initialized (male + female persona)")
		go botRedisListener()
	} else {
		log.Println("[bot] Bot corpus training failed — bot matching disabled")
	}
}

// ---------------------------------------------------------------------------
// Match Scheduling
// ---------------------------------------------------------------------------

// ScheduleBotMatch starts a timer that will match the user with a bot
// if no human partner is found within BotMatchDelay.
//
// Safe to call even if the bot is not configured (no-op).
func ScheduleBotMatch(userID string) {
	if !botConfigured {
		return
	}

	timer := time.AfterFunc(config.BotMatchDelay, func() {
		timers.Delete(userID)
		matchWithBot(userID)
	})

	// If there's an existing timer for this user, stop it first
	if old, loaded := timers.LoadAndDelete(userID); loaded {
		old.(*time.Timer).Stop()
	}
	timers.Store(userID, timer)
}

// CancelBotMatch cancels a pending bot match timer for the given user.
// Safe to call even if no timer exists (no-op).
func CancelBotMatch(userID string) {
	if val, loaded := timers.LoadAndDelete(userID); loaded {
		val.(*time.Timer).Stop()
	}
}

// IsBotUser checks if the given userID belongs to an active bot session. O(1).
func IsBotUser(userID string) bool {
	_, ok := botUserIDs.Load(userID)
	return ok
}

// ---------------------------------------------------------------------------
// Match Execution
// ---------------------------------------------------------------------------

// matchWithBot removes the user from the queue and creates a stranger room
// with a bot partner. Called when the bot timer fires.
func matchWithBot(userID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rdb := redis.GetRawClient()

	// Check if user is still in the queue (they may have been matched or left)
	isMember, err := rdb.SIsMember(ctx, config.DefaultMatchQueue, userID).Result()
	if err != nil || !isMember {
		return // User already matched or left queue
	}

	// Remove from queue
	rdb.SRem(ctx, config.DefaultMatchQueue, userID)

	// Look up the user's gender to pick an opposite-gender bot
	var userGender string
	err = postgress.GetRawDB().QueryRow(
		`SELECT gender FROM users WHERE id = $1`, userID,
	).Scan(&userGender)
	if err != nil {
		log.Printf("[bot] Failed to query user gender for %s: %v", userID, err)
		// Put user back in queue so they can match with a human
		rdb.SAdd(ctx, config.DefaultMatchQueue, userID)
		return
	}

	// Pick the gendered bot client (opposite gender to the user)
	var botClientForSession bot.BotService
	switch userGender {
	case config.GenderFemale:
		botClientForSession = botMaleClient // female user → male bot
	default:
		botClientForSession = botFemaleClient // male/any user → female bot
	}

	// Generate ephemeral bot identity
	botUserID := uuid.New().String()
	botName := pickBotName(userGender)

	// Create stranger room
	roomID := config.STRANGER_PREFIX + uuid.New().String()

	// Store both participants in Redis (same as human match)
	rdb.SAdd(ctx, config.STRANGER_MEMBERS_COLON+roomID, userID, botUserID)
	rdb.Expire(ctx, config.STRANGER_MEMBERS_COLON+roomID, 24*time.Hour)

	// Create bot session
	session := &BotSession{
		RoomID:     roomID,
		BotUserID:  botUserID,
		BotName:    botName,
		UserID:     userID,
		client:     botClientForSession,
		done:       make(chan struct{}),
		replyQueue: make(chan string, 8), // buffered — drops if user spams beyond 8 queued
	}
	session.lastUserMsg.Store(time.Now().UnixNano())
	sessions.Store(roomID, session)
	botUserIDs.Store(botUserID, roomID)

	// Notify user with match_found — identical payload to human match
	notifyBotMatch(ctx, userID, roomID, botName)

	// Auto-subscribe user to the stranger room
	if e := chat.GetEngine(); e != nil {
		e.JoinRoomForUser(userID, roomID)
	}

	log.Printf("[bot] Matched user %s with bot %s (%s) in room %s", userID, botName, botUserID, roomID)

	// Send an initial greeting after a short delay (simulates bot typing)
	go func() {
		select {
		case <-session.done:
			return
		case <-time.After(randomDelay(1500, 3000)):
		}

		sendBotTypingStart(ctx, session)

		select {
		case <-session.done:
			return
		case <-time.After(randomDelay(800, 2000)):
		}

		reply, err := session.client.Chat(ctx, "hello")
		if err != nil {
			log.Printf("[bot] Failed to generate greeting for room %s: %v", roomID, err)
			sendBotTypingEnd(ctx, session)
			return
		}
		sendBotTypingEnd(ctx, session)
		sendBotMessage(ctx, session, reply.Text)
	}()

	// Start the session watchdog (max duration + inactivity)
	go sessionWatchdog(session)

	// Start the reply worker (single goroutine processes all replies for this session)
	go replyWorker(session)
}

// pickBotName returns a random name from the opposite gender's pool.
func pickBotName(userGender string) string {
	switch userGender {
	case config.GenderMale:
		return femaleNames[rand.IntN(len(femaleNames))]
	case config.GenderFemale:
		return maleNames[rand.IntN(len(maleNames))]
	default: // "Any" or unknown — pick randomly from either pool
		if rand.IntN(2) == 0 {
			return femaleNames[rand.IntN(len(femaleNames))]
		}
		return maleNames[rand.IntN(len(maleNames))]
	}
}

// ---------------------------------------------------------------------------
// Bot Messaging
// ---------------------------------------------------------------------------

// notifyBotMatch sends a match_found event to the user — identical to human match.
func notifyBotMatch(ctx context.Context, targetUser, roomID, partnerName string) {
	eventPayload := map[string]interface{}{
		config.FieldType:            config.MsgTypeMatchFound,
		config.FieldRoomID:          roomID,
		config.FieldPartnerFakeName: partnerName,
		config.FieldPartnerAvatar:   "",
	}

	envelope := map[string]interface{}{
		config.FieldType: config.MsgTypePrivate,
		config.FieldTo:   targetUser,
		config.FieldFrom: config.SystemSender,
		config.FieldData: eventPayload,
	}

	envBytes, err := json.Marshal(envelope)
	if err != nil {
		log.Printf("[bot] Failed to marshal match notification: %v", err)
		return
	}
	redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, envBytes)
}

// sendBotMessage publishes a chat message from the bot to the room.
func sendBotMessage(ctx context.Context, session *BotSession, text string) {
	msg := map[string]interface{}{
		config.FieldType:     config.MsgTypeSendMessage,
		config.FieldFrom:     session.BotUserID,
		config.FieldRoomID:   session.RoomID,
		config.FieldText:     text,
		config.FieldFromName: session.BotName,
	}

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[bot] Failed to marshal bot message: %v", err)
		return
	}
	redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, msgBytes)
}

// sendBotTypingStart publishes a typing_start event from the bot.
func sendBotTypingStart(ctx context.Context, session *BotSession) {
	msg := map[string]interface{}{
		config.FieldType:   config.MsgTypeTypingStart,
		config.FieldFrom:   session.BotUserID,
		config.FieldRoomID: session.RoomID,
	}
	msgBytes, _ := json.Marshal(msg)
	redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, msgBytes)
}

// sendBotTypingEnd publishes a typing_end event from the bot.
func sendBotTypingEnd(ctx context.Context, session *BotSession) {
	msg := map[string]interface{}{
		config.FieldType:   config.MsgTypeTypingEnd,
		config.FieldFrom:   session.BotUserID,
		config.FieldRoomID: session.RoomID,
	}
	msgBytes, _ := json.Marshal(msg)
	redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, msgBytes)
}

// ---------------------------------------------------------------------------
// Redis Listener — Receives User Messages, Generates Bot Replies
// ---------------------------------------------------------------------------

// botRedisListener subscribes to chat:global and processes messages
// destined for rooms with active bot sessions.
func botRedisListener() {
	for {
		select {
		case <-botDone:
			return
		default:
		}

		botSubscribeAndListen()

		// Subscription broke — wait and reconnect
		log.Println("[bot] Redis Pub/Sub disconnected. Reconnecting in 2s...")
		select {
		case <-botDone:
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func botSubscribeAndListen() {
	ctx := context.Background()
	pubsub := redis.Subscribe(ctx, config.CHAT_GLOBAL_CHANNEL)
	if pubsub == nil {
		log.Println("[bot] Redis Subscribe returned nil, will retry...")
		return
	}
	defer pubsub.Close()

	ch := pubsub.Channel()

	for {
		select {
		case <-botDone:
			return
		case msg, ok := <-ch:
			if !ok {
				return // Channel closed — reconnect
			}

			payload := []byte(msg.Payload)

			// Extract fields using gjson (zero-alloc)
			fields := gjson.GetManyBytes(payload,
				config.FieldType,   // [0]
				config.FieldRoomID, // [1]
				config.FieldFrom,   // [2]
				config.FieldText,   // [3]
			)
			msgType := fields[0].String()
			roomID := fields[1].String()
			senderID := fields[2].String()
			text := fields[3].String()

			// Only process send_message events
			if msgType != config.MsgTypeSendMessage {
				continue
			}

			// Check if this room has an active bot session
			val, ok := sessions.Load(roomID)
			if !ok {
				continue
			}
			session := val.(*BotSession)

			// Ignore messages sent by the bot itself (prevent echo loops)
			if senderID == session.BotUserID {
				continue
			}

			// Ignore empty messages
			if text == "" {
				continue
			}

			// Record that the user sent a message (for inactivity tracking)
			session.lastUserMsg.Store(time.Now().UnixNano())

			// Queue the reply — non-blocking send; drop if buffer full (user spamming)
			select {
			case session.replyQueue <- text:
			default:
				// reply queue full — skip this message to avoid goroutine pile-up
			}
		}
	}
}

// replyWorker is the single goroutine per session that processes user messages
// sequentially. This ensures replies arrive in order and prevents goroutine spam.
func replyWorker(session *BotSession) {
	for {
		select {
		case <-session.done:
			return
		case userText, ok := <-session.replyQueue:
			if !ok {
				return
			}
			handleBotReply(session, userText)
		}
	}
}

// handleBotReply generates a reply to the user's message with human-like delays.
func handleBotReply(session *BotSession, userText string) {
	// Check if session is still active
	select {
	case <-session.done:
		return
	default:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Simulate reading time (proportional to message length)
	readDelay := randomDelay(500, 1500)
	select {
	case <-session.done:
		return
	case <-time.After(readDelay):
	}

	// Send typing indicator
	sendBotTypingStart(ctx, session)

	// Simulate typing time (longer for longer messages)
	typingDelay := randomDelay(800, 2500)
	select {
	case <-session.done:
		return
	case <-time.After(typingDelay):
	}

	// Generate reply using Markov chain
	reply, err := session.client.Chat(ctx, userText)
	if err != nil {
		log.Printf("[bot] Failed to generate reply for room %s: %v", session.RoomID, err)
		sendBotTypingEnd(ctx, session)
		return
	}

	// Send the message
	sendBotTypingEnd(ctx, session)
	sendBotMessage(ctx, session, reply.Text)
}

// ---------------------------------------------------------------------------
// Session Watchdog — Hard Timeout + Inactivity
// ---------------------------------------------------------------------------

// sessionWatchdog ends the bot session if:
//   - the total session duration exceeds BotSessionMaxDuration, OR
//   - the user hasn't sent any message within BotInactivityTimeout.
//
// Whichever fires first causes a force-disconnect.
func sessionWatchdog(session *BotSession) {
	maxTimer := time.NewTimer(config.BotSessionMaxDuration)
	defer maxTimer.Stop()

	inactTicker := time.NewTicker(5 * time.Second) // check inactivity every 5s
	defer inactTicker.Stop()

	for {
		select {
		case <-session.done:
			return // session already ended by user action

		case <-maxTimer.C:
			log.Printf("[bot] Session %s exceeded max duration (%v) — force ending",
				session.RoomID, config.BotSessionMaxDuration)
			forceEndSession(session)
			return

		case <-inactTicker.C:
			lastMsg := time.Unix(0, session.lastUserMsg.Load())
			if time.Since(lastMsg) >= config.BotInactivityTimeout {
				log.Printf("[bot] Session %s — user inactive for %v — force ending",
					session.RoomID, config.BotInactivityTimeout)
				forceEndSession(session)
				return
			}
		}
	}
}

// forceEndSession stops the bot session and notifies the user that the stranger left.
// Mirrors the same disconnect flow used by MatchActionHandler for human skips.
func forceEndSession(session *BotSession) {
	// 1. Stop the bot session
	StopBotSession(session.RoomID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rdb := redis.GetRawClient()

	// 2. Notify the user that "Stranger has left"
	disconnectData := map[string]interface{}{
		config.FieldType:   config.MsgTypeStrangerDisconnected,
		config.FieldRoomID: session.RoomID,
	}
	envelope := map[string]interface{}{
		config.FieldType: config.MsgTypePrivate,
		config.FieldTo:   session.UserID,
		config.FieldFrom: config.SystemSender,
		config.FieldData: disconnectData,
	}
	notifyBytes, _ := json.Marshal(envelope)
	redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, notifyBytes)

	// 3. Mark the room as closed
	rdb.Set(ctx, config.CHAT_CLOSED_COLON+session.RoomID, "1", 24*time.Hour)
	rdb.Del(ctx, config.STRANGER_MEMBERS_COLON+session.RoomID)

	// 4. Broadcast room_closed so the engine evicts clients
	closedEvent, _ := json.Marshal(map[string]string{
		config.FieldType:   config.MsgTypeRoomClosed,
		config.FieldRoomID: session.RoomID,
	})
	redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, closedEvent)
}

// ---------------------------------------------------------------------------
// Session Cleanup
// ---------------------------------------------------------------------------

// StopBotSession stops a bot session for the given room. Idempotent.
func StopBotSession(roomID string) {
	val, loaded := sessions.LoadAndDelete(roomID)
	if !loaded {
		return
	}
	session := val.(*BotSession)
	session.once.Do(func() {
		close(session.done)
	})
	botUserIDs.Delete(session.BotUserID)
	log.Printf("[bot] Stopped bot session for room %s", roomID)
}

// StopAllSessions stops all active bot sessions and closes the bot client.
// Call this during graceful shutdown.
func StopAllSessions() {
	if botDone != nil {
		close(botDone)
	}

	// Stop all active sessions
	sessions.Range(func(key, value any) bool {
		session := value.(*BotSession)
		session.once.Do(func() {
			close(session.done)
		})
		botUserIDs.Delete(session.BotUserID)
		sessions.Delete(key)
		return true
	})

	// Cancel all pending timers
	timers.Range(func(key, value any) bool {
		value.(*time.Timer).Stop()
		timers.Delete(key)
		return true
	})

	// Close the bot clients
	if botFemaleClient != nil && botFemaleClient.IsConfigured() {
		botFemaleClient.Close()
	}
	if botMaleClient != nil && botMaleClient.IsConfigured() {
		botMaleClient.Close()
	}

	log.Println("[bot] All bot sessions stopped")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// randomDelay returns a random duration between minMs and maxMs milliseconds.
func randomDelay(minMs, maxMs int) time.Duration {
	ms := minMs + rand.IntN(maxMs-minMs)
	return time.Duration(ms) * time.Millisecond
}
