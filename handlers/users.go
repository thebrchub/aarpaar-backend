package handlers

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// GetMeHandler returns the authenticated user's own profile.
//
// GET /api/v1/users/me (requires auth)
// ---------------------------------------------------------------------------

func GetMeHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Check ban status and return profile
	query := `
		SELECT COALESCE(
			(SELECT row_to_json(t)::text FROM (
				SELECT id, email, name, username, avatar_url, is_private, created_at
				FROM users WHERE id = $1 AND is_banned = false
			) t),
			''
		)
	`

	var rawJSON string
	err := postgress.GetRawDB().QueryRow(query, userID).Scan(&rawJSON)
	if err != nil || rawJSON == "" {
		JSONError(w, "User not found or banned", http.StatusForbidden)
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(rawJSON))
}

// ---------------------------------------------------------------------------
// SearchUsersHandler searches for users by username or name.
//
// GET /api/v1/users/search?query=srikanth (requires auth)
// ---------------------------------------------------------------------------

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

	query := `
		SELECT COALESCE(json_agg(t), '[]')::text
		FROM (
			SELECT id, name, username, avatar_url
			FROM users
			WHERE is_banned = false
			  AND (username ILIKE '%' || $1 || '%' OR name ILIKE '%' || $1 || '%')
			LIMIT 20
		) t;
	`

	var rawJSONBytes []byte
	err := postgress.GetRawDB().QueryRow(query, q).Scan(&rawJSONBytes)
	if err != nil {
		JSONError(w, "Search failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(rawJSONBytes)
}

// ---------------------------------------------------------------------------
// UpdateMeHandler partially updates the authenticated user's profile.
// Only the fields provided in the body are updated (PATCH semantics).
//
// PATCH /api/v1/users/me (requires auth)
// Body: { "username": "ninja" } or { "name": "New Name" } or both
// ---------------------------------------------------------------------------

func UpdateMeHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var body struct {
		Username  *string `json:"username"`
		Name      *string `json:"name"`
		IsPrivate *bool   `json:"is_private"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if body.Username == nil && body.Name == nil && body.IsPrivate == nil {
		JSONError(w, "Nothing to update", http.StatusBadRequest)
		return
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
	if body.IsPrivate != nil {
		sets = append(sets, fmt.Sprintf("is_private = $%d", i))
		args = append(args, *body.IsPrivate)
		i++
	}

	args = append(args, userID)
	query := fmt.Sprintf(`
		WITH updated AS (
			UPDATE users SET %s
			WHERE id = $%d AND is_banned = false
			RETURNING id, email, name, username, avatar_url, is_private, created_at
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

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(rawJSON))
}

// ---------------------------------------------------------------------------
// PutMeHandler replaces the authenticated user's profile fields entirely.
// Both username and name are required in the body (PUT semantics).
//
// PUT /api/v1/users/me (requires auth)
// Body: { "username": "ninja", "name": "Ninja Coder" }
// ---------------------------------------------------------------------------

func PutMeHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var body struct {
		Username  string `json:"username"`
		Name      string `json:"name"`
		IsPrivate *bool  `json:"is_private"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		JSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if body.Username == "" || body.Name == "" {
		JSONError(w, "Both username and name are required", http.StatusBadRequest)
		return
	}

	isPrivate := false
	if body.IsPrivate != nil {
		isPrivate = *body.IsPrivate
	}

	query := `
		WITH updated AS (
			UPDATE users SET username = $1, name = $2, is_private = $3
			WHERE id = $4 AND is_banned = false
			RETURNING id, email, name, username, avatar_url, is_private, created_at
		)
		SELECT COALESCE(
			(SELECT row_to_json(updated)::text FROM updated),
			''
		)
	`

	var rawJSON string
	err := postgress.GetRawDB().QueryRow(query, body.Username, body.Name, isPrivate, userID).Scan(&rawJSON)
	if err != nil || rawJSON == "" {
		log.Printf("[PutMe] DB error: %v", err)
		JSONError(w, "Update failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(rawJSON))
}
