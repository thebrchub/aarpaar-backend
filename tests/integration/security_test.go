package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebrchub/aarpaar/tests/testutil"
)

// ---------------------------------------------------------------------------
// Security Tests — SQL Injection Prevention
// ---------------------------------------------------------------------------

func TestSQLInjection(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "sqliuser", "sqli@test.com")

	tests := []struct {
		name     string
		endpoint string
		param    string
		value    string
	}{
		{"search users DROP TABLE", "/api/v1/users/search", "query", "'; DROP TABLE users;--"},
		{"search users OR 1=1", "/api/v1/users/search", "query", "\" OR 1=1--"},
		{"check username injection", "/api/v1/users/check-username", "username", "admin';--"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", srv.URL+tt.endpoint+"?"+tt.param+"="+tt.value, nil)
			req.Header.Set("Authorization", token)

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			// Should NOT be 500 (which would indicate SQL error)
			assert.NotEqual(t, http.StatusInternalServerError, resp.StatusCode,
				"SQL injection attempt should not cause server error")
		})
	}
}

// ---------------------------------------------------------------------------
// Security Tests — CORS
// ---------------------------------------------------------------------------

func TestCORS(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	t.Run("preflight OPTIONS", func(t *testing.T) {
		req, _ := http.NewRequest("OPTIONS", srv.URL+"/api/v1/users/me", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		req.Header.Set("Access-Control-Request-Method", "GET")
		req.Header.Set("Access-Control-Request-Headers", "Authorization")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		// CORS origin is "*" in test mode, so should be allowed
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		assert.NotEmpty(t, resp.Header.Get("Access-Control-Allow-Origin"))
	})
}

// ---------------------------------------------------------------------------
// Security Tests — Body Limit
// ---------------------------------------------------------------------------

func TestBodyLimit(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "bodylimit", "bodylimit@test.com")

	t.Run("oversized body rejected", func(t *testing.T) {
		// Send body > 1 MB
		largeBody := strings.Repeat("A", 2*1024*1024) // 2 MB
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups", strings.NewReader(largeBody))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		// Should be 400 or 413 — handler will get an error when reading body
		assert.True(t, resp.StatusCode >= 400, "oversized body should be rejected, got %d", resp.StatusCode)
	})
}

// ---------------------------------------------------------------------------
// Security Tests — Banned User Access
// ---------------------------------------------------------------------------

func TestBannedUserAccess(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "banned", "banned@test.com")
	testutil.SeedBan(t, userID)

	endpoints := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/users/me"},
		{"GET", "/api/v1/rooms"},
		{"GET", "/api/v1/friends"},
		{"GET", "/api/v1/calls/config"},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			req, _ := http.NewRequest(ep.method, srv.URL+ep.path, nil)
			req.Header.Set("Authorization", token)

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusForbidden, resp.StatusCode,
				"banned user should get 403 on %s %s", ep.method, ep.path)
		})
	}
}

// ---------------------------------------------------------------------------
// Security Tests — Auth Header Validation
// ---------------------------------------------------------------------------

func TestAuthHeaderVariations(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	tests := []struct {
		name string
		auth string
		want int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"invalid Bearer", "Bearer invalid", http.StatusUnauthorized},
		{"empty Bearer", "Bearer ", http.StatusUnauthorized},
		{"wrong scheme", "Basic dXNlcjpwYXNz", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", srv.URL+"/api/v1/users/me", nil)
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tt.want, resp.StatusCode)
		})
	}
}
