package config

import (
	"log"
	"os"
	"strings"
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

	// Razorpay / Payment configuration
	RazorpayKeyID         string // Razorpay API key ID (for frontend checkout)
	RazorpayKeySecret     string // Razorpay API key secret
	RazorpayWebhookSecret string // Razorpay webhook signature secret
	PaymentProviderName   string // "razorpay" or "stub" (default "stub")

	// Firebase Cloud Messaging (optional — push notifications disabled if not set)
	FirebaseCredentials string // Raw JSON or base64-encoded Firebase service account key

	// Internal API key for service-to-service auth (e.g. JWT validation endpoint)
	InternalAPIKey    []byte
	InternalAPIKeySet bool

	// Group Calls (disabled by default — enable via GROUP_CALLS_ENABLED=true)
	GroupCallsEnabled bool // Whether group call features are enabled

	// Rate Limiting
	RateLimitRate  int // Requests per second per IP (default 5)
	RateLimitBurst int // Burst size (default 10)

	// ICE Servers (built once at init from STUN_URLS + TURN config)
	ICEServers []ICEServer

	// ---------------------------------------------------------------------------
	// Storage (Cloudflare R2 / S3-compatible)
	// ---------------------------------------------------------------------------
	StorageEndpoint  string // S3/R2 endpoint URL
	StorageAccessKey string // Access key ID
	StorageSecretKey string // Secret access key
	StorageBucket    string // Bucket name
	StorageRegion    string // Region (default "auto" for R2)
	StoragePublicURL string // Public CDN URL (empty for private buckets)

	// Arena media upload defaults (env overrides; admin can change at runtime via app_settings)
	ArenaMaxImageSizeKB int // ARENA_MAX_IMAGE_SIZE_KB (default 100)
	ArenaMaxVideoSizeKB int // ARENA_MAX_VIDEO_SIZE_KB (default 500)
)

// ICEServer matches the WebRTC RTCIceServer interface.
type ICEServer struct {
	URLs       any    `json:"urls"` // string or []string
	Username   string `json:"username,omitempty"`
	Credential string `json:"credential,omitempty"`
}

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
	BotEnabled = helper.GetEnv("BOT_ENABLED", "false") == "true"

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

	// Payment provider
	PaymentProviderName = helper.GetEnv("PAYMENT_PROVIDER", "stub")
	RazorpayKeyID = helper.GetEnv("RAZORPAY_KEY_ID", "")
	RazorpayKeySecret = helper.GetEnv("RAZORPAY_KEY_SECRET", "")
	RazorpayWebhookSecret = helper.GetEnv("RAZORPAY_WEBHOOK_SECRET", "")

	// Firebase Cloud Messaging (optional)
	FirebaseCredentials = helper.GetEnv("FIREBASE_CREDENTIALS", "")

	// Internal API key for service-to-service auth
	InternalAPIKey = []byte(helper.GetEnv("INTERNAL_API_KEY", ""))
	InternalAPIKeySet = len(InternalAPIKey) > 0

	// Group Calls (disabled by default — requires explicit opt-in)
	GroupCallsEnabled = helper.GetEnv("GROUP_CALLS_ENABLED", "false") == "true"

	// Rate Limiting (same defaults for HTTP and WebSocket)
	RateLimitRate = helper.GetEnvInt("RATE_LIMIT_RATE", 5)
	RateLimitBurst = helper.GetEnvInt("RATE_LIMIT_BURST", 10)

	// ICE Servers — built once from STUN_URLS (comma-separated) + TURN config
	stunURLs := helper.GetEnv("STUN_URLS", "stun:stun.l.google.com:19302,stun:stun.cloudflare.com:3478")
	for u := range strings.SplitSeq(stunURLs, ",") {
		u = strings.TrimSpace(u)
		if u != "" {
			ICEServers = append(ICEServers, ICEServer{URLs: u})
		}
	}
	if TURNURL != "" {
		ICEServers = append(ICEServers, ICEServer{
			URLs:       TURNURL,
			Username:   TURNUsername,
			Credential: TURNPassword,
		})
	}
	if TURNURL2 != "" {
		ICEServers = append(ICEServers, ICEServer{
			URLs:       TURNURL2,
			Username:   TURNUsername2,
			Credential: TURNPassword2,
		})
	}

	// Storage (Cloudflare R2 / S3-compatible — optional, Arena disabled if not set)
	StorageEndpoint = helper.GetEnv("STORAGE_ENDPOINT", "")
	StorageAccessKey = helper.GetEnv("STORAGE_ACCESS_KEY_ID", "")
	StorageSecretKey = helper.GetEnv("STORAGE_SECRET_ACCESS_KEY", "")
	StorageBucket = helper.GetEnv("STORAGE_BUCKET", "")
	StorageRegion = helper.GetEnv("STORAGE_REGION", "auto")
	StoragePublicURL = helper.GetEnv("STORAGE_PUBLIC_URL", "")

	// Arena media defaults (env override; runtime override via app_settings.arena_limits)
	ArenaMaxImageSizeKB = helper.GetEnvInt("ARENA_MAX_IMAGE_SIZE_KB", 100)
	ArenaMaxVideoSizeKB = helper.GetEnvInt("ARENA_MAX_VIDEO_SIZE_KB", 500)
}
