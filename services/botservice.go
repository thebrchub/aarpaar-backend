package services

import (
	"bufio"
	"context"
	"log"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goccy/go-json"
	"github.com/google/uuid"
	"github.com/shivanand-burli/go-starter-kit/bot"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// Bot Service — Retrieval-Based Chatbot for Matchmaking Fallback
//
// When no human partner is available, a bot is matched with the user after
// a configurable delay. The bot identity is entirely in-memory — no database
// rows, no schema changes. The frontend receives identical payloads to a
// human match, making the bot indistinguishable from a real stranger.
//
// Architecture:
//   - 1 global bot.Client (loads corpus once at startup)
//   - Per-match: bot.Session with isolated conversation state
//   - 1000+ personas defined in the corpus file
//   - Persona name = bot display name (no separate name pool)
// ---------------------------------------------------------------------------

// botClient is the single global bot client (shared corpus index).
var botClient *bot.Client

// botConfigured is true when the bot client is ready.
var botConfigured bool

// personaNames holds all persona tags extracted from the corpus at startup.
var personaNames []string

// sessions tracks active bot chat sessions (roomID → *BotSession).
var sessions sync.Map

// botUserIDs is a reverse lookup map (botUserID → roomID) for O(1) IsBotUser checks.
var botUserIDs sync.Map

// timers tracks pending bot match timers (userID → *time.Timer).
var timers sync.Map

// botDone is closed to signal all bot goroutines to stop (graceful shutdown).
var botDone chan struct{}

// botDoneOnce ensures botDone is closed exactly once (prevents panic on double-close).
var botDoneOnce sync.Once

// botNamespace is a fixed UUID v5 namespace for generating deterministic bot user IDs.
var botNamespace = uuid.MustParse("a3bb189e-8bf9-3888-9912-ace4e6543002")

// ---------------------------------------------------------------------------
// Bot Session
// ---------------------------------------------------------------------------

// BotSession represents an active bot conversation in a stranger room.
type BotSession struct {
	RoomID      string
	BotUserID   string
	BotName     string
	UserID      string       // the real user in this session (for targeted disconnect notify)
	session     *bot.Session // per-session SDK session with isolated state
	done        chan struct{}
	once        sync.Once    // ensures done is closed exactly once
	lastUserMsg atomic.Int64 // unix-nano timestamp of the last message from the real user
	replyQueue  chan string  // serialized reply queue — single worker goroutine drains this
}

// ---------------------------------------------------------------------------
// Initialization
// ---------------------------------------------------------------------------

// InitBot initializes the retrieval-based chatbot client with persona support.
// Call this once during application startup after Redis is connected.
//
// If BotEnabled is false, the client is not created and ScheduleBotMatch
// becomes a no-op. This mirrors the RTC optional client pattern.
func InitBot() {
	botDone = make(chan struct{})

	if !config.BotEnabled {
		log.Println("[bot] Bot not enabled — bot matching disabled")
		return
	}

	// Get corpus data (already loaded from env or file by config.Init)
	corpusData := config.BotCorpusData
	if strings.TrimSpace(corpusData) == "" {
		log.Println("[bot] Corpus data is empty — bot matching disabled")
		return
	}

	// Create the single bot client with retrieval engine
	client, err := bot.NewClient(bot.Config{
		CorpusData:        corpusData,
		CorpusFormat:      "tsv",
		AskBackRate:       0.6,
		HistorySize:       200,
		MaxRetries:        15,
		HumanizeRetrieval: true,
		Humanize: bot.HumanizeConfig{
			Enabled:      true,
			TypoRate:     0.015,
			EmojiRate:    0.03,
			FillerRate:   0.03,
			FragmentRate: 0.02,
			CasingJitter: true,
		},
	})
	if err != nil {
		log.Printf("[bot] Failed to initialize bot client: %v", err)
		log.Println("[bot] Bot matching disabled")
		return
	}

	botClient = client

	// Extract persona names from the corpus
	personaNames = extractPersonaNames(corpusData)
	if len(personaNames) == 0 {
		log.Println("[bot] WARNING: No persona tags found in corpus — bot will use global responses only")
	} else {
		log.Printf("[bot] Loaded %d personas from corpus", len(personaNames))
	}

	botConfigured = true
	log.Printf("[bot] Bot service initialized (retrieval engine, %d personas)", len(personaNames))
	go botRedisListener()
}

// extractPersonaNames parses the corpus TSV and extracts unique persona tags,
// capitalizing the first letter for use as display names.
// Persona entries look like: [persona_name]trigger\tresponse
func extractPersonaNames(corpus string) []string {
	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(strings.NewReader(corpus))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		// Check for [persona] prefix
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end > 1 {
				name := capitalizeFirst(line[1:end])
				seen[name] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	return names
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

	// Stop any existing timer BEFORE creating a new one (prevents double-match race)
	if old, loaded := timers.LoadAndDelete(userID); loaded {
		old.(*time.Timer).Stop()
	}

	timer := time.AfterFunc(config.BotMatchDelay, func() {
		timers.Delete(userID)
		matchWithBot(userID)
	})
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

// PickRandomName returns a random persona name from the corpus.
// Used by the human match handler so both bot and human matches
// send identical-looking display names (anti-detection).
func PickRandomName() string {
	if len(personaNames) == 0 {
		return config.DefaultStrangerName
	}
	return personaNames[rand.IntN(len(personaNames))]
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

	// Pick a random persona
	persona := pickPersona()

	// Create a per-session bot.Session with isolated conversation state
	sess := botClient.NewSession(bot.SessionConfig{
		Persona:     strings.ToLower(persona),
		HistorySize: 200,
	})

	// Generate bot user ID (UUID v5 — deterministic from persona + timestamp, indistinguishable from real UUIDs)
	botUserID := uuid.NewSHA1(botNamespace, []byte(persona+time.Now().String())).String()
	botName := persona

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
		session:    sess,
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

	// Send an initial greeting after a short delay (simulates bot typing).
	// Uses its own context because matchWithBot returns (and cancels ctx) immediately.
	go func() {
		select {
		case <-session.done:
			return
		case <-time.After(randomDelay(1500, 3000)):
		}

		greetCtx, greetCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer greetCancel()

		sendBotTypingStart(greetCtx, session)

		select {
		case <-session.done:
			return
		case <-time.After(randomDelay(800, 2000)):
		}

		// Use Initiate() for persona-aware openers, fallback to Chat("hello")
		reply, err := session.session.Initiate(greetCtx)
		if err != nil {
			reply, err = session.session.Chat(greetCtx, "hello")
			if err != nil {
				log.Printf("[bot] Failed to generate greeting for room %s: %v", session.RoomID, err)
				sendBotTypingEnd(greetCtx, session)
				return
			}
		}
		sendBotTypingEnd(greetCtx, session)
		sendBotMessage(greetCtx, session, reply.Text)
	}()

	// Start the session watchdog (max duration + inactivity)
	go sessionWatchdog(session)

	// Start the reply worker (single goroutine processes all replies for this session)
	go replyWorker(session)
}

// pickPersona returns a random persona name from the corpus.
// If no personas are defined, returns "Stranger".
func pickPersona() string {
	if len(personaNames) == 0 {
		return config.DefaultStrangerName
	}
	return personaNames[rand.IntN(len(personaNames))]
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

// sendBotDeliveryReceipt publishes a message_delivered event from the bot.
func sendBotDeliveryReceipt(ctx context.Context, session *BotSession) {
	msg := map[string]interface{}{
		config.FieldType:        config.MsgTypeMessageDelivered,
		config.FieldRoomID:      session.RoomID,
		config.FieldUserID:      session.BotUserID,
		config.FieldDeliveredAt: time.Now().UTC().Format(time.RFC3339),
	}
	msgBytes, _ := json.Marshal(msg)
	redis.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, msgBytes)
}

// sendBotReadReceipt publishes a message_read event from the bot.
func sendBotReadReceipt(ctx context.Context, session *BotSession) {
	msg := map[string]interface{}{
		config.FieldType:   config.MsgTypeMessageRead,
		config.FieldRoomID: session.RoomID,
		config.FieldUserID: session.BotUserID,
		config.FieldReadAt: time.Now().UTC().Format(time.RFC3339),
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

	// Simulate delivery receipt (200-800ms after receiving message).
	// Uses its own context so it survives if handleBotReply returns early.
	go func() {
		select {
		case <-session.done:
			return
		case <-time.After(randomDelay(200, 800)):
			rcptCtx, rcptCancel := context.WithTimeout(context.Background(), 5*time.Second)
			sendBotDeliveryReceipt(rcptCtx, session)
			rcptCancel()
		}
	}()

	// Read delay — proportional to message length (simulates reading)
	readMs := 300 + len(userText)*30
	if readMs > 3000 {
		readMs = 3000
	}
	readDelay := addJitter(readMs, 0.2)
	select {
	case <-session.done:
		return
	case <-time.After(readDelay):
	}

	// Send read receipt after "reading" the message
	sendBotReadReceipt(ctx, session)

	// 5% chance to skip typing indicator entirely (humans sometimes do this)
	skipTyping := rand.IntN(100) < 5

	// Send typing indicator
	if !skipTyping {
		sendBotTypingStart(ctx, session)
	}

	// Generate reply using retrieval engine
	reply, err := session.session.Chat(ctx, userText)
	if err != nil {
		log.Printf("[bot] Failed to generate reply for room %s: %v", session.RoomID, err)
		if !skipTyping {
			sendBotTypingEnd(ctx, session)
		}
		return
	}

	// Typing delay — proportional to reply length (simulates typing)
	typeMs := 200 + len(reply.Text)*40
	if typeMs > 5000 {
		typeMs = 5000
	}
	typingDelay := addJitter(typeMs, 0.2)
	select {
	case <-session.done:
		return
	case <-time.After(typingDelay):
	}

	// Send the message
	if !skipTyping {
		sendBotTypingEnd(ctx, session)
	}
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
	// Close the SDK session (releases per-session state, not the shared client)
	session.session.Close()
	botUserIDs.Delete(session.BotUserID)
	log.Printf("[bot] Stopped bot session for room %s", roomID)
}

// StopAllSessions stops all active bot sessions and closes the bot client.
// Call this during graceful shutdown.
func StopAllSessions() {
	botDoneOnce.Do(func() {
		if botDone != nil {
			close(botDone)
		}
	})

	// Stop all active sessions
	sessions.Range(func(key, value any) bool {
		session := value.(*BotSession)
		session.once.Do(func() {
			close(session.done)
		})
		session.session.Close()
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

	// Close the global bot client
	if botClient != nil {
		botClient.Close()
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

// addJitter adds a ±jitterFraction random variance to the given millisecond value.
func addJitter(ms int, jitterFraction float64) time.Duration {
	jitter := int(float64(ms) * jitterFraction)
	if jitter > 0 {
		ms = ms - jitter + rand.IntN(2*jitter+1)
	}
	if ms < 0 {
		ms = 0
	}
	return time.Duration(ms) * time.Millisecond
}

// capitalizeFirst returns the string with the first letter uppercased.
// Used to convert corpus tags ("aarav") to display names ("Aarav").
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
