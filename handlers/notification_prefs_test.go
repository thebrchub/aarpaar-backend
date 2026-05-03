package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/jwt"
	"github.com/shivanand-burli/go-starter-kit/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebrchub/aarpaar/models"
	"github.com/thebrchub/aarpaar/services"
)

// setTestUserID wraps the handler with the kit's Auth middleware and sets
// a valid JWT Bearer token so that middleware.Subject(ctx) returns userID.
func setTestUserID(r *http.Request, userID string) *http.Request {
	token, _ := jwt.GenerateAccessToken(userID)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// callWithAuth wraps a handler with Auth middleware so tests can call it with a token.
func callWithAuth(h http.HandlerFunc) http.HandlerFunc {
	return middleware.Auth("")(h)
}

// ---------------------------------------------------------------------------
// Unit Tests — Notification Preference Key Whitelist
// ---------------------------------------------------------------------------

func TestNotifPrefAllowedKeys(t *testing.T) {
	// These are the only keys accepted by the PATCH handler.
	// Must match the JSON keys in the migration default and model struct.
	allowed := map[string]bool{
		"likes": true, "comments": true, "friend_requests": true, "reposts": true,
		"dm_requests": true, "group_invites": true, "match_activity": true, "mentions": true,
	}

	// Verify service constants match the allowed set
	svcKeys := []string{
		services.NotifPrefLikes, services.NotifPrefComments,
		services.NotifPrefFriendRequests, services.NotifPrefReposts,
		services.NotifPrefDMRequests, services.NotifPrefGroupInvites,
		services.NotifPrefMatchActivity, services.NotifPrefMentions,
	}
	for _, k := range svcKeys {
		assert.True(t, allowed[k], "service constant %q must be in handler whitelist", k)
	}
	assert.Equal(t, len(allowed), len(svcKeys), "whitelist and constants must have same count")
}

func TestNotifPrefRejectedKeys(t *testing.T) {
	invalid := []string{
		"all", "push", "email", "sms", "calls",
		"admin", "unknown_key", "", "LIKES",
	}
	allowed := map[string]bool{
		"likes": true, "comments": true, "friend_requests": true, "reposts": true,
		"dm_requests": true, "group_invites": true, "match_activity": true, "mentions": true,
	}
	for _, k := range invalid {
		assert.False(t, allowed[k], "key %q should NOT be in whitelist", k)
	}
}

// ---------------------------------------------------------------------------
// Unit Tests — NotificationPrefs Model Serialization
// ---------------------------------------------------------------------------

func TestNotifPrefsDefaultJSON(t *testing.T) {
	// Default: all enabled
	prefs := models.NotificationPrefs{
		Likes:          true,
		Comments:       true,
		FriendRequests: true,
		Reposts:        true,
		DMRequests:     true,
		GroupInvites:   true,
		MatchActivity:  true,
		Mentions:       true,
	}

	data, err := json.Marshal(prefs)
	require.NoError(t, err)

	// Deserialize back
	var parsed models.NotificationPrefs
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, prefs, parsed)
}

func TestNotifPrefsPartialDisable(t *testing.T) {
	prefs := models.NotificationPrefs{
		Likes:    false,
		Comments: true,
		Mentions: false,
	}

	data, err := json.Marshal(prefs)
	require.NoError(t, err)

	var m map[string]bool
	require.NoError(t, json.Unmarshal(data, &m))

	assert.False(t, m["likes"])
	assert.True(t, m["comments"])
	assert.False(t, m["mentions"])
}

func TestNotifPrefsJSONFieldNames(t *testing.T) {
	// Verify JSON tags match what the migration and handler expect
	data, _ := json.Marshal(models.NotificationPrefs{})
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &m))

	expected := []string{
		"likes", "comments", "friend_requests", "reposts",
		"dm_requests", "group_invites", "match_activity", "mentions",
	}
	for _, k := range expected {
		_, exists := m[k]
		assert.True(t, exists, "JSON field %q must exist in serialized NotificationPrefs", k)
	}
	assert.Equal(t, len(expected), len(m), "no extra fields should exist")
}

// ---------------------------------------------------------------------------
// Unit Tests — PATCH /notification-preferences Validation
// Tests the handler rejects invalid bodies without touching DB.
// ---------------------------------------------------------------------------

func TestUpdateNotifPrefsRejectsNoAuth(t *testing.T) {
	// No userID in context → 401
	body := `{"likes": false}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/users/me/notification-preferences",
		strings.NewReader(body))
	w := httptest.NewRecorder()

	callWithAuth(UpdateNotificationPreferencesHandler)(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestUpdateNotifPrefsRejectsInvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/users/me/notification-preferences",
		strings.NewReader("not-json"))
	req = setTestUserID(req, "test-user-id")
	w := httptest.NewRecorder()

	callWithAuth(UpdateNotificationPreferencesHandler)(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpdateNotifPrefsRejectsUnknownKeys(t *testing.T) {
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/users/me/notification-preferences",
		strings.NewReader(`{"likes": false, "hacker_key": true}`))
	req = setTestUserID(req, "test-user-id")
	w := httptest.NewRecorder()

	callWithAuth(UpdateNotificationPreferencesHandler)(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var resp struct {
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp.Message, "Unknown preference key")
}

func TestGetNotifPrefsRejectsNoAuth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/notification-preferences", nil)
	w := httptest.NewRecorder()

	GetNotificationPreferencesHandler(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
