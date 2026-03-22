package handlers

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/services"
)

// GetMeHandler returns the authenticated user's own profile.
func GetMeHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Cache own profile for 60s
	cacheKey := config.CacheUserMe + userID
	rdb := redis.GetRawClient()
	if cached, err := rdb.Get(r.Context(), cacheKey).Bytes(); err == nil && len(cached) > 0 {
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
		return
	}

	// Check ban status and return profile
	query := `
		SELECT COALESCE(
			(SELECT row_to_json(t)::text FROM (
				SELECT id, email, name, username, avatar_url, mobile, gender, is_private, show_last_seen, bio, created_at,
				       total_donated
				FROM users WHERE id = $1 AND is_banned = false
			) t),
			''
		)
	`

	var rawJSON string
	err := postgress.GetRawDB().QueryRow(query, userID).Scan(&rawJSON)
	if err != nil {
		log.Printf("[users] GetMe query failed user=%s: %v", userID, err)
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}
	if rawJSON == "" {
		JSONError(w, "User not found or banned", http.StatusForbidden)
		return
	}

	// Enrich with computed badge
	rawJSON = enrichWithBadge(rawJSON, userID)

	// Cache the enriched result
	rdb.Set(r.Context(), cacheKey, []byte(rawJSON), 60*time.Second)

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(rawJSON))
}

