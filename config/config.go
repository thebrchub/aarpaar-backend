package config

import (
	"log"
	"os"
	"time"

	"github.com/shivanand-burli/go-starter-kit/helper"
)

// ---------------------------------------------------------------------------
// Runtime Variables (set once during Init, then read-only)
// ---------------------------------------------------------------------------

var (
	GoogleClientID string        // Google OAuth client ID for token verification
	ServerPort     string        // HTTP server listen port (e.g. ":8080")
	PostgresConn   string        // Postgres connection string
	PGTimeout      int           // Postgres query timeout in seconds
	RedisHost      string        // Redis hostname
	RedisPort      int           // Redis port number
	RedisCacheName string        // Prefix for all Redis cache keys
	CORSOrigin     string        // Allowed CORS origin (e.g. "https://yourdomain.com")
	RedisOpTimeout time.Duration // Timeout for individual Redis operations

	// LiveKit Cloud configuration (for group video calls)
	LiveKitURL       string // LiveKit Cloud WebSocket URL (e.g. "wss://your-app.livekit.cloud")
	LiveKitAPIKey    string // LiveKit API key
	LiveKitAPISecret string // LiveKit API secret (HMAC signing)

	// TURN server configuration (for P2P NAT traversal fallback)
	TURNURL      string // TURN server URL (e.g. "turn:relay.metered.ca:443?transport=tcp")
	TURNUsername string // TURN static username or API key
	TURNPassword string // TURN static credential

	// Secondary TURN over TCP (for restrictive firewalls)
	TURNURL2      string // Second TURN URL (e.g. "turns:relay.metered.ca:443?transport=tcp")
	TURNUsername2 string
	TURNPassword2 string

	// Bot matchmaking fallback (optional — disabled if BOT_ENABLED is not "true")
	BotEnabled            bool          // Whether bot matching is enabled
	BotCorpusData         string        // Raw corpus TSV content loaded from BOT_CORPUS_PATH or BOT_CORPUS_DATA env
	BotMatchDelay         time.Duration // Delay before matching user with a bot (default 5s)
	BotSessionMaxDuration time.Duration // Hard cap on how long a bot session can last (default 1m)
	BotInactivityTimeout  time.Duration // End session if user doesn't reply within this window (default 30s)

	// Moderation (BENKI_ADMIN)
	BenkiAdminEmail string // Email of the super admin (BENKI_ADMIN) for moderation access

	// Domain (for vanity links)
	Domain string // Public domain for vanity URLs (e.g. "zquab.com")
)

// ---------------------------------------------------------------------------
// Context Keys (typed to avoid collisions with other packages)
// ---------------------------------------------------------------------------

// contextKey is an unexported type used for context value keys.
type contextKey string

// UserIDKey is the key used to store the authenticated user's ID in
// the request context. Set by the auth middleware, read by handlers.
const UserIDKey contextKey = "user_id"

// ---------------------------------------------------------------------------
// Redis Key Constants
// ---------------------------------------------------------------------------

const (
	STRANGER_PREFIX = "stranger_" // Prefix for anonymous stranger room IDs

	MATCH_LOCATION_COLON = "match:location:" // match:location:{userId} -> JSON location data (5-min TTL)

	CHAT_GLOBAL_CHANNEL    = "chat:global"            // Global Pub/Sub channel for cross-server message broadcasting
	CHAT_BUFFER_COLON      = "chat:buffer:"           // chat:buffer:{room_id} -> List of messages to be saved to Postgres
	CHAT_PROCESSING_COLON  = "chat:processing:"       // chat:processing:{room_id} -> Temporary key for the flusher to process messages
	CHAT_DIRTY_TARGETS     = "chat:dirty_targets"     // Set of room_ids that have pending messages in the buffer
	CHAT_CLOSED_COLON      = "chat:closed:"           // chat:closed:{room_id} -> Marker that a stranger room is closed
	CHAT_READ_RECEIPTS     = "chat:read_receipts"     // Hash: {room_id}:{user_id} -> RFC3339 timestamp (batched to Postgres)
	CHAT_DELIVERY_RECEIPTS = "chat:delivery_receipts" // Hash: {room_id}:{user_id} -> RFC3339 timestamp (batched to Postgres)
	FRIEND_REQUEST_COLON   = "friend_req:"            // friend_req:{room_id}:{user_id} -> One-sided friend request marker
	STRANGER_MEMBERS_COLON = "stranger_members:"      // stranger_members:{room_id} -> Set of both user IDs in a stranger room
	CALL_ACTIVE_COLON      = "call:active:"           // call:active:{user_id} -> JSON call state (tracks active calls per user)
	GROUP_CALL_COLON       = "group_call:"            // group_call:{roomId} -> hash with callId, initiatedBy, startedAt, participants
	BOT_SESSIONS_COLON     = "bot:session:"           // bot:session:{room_id} -> Marker for active bot chat sessions
)

