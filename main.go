package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "go.uber.org/automaxprocs" // Automatically set GOMAXPROCS to match container CPU quota

	"github.com/shivanand-burli/go-starter-kit/jwt"
	"github.com/shivanand-burli/go-starter-kit/middleware"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/push"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/shivanand-burli/go-starter-kit/rtc"

	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/handlers"
	mw "github.com/thebrchub/aarpaar/middleware"
	"github.com/thebrchub/aarpaar/services"

	sdkpay "github.com/shivanand-burli/go-starter-kit/payment"
)

func main() {
	// -----------------------------------------------------------------------
	// 1. LOAD CONFIGURATION
	// -----------------------------------------------------------------------
	config.Init()
	err := jwt.Init()
	if err != nil {
		log.Fatalf("[boot] JWT initialization failed: %v", err)
	}

	log.Println("[boot] Configuration loaded")

	// -----------------------------------------------------------------------
	// 2. CONNECT TO POSTGRES
	// -----------------------------------------------------------------------
	if err := postgress.Init(config.PostgresConn, config.PGTimeout); err != nil {
		log.Fatalf("[boot] Postgres connection failed: %v", err)
	}
	log.Println("[boot] Postgres connected")

	// -----------------------------------------------------------------------
	// 2.5 RUN AUTO-MIGRATIONS (creates tables, indexes, triggers if needed)
	// -----------------------------------------------------------------------
	services.RunMigrations()
	log.Println("[boot] Database migrations complete")

	// -----------------------------------------------------------------------
	// 3. CONNECT TO REDIS
	// -----------------------------------------------------------------------
	if err := redis.InitCache(config.RedisCacheName, config.RedisHost, config.RedisPort); err != nil {
		log.Fatalf("[boot] Redis connection failed: %v", err)
	}
	log.Println("[boot] Redis connected")

	// -----------------------------------------------------------------------
	// 3.5 INITIALIZE RTC CLIENT (LiveKit — single shared instance for 10K+ concurrency)
	// -----------------------------------------------------------------------
	handlers.RTC = rtc.NewClientOptional(rtc.Config{
		URL:       config.LiveKitURL,
		APIKey:    config.LiveKitAPIKey,
		APISecret: config.LiveKitAPISecret,
	})
	chat.SetRTC(handlers.RTC) // Share RTC client with bgpool for orphan cleanup
	if handlers.RTC.IsConfigured() {
		log.Println("[boot] RTC (LiveKit) client initialized")
	} else {
		log.Println("[boot] RTC (LiveKit) not configured — group calls disabled")
	}

	// -----------------------------------------------------------------------
	// 4. START THE WEBSOCKET ENGINE (begins listening to Redis Pub/Sub)
	// -----------------------------------------------------------------------
	engine := chat.NewEngine()
	log.Println("[boot] Chat engine started")

	// -----------------------------------------------------------------------
	// 5. START THE MESSAGE FLUSHER (background: Redis -> Postgres)
	// -----------------------------------------------------------------------
	services.StartFlusher()
	log.Println("[boot] Message flusher started")

	// -----------------------------------------------------------------------
	// 5.5 INITIALIZE BOT SERVICE (retrieval-based chatbot for match fallback)
	// -----------------------------------------------------------------------
	services.InitBot()
	config.BotCorpusData = "" // Free ~8 MB raw corpus string — already parsed into retrieval engine
	engine.OnUserOffline = services.CancelBotMatch

	// -----------------------------------------------------------------------
	// 5.6 INITIALIZE PAYMENT SERVICE (Razorpay)
	// -----------------------------------------------------------------------
	if config.PaymentProviderName == "razorpay" {
		handlers.PaymentSvc = sdkpay.NewRazorpayService()
		log.Println("[boot] Payment service initialized (razorpay)")
	} else {
		log.Println("[boot] Payment service not configured — donations disabled")
	}

	// -----------------------------------------------------------------------
	// 5.7 INITIALIZE PUSH NOTIFICATION SERVICE (FCM)
	// -----------------------------------------------------------------------
	if config.FirebaseCredentials != "" {
		if err := services.InitPush(); err != nil {
			log.Printf("[boot] Push notification init failed: %v", err)
		} else {
			log.Println("[boot] Push notification service initialized (FCM)")
		}
	} else {
		log.Println("[boot] Push notifications not configured — FIREBASE_CREDENTIALS not set")
	}

	// Wire up push callbacks on the engine (breaks chat ↔ services import cycle)
	engine.SendPushToUser = func(ctx context.Context, userID string, data map[string]string, highPriority bool) {
		p := services.PushPayload{Data: data}
		if highPriority {
			p.Priority = push.PriorityHigh
		}
		// Data-only FCM messages — the service worker's onBackgroundMessage
		// is the sole handler for displaying notifications. Setting Title/Body
		// would add a notification payload that causes duplicate browser
		// notifications (one auto-displayed + one from the SW).
		services.SendPushToUser(ctx, userID, p)
	}
	engine.ShouldPushMessage = services.ShouldPushMessage

	// -----------------------------------------------------------------------
	// 6. RATE LIMITER (10 requests/sec per IP, burst of 20)
	// -----------------------------------------------------------------------
	limiter := middleware.NewIPRateLimiter(10, 20)

	// -----------------------------------------------------------------------
	// 7. ROUTES
	// -----------------------------------------------------------------------
	mux := http.NewServeMux()

	// Health check — no auth needed
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// --- Auth (public) ---
	mux.HandleFunc("POST /api/v1/auth/google", handlers.GoogleLoginHandler)
	mux.HandleFunc("POST /api/v1/auth/refresh", handlers.RefreshTokenHandler)

	// --- Auth (internal, API key protected) ---
	mux.HandleFunc("POST /api/v1/auth/validate", mw.APIKeyOnly(handlers.ValidateTokenHandler))

	// --- Config (public) ---
	mux.HandleFunc("GET /api/v1/config/firebase", handlers.GetFirebaseConfigHandler)

	// --- Auth (protected) ---
	mux.HandleFunc("POST /api/v1/auth/device", mw.Auth(handlers.RegisterDeviceHandler))

	// --- Users (protected) ---
	mux.HandleFunc("GET /api/v1/users/me", mw.Auth(handlers.GetMeHandler))
	mux.HandleFunc("PATCH /api/v1/users/me", mw.Auth(handlers.UpdateMeHandler))
	mux.HandleFunc("PUT /api/v1/users/me", mw.Auth(handlers.PutMeHandler))
	mux.HandleFunc("GET /api/v1/users/search", mw.Auth(handlers.SearchUsersHandler))
	mux.HandleFunc("GET /api/v1/users/check-username", mw.Auth(handlers.CheckUsernameHandler))

	// --- Rooms (protected) ---
	mux.HandleFunc("GET /api/v1/rooms", mw.Auth(handlers.GetRoomsHandler))
	mux.HandleFunc("POST /api/v1/rooms", mw.Auth(handlers.CreateDMHandler))
	mux.HandleFunc("GET /api/v1/rooms/requests", mw.Auth(handlers.GetDMRequestsHandler))
	mux.HandleFunc("GET /api/v1/rooms/{roomId}/messages", mw.Auth(handlers.GetRoomMessagesHandler))
	mux.HandleFunc("POST /api/v1/rooms/{roomId}/accept", mw.Auth(handlers.AcceptDMRequestHandler))
	mux.HandleFunc("POST /api/v1/rooms/{roomId}/reject", mw.Auth(handlers.RejectDMRequestHandler))

	// --- Friends (protected) ---
	mux.HandleFunc("GET /api/v1/friends", mw.Auth(handlers.GetFriendsHandler))
	mux.HandleFunc("GET /api/v1/friends/search", mw.Auth(handlers.SearchFriendsHandler))
	mux.HandleFunc("GET /api/v1/friends/requests", mw.Auth(handlers.GetFriendRequestsHandler))
	mux.HandleFunc("POST /api/v1/friends/request", mw.Auth(handlers.SendFriendRequestHandler))
	mux.HandleFunc("POST /api/v1/friends/accept", mw.Auth(handlers.AcceptFriendRequestHandler))
	mux.HandleFunc("POST /api/v1/friends/reject", mw.Auth(handlers.RejectFriendRequestHandler))
	mux.HandleFunc("DELETE /api/v1/friends/request/{username}", mw.Auth(handlers.WithdrawFriendRequestHandler))
	mux.HandleFunc("DELETE /api/v1/friends/{username}", mw.Auth(handlers.RemoveFriendHandler))
	mux.HandleFunc("POST /api/v1/friends/block/{username}", mw.Auth(handlers.BlockUserHandler))
	mux.HandleFunc("DELETE /api/v1/friends/block/{username}", mw.Auth(handlers.UnblockUserHandler))
	mux.HandleFunc("GET /api/v1/friends/blocked", mw.Auth(handlers.GetBlockedUsersHandler))

	// --- Matchmaking (protected) ---
	mux.HandleFunc("POST /api/v1/match/enter", mw.Auth(handlers.EnterMatchQueueHandler))
	mux.HandleFunc("POST /api/v1/match/leave", mw.Auth(handlers.LeaveMatchQueueHandler))
	mux.HandleFunc("POST /api/v1/match/action", mw.Auth(handlers.MatchActionHandler))
	mux.HandleFunc("POST /api/v1/match/report", mw.Auth(handlers.ReportUserHandler))

	// --- Calls (protected) ---
	mux.HandleFunc("GET /api/v1/calls/config", mw.Auth(handlers.GetCallConfigHandler))
	mux.HandleFunc("GET /api/v1/calls/history", mw.Auth(handlers.GetCallHistoryHandler))

	// --- Groups (protected) ---
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
	mux.HandleFunc("GET /api/v1/groups/invites", mw.Auth(handlers.GetGroupInvitesHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/invites/accept", mw.Auth(handlers.AcceptGroupInviteHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/invites/decline", mw.Auth(handlers.DeclineGroupInviteHandler))

	// --- Group Calls (protected) ---
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls", mw.Auth(handlers.StartGroupCallHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/join", mw.Auth(handlers.JoinGroupCallHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/leave", mw.Auth(handlers.LeaveGroupCallHandler))
	mux.HandleFunc("GET /api/v1/groups/{groupId}/calls/{callId}", mw.Auth(handlers.GetGroupCallStatusHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/mute", mw.Auth(handlers.MuteParticipantHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/kick", mw.Auth(handlers.KickParticipantHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/admins", mw.Auth(handlers.PromoteCallAdminHandler))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/end", mw.Auth(handlers.ForceEndCallHandler))

	// --- Vanity Links (protected) ---
	mux.HandleFunc("PATCH /api/v1/groups/{groupId}/vanity", mw.Auth(handlers.SetVanitySlugHandler))
	mux.HandleFunc("POST /api/v1/vanity/{slug}", mw.Auth(handlers.JoinGroupByVanityHandler))

	// --- Donations (protected) ---
	mux.HandleFunc("POST /api/v1/donate/create-order", mw.Auth(handlers.CreateDonationOrderHandler))
	mux.HandleFunc("GET /api/v1/donate/status/{orderId}", mw.Auth(handlers.GetDonationStatusHandler))
	mux.HandleFunc("GET /api/v1/donate/history", mw.Auth(handlers.GetDonationHistoryHandler))
	mux.HandleFunc("GET /api/v1/badges/tiers", mw.Auth(handlers.GetBadgeTiersHandler))

	// --- Razorpay Webhook (public — uses webhook signature verification) ---
	mux.HandleFunc("POST /api/v1/webhook/razorpay", handlers.RazorpayWebhookHandler)

	// --- Leaderboard (protected) ---
	mux.HandleFunc("GET /api/v1/leaderboard", mw.Auth(handlers.GetLeaderboardHandler))

	// --- Online Count (public) ---
	mux.HandleFunc("GET /api/v1/online", handlers.GetOnlineCountHandler)

	// --- Admin (protected, BENKI_ADMIN only) ---
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
	mux.HandleFunc("GET /api/v1/admin/bot", mw.Auth(mw.BenkiAdminOnly(handlers.GetBotStatusHandler)))
	mux.HandleFunc("POST /api/v1/admin/bot", mw.Auth(mw.BenkiAdminOnly(handlers.ToggleBotHandler)))

	// --- WebSocket (protected) ---
	mux.HandleFunc("GET /ws", mw.Auth(func(w http.ResponseWriter, r *http.Request) {
		userID := r.Context().Value(config.UserIDKey).(string)
		chat.ServeWs(engine, w, r, userID)
	}))

	// -----------------------------------------------------------------------
	// 8. HTTP SERVER with middleware stack:
	//    CORS → Body Limit → Rate Limit → Router
	// -----------------------------------------------------------------------
	handler := mw.CORS(mw.BodyLimit(limiter.LimitHandler(mux)))

	server := &http.Server{
		Addr:              "0.0.0.0:" + config.ServerPort,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       0, // Disabled: WebSocket connections are long-lived; readPump manages its own deadlines via SetReadDeadline
		WriteTimeout:      0, // Disabled: WriteTimeout counts from handler start — kills WebSocket connections after N seconds. writePump manages its own deadlines via SetWriteDeadline
		IdleTimeout:       120 * time.Second,
	}

	// -----------------------------------------------------------------------
	// 9. GRACEFUL SHUTDOWN
	//
	// Listen for interrupt signals (Ctrl+C or container stop).
	// When received:
	//   1. Stop accepting new HTTP connections
	//   2. Close all WebSocket connections
	//   3. Stop the flusher (with final flush)
	//   4. Close Postgres
	// -----------------------------------------------------------------------
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("[boot] Server listening on %s", config.ServerPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[boot] Server error: %v", err)
		}
	}()

	// Block until we receive a shutdown signal
	<-quit
	log.Println("[shutdown] Signal received, shutting down...")

	// Give in-flight requests 10 seconds to finish
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("[shutdown] HTTP server shutdown error: %v", err)
	}
	log.Println("[shutdown] HTTP server stopped")

	// Stop all active bot sessions first (they may still publish to Redis)
	services.StopAllSessions()
	log.Println("[shutdown] Bot service stopped")

	// Stop the flusher (runs one final flush before returning)
	services.StopFlusher()
	log.Println("[shutdown] Message flusher stopped")

	// Close all WebSocket connections and stop Redis Pub/Sub listener LAST
	// so that bot/flusher can still publish/receive during their shutdown.
	engine.Shutdown()
	log.Println("[shutdown] WebSocket engine stopped")

	// Close FCM push service (releases idle HTTP connections)
	services.ClosePush()
	log.Println("[shutdown] Push service closed")

	// Stop rate limiter sweeper goroutine
	limiter.Close()
	log.Println("[shutdown] Rate limiter closed")

	// Close Redis connection cleanly
	if err := redis.Close(); err != nil {
		log.Printf("[shutdown] Redis close error: %v", err)
	}
	log.Println("[shutdown] Redis connection closed")

	// Close database connection
	if err := postgress.Close(); err != nil {
		log.Printf("[shutdown] Postgres close error: %v", err)
	}

	log.Println("[shutdown] Server stopped cleanly")
}