// SearchUsersHandler searches for users by username or name.
func SearchUsersHandler(w http.ResponseWriter, r *http.Request) {
	_, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	q := r.URL.Query().Get("query")
	if q == "" || len(q) > 30 {
		JSONError(w, "Query parameter is required and must be <= 30 characters", http.StatusBadRequest)
		return
	}

	// Escape LIKE metacharacters to prevent wildcard injection (DoS via pathological patterns)
	q = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)

	limit, offset := parsePagination(r)

	query := `
		SELECT COALESCE(json_agg(t), '[]')::text
		FROM (
			SELECT id, name, username, avatar_url, is_private
			FROM users
			WHERE is_banned = false
			  AND (username ILIKE '%' || $1 || '%' OR name ILIKE '%' || $1 || '%')
			LIMIT $2 OFFSET $3
		) t;
	`

	var rawJSONBytes []byte
	err := postgress.GetRawDB().QueryRow(query, q, limit, offset).Scan(&rawJSONBytes)
	if err != nil {
		log.Printf("[users] SearchUsers query failed q=%s: %v", q, err)
		JSONError(w, "Search failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(rawJSONBytes)
}

// CheckUsernameHandler checks if a username is available.
func CheckUsernameHandler(w http.ResponseWriter, r *http.Request) {
	_, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	username := r.URL.Query().Get("username")
	if username == "" || len(username) > 30 {
		JSONError(w, "Username is required and must be <= 30 characters", http.StatusBadRequest)
		return
	}

	var exists bool
	err := postgress.GetRawDB().QueryRow(
		`SELECT EXISTS(SELECT 1 FROM users WHERE username = $1)`, username,
	).Scan(&exists)
	if err != nil {
		log.Printf("[users] CheckUsername query failed username=%s: %v", username, err)
		JSONError(w, "Check failed", http.StatusInternalServerError)
		return
	}

	if exists {
		JSONMessage(w, "taken", "Username is already taken")
	} else {
		JSONMessage(w, "available", "Username is available")
	}
}

// UpdateMeHandler partially updates the authenticated user's profile.
// Only the fields provided in the body are updated (PATCH semantics).
func UpdateMeHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var body struct {
		Username     *string `json:"username"`
		Name         *string `json:"name"`
		Mobile       *string `json:"mobile"`
		Gender       *string `json:"gender"`
		AvatarURL    *string `json:"avatar_url"`
		IsPrivate    *bool   `json:"is_private"`
		ShowLastSeen *bool   `json:"show_last_seen"`
		Bio          *string `json:"bio"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if body.Username == nil && body.Name == nil && body.Mobile == nil && body.Gender == nil && body.AvatarURL == nil && body.IsPrivate == nil && body.ShowLastSeen == nil && body.Bio == nil {
		JSONError(w, "Nothing to update", http.StatusBadRequest)
		return
	}

	// Username is immutable once set — reject attempts to change it
	if body.Username != nil {
		var existingUsername *string
		err := postgress.GetRawDB().QueryRow(
			`SELECT username FROM users WHERE id = $1`, userID,
		).Scan(&existingUsername)
		if err != nil {
			JSONError(w, "Failed to verify username", http.StatusInternalServerError)
			return
		}
		if existingUsername != nil && *existingUsername != "" {
			JSONError(w, "Username cannot be changed once set", http.StatusConflict)
			return
		}
	}

	// Build dynamic SET clause
	sets := []string{}
	args := []interface{}{}
	i := 1

	if body.Username != nil {
		sets = append(sets, fmt.Sprintf("username = $%d", i))
		args = append(args, *body.Username)
		i++
	}
	if body.Name != nil {
		sets = append(sets, fmt.Sprintf("name = $%d", i))
		args = append(args, *body.Name)
		i++
	}
	if body.Mobile != nil {
		sets = append(sets, fmt.Sprintf("mobile = $%d", i))
		args = append(args, *body.Mobile)
		i++
	}
	if body.Gender != nil {
		sets = append(sets, fmt.Sprintf("gender = $%d", i))
		args = append(args, *body.Gender)
		i++
	}
	if body.AvatarURL != nil {
		sets = append(sets, fmt.Sprintf("avatar_url = $%d", i))
		args = append(args, *body.AvatarURL)
		i++
	}
	if body.IsPrivate != nil {
		sets = append(sets, fmt.Sprintf("is_private = $%d", i))
		args = append(args, *body.IsPrivate)
		i++
	}
	if body.ShowLastSeen != nil {
		sets = append(sets, fmt.Sprintf("show_last_seen = $%d", i))
		args = append(args, *body.ShowLastSeen)
		i++
	}
	if body.Bio != nil {
		limits := services.GetArenaLimits()
		maxBio := limits.FreeBioLength
		if IsUserVIP(r.Context(), userID) {
			maxBio = limits.MaxBioLength
		}
		if len(*body.Bio) > maxBio {
			JSONError(w, fmt.Sprintf("Bio too long (max %d chars)", maxBio), http.StatusBadRequest)
			return
		}
		sets = append(sets, fmt.Sprintf("bio = $%d", i))
		args = append(args, *body.Bio)
		i++
	}

	args = append(args, userID)
	query := fmt.Sprintf(`
		WITH updated AS (
			UPDATE users SET %s
			WHERE id = $%d AND is_banned = false
			RETURNING id, email, name, username, avatar_url, mobile, gender, is_private, show_last_seen, bio, created_at
		)
		SELECT COALESCE(
			(SELECT row_to_json(updated)::text FROM updated),
			''
		)
	`, strings.Join(sets, ", "), i)

	var rawJSON string
	err := postgress.GetRawDB().QueryRow(query, args...).Scan(&rawJSON)
	if err != nil || rawJSON == "" {
		log.Printf("[UpdateMe] DB error: %v", err)
		JSONError(w, "Update failed", http.StatusInternalServerError)
		return
	}

	redis.GetRawClient().Del(r.Context(), config.CacheUserMe+userID)

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(rawJSON))
}

// PutMeHandler replaces the authenticated user's profile fields entirely.
// Both username and name are required in the body (PUT semantics).
func PutMeHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var body struct {
		Username     string  `json:"username"`
		Name         string  `json:"name"`
		Mobile       *string `json:"mobile"`
		Gender       *string `json:"gender"`
		IsPrivate    *bool   `json:"is_private"`
		ShowLastSeen *bool   `json:"show_last_seen"`
		Bio          *string `json:"bio"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if body.Username == "" || body.Name == "" {
		JSONError(w, "Both username and name are required", http.StatusBadRequest)
		return
	}

	// Username is immutable once set — reject attempts to change it
	var existingUsername *string
	err := postgress.GetRawDB().QueryRow(
		`SELECT username FROM users WHERE id = $1`, userID,
	).Scan(&existingUsername)
	if err != nil {
		JSONError(w, "Failed to verify username", http.StatusInternalServerError)
		return
	}
	if existingUsername != nil && *existingUsername != "" && *existingUsername != body.Username {
		JSONError(w, "Username cannot be changed once set", http.StatusConflict)
		return
	}

	isPrivate := false
	if body.IsPrivate != nil {
		isPrivate = *body.IsPrivate
	}

	showLastSeen := true
	if body.ShowLastSeen != nil {
		showLastSeen = *body.ShowLastSeen
	}

	mobile := ""
	if body.Mobile != nil {
		mobile = *body.Mobile
	}

	gender := "Any"
	if body.Gender != nil {
		gender = *body.Gender
	}

	bio := ""
	if body.Bio != nil {
		limits := services.GetArenaLimits()
		maxBio := limits.FreeBioLength
		if IsUserVIP(r.Context(), userID) {
			maxBio = limits.MaxBioLength
		}
		if len(*body.Bio) > maxBio {
			JSONError(w, fmt.Sprintf("Bio too long (max %d chars)", maxBio), http.StatusBadRequest)
			return
		}
		bio = *body.Bio
	}

	query := `
		WITH updated AS (
			UPDATE users SET username = $1, name = $2, is_private = $3, mobile = $4, gender = $5, show_last_seen = $6, bio = $7
			WHERE id = $8 AND is_banned = false
			RETURNING id, email, name, username, avatar_url, mobile, gender, is_private, show_last_seen, bio, created_at
		)
		SELECT COALESCE(
			(SELECT row_to_json(updated)::text FROM updated),
			''
		)
	`

	var rawJSON string
	err = postgress.GetRawDB().QueryRow(query, body.Username, body.Name, isPrivate, mobile, gender, showLastSeen, bio, userID).Scan(&rawJSON)
	if err != nil || rawJSON == "" {
		log.Printf("[PutMe] DB error: %v", err)
		JSONError(w, "Update failed", http.StatusInternalServerError)
		return
	}

	redis.GetRawClient().Del(r.Context(), config.CacheUserMe+userID)

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(rawJSON))
}

