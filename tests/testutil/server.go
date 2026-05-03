// Package testutil provides shared helpers for integration and WebSocket tests.
package testutil

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shivanand-burli/go-starter-kit/jwt"
	kitMW "github.com/shivanand-burli/go-starter-kit/middleware"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/shivanand-burli/go-starter-kit/rtc"

	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/handlers"
	mw "github.com/thebrchub/aarpaar/middleware"
	"github.com/thebrchub/aarpaar/services"
)

// TestJWTPrivateKey and TestJWTPublicKey are Ed25519 keys for testing.
// Generate with: go run tests/keygen/main.go
const (
	TestJWTPrivateKey = "MC4CAQAwBQYDK2VwBCIEIHLsXh0Yp5Lkaq8E3MFp1RL7xHxHqbS3KhZ7KiL3XoV"
	TestJWTPublicKey  = "MCowBQYDK2VwAyEAuDG7hJnQ3vUqFxVaKqBYzPBxYrL4bBKjfsGzLHB6JEY="
)

var (
	setupOnce sync.Once
	testDB    *pgxpool.Pool
)

// SetTestEnv sets environment variables for tests.
func SetTestEnv() {
	envVars := map[string]string{
		"GOOGLE_CLIENT_ID":    "test-client-id",
		"SERVER_PORT":         "2029",
		"POSTGRES_CONN_STR":   getEnvOrDefault("TEST_POSTGRES_CONN_STR", "postgresql://postgres:root@localhost:5432/aarpaar_test?sslmode=disable"),
		"PG_TIMEOUT":          "5",
		"REDIS_HOST":          getEnvOrDefault("TEST_REDIS_HOST", "localhost"),
		"REDIS_PORT":          getEnvOrDefault("TEST_REDIS_PORT", "6378"),
		"REDIS_CACHE_NAME":    "aarpaar_test",
		"CORS_ORIGIN":         "*",
		"BOT_ENABLED":         "false",
		"BENKI_ADMIN_EMAIL":   "admin@test.com",
		"JWT_ISSUER":          "test-issuer",
		"ACCESS_TOKEN_TTL":    "15m",
		"REFRESH_TOKEN_TTL":   "168h",
		"JWT_PRIVATE_KEY":     TestJWTPrivateKey,
		"JWT_PUBLIC_KEY":      TestJWTPublicKey,
		"REFRESH_SECRET":      "test-secret-key-minimum-32-bytes!!",
		"LIVEKIT_URL":         "",
		"LIVEKIT_API_KEY":     "",
		"LIVEKIT_API_SECRET":  "",
		"GROUP_CALLS_ENABLED": "false",
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
		testDB = postgress.GetPool()

		// Migrations
		services.RunMigrations()

		// Redis
		if err := redis.InitCache(config.RedisCacheName, "", 0); err != nil {
			log.Fatalf("[test] Redis init failed: %v", err)
		}

		// Arena limits (loads defaults since no app_settings row exists in test DB)
		services.RefreshArenaLimits(context.Background())

		// RTC optional stub
		handlers.RTC = rtc.NewClientOptional(rtc.Config{})
		chat.SetRTC(handlers.RTC)
	})
}

