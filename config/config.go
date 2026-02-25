package config

import (
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
}
