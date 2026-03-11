package integration

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/tests/testutil"
)

// singleConnClient returns an HTTP client that reuses a single TCP connection.
// This ensures all requests share the same RemoteAddr (IP:port), which is
// required for rate limiter tests since go-starter-kit keys on RemoteAddr.
func singleConnClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			MaxConnsPerHost: 1,
		},
	}
}

// doAndDrain performs the request, drains the body (so the connection is
// returned to the pool for reuse), and returns the status code.
func doAndDrain(t *testing.T, client *http.Client, req *http.Request) int {
	t.Helper()
	resp, err := client.Do(req)
	require.NoError(t, err)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

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
// Security Tests — Rate Limiting
// ---------------------------------------------------------------------------

func TestGlobalRateLimit(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "ratelimit", "ratelimit@test.com")
	client := singleConnClient()

	// The global limiter uses config.RateLimitBurst as its burst.
	// Exceeding the burst in a single burst should produce 429s.
	totalRequests := config.RateLimitBurst + 5
	var got429 bool
	for i := 0; i < totalRequests; i++ {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/rooms", nil)
		req.Header.Set("Authorization", token)

		if doAndDrain(t, client, req) == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	assert.True(t, got429, "expected 429 after exceeding global rate limit burst of %d", config.RateLimitBurst)
}

func TestAuthEndpointRateLimit(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	client := singleConnClient()

	// Auth endpoints have a tighter limiter: 2 req/sec, burst 5.
	const authBurst = 5
	totalRequests := authBurst + 3
	var got429 bool
	for i := 0; i < totalRequests; i++ {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/auth/google", strings.NewReader(`{"token":"fake"}`))
		req.Header.Set("Content-Type", "application/json")

		if doAndDrain(t, client, req) == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	assert.True(t, got429, "expected 429 on auth endpoint after exceeding burst of %d", authBurst)
}

func TestHealthEndpointRateLimit(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	client := singleConnClient()

	// Health endpoint has its own limiter: 2 req/sec, burst 3.
	const healthBurst = 3
	totalRequests := healthBurst + 3
	var got429 bool
	for i := 0; i < totalRequests; i++ {
		req, _ := http.NewRequest("GET", srv.URL+"/health", nil)

		if doAndDrain(t, client, req) == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	assert.True(t, got429, "expected 429 on /health after exceeding burst of %d", healthBurst)
}

func TestWebhookBypassesRateLimit(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	client := singleConnClient()

	// Webhook routes bypass rate limiting entirely.
	// Send many requests — none should return 429.
	for i := 0; i < 30; i++ {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/webhook/razorpay", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")

		status := doAndDrain(t, client, req)
		assert.NotEqual(t, http.StatusTooManyRequests, status,
			"webhook request %d should not be rate limited", i+1)
	}
}

func TestRateLimitPerIP(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "rlperip", "rlperip@test.com")
	client := singleConnClient()

	// Exhaust global rate limit burst
	for i := 0; i < config.RateLimitBurst+5; i++ {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/rooms", nil)
		req.Header.Set("Authorization", token)
		doAndDrain(t, client, req)
	}

	// After exhausting, subsequent requests should be rate-limited.
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/rooms", nil)
	req.Header.Set("Authorization", token)

	assert.Equal(t, http.StatusTooManyRequests, doAndDrain(t, client, req),
		"should be rate limited after exhausting burst")
}

func TestConcurrentRateLimit(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "concrl", "concrl@test.com")
	client := singleConnClient()

	// First exhaust the burst sequentially to avoid connection pooling issues
	for i := 0; i < config.RateLimitBurst; i++ {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/rooms", nil)
		req.Header.Set("Authorization", token)
		doAndDrain(t, client, req)
	}

	// Now send concurrent requests — all should get 429
	const numRequests = 10
	results := make([]int, numRequests)
	var wg sync.WaitGroup

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req, _ := http.NewRequest("GET", srv.URL+"/api/v1/rooms", nil)
			req.Header.Set("Authorization", token)
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			results[idx] = resp.StatusCode
		}(i)
	}
	wg.Wait()

	var count429 int
	for _, code := range results {
		if code == http.StatusTooManyRequests {
			count429++
		}
	}
	assert.Greater(t, count429, 0, "requests after burst exhaustion should trigger 429")
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
