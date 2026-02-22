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

	CHAT_GLOBAL_CHANNEL    = "chat:global"        // Global Pub/Sub channel for cross-server message broadcasting
	CHAT_BUFFER_COLON      = "chat:buffer:"       // chat:buffer:{room_id} -> List of messages to be saved to Postgres
	CHAT_PROCESSING_COLON  = "chat:processing:"   // chat:processing:{room_id} -> Temporary key for the flusher to process messages
	CHAT_DIRTY_TARGETS     = "chat:dirty_targets" // Set of room_ids that have pending messages in the buffer
	CHAT_CLOSED_COLON      = "chat:closed:"       // chat:closed:{room_id} -> Marker that a stranger room is closed
	FRIEND_REQUEST_COLON   = "friend_req:"        // friend_req:{room_id}:{user_id} -> One-sided friend request marker
	STRANGER_MEMBERS_COLON = "stranger_members:"  // stranger_members:{room_id} -> Set of both user IDs in a stranger room
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
}