// ---------------------------------------------------------------------------
// Init loads all required environment variables.
// Call this once at application startup before anything else.
// ---------------------------------------------------------------------------
func Init() {
	// Google Auth
	GoogleClientID = helper.GetEnv("GOOGLE_CLIENT_ID", "")
	if GoogleClientID == "" {
		panic("GOOGLE_CLIENT_ID is required")
	}

	// Server
	ServerPort = helper.GetEnv("SERVER_PORT", ":8080")

	// CORS
	CORSOrigin = helper.GetEnv("CORS_ORIGIN", "*")

	// Redis operation timeout (for non-Pub/Sub calls)
	RedisOpTimeout = 5 * time.Second

	// Postgres
	PostgresConn = helper.GetEnv("POSTGRES_CONN_STR", "")
	if PostgresConn == "" {
		panic("POSTGRES_CONN_STR is required")
	}
	PGTimeout = helper.GetEnvInt("PG_TIMEOUT", 5)

	// Redis
	RedisHost = helper.GetEnv("REDIS_HOST", "localhost")
	RedisPort = helper.GetEnvInt("REDIS_PORT", 6379)
	RedisCacheName = helper.GetEnv("REDIS_CACHE_NAME", "aarpaar")

	// LiveKit Cloud (optional — group calls disabled if not set)
	LiveKitURL = helper.GetEnv("LIVEKIT_URL", "")
	LiveKitAPIKey = helper.GetEnv("LIVEKIT_API_KEY", "")
	LiveKitAPISecret = helper.GetEnv("LIVEKIT_API_SECRET", "")

	// TURN (optional — P2P calls fall back to STUN-only if not set)
	TURNURL = helper.GetEnv("TURN_URL", "")
	TURNUsername = helper.GetEnv("TURN_USERNAME", "")
	TURNPassword = helper.GetEnv("TURN_PASSWORD", "")

	// Secondary TURN (optional — for TCP/TLS fallback behind strict firewalls)
	TURNURL2 = helper.GetEnv("TURN_URL_2", "")
	TURNUsername2 = helper.GetEnv("TURN_USERNAME_2", "")
	TURNPassword2 = helper.GetEnv("TURN_PASSWORD_2", "")

	// Bot matchmaking fallback (optional — disabled unless BOT_ENABLED=true)
	BotEnabled = helper.GetEnv("BOT_ENABLED", "true") == "true"

	// Corpus: prefer BOT_CORPUS_DATA (inline string) over BOT_CORPUS_PATH (file path).
	// This lets you embed the entire TSV in an env var (e.g. Docker secrets, K8s ConfigMap)
	// without shipping a separate file.
	BotCorpusData = helper.GetEnv("BOT_CORPUS_DATA", "")
	if BotCorpusData == "" {
		corpusPath := helper.GetEnv("BOT_CORPUS_PATH", "./corpus/chat.tsv")
		raw, err := os.ReadFile(corpusPath)
		if err != nil {
			log.Printf("[config] Failed to read corpus file %s: %v", corpusPath, err)
		} else {
			BotCorpusData = string(raw)
		}
	}
	botDelaySec := helper.GetEnvInt("BOT_MATCH_DELAY_SECONDS", 5)
	BotMatchDelay = time.Duration(botDelaySec) * time.Second
	botMaxDurSec := helper.GetEnvInt("BOT_SESSION_MAX_DURATION_SECONDS", 60)
	BotSessionMaxDuration = time.Duration(botMaxDurSec) * time.Second
	botInactSec := helper.GetEnvInt("BOT_INACTIVITY_TIMEOUT_SECONDS", 60)
	BotInactivityTimeout = time.Duration(botInactSec) * time.Second

	// Moderation
	BenkiAdminEmail = helper.GetEnv("BENKI_ADMIN_EMAIL", "")

	// Domain
	Domain = helper.GetEnv("DOMAIN", "")
}