// BuildTestMux builds the same HTTP mux as main.go (without starting a server).
func BuildTestMux(engine *chat.Engine) http.Handler {
	globalLimiter := kitMW.NewIPRateLimiter(config.RateLimitRate, config.RateLimitBurst)
	authLimiter := kitMW.NewIPRateLimiter(2, 5)
	healthLimiter := kitMW.NewIPRateLimiter(2, 3)
	mux := http.NewServeMux()

	// Health check (own rate limit)
	mux.HandleFunc("GET /health", healthLimiter.LimitMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))

	// Auth (public — tighter rate limit)
	mux.HandleFunc("POST /api/v1/auth/google", authLimiter.LimitMiddleware(handlers.GoogleLoginHandler))
	mux.HandleFunc("POST /api/v1/auth/refresh", authLimiter.LimitMiddleware(kitMW.HandleRefresh("")))
	mux.HandleFunc("POST /api/v1/auth/logout", authLimiter.LimitMiddleware(kitMW.HandleLogout("")))

	// Auth (protected)
	mux.HandleFunc("POST /api/v1/auth/device", kitMW.Chain(handlers.RegisterDeviceHandler, kitMW.Auth("")))

	// Users
	mux.HandleFunc("GET /api/v1/users/me", kitMW.Chain(handlers.GetMeHandler, kitMW.Auth("")))
	mux.HandleFunc("PATCH /api/v1/users/me", kitMW.Chain(handlers.UpdateMeHandler, kitMW.Auth("")))
	mux.HandleFunc("PUT /api/v1/users/me", kitMW.Chain(handlers.PutMeHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/users/search", kitMW.Chain(handlers.SearchUsersHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/users/check-username", kitMW.Chain(handlers.CheckUsernameHandler, kitMW.Auth("")))

	// Rooms
	mux.HandleFunc("GET /api/v1/rooms", kitMW.Chain(handlers.GetRoomsHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/rooms", kitMW.Chain(handlers.CreateDMHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/rooms/requests", kitMW.Chain(handlers.GetDMRequestsHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/rooms/{roomId}/messages", kitMW.Chain(handlers.GetRoomMessagesHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/rooms/{roomId}/accept", kitMW.Chain(handlers.AcceptDMRequestHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/rooms/{roomId}/reject", kitMW.Chain(handlers.RejectDMRequestHandler, kitMW.Auth("")))

	// Friends
	mux.HandleFunc("GET /api/v1/friends", kitMW.Chain(handlers.GetFriendsHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/friends/requests", kitMW.Chain(handlers.GetFriendRequestsHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/friends/request", kitMW.Chain(handlers.SendFriendRequestHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/friends/accept", kitMW.Chain(handlers.AcceptFriendRequestHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/friends/reject", kitMW.Chain(handlers.RejectFriendRequestHandler, kitMW.Auth("")))
	mux.HandleFunc("DELETE /api/v1/friends/{username}", kitMW.Chain(handlers.RemoveFriendHandler, kitMW.Auth("")))

	// Matchmaking
	mux.HandleFunc("POST /api/v1/match/enter", kitMW.Chain(handlers.EnterMatchQueueHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/match/leave", kitMW.Chain(handlers.LeaveMatchQueueHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/match/action", kitMW.Chain(handlers.MatchActionHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/match/report", kitMW.Chain(handlers.ReportUserHandler, kitMW.Auth("")))

	// Calls
	mux.HandleFunc("GET /api/v1/calls/config", kitMW.Chain(handlers.GetCallConfigHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/calls/history", kitMW.Chain(handlers.GetCallHistoryHandler, kitMW.Auth("")))

	// Groups
	mux.HandleFunc("GET /api/v1/groups", kitMW.Chain(handlers.ListGroupsHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/groups", kitMW.Chain(handlers.CreateGroupHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/groups/{groupId}", kitMW.Chain(handlers.GetGroupHandler, kitMW.Auth("")))
	mux.HandleFunc("PATCH /api/v1/groups/{groupId}", kitMW.Chain(handlers.UpdateGroupHandler, kitMW.Auth("")))
	mux.HandleFunc("DELETE /api/v1/groups/{groupId}", kitMW.Chain(handlers.DeleteGroupHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/join", kitMW.Chain(handlers.JoinGroupHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/members", kitMW.Chain(handlers.AddGroupMembersHandler, kitMW.Auth("")))
	mux.HandleFunc("DELETE /api/v1/groups/{groupId}/members/{userId}", kitMW.Chain(handlers.RemoveGroupMemberHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/admins", kitMW.Chain(handlers.PromoteAdminHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/invite", kitMW.Chain(handlers.GenerateInviteHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/invite/{inviteCode}", kitMW.Chain(handlers.JoinGroupByInviteHandler, kitMW.Auth("")))

	// Group Calls
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls", kitMW.Chain(handlers.StartGroupCallHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/join", kitMW.Chain(handlers.JoinGroupCallHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/leave", kitMW.Chain(handlers.LeaveGroupCallHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/groups/{groupId}/calls/{callId}", kitMW.Chain(handlers.GetGroupCallStatusHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/mute", kitMW.Chain(handlers.MuteParticipantHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/kick", kitMW.Chain(handlers.KickParticipantHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/admins", kitMW.Chain(handlers.PromoteCallAdminHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/end", kitMW.Chain(handlers.ForceEndCallHandler, kitMW.Auth("")))

	// Vanity
	mux.HandleFunc("PATCH /api/v1/groups/{groupId}/vanity", kitMW.Chain(handlers.SetVanitySlugHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/vanity/{slug}", kitMW.Chain(handlers.JoinGroupByVanityHandler, kitMW.Auth("")))

	// Donations
	mux.HandleFunc("POST /api/v1/donate/create-order", kitMW.Chain(handlers.CreateDonationOrderHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/donate/status/{orderId}", kitMW.Chain(handlers.GetDonationStatusHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/donate/history", kitMW.Chain(handlers.GetDonationHistoryHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/badges/tiers", kitMW.Chain(handlers.GetBadgeTiersHandler, kitMW.Auth("")))

	// Razorpay Webhook (bypasses rate limit)
	mux.HandleFunc("POST /api/v1/webhook/razorpay", handlers.RazorpayWebhookHandler)

	// Leaderboard
	mux.HandleFunc("GET /api/v1/leaderboard", kitMW.Chain(handlers.GetLeaderboardHandler, kitMW.Auth("")))

	// Online count (own rate limit)
	mux.HandleFunc("GET /api/v1/online", healthLimiter.LimitMiddleware(handlers.GetOnlineCountHandler))

	// Arena Posts
	mux.HandleFunc("POST /api/v1/arena/posts", kitMW.Chain(handlers.CreatePostHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/arena/posts/{postId}", kitMW.Chain(handlers.GetPostHandler, kitMW.Auth("")))
	mux.HandleFunc("DELETE /api/v1/arena/posts/{postId}", kitMW.Chain(handlers.DeletePostHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/arena/posts/{postId}/repost", kitMW.Chain(handlers.RepostHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/arena/posts/{postId}/pin", kitMW.Chain(handlers.PinPostHandler, kitMW.Auth("")))
	mux.HandleFunc("DELETE /api/v1/arena/posts/{postId}/pin", kitMW.Chain(handlers.PinPostHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/arena/posts/{postId}/report", kitMW.Chain(handlers.ReportPostHandler, kitMW.Auth("")))

	// Arena Likes
	mux.HandleFunc("POST /api/v1/arena/posts/{postId}/like", kitMW.Chain(handlers.LikePostHandler, kitMW.Auth("")))
	mux.HandleFunc("DELETE /api/v1/arena/posts/{postId}/like", kitMW.Chain(handlers.UnlikePostHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/arena/posts/{postId}/likes", kitMW.Chain(handlers.GetPostLikersHandler, kitMW.Auth("")))

	// Arena Bookmarks
	mux.HandleFunc("POST /api/v1/arena/posts/{postId}/bookmark", kitMW.Chain(handlers.BookmarkPostHandler, kitMW.Auth("")))
	mux.HandleFunc("DELETE /api/v1/arena/posts/{postId}/bookmark", kitMW.Chain(handlers.UnbookmarkPostHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/arena/bookmarks", kitMW.Chain(handlers.GetBookmarksHandler, kitMW.Auth("")))

	// Arena Views
	mux.HandleFunc("POST /api/v1/arena/posts/views", kitMW.Chain(handlers.RecordViewsHandler, kitMW.Auth("")))

	// Arena Post Activity
	mux.HandleFunc("GET /api/v1/arena/posts/{postId}/activity", kitMW.Chain(handlers.GetPostActivityHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/arena/posts/{postId}/reposts", kitMW.Chain(handlers.GetRepostsHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/arena/posts/{postId}/profile-click", kitMW.Chain(handlers.RecordProfileClickHandler, kitMW.Auth("")))

	// Arena Comments
	mux.HandleFunc("POST /api/v1/arena/posts/{postId}/comments", kitMW.Chain(handlers.CreateCommentHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/arena/posts/{postId}/comments", kitMW.Chain(handlers.GetCommentsHandler, kitMW.Auth("")))
	mux.HandleFunc("DELETE /api/v1/arena/posts/{postId}/comments/{commentId}", kitMW.Chain(handlers.DeleteCommentHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/arena/comments/{commentId}/like", kitMW.Chain(handlers.LikeCommentHandler, kitMW.Auth("")))
	mux.HandleFunc("DELETE /api/v1/arena/comments/{commentId}/like", kitMW.Chain(handlers.UnlikeCommentHandler, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/arena/comments/{commentId}/report", kitMW.Chain(handlers.ReportCommentHandler, kitMW.Auth("")))

	// Arena Feeds
	mux.HandleFunc("GET /api/v1/arena/feed/global", kitMW.Chain(handlers.GlobalFeedHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/arena/feed/network", kitMW.Chain(handlers.NetworkFeedHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/arena/feed/trending", kitMW.Chain(handlers.TrendingFeedHandler, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/arena/users/{userId}/posts", kitMW.Chain(handlers.UserPostsHandler, kitMW.Auth("")))

	// Arena Admin
	mux.HandleFunc("DELETE /api/v1/admin/arena/posts/{postId}", kitMW.Chain(handlers.AdminDeletePostHandler, mw.BenkiAdminOnly, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/admin/arena/reports", kitMW.Chain(handlers.AdminGetPostReportsHandler, mw.BenkiAdminOnly, kitMW.Auth("")))

	// Admin
	mux.HandleFunc("GET /api/v1/admin/stats", kitMW.Chain(handlers.GetAdminStatsHandler, mw.BenkiAdminOnly, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/admin/users", kitMW.Chain(handlers.GetAdminUsersHandler, mw.BenkiAdminOnly, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/admin/users/{userId}/ban", kitMW.Chain(handlers.BanUserHandler, mw.BenkiAdminOnly, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/admin/users/{userId}/unban", kitMW.Chain(handlers.UnbanUserHandler, mw.BenkiAdminOnly, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/admin/reports", kitMW.Chain(handlers.GetAdminReportsHandler, mw.BenkiAdminOnly, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/admin/users/{userId}/reports", kitMW.Chain(handlers.GetAdminUserReportsHandler, mw.BenkiAdminOnly, kitMW.Auth("")))
	mux.HandleFunc("POST /api/v1/admin/badges", kitMW.Chain(handlers.CreateBadgeTierHandler, mw.BenkiAdminOnly, kitMW.Auth("")))
	mux.HandleFunc("PATCH /api/v1/admin/badges/{badgeId}", kitMW.Chain(handlers.UpdateBadgeTierHandler, mw.BenkiAdminOnly, kitMW.Auth("")))
	mux.HandleFunc("DELETE /api/v1/admin/badges/{badgeId}", kitMW.Chain(handlers.DeleteBadgeTierHandler, mw.BenkiAdminOnly, kitMW.Auth("")))
	mux.HandleFunc("GET /api/v1/admin/settings/{key}", kitMW.Chain(handlers.GetAppSettingHandler, mw.BenkiAdminOnly, kitMW.Auth("")))
	mux.HandleFunc("PATCH /api/v1/admin/settings/{key}", kitMW.Chain(handlers.UpdateAppSettingHandler, mw.BenkiAdminOnly, kitMW.Auth("")))

	// WebSocket
	mux.HandleFunc("GET /ws", kitMW.Chain(func(w http.ResponseWriter, r *http.Request) {
		userID := kitMW.Subject(r.Context())
		chat.ServeWs(engine, w, r, userID)
	}, kitMW.Auth("")))

	handler := kitMW.NewCORS(kitMW.CORSConfig{
		Origin:      config.CORSOrigin,
		Credentials: true,
	})(rateLimitRouter(globalLimiter, mux))
	return handler
}

// rateLimitRouter applies the global rate limiter except for routes with their
// own limiter or webhook routes that bypass limiting entirely.
func rateLimitRouter(globalLimiter *kitMW.IPRateLimiter, mux *http.ServeMux) http.Handler {
	bypassPrefixes := []string{
		"/health",
		"/api/v1/auth/",
		"/api/v1/online",
		"/api/v1/webhook/",
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, p := range bypassPrefixes {
			if strings.HasPrefix(r.URL.Path, p) {
				mux.ServeHTTP(w, r)
				return
			}
		}
		globalLimiter.LimitHandler(mux).ServeHTTP(w, r)
	})
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
	db := postgress.GetPool()

	// Dynamically discover all user-created tables to avoid hard-coded list going stale
	rows, err := db.Query(context.Background(),
		`SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename`)
	if err != nil {
		t.Fatalf("[cleanup] Failed to list tables: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Logf("[cleanup] Scan table name: %v", err)
			continue
		}
		tables = append(tables, name)
	}

	for _, table := range tables {
		_, err := db.Exec(context.Background(), "TRUNCATE "+table+" CASCADE")
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
	db := postgress.GetPool()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := db.QueryRow(ctx,
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
	db := postgress.GetPool()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := db.QueryRow(ctx,
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
	db := postgress.GetPool()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Ensure user_id_1 < user_id_2 for the friendship table constraint
	u1, u2 := userID1, userID2
	if u1 > u2 {
		u1, u2 = u2, u1
	}

	_, err := db.Exec(ctx,
		`INSERT INTO friendships (user_id_1, user_id_2) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		u1, u2)
	if err != nil {
		t.Fatalf("[seed] Failed to create friendship: %v", err)
	}

	// Create DM room
	var roomID string
	err = db.QueryRow(ctx,
		`INSERT INTO rooms (type) VALUES ('DM') RETURNING id::text`,
	).Scan(&roomID)
	if err != nil {
		t.Fatalf("[seed] Failed to create room: %v", err)
	}

	_, err = db.Exec(ctx,
		`INSERT INTO room_members (room_id, user_id, status) VALUES ($1, $2, 'active'), ($1, $3, 'active')`,
		roomID, userID1, userID2)
	if err != nil {
		t.Fatalf("[seed] Failed to add room members: %v", err)
	}

	// Invalidate network feed, rooms, and friends list cache for both users (same as production handler)
	services.InvalidateNetworkFeedCache(ctx, userID1, userID2)
	services.InvalidateRoomsCache(ctx, userID1, userID2)

	// Invalidate friends list cache (mirrors handlers.invalidateFriendsListCache)
	rdb := redis.GetRawClient()
	friendsKeys := make([]string, 0, 12)
	for _, uid := range []string{userID1, userID2} {
		for _, limit := range []int{config.DefaultPageLimit, config.MaxPageLimit} {
			for _, offset := range []int{0, config.DefaultPageLimit, config.DefaultPageLimit * 2} {
				friendsKeys = append(friendsKeys, fmt.Sprintf("%s%s:%d:%d", config.CacheFriends, uid, limit, offset))
			}
		}
	}
	rdb.Del(ctx, friendsKeys...)

	return roomID
}

// SeedMessage inserts a message into a room.
func SeedMessage(t *testing.T, roomID, senderID, content string) int64 {
	t.Helper()
	db := postgress.GetPool()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var msgID int64
	err := db.QueryRow(ctx,
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
	db := postgress.GetPool()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := db.Exec(ctx, `UPDATE users SET is_banned = true WHERE id = $1`, userID)
	if err != nil {
		t.Fatalf("[seed] Failed to ban user: %v", err)
	}

	// Also set in Redis cache
	redis.GetRawClient().Set(ctx, config.CacheBan+userID, "1", 24*time.Hour)
}

// SeedBlock creates a block relationship.
func SeedBlock(t *testing.T, blockerID, blockedID string) {
	t.Helper()
	db := postgress.GetPool()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := db.Exec(ctx,
		`INSERT INTO blocked_users (blocker_id, blocked_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		blockerID, blockedID)
	if err != nil {
		t.Fatalf("[seed] Failed to create block: %v", err)
	}
}

// SeedPost creates a post in the database and returns its ID.
func SeedPost(t *testing.T, userID, caption string) int64 {
	t.Helper()
	db := postgress.GetPool()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var postID int64
	err := db.QueryRow(ctx,
		`INSERT INTO posts (user_id, caption, post_type, visibility) VALUES ($1, $2, 'original', 'public') RETURNING id`,
		userID, caption,
	).Scan(&postID)
	if err != nil {
		t.Fatalf("[seed] Failed to create post: %v", err)
	}
	return postID
}
