// Package testutil provides shared helpers for integration and WebSocket tests.
package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/shivanand-burli/go-starter-kit/jwt"
	"github.com/shivanand-burli/go-starter-kit/middleware"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/shivanand-burli/go-starter-kit/rtc"

	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/handlers"
	mw "github.com/thebrchub/aarpaar/middleware"
	"github.com/thebrchub/aarpaar/services"
)

var (
	setupOnce sync.Once
	testDB    *sql.DB
)

// SetTestEnv sets environment variables for tests.
func SetTestEnv() {
	envVars := map[string]string{
		"GOOGLE_CLIENT_ID":   "test-client-id",
		"SERVER_PORT":        "2029",
		"POSTGRES_CONN_STR":  getEnvOrDefault("TEST_POSTGRES_CONN_STR", "postgresql://postgres:root@localhost:5432/aarpaar_test?sslmode=disable"),
		"PG_TIMEOUT":         "5",
		"REDIS_HOST":         getEnvOrDefault("TEST_REDIS_HOST", "localhost"),
		"REDIS_PORT":         getEnvOrDefault("TEST_REDIS_PORT", "6378"),
		"REDIS_CACHE_NAME":   "aarpaar_test",
		"CORS_ORIGIN":        "*",
		"BOT_ENABLED":        "false",
		"BENKI_ADMIN_EMAIL":  "admin@test.com",
		"JWT_ISSUER":         "test-issuer",
		"ACCESS_TOKEN_TTL":   "15m",
		"REFRESH_TOKEN_TTL":  "168h",
		"JWT_PRIVATE_KEY":    TestJWTPrivateKey,
		"JWT_PUBLIC_KEY":     TestJWTPublicKey,
		"REFRESH_SECRET":     "test-secret-key-minimum-32-bytes!!",
		"LIVEKIT_URL":        "",
		"LIVEKIT_API_KEY":    "",
		"LIVEKIT_API_SECRET": "",
	}
	for k, v := range envVars {
		os.Setenv(k, v)
	}
}

func getEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// SetupTestInfra initialises Postgres, Redis, JWT, config once.
// Returns a cleanup function.
func SetupTestInfra(t *testing.T) {
	t.Helper()
	setupOnce.Do(func() {
		SetTestEnv()

		// Load config from env
		config.Init()

		// Init JWT
		if err := jwt.Init(); err != nil {
			log.Fatalf("[test] JWT init failed: %v", err)
		}

		// Postgres
		if err := postgress.Init(config.PostgresConn, config.PGTimeout); err != nil {
			log.Fatalf("[test] Postgres init failed: %v", err)
		}
		testDB = postgress.GetRawDB()

		// Migrations
		services.RunMigrations()

		// Redis
		if err := redis.InitCache(config.RedisCacheName, config.RedisHost, config.RedisPort); err != nil {
			log.Fatalf("[test] Redis init failed: %v", err)
		}

		// RTC optional stub
		handlers.RTC = rtc.NewClientOptional(rtc.Config{})
		chat.RTC = handlers.RTC
	})
}