// ---------------------------------------------------------------------------
// Badge Enrichment
// ---------------------------------------------------------------------------

// enrichWithBadge takes a raw JSON user profile string and adds a "badge" field
// computed from total_donated and badge_tiers. Returns the enriched JSON string.
func enrichWithBadge(rawJSON string, userID string) string {
	var profile map[string]interface{}
	if err := json.Unmarshal([]byte(rawJSON), &profile); err != nil {
		return rawJSON
	}

	totalDonated, _ := profile["total_donated"].(float64)

	if totalDonated > 0 {
		badge := computeBadgeFromDB(totalDonated)
		if badge != nil {
			profile["badge"] = badge
		}
	}

	enriched, err := json.Marshal(profile)
	if err != nil {
		return rawJSON
	}
	return string(enriched)
}

// ---------------------------------------------------------------------------
// GET /api/v1/users/me/notification-preferences
// ---------------------------------------------------------------------------

func GetNotificationPreferencesHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Cache notification preferences for 30s (rarely changes, frequently read)
	cacheKey := config.CacheUserNotifP + userID
	rdb := redis.GetRawClient()
	if cached, err := rdb.Get(r.Context(), cacheKey).Bytes(); err == nil && len(cached) > 0 {
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
		return
	}

	var prefsJSON []byte
	err := postgress.GetRawDB().QueryRow(
		`SELECT notification_prefs FROM users WHERE id = $1`, userID,
	).Scan(&prefsJSON)
	if err != nil {
		log.Printf("[users] GetNotificationPreferences failed user=%s: %v", userID, err)
		JSONError(w, "Failed to load preferences", http.StatusInternalServerError)
		return
	}

	rdb.Set(r.Context(), cacheKey, prefsJSON, config.CacheTTLMedium)

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(prefsJSON)
}

// ---------------------------------------------------------------------------
// PATCH /api/v1/users/me/notification-preferences
// Body: { "likes": false, "comments": true, ... } (partial update)
// ---------------------------------------------------------------------------

func UpdateNotificationPreferencesHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var incoming map[string]bool
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Whitelist allowed keys to prevent injection of arbitrary JSON fields
	allowed := map[string]bool{
		"likes": true, "comments": true, "friend_requests": true, "reposts": true,
		"dm_requests": true, "group_invites": true, "match_activity": true, "mentions": true,
	}
	for k := range incoming {
		if !allowed[k] {
			JSONError(w, "Unknown preference key: "+k, http.StatusBadRequest)
			return
		}
	}

	// Merge with jsonb_set is fragile for multiple keys; instead use || merge
	patchJSON, err := json.Marshal(incoming)
	if err != nil {
		JSONError(w, "Invalid preferences", http.StatusBadRequest)
		return
	}

	var updatedPrefs []byte
	err = postgress.GetRawDB().QueryRow(
		`UPDATE users SET notification_prefs = notification_prefs || $1::jsonb
		 WHERE id = $2 RETURNING notification_prefs`,
		patchJSON, userID,
	).Scan(&updatedPrefs)
	if err != nil {
		log.Printf("[users] UpdateNotificationPreferences failed user=%s: %v", userID, err)
		JSONError(w, "Failed to update preferences", http.StatusInternalServerError)
		return
	}

	// Invalidate cached preferences
	services.InvalidateNotifPrefsCache(r.Context(), userID)
	redis.GetRawClient().Del(r.Context(), config.CacheUserNotifP+userID)

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(updatedPrefs)
}
