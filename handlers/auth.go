package handlers

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/goccy/go-json"

	"github.com/jackc/pgx/v5"
	"github.com/shivanand-burli/go-starter-kit/helper"
	"github.com/shivanand-burli/go-starter-kit/jwt"
	"github.com/shivanand-burli/go-starter-kit/middleware"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/services"
	"google.golang.org/api/idtoken"
)

// IDTokenValidator is set at startup with a tuned HTTP client.
var IDTokenValidator *idtoken.Validator

// ---------------------------------------------------------------------------
// Request / Response Types
// ---------------------------------------------------------------------------

type LoginRequest struct {
	GoogleIDToken string `json:"google_id_token"`
}

type LoginResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	UserID       string `json:"user_id"`
}

// GoogleLoginHandler verifies a Google ID token, creates or updates the
// user in Postgres, and returns JWT access + refresh tokens.
func GoogleLoginHandler(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := helper.ReadJSON(r, &req); err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// 1. Verify the Google ID token (checks signature + expiry)
	payload, err := IDTokenValidator.Validate(context.Background(), req.GoogleIDToken, config.GoogleClientID)
	if err != nil {
		log.Printf("[auth] Google token validation failed: %v", err)
		helper.Error(w, http.StatusUnauthorized, "Invalid Google Token")
		return
	}

	// 2. Safely extract user info from Google's claims
	googleID := payload.Subject

	email, ok := payload.Claims["email"].(string)
	if !ok || email == "" {
		helper.Error(w, http.StatusBadRequest, "Google token missing email claim")
		return
	}

	name, ok := payload.Claims["name"].(string)
	if !ok || name == "" {
		name = email // Fallback: use email as display name
	}

	picture := ""
	if pic, ok := payload.Claims["picture"].(string); ok {
		picture = pic
	}

	// 3. Create or update the user in Postgres
	userID, isBanned, err := upsertUser(googleID, email, name, picture)
	if err != nil {
		log.Printf("[auth] upsertUser failed email=%s: %v", email, err)
		helper.Error(w, http.StatusInternalServerError, "Database error")
		return
	}

	// 3.5. Check if the user is banned
	if isBanned {
		helper.Error(w, http.StatusForbidden, "Account is banned")
		return
	}

	// 4. Generate JWT access and refresh tokens
	accessToken, refreshToken, err := jwt.GenerateToken(userID, nil)
	if err != nil {
		log.Printf("[auth] GenerateToken failed user=%s: %v", userID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to generate tokens")
		return
	}

	// 5. Set auth cookies (mobile ignores these, web uses them)
	middleware.SetAuthCookies(w, "", accessToken, refreshToken)

	// 6. Return the tokens to the client
	helper.JSON(w, http.StatusOK, LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		UserID:       userID,
	})
}

// ---------------------------------------------------------------------------
// upsertUser inserts a new user or updates an existing one (matched by google_id).
// Returns the user's UUID from Postgres.
// ---------------------------------------------------------------------------

func upsertUser(googleID, email, name, avatar string) (string, bool, error) {
	db := postgress.GetPool()
	var userID string
	var isBanned bool

	// ON CONFLICT requires a UNIQUE constraint on the google_id column.
	// Username is NULL on first signup — the user sets it later via PATCH/PUT.
	// Preserve user-set name: only overwrite if current name is empty/NULL.
	query := `
		INSERT INTO users (google_id, email, name, avatar_url)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (google_id) DO UPDATE 
		SET email = EXCLUDED.email,
		    name = CASE WHEN users.name = '' OR users.name IS NULL THEN EXCLUDED.name ELSE users.name END,
		    avatar_url = CASE WHEN users.avatar_url = '' OR users.avatar_url IS NULL THEN EXCLUDED.avatar_url ELSE users.avatar_url END,
		    updated_at = NOW()
		RETURNING id, is_banned;
	`
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.PGTimeout)*time.Second)
	defer cancel()
	err := db.QueryRow(ctx, query, googleID, email, name, avatar).Scan(&userID, &isBanned)
	if err != nil && err != pgx.ErrNoRows {
		return "", false, err
	}
	return userID, isBanned, nil
}

// ---------------------------------------------------------------------------
// Refresh Token
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Device Registration (Push Notifications)
// ---------------------------------------------------------------------------

type DeviceRegisterRequest struct {
	Token      string `json:"token"`
	DeviceType string `json:"device_type"` // e.g., "android", "ios", "web"
}

// RegisterDeviceHandler saves an FCM/APNs token for push notifications.
// If the same token already exists (e.g. phone handed to a new person),
// it re-assigns the token to the current user.
func RegisterDeviceHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// 1. Get the authenticated user ID from context (set by auth middleware)
	userID := middleware.Subject(r.Context())
	if userID == "" {
		helper.Error(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	// 2. Parse the request body
	var req DeviceRegisterRequest
	if err := helper.ReadJSON(r, &req); err != nil {
		helper.Error(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Token == "" || req.DeviceType == "" {
		helper.Error(w, http.StatusBadRequest, "token and device_type are required")
		return
	}

	// 3. Upsert the device token in Postgres
	query := `
		INSERT INTO device_tokens (user_id, token, device_type, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (token) DO UPDATE 
		SET user_id = EXCLUDED.user_id, 
		    device_type = EXCLUDED.device_type, 
		    updated_at = NOW();
	`
	_, err := postgress.Exec(ctx, query, userID, req.Token, req.DeviceType)
	if err != nil {
		log.Printf("[auth] RegisterDevice failed user=%s: %v", userID, err)
		helper.Error(w, http.StatusInternalServerError, "Failed to save device token")
		return
	}

	// Invalidate the device token cache so the next push picks up the new token
	cacheCtx, cacheCancel := context.WithTimeout(r.Context(), config.RedisOpTimeout)
	redis.GetRawClient().Del(cacheCtx, config.CachePushTokens+userID)
	cacheCancel()

	// 4. Confirm success
	helper.JSON(w, http.StatusOK, map[string]string{"status": "success", "message": "Device registered for push notifications"})
}

// GetFirebaseConfigHandler returns the public Firebase web SDK configuration.
// This is non-sensitive data that frontends need to initialize Firebase Messaging
// and obtain an FCM token for push notifications.
func GetFirebaseConfigHandler(w http.ResponseWriter, r *http.Request) {
	cfg, err := services.GetPublicConfig()
	if err != nil {
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{})
		return
	}
	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(cfg)
}
