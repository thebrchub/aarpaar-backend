package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/shivanand-burli/go-starter-kit/jwt"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	redisKit "github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// JWT Authentication Middleware
//
// This middleware sits in front of every protected endpoint.
// It reads the "Authorization: Bearer <token>" header, validates the JWT
// using the go-starter-kit, and injects the user's ID into the request
// context so handlers can access it with:
//
//	userID := r.Context().Value(config.USER_ID).(string)
//
// If the token is missing or invalid, the request is rejected with 401.
// ---------------------------------------------------------------------------

// Auth wraps an http.HandlerFunc and enforces JWT authentication.
// Only requests with a valid Bearer token are allowed through.
//
// For WebSocket connections (which cannot send custom headers from
// browsers), the token may alternatively be passed as a "token" query
// parameter: /ws?token=<jwt>
func Auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Read the Authorization header; fall back to ?token= query param
		//    (needed for browser WebSocket connections).
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			if tok := r.URL.Query().Get("token"); tok != "" {
				authHeader = "Bearer " + tok
			} else {
				http.Error(w, "Missing authorization header", http.StatusUnauthorized)
				return
			}
		}

		// 2. Validate the token (go-starter-kit strips "Bearer " internally)
		claims, err := jwt.VerifyToken(authHeader)
		if err != nil {
			http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}

		// 3. Extract the user ID from the "sub" (subject) claim
		userID, ok := claims["sub"].(string)
		if !ok || userID == "" {
			http.Error(w, "Invalid token claims", http.StatusUnauthorized)
			return
		}

		// 4. Check if user is banned (Redis-cached for performance)
		if isUserBanned(userID) {
			http.Error(w, "Account is banned", http.StatusForbidden)
			return
		}

		// 5. Inject the user ID into the request context for downstream handlers
		ctx := context.WithValue(r.Context(), config.UserIDKey, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// isUserBanned checks if a user is banned, using Redis cache first (ban:<userId>),
// then falling back to Postgres. Since JWTs cannot be invalidated, this check runs
// on every authenticated request to enforce bans in real time.
func isUserBanned(userID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rdb := redisKit.GetRawClient()
	banKey := "ban:" + userID

	// Fast path: check Redis cache
	val, err := rdb.Get(ctx, banKey).Result()
	if err == nil {
		return val == "1"
	}

	// Slow path: check Postgres
	var isBanned bool
	err = postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT is_banned FROM users WHERE id = $1`, userID,
	).Scan(&isBanned)
	if err != nil {
		return false // fail open (user may not exist yet)
	}

	// Cache result in Redis (24h TTL for both banned and not-banned)
	if isBanned {
		rdb.Set(ctx, banKey, "1", 24*time.Hour)
	} else {
		rdb.Set(ctx, banKey, "0", 24*time.Hour)
	}

	return isBanned
}
