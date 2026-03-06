package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/goccy/go-json"

	"github.com/shivanand-burli/go-starter-kit/jwt"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/thebrchub/aarpaar/config"
	"google.golang.org/api/idtoken"
)

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
//
// @Summary		Google OAuth login
// @Description	Verifies a Google ID token, upserts the user, and returns JWT tokens.
// @Tags		Auth
// @Accept		json
// @Produce		json
// @Param		body	body	LoginRequest	true	"Google ID token"
// @Success		200	{object}	LoginResponse
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Failure		403	{object}	StatusMessage
// @Failure		500	{object}	StatusMessage
// @Router		/auth/google [post]
func GoogleLoginHandler(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// 1. Verify the Google ID token (checks signature + expiry)
	payload, err := idtoken.Validate(context.Background(), req.GoogleIDToken, config.GoogleClientID)
	if err != nil {
		JSONError(w, "Invalid Google Token", http.StatusUnauthorized)
		return
	}

	// 2. Safely extract user info from Google's claims
	googleID := payload.Subject

	email, ok := payload.Claims["email"].(string)
	if !ok || email == "" {
		JSONError(w, "Google token missing email claim", http.StatusBadRequest)
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
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}

	// 3.5. Check if the user is banned
	if isBanned {
		JSONError(w, "Account is banned", http.StatusForbidden)
		return
	}

	// 4. Generate JWT access and refresh tokens
	accessToken, refreshToken, err := jwt.GenerateToken(userID, nil)
	if err != nil {
		JSONError(w, "Failed to generate tokens", http.StatusInternalServerError)
		return
	}

	// 5. Return the tokens to the client
	JSONSuccess(w, LoginResponse{
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
	db := postgress.GetRawDB()
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
	err := db.QueryRowContext(ctx, query, googleID, email, name, avatar).Scan(&userID, &isBanned)
	if err != nil && err != sql.ErrNoRows {
		return "", false, err
	}
	return userID, isBanned, nil
}

// ---------------------------------------------------------------------------
// Refresh Token
// ---------------------------------------------------------------------------

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type RefreshResponse struct {
	AccessToken string `json:"access_token"`
}

// RefreshTokenHandler exchanges a valid refresh token for a new access token.
//
// @Summary		Refresh access token
// @Description	Exchanges a valid refresh token for a new access token.
// @Tags		Auth
// @Accept		json
// @Produce		json
// @Param		body	body	RefreshRequest	true	"Refresh token"
// @Success		200	{object}	RefreshResponse
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Router		/auth/refresh [post]
func RefreshTokenHandler(w http.ResponseWriter, r *http.Request) {
	var req RefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate the refresh token and get a new access token
	accessToken, _, err := jwt.RefreshToken(req.RefreshToken)
	if err != nil {
		JSONError(w, err.Error(), http.StatusUnauthorized)
		return
	}

	JSONSuccess(w, RefreshResponse{
		AccessToken: accessToken,
	})
}

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
//
// @Summary		Register device for push notifications
// @Description	Saves an FCM/APNs token. Re-assigns to current user if token already exists.
// @Tags		Auth
// @Accept		json
// @Produce		json
// @Param		body	body	DeviceRegisterRequest	true	"Device token info"
// @Success		200	{object}	StatusMessage
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Failure		500	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/auth/device [post]
func RegisterDeviceHandler(w http.ResponseWriter, r *http.Request) {
	// 1. Get the authenticated user ID from context (set by auth middleware)
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 2. Parse the request body
	var req DeviceRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Token == "" || req.DeviceType == "" {
		JSONError(w, "token and device_type are required", http.StatusBadRequest)
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
	_, err := postgress.Exec(query, userID, req.Token, req.DeviceType)
	if err != nil {
		JSONError(w, "Failed to save device token", http.StatusInternalServerError)
		return
	}

	// 4. Confirm success
	JSONMessage(w, "success", "Device registered for push notifications")
}