// BuildTestMux builds the same HTTP mux as main.go (without starting a server).
func BuildTestMux(engine *chat.Engine) http.Handler {
	limiter := middleware.NewIPRateLimiter(10, 20)
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Auth (public)
	mux.HandleFunc("POST /api/v1/auth/google", handlers.GoogleLoginHandler)
	mux.HandleFunc("POST /api/v1/auth/refresh", handlers.RefreshTokenHandler)

	// Auth (protected)
	mux.HandleFunc("POST /api/v1/auth/device", mw.Auth(handlers.RegisterDeviceHandler))

	// Users
	mux.HandleFunc("GET /api/v1/users/me", mw.Auth(handlers.GetMeHandler))
	mux.HandleFunc("PATCH /api/v1/users/me", mw.Auth(handlers.UpdateMeHandler))
	mux.HandleFunc("PUT /api/v1/users/me", mw.Auth(handlers.PutMeHandler))
	mux.HandleFunc("GET /api/v1/users/search", mw.Auth(handlers.SearchUsersHandler))
	mux.HandleFunc("GET /api/v1/users/check-username", mw.Auth(handlers.CheckUsernameHandler))

	// Rooms
	mux.HandleFunc("GET /api/v1/rooms", mw.Auth(handlers.GetRoomsHandler))
	mux.HandleFunc("POST /api/v1/rooms", mw.Auth(handlers.CreateDMHandler))
	mux.HandleFunc("GET /api/v1/rooms/requests", mw.Auth(handlers.GetDMRequestsHandler))
	mux.HandleFunc("GET /api/v1/rooms/{roomId}/messages", mw.Auth(handlers.GetRoomMessagesHandler))
	mux.HandleFunc("POST /api/v1/rooms/{roomId}/accept", mw.Auth(handlers.AcceptDMRequestHandler))
	mux.HandleFunc("POST /api/v1/rooms/{roomId}/reject", mw.Auth(handlers.RejectDMRequestHandler))

	// Friends
	mux.HandleFunc("GET /api/v1/friends", mw.Auth(handlers.GetFriendsHandler))
	mux.HandleFunc("GET /api/v1/friends/requests", mw.Auth(handlers.GetFriendRequestsHandler))
	mux.HandleFunc("POST /api/v1/friends/request", mw.Auth(handlers.SendFriendRequestHandler))
	mux.HandleFunc("POST /api/v1/friends/accept", mw.Auth(handlers.AcceptFriendRequestHandler))
	mux.HandleFunc("POST /api/v1/friends/reject", mw.Auth(handlers.RejectFriendRequestHandler))
	mux.HandleFunc("DELETE /api/v1/friends/{username}", mw.Auth(handlers.RemoveFriendHandler))

	// Matchmaking
	mux.HandleFunc("POST /api/v1/match/enter", mw.Auth(handlers.EnterMatchQueueHandler))
	mux.HandleFunc("POST /api/v1/match/leave", mw.Auth(handlers.LeaveMatchQueueHandler))
	mux.HandleFunc("POST /api/v1/match/action", mw.Auth(handlers.MatchActionHandler))
	mux.HandleFunc("POST /api/v1/match/report", mw.Auth(handlers.ReportUserHandler))

	// Calls
	mux.HandleFunc("GET /api/v1/calls/config", mw.Auth(handlers.GetCallConfigHandler))
	mux.HandleFunc("GET /api/v1/calls/history", mw.Auth(handlers.GetCallHistoryHandler))

	// Groups
	mux.HandleFunc("GET /api/v1/groups", mw.Auth(handlers.ListGroupsHandler))
	mux.HandleFunc("POST /api/v1/groups", mw.Auth(handlers.CreateGroupHandler))
	mux.HandleFunc("GET /api/v1/groups/{groupId}", mw.Auth(handlers.GetGroupHandler))
	mux.HandleFunc("PATCH /api/v1/groups/{groupId}", mw.Auth(handlers.UpdateGroupHandler))
	mux.HandleFunc("DELETE /api/v1/groups/{groupId}", mw.Auth(handlers.DeleteGroupHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/join", mw.Auth(handlers.JoinGroupHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/members", mw.Auth(handlers.AddGroupMembersHandler))
	mux.HandleFunc("DELETE /api/v1/groups/{groupId}/members/{userId}", mw.Auth(handlers.RemoveGroupMemberHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/admins", mw.Auth(handlers.PromoteAdminHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/invite", mw.Auth(handlers.GenerateInviteHandler))
	mux.HandleFunc("POST /api/v1/invite/{inviteCode}", mw.Auth(handlers.JoinGroupByInviteHandler))

	// Group Calls
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls", mw.Auth(handlers.StartGroupCallHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/join", mw.Auth(handlers.JoinGroupCallHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/leave", mw.Auth(handlers.LeaveGroupCallHandler))
	mux.HandleFunc("GET /api/v1/groups/{groupId}/calls/{callId}", mw.Auth(handlers.GetGroupCallStatusHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/mute", mw.Auth(handlers.MuteParticipantHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/kick", mw.Auth(handlers.KickParticipantHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/admins", mw.Auth(handlers.PromoteCallAdminHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/end", mw.Auth(handlers.ForceEndCallHandler))

	// Vanity
	mux.HandleFunc("PATCH /api/v1/groups/{groupId}/vanity", mw.Auth(handlers.SetVanitySlugHandler))
	mux.HandleFunc("POST /api/v1/vanity/{slug}", mw.Auth(handlers.JoinGroupByVanityHandler))

	// Donations
	mux.HandleFunc("POST /api/v1/donate/create-order", mw.Auth(handlers.CreateDonationOrderHandler))
	mux.HandleFunc("GET /api/v1/donate/status/{orderId}", mw.Auth(handlers.GetDonationStatusHandler))
	mux.HandleFunc("GET /api/v1/donate/history", mw.Auth(handlers.GetDonationHistoryHandler))
	mux.HandleFunc("GET /api/v1/badges/tiers", mw.Auth(handlers.GetBadgeTiersHandler))

	// Razorpay Webhook
	mux.HandleFunc("POST /api/v1/webhook/razorpay", handlers.RazorpayWebhookHandler)

	// Leaderboard
	mux.HandleFunc("GET /api/v1/leaderboard", mw.Auth(handlers.GetLeaderboardHandler))

	// Online count
	mux.HandleFunc("GET /api/v1/online", handlers.GetOnlineCountHandler)

	// Admin
	mux.HandleFunc("GET /api/v1/admin/stats", mw.Auth(mw.BenkiAdminOnly(handlers.GetAdminStatsHandler)))
	mux.HandleFunc("GET /api/v1/admin/users", mw.Auth(mw.BenkiAdminOnly(handlers.GetAdminUsersHandler)))
	mux.HandleFunc("POST /api/v1/admin/users/{userId}/ban", mw.Auth(mw.BenkiAdminOnly(handlers.BanUserHandler)))
	mux.HandleFunc("POST /api/v1/admin/users/{userId}/unban", mw.Auth(mw.BenkiAdminOnly(handlers.UnbanUserHandler)))
	mux.HandleFunc("GET /api/v1/admin/reports", mw.Auth(mw.BenkiAdminOnly(handlers.GetAdminReportsHandler)))
	mux.HandleFunc("GET /api/v1/admin/users/{userId}/reports", mw.Auth(mw.BenkiAdminOnly(handlers.GetAdminUserReportsHandler)))
	mux.HandleFunc("POST /api/v1/admin/badges", mw.Auth(mw.BenkiAdminOnly(handlers.CreateBadgeTierHandler)))
	mux.HandleFunc("PATCH /api/v1/admin/badges/{badgeId}", mw.Auth(mw.BenkiAdminOnly(handlers.UpdateBadgeTierHandler)))
	mux.HandleFunc("DELETE /api/v1/admin/badges/{badgeId}", mw.Auth(mw.BenkiAdminOnly(handlers.DeleteBadgeTierHandler)))
	mux.HandleFunc("GET /api/v1/admin/settings/{key}", mw.Auth(mw.BenkiAdminOnly(handlers.GetAppSettingHandler)))
	mux.HandleFunc("PATCH /api/v1/admin/settings/{key}", mw.Auth(mw.BenkiAdminOnly(handlers.UpdateAppSettingHandler)))

	// WebSocket
	mux.HandleFunc("GET /ws", mw.Auth(func(w http.ResponseWriter, r *http.Request) {
		userID := r.Context().Value(config.UserIDKey).(string)
		chat.ServeWs(engine, w, r, userID)
	}))

	handler := mw.CORS(mw.BodyLimit(limiter.LimitHandler(mux)))
	return handler
}

// StartTestServer spins up a full httptest.Server with the real handler stack.
// Returns the server and a cleanup function.
func StartTestServer(t *testing.T) (*httptest.Server, *chat.Engine, func()) {
	t.Helper()
	SetupTestInfra(t)

	engine := chat.NewEngine()
	handler := BuildTestMux(engine)
	srv := httptest.NewServer(handler)

	cleanup := func() {
		srv.Close()
		engine.Shutdown()
		CleanAll(t)
	}

	return srv, engine, cleanup
}

// CleanAll truncates all tables and flushes Redis for a fresh test state.
func CleanAll(t *testing.T) {
	t.Helper()
	db := postgress.GetRawDB()
	tables := []string{
		"call_logs", "donations", "messages", "room_members",
		"rooms", "blocked_users", "user_reports", "friend_requests",
		"friendships", "device_tokens", "badge_tiers", "app_settings", "users",
	}
	for _, table := range tables {
		_, err := db.Exec("TRUNCATE " + table + " CASCADE")
		if err != nil {
			// Table might not exist yet, that's fine
			t.Logf("[cleanup] TRUNCATE %s: %v", table, err)
		}
	}

	// Flush Redis
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	redis.GetRawClient().FlushDB(ctx)
}

// SeedUser creates a test user in the database and returns their ID and a Bearer token.
func SeedUser(t *testing.T, name, email string) (userID, token string) {
	t.Helper()
	db := postgress.GetRawDB()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := db.QueryRowContext(ctx,
		`INSERT INTO users (google_id, email, name, username, gender)
		 VALUES ($1, $2, $3, $4, 'Any')
		 RETURNING id::text`,
		"google_"+name, email, name, name,
	).Scan(&userID)
	if err != nil {
		t.Fatalf("[seed] Failed to insert user %s: %v", name, err)
	}

	token = AuthToken(userID)
	return userID, token
}

// SeedUserWithUsername creates a test user with a specific username and returns their ID and token.
func SeedUserWithUsername(t *testing.T, name, email, username string) (userID, token string) {
	t.Helper()
	db := postgress.GetRawDB()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := db.QueryRowContext(ctx,
		`INSERT INTO users (google_id, email, name, username, gender)
		 VALUES ($1, $2, $3, $4, 'Any')
		 RETURNING id::text`,
		"google_"+username, email, name, username,
	).Scan(&userID)
	if err != nil {
		t.Fatalf("[seed] Failed to insert user %s: %v", username, err)
	}

	token = AuthToken(userID)
	return userID, token
}

// AuthToken generates a JWT access token for the given userID.
func AuthToken(userID string) string {
	accessToken, _, err := jwt.GenerateToken(userID, nil)
	if err != nil {
		panic(fmt.Sprintf("failed to generate test token: %v", err))
	}
	return "Bearer " + accessToken
}

// SeedFriendship creates a mutual friendship between two users + a DM room.
func SeedFriendship(t *testing.T, userID1, userID2 string) string {
	t.Helper()
	db := postgress.GetRawDB()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Ensure user_id_1 < user_id_2 for the friendship table constraint
	u1, u2 := userID1, userID2
	if u1 > u2 {
		u1, u2 = u2, u1
	}

	_, err := db.ExecContext(ctx,
		`INSERT INTO friendships (user_id_1, user_id_2) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		u1, u2)
	if err != nil {
		t.Fatalf("[seed] Failed to create friendship: %v", err)
	}

	// Create DM room
	var roomID string
	err = db.QueryRowContext(ctx,
		`INSERT INTO rooms (type) VALUES ('DM') RETURNING id::text`,
	).Scan(&roomID)
	if err != nil {
		t.Fatalf("[seed] Failed to create room: %v", err)
	}

	_, err = db.ExecContext(ctx,
		`INSERT INTO room_members (room_id, user_id, status) VALUES ($1, $2, 'active'), ($1, $3, 'active')`,
		roomID, userID1, userID2)
	if err != nil {
		t.Fatalf("[seed] Failed to add room members: %v", err)
	}

	return roomID
}

// SeedMessage inserts a message into a room.
func SeedMessage(t *testing.T, roomID, senderID, content string) int64 {
	t.Helper()
	db := postgress.GetRawDB()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var msgID int64
	err := db.QueryRowContext(ctx,
		`INSERT INTO messages (room_id, sender_id, content) VALUES ($1, $2, $3) RETURNING id`,
		roomID, senderID, content,
	).Scan(&msgID)
	if err != nil {
		t.Fatalf("[seed] Failed to insert message: %v", err)
	}
	return msgID
}

// SeedBan bans a user.
func SeedBan(t *testing.T, userID string) {
	t.Helper()
	db := postgress.GetRawDB()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := db.ExecContext(ctx, `UPDATE users SET is_banned = true WHERE id = $1`, userID)
	if err != nil {
		t.Fatalf("[seed] Failed to ban user: %v", err)
	}

	// Also set in Redis cache
	redis.GetRawClient().Set(ctx, "ban:"+userID, "1", 24*time.Hour)
}

// SeedBlock creates a block relationship.
func SeedBlock(t *testing.T, blockerID, blockedID string) {
	t.Helper()
	db := postgress.GetRawDB()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := db.ExecContext(ctx,
		`INSERT INTO blocked_users (blocker_id, blocked_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		blockerID, blockedID)
	if err != nil {
		t.Fatalf("[seed] Failed to create block: %v", err)
	}
}
