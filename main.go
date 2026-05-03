package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	_ "go.uber.org/automaxprocs" // Automatically set GOMAXPROCS to match container CPU quota

	"github.com/shivanand-burli/go-starter-kit/cron"
	"github.com/shivanand-burli/go-starter-kit/helper"
	"github.com/shivanand-burli/go-starter-kit/jwt"
	"github.com/shivanand-burli/go-starter-kit/middleware"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/push"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/shivanand-burli/go-starter-kit/rtc"
	"github.com/shivanand-burli/go-starter-kit/storage"

	"github.com/thebrchub/aarpaar/chat"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/handlers"
	mw "github.com/thebrchub/aarpaar/middleware"
	"github.com/thebrchub/aarpaar/services"

	sdkpay "github.com/shivanand-burli/go-starter-kit/payment"

	"google.golang.org/api/idtoken"
	"google.golang.org/api/option"
)

func main() {
	// -----------------------------------------------------------------------
	// 0. MEMORY TUNING (GC soft target for 10M+ RPS)
	// -----------------------------------------------------------------------
	helper.TuneMemory(256)

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
	if err := redis.InitCache(config.RedisCacheName, "", 0); err != nil {
		log.Fatalf("[boot] Redis connection failed: %v", err)
	}
	log.Println("[boot] Redis connected")

	// -----------------------------------------------------------------------
	// 3.1 INITIALIZE GOOGLE ID TOKEN VALIDATOR (tuned HTTP client)
	// -----------------------------------------------------------------------
	httpClient := helper.NewHTTPClient(helper.TransportConfig{})
	validator, err := idtoken.NewValidator(context.Background(), option.WithHTTPClient(httpClient))
	if err != nil {
		log.Fatalf("[boot] idtoken validator init failed: %v", err)
	}
	handlers.IDTokenValidator = validator
	log.Println("[boot] Google ID token validator initialized")

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
	// 5. CRON SCHEDULER (replaces individual ticker goroutines)
	// -----------------------------------------------------------------------
	scheduler := cron.NewScheduler(cron.Config{WorkerPoolSize: 4})
	scheduler.Register("flusher", config.FlushInterval, services.FlushTick)
	scheduler.Register("arena-refresh", 60*time.Second, services.RefreshArenaLimits)
	scheduler.Register("bot-sweeper", 60*time.Second, services.SweepStaleBotSessions)
	scheduler.Register("engine-metrics", 120*time.Second, chat.EngineMetricsTick)

	// Eager load arena limits before scheduler starts ticking
	services.RefreshArenaLimits(context.Background())

	scheduler.Start()
	log.Println("[boot] Cron scheduler started (flusher, arena-refresh, bot-sweeper, engine-metrics)")

	// -----------------------------------------------------------------------
	// 5.5 INITIALIZE BOT SERVICE (retrieval-based chatbot for match fallback)
	// -----------------------------------------------------------------------
	services.InitBot()
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
	// 5.8 INITIALIZE STORAGE CLIENT (Cloudflare R2 / S3-compatible)
	// -----------------------------------------------------------------------
	if config.StorageBucket != "" {
		store, err := storage.NewS3Client(storage.S3Config{
			Endpoint:        config.StorageEndpoint,
			AccessKeyID:     config.StorageAccessKey,
			SecretAccessKey: config.StorageSecretKey,
			Bucket:          config.StorageBucket,
			Region:          config.StorageRegion,
			PublicURL:       config.StoragePublicURL,
		})
		if err != nil {
			log.Printf("[boot] Storage client init failed: %v", err)
		} else {
			handlers.Store = store
			log.Println("[boot] Storage client initialized (R2/S3)")
		}
	} else {
		log.Println("[boot] Storage not configured — Arena media uploads disabled")
	}

	// -----------------------------------------------------------------------
	// 6. RATE LIMITERS
	//    - global:  general API traffic (configurable via env)
	//    - auth:    tighter limit for auth endpoints (brute-force protection)
	//    - health:  tight limit for health/online (LB probes)
	//    - webhook: bypassed (trusted, signature-verified)
	// -----------------------------------------------------------------------
	globalLimiter := middleware.NewIPRateLimiter(config.RateLimitRate, config.RateLimitBurst)
	authLimiter := middleware.NewIPRateLimiter(2, 5)
	healthLimiter := middleware.NewIPRateLimiter(2, 3)

	// Ban check — runs after JWT verification on every authenticated request
	banCheck := func(r *http.Request, claims map[string]any) error {
		userID := middleware.Subject(r.Context())
		banned, _ := redis.FetchRaw(r.Context(), config.CacheBan+userID, 24*time.Hour, func(ctx context.Context) ([]byte, error) {
			var b bool
			err := postgress.GetPool().QueryRow(ctx, `SELECT is_banned FROM users WHERE id = $1`, userID).Scan(&b)
			if err != nil {
				return []byte("0"), err
			}
			if b {
				return []byte("1"), nil
			}
			return []byte("0"), nil
		})
		if string(banned) == "1" {
			return errors.New("account is banned")
		}
		return nil
	}
	authMW := middleware.AuthWithCheck("", banCheck)

	// -----------------------------------------------------------------------
	// 7. ROUTES
	// -----------------------------------------------------------------------
	mux := http.NewServeMux()

	// Health check — no auth needed (own rate limit)
	mux.HandleFunc("GET /health", healthLimiter.LimitMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))

	// --- Auth (public — tighter rate limit for brute-force protection) ---
	mux.HandleFunc("POST /api/v1/auth/google", authLimiter.LimitMiddleware(handlers.GoogleLoginHandler))
	mux.HandleFunc("POST /api/v1/auth/refresh", authLimiter.LimitMiddleware(middleware.HandleRefresh("")))
	mux.HandleFunc("POST /api/v1/auth/logout", authLimiter.LimitMiddleware(middleware.HandleLogout("")))

	// --- Config (protected) ---
	mux.HandleFunc("GET /api/v1/config/firebase", middleware.Chain(handlers.GetFirebaseConfigHandler, authMW))

	// --- Auth (protected) ---
	mux.HandleFunc("POST /api/v1/auth/device", middleware.Chain(handlers.RegisterDeviceHandler, authMW))

	// --- Users (protected) ---
	mux.HandleFunc("GET /api/v1/users/me", middleware.Chain(handlers.GetMeHandler, authMW))
	mux.HandleFunc("PATCH /api/v1/users/me", middleware.Chain(handlers.UpdateMeHandler, authMW))
	mux.HandleFunc("PUT /api/v1/users/me", middleware.Chain(handlers.PutMeHandler, authMW))
	mux.HandleFunc("GET /api/v1/users/me/notification-preferences", middleware.Chain(handlers.GetNotificationPreferencesHandler, authMW))
	mux.HandleFunc("PATCH /api/v1/users/me/notification-preferences", middleware.Chain(handlers.UpdateNotificationPreferencesHandler, authMW))
	mux.HandleFunc("GET /api/v1/users/search", middleware.Chain(handlers.SearchUsersHandler, authMW))
	mux.HandleFunc("GET /api/v1/users/check-username", middleware.Chain(handlers.CheckUsernameHandler, authMW))

	// --- Rooms (protected) ---
	mux.HandleFunc("GET /api/v1/rooms", middleware.Chain(handlers.GetRoomsHandler, authMW))
	mux.HandleFunc("POST /api/v1/rooms", middleware.Chain(handlers.CreateDMHandler, authMW))
	mux.HandleFunc("GET /api/v1/rooms/requests", middleware.Chain(handlers.GetDMRequestsHandler, authMW))
	mux.HandleFunc("GET /api/v1/rooms/{roomId}/messages", middleware.Chain(handlers.GetRoomMessagesHandler, authMW))
	mux.HandleFunc("POST /api/v1/rooms/{roomId}/accept", middleware.Chain(handlers.AcceptDMRequestHandler, authMW))
	mux.HandleFunc("POST /api/v1/rooms/{roomId}/reject", middleware.Chain(handlers.RejectDMRequestHandler, authMW))

	// --- Friends (protected) ---
	mux.HandleFunc("GET /api/v1/friends", middleware.Chain(handlers.GetFriendsHandler, authMW))
	mux.HandleFunc("GET /api/v1/friends/search", middleware.Chain(handlers.SearchFriendsHandler, authMW))
	mux.HandleFunc("GET /api/v1/friends/requests", middleware.Chain(handlers.GetFriendRequestsHandler, authMW))
	mux.HandleFunc("POST /api/v1/friends/request", middleware.Chain(handlers.SendFriendRequestHandler, authMW))
	mux.HandleFunc("POST /api/v1/friends/accept", middleware.Chain(handlers.AcceptFriendRequestHandler, authMW))
	mux.HandleFunc("POST /api/v1/friends/reject", middleware.Chain(handlers.RejectFriendRequestHandler, authMW))
	mux.HandleFunc("DELETE /api/v1/friends/request/{username}", middleware.Chain(handlers.WithdrawFriendRequestHandler, authMW))
	mux.HandleFunc("DELETE /api/v1/friends/{username}", middleware.Chain(handlers.RemoveFriendHandler, authMW))
	mux.HandleFunc("POST /api/v1/friends/block/{username}", middleware.Chain(handlers.BlockUserHandler, authMW))
	mux.HandleFunc("DELETE /api/v1/friends/block/{username}", middleware.Chain(handlers.UnblockUserHandler, authMW))
	mux.HandleFunc("GET /api/v1/friends/blocked", middleware.Chain(handlers.GetBlockedUsersHandler, authMW))

	// --- Matchmaking (protected) ---
	mux.HandleFunc("POST /api/v1/match/enter", middleware.Chain(handlers.EnterMatchQueueHandler, authMW))
	mux.HandleFunc("POST /api/v1/match/leave", middleware.Chain(handlers.LeaveMatchQueueHandler, authMW))
	mux.HandleFunc("POST /api/v1/match/action", middleware.Chain(handlers.MatchActionHandler, authMW))
	mux.HandleFunc("POST /api/v1/match/report", middleware.Chain(handlers.ReportUserHandler, authMW))

	// --- Calls (protected) ---
	mux.HandleFunc("GET /api/v1/calls/config", middleware.Chain(handlers.GetCallConfigHandler, authMW))
	mux.HandleFunc("GET /api/v1/calls/history", middleware.Chain(handlers.GetCallHistoryHandler, authMW))

	// --- Groups (protected) ---
	mux.HandleFunc("GET /api/v1/groups", middleware.Chain(handlers.ListGroupsHandler, authMW))
	mux.HandleFunc("POST /api/v1/groups", middleware.Chain(handlers.CreateGroupHandler, authMW))
	mux.HandleFunc("GET /api/v1/groups/{groupId}", middleware.Chain(handlers.GetGroupHandler, authMW))
	mux.HandleFunc("PATCH /api/v1/groups/{groupId}", middleware.Chain(handlers.UpdateGroupHandler, authMW))
	mux.HandleFunc("DELETE /api/v1/groups/{groupId}", middleware.Chain(handlers.DeleteGroupHandler, authMW))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/join", middleware.Chain(handlers.JoinGroupHandler, authMW))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/members", middleware.Chain(handlers.AddGroupMembersHandler, authMW))
	mux.HandleFunc("DELETE /api/v1/groups/{groupId}/members/{userId}", middleware.Chain(handlers.RemoveGroupMemberHandler, authMW))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/admins", middleware.Chain(handlers.PromoteAdminHandler, authMW))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/invite", middleware.Chain(handlers.GenerateInviteHandler, authMW))
	mux.HandleFunc("POST /api/v1/invite/{inviteCode}", middleware.Chain(handlers.JoinGroupByInviteHandler, authMW))
	mux.HandleFunc("GET /api/v1/groups/invites", middleware.Chain(handlers.GetGroupInvitesHandler, authMW))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/invites/accept", middleware.Chain(handlers.AcceptGroupInviteHandler, authMW))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/invites/decline", middleware.Chain(handlers.DeclineGroupInviteHandler, authMW))

	// --- Group Calls (protected) ---
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls", middleware.Chain(handlers.StartGroupCallHandler, authMW))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/join", middleware.Chain(handlers.JoinGroupCallHandler, authMW))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/leave", middleware.Chain(handlers.LeaveGroupCallHandler, authMW))
	mux.HandleFunc("GET /api/v1/groups/{groupId}/calls/{callId}", middleware.Chain(handlers.GetGroupCallStatusHandler, authMW))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/mute", middleware.Chain(handlers.MuteParticipantHandler, authMW))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/kick", middleware.Chain(handlers.KickParticipantHandler, authMW))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/admins", middleware.Chain(handlers.PromoteCallAdminHandler, authMW))
	mux.HandleFunc("POST /api/v1/groups/{groupId}/calls/{callId}/end", middleware.Chain(handlers.ForceEndCallHandler, authMW))

	// --- Vanity Links (protected) ---
	mux.HandleFunc("PATCH /api/v1/groups/{groupId}/vanity", middleware.Chain(handlers.SetVanitySlugHandler, authMW))
	mux.HandleFunc("POST /api/v1/vanity/{slug}", middleware.Chain(handlers.JoinGroupByVanityHandler, authMW))

	// --- Donations (protected) ---
	mux.HandleFunc("POST /api/v1/donate/create-order", middleware.Chain(handlers.CreateDonationOrderHandler, authMW))
	mux.HandleFunc("GET /api/v1/donate/status/{orderId}", middleware.Chain(handlers.GetDonationStatusHandler, authMW))
	mux.HandleFunc("GET /api/v1/donate/history", middleware.Chain(handlers.GetDonationHistoryHandler, authMW))
	mux.HandleFunc("GET /api/v1/badges/tiers", middleware.Chain(handlers.GetBadgeTiersHandler, authMW))

	// --- Razorpay Webhook (public — signature-verified, bypasses rate limit) ---
	mux.HandleFunc("POST /api/v1/webhook/razorpay", handlers.RazorpayWebhookHandler)

	// --- Leaderboard (protected) ---
	mux.HandleFunc("GET /api/v1/leaderboard", middleware.Chain(handlers.GetLeaderboardHandler, authMW))

	// --- Online Count (own rate limit) ---
	mux.HandleFunc("GET /api/v1/online", healthLimiter.LimitMiddleware(handlers.GetOnlineCountHandler))

	// --- Admin (protected, BENKI_ADMIN only) ---
	mux.HandleFunc("GET /api/v1/admin/stats", middleware.Chain(handlers.GetAdminStatsHandler, mw.BenkiAdminOnly, authMW))
	mux.HandleFunc("GET /api/v1/admin/users", middleware.Chain(handlers.GetAdminUsersHandler, mw.BenkiAdminOnly, authMW))
	mux.HandleFunc("POST /api/v1/admin/users/{userId}/ban", middleware.Chain(handlers.BanUserHandler, mw.BenkiAdminOnly, authMW))
	mux.HandleFunc("POST /api/v1/admin/users/{userId}/unban", middleware.Chain(handlers.UnbanUserHandler, mw.BenkiAdminOnly, authMW))
	mux.HandleFunc("GET /api/v1/admin/reports", middleware.Chain(handlers.GetAdminReportsHandler, mw.BenkiAdminOnly, authMW))
	mux.HandleFunc("GET /api/v1/admin/users/{userId}/reports", middleware.Chain(handlers.GetAdminUserReportsHandler, mw.BenkiAdminOnly, authMW))
	mux.HandleFunc("POST /api/v1/admin/badges", middleware.Chain(handlers.CreateBadgeTierHandler, mw.BenkiAdminOnly, authMW))
	mux.HandleFunc("PATCH /api/v1/admin/badges/{badgeId}", middleware.Chain(handlers.UpdateBadgeTierHandler, mw.BenkiAdminOnly, authMW))
	mux.HandleFunc("DELETE /api/v1/admin/badges/{badgeId}", middleware.Chain(handlers.DeleteBadgeTierHandler, mw.BenkiAdminOnly, authMW))
	mux.HandleFunc("GET /api/v1/admin/settings/{key}", middleware.Chain(handlers.GetAppSettingHandler, mw.BenkiAdminOnly, authMW))
	mux.HandleFunc("PATCH /api/v1/admin/settings/{key}", middleware.Chain(handlers.UpdateAppSettingHandler, mw.BenkiAdminOnly, authMW))
	mux.HandleFunc("GET /api/v1/admin/bot", middleware.Chain(handlers.GetBotStatusHandler, mw.BenkiAdminOnly, authMW))
	mux.HandleFunc("POST /api/v1/admin/bot", middleware.Chain(handlers.ToggleBotHandler, mw.BenkiAdminOnly, authMW))

	// --- Arena Posts (protected) ---
	mux.HandleFunc("POST /api/v1/arena/media/presign", middleware.Chain(handlers.PresignUploadHandler, authMW))
	mux.HandleFunc("POST /api/v1/arena/posts", middleware.Chain(handlers.CreatePostHandler, authMW))
	mux.HandleFunc("GET /api/v1/arena/posts/{postId}", middleware.Chain(handlers.GetPostHandler, authMW))
	mux.HandleFunc("DELETE /api/v1/arena/posts/{postId}", middleware.Chain(handlers.DeletePostHandler, authMW))
	mux.HandleFunc("POST /api/v1/arena/posts/{postId}/repost", middleware.Chain(handlers.RepostHandler, authMW))
	mux.HandleFunc("POST /api/v1/arena/posts/{postId}/pin", middleware.Chain(handlers.PinPostHandler, authMW))
	mux.HandleFunc("DELETE /api/v1/arena/posts/{postId}/pin", middleware.Chain(handlers.PinPostHandler, authMW))
	mux.HandleFunc("POST /api/v1/arena/posts/{postId}/report", middleware.Chain(handlers.ReportPostHandler, authMW))

	// --- Arena Likes (protected) ---
	mux.HandleFunc("POST /api/v1/arena/posts/{postId}/like", middleware.Chain(handlers.LikePostHandler, authMW))
	mux.HandleFunc("DELETE /api/v1/arena/posts/{postId}/like", middleware.Chain(handlers.UnlikePostHandler, authMW))
	mux.HandleFunc("GET /api/v1/arena/posts/{postId}/likes", middleware.Chain(handlers.GetPostLikersHandler, authMW))

	// --- Arena Bookmarks (protected) ---
	mux.HandleFunc("POST /api/v1/arena/posts/{postId}/bookmark", middleware.Chain(handlers.BookmarkPostHandler, authMW))
	mux.HandleFunc("DELETE /api/v1/arena/posts/{postId}/bookmark", middleware.Chain(handlers.UnbookmarkPostHandler, authMW))
	mux.HandleFunc("GET /api/v1/arena/bookmarks", middleware.Chain(handlers.GetBookmarksHandler, authMW))

	// --- Arena Views (protected) ---
	mux.HandleFunc("POST /api/v1/arena/posts/views", middleware.Chain(handlers.RecordViewsHandler, authMW))

	// --- Arena Post Activity & Tracking (protected) ---
	mux.HandleFunc("GET /api/v1/arena/posts/{postId}/activity", middleware.Chain(handlers.GetPostActivityHandler, authMW))
	mux.HandleFunc("GET /api/v1/arena/posts/{postId}/reposts", middleware.Chain(handlers.GetRepostsHandler, authMW))
	mux.HandleFunc("POST /api/v1/arena/posts/{postId}/profile-click", middleware.Chain(handlers.RecordProfileClickHandler, authMW))

	// --- Arena Comments (protected) ---
	mux.HandleFunc("POST /api/v1/arena/posts/{postId}/comments", middleware.Chain(handlers.CreateCommentHandler, authMW))
	mux.HandleFunc("GET /api/v1/arena/posts/{postId}/comments", middleware.Chain(handlers.GetCommentsHandler, authMW))
	mux.HandleFunc("DELETE /api/v1/arena/posts/{postId}/comments/{commentId}", middleware.Chain(handlers.DeleteCommentHandler, authMW))
	mux.HandleFunc("POST /api/v1/arena/comments/{commentId}/like", middleware.Chain(handlers.LikeCommentHandler, authMW))
	mux.HandleFunc("DELETE /api/v1/arena/comments/{commentId}/like", middleware.Chain(handlers.UnlikeCommentHandler, authMW))
	mux.HandleFunc("POST /api/v1/arena/comments/{commentId}/report", middleware.Chain(handlers.ReportCommentHandler, authMW))

	// --- Arena Feeds (protected) ---
	mux.HandleFunc("GET /api/v1/arena/feed/global", middleware.Chain(handlers.GlobalFeedHandler, authMW))
	mux.HandleFunc("GET /api/v1/arena/feed/network", middleware.Chain(handlers.NetworkFeedHandler, authMW))
	mux.HandleFunc("GET /api/v1/arena/feed/trending", middleware.Chain(handlers.TrendingFeedHandler, authMW))
	mux.HandleFunc("GET /api/v1/arena/users/{userId}/posts", middleware.Chain(handlers.UserPostsHandler, authMW))

	// --- Arena Admin (BENKI_ADMIN only) ---
	mux.HandleFunc("DELETE /api/v1/admin/arena/posts/{postId}", middleware.Chain(handlers.AdminDeletePostHandler, mw.BenkiAdminOnly, authMW))
	mux.HandleFunc("GET /api/v1/admin/arena/reports", middleware.Chain(handlers.AdminGetPostReportsHandler, mw.BenkiAdminOnly, authMW))

	// --- WebSocket (protected) ---
	mux.HandleFunc("GET /ws", middleware.Chain(func(w http.ResponseWriter, r *http.Request) {
		userID := middleware.Subject(r.Context())
		chat.ServeWs(engine, w, r, userID)
	}, authMW))

	// -----------------------------------------------------------------------
	// 8. HTTP SERVER with middleware stack:
	//    CORS → Body Limit → Rate Limit → Router
	//
	//    Routes with their own limiters (auth, health, online) are excluded
	//    from the global limiter. Webhook bypasses rate limiting entirely.
	// -----------------------------------------------------------------------
	handler := middleware.NewCORS(middleware.CORSConfig{
		Origin:      config.CORSOrigin,
		Credentials: true,
	})(middleware.Compress(rateLimitRouter(globalLimiter, mux)))

	server := &http.Server{
		Addr:              "0.0.0.0:" + config.ServerPort,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       0, // Disabled: WebSocket connections are long-lived; readPump manages its own deadlines via SetReadDeadline
		WriteTimeout:      0, // Disabled: WriteTimeout counts from handler start — kills WebSocket connections after N seconds. writePump manages its own deadlines via SetWriteDeadline
		IdleTimeout:       120 * time.Second,
	}

	// -----------------------------------------------------------------------
	// 9. GRACEFUL SHUTDOWN via helper.ListenAndServe
	// -----------------------------------------------------------------------
	log.Printf("[boot] Server listening on %s", config.ServerPort)
	helper.ListenAndServe(server, 10*time.Second,
		func() { scheduler.Stop(); log.Println("[shutdown] Cron scheduler stopped") },
		func() { services.FlushTick(context.Background()); log.Println("[shutdown] Final flush completed") },
		func() { services.StopAllSessions(); log.Println("[shutdown] Bot service stopped") },
		func() { engine.Shutdown(); log.Println("[shutdown] WebSocket engine stopped") },
		func() { services.ClosePush(); log.Println("[shutdown] Push service closed") },
		func() {
			globalLimiter.Close()
			authLimiter.Close()
			healthLimiter.Close()
			log.Println("[shutdown] Rate limiters closed")
		},
		func() {
			if handlers.Store != nil {
				if s, ok := handlers.Store.(*storage.S3Client); ok {
					s.Close()
				}
				log.Println("[shutdown] Storage client closed")
			}
		},
		func() { _ = redis.Close(); log.Println("[shutdown] Redis closed") },
		func() { _ = postgress.Close(); log.Println("[shutdown] Postgres closed") },
	)
}

// rateLimitRouter applies the global rate limiter to all routes except those
// that already have their own limiter (auth, health, online) or should bypass
// rate limiting entirely (webhook).
func rateLimitRouter(globalLimiter *middleware.IPRateLimiter, mux *http.ServeMux) http.Handler {
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
