package integration

import (
	"io"
	"net/http"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebrchub/aarpaar/tests/testutil"
)

// ---------------------------------------------------------------------------
// Integration Tests — Health & Online Endpoints
// ---------------------------------------------------------------------------

func TestHealthCheck(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	req, _ := http.NewRequest("GET", srv.URL+"/health", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestOnlineCount(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	// /online is public — no auth needed
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/online", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// ---------------------------------------------------------------------------
// Integration Tests — Calls Endpoints
// ---------------------------------------------------------------------------

func TestCallConfig(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "calluser", "calluser@test.com")

	t.Run("get call config", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/calls/config", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, _ := io.ReadAll(resp.Body)
		var data map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &data))

		// ICE servers should always be present
		iceServers, ok := data["iceServers"]
		assert.True(t, ok, "iceServers should be present")
		assert.NotNil(t, iceServers)
	})

	t.Run("livekit not exposed when group calls disabled", func(t *testing.T) {
		// GROUP_CALLS_ENABLED defaults to "false" in test env
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/calls/config", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, _ := io.ReadAll(resp.Body)
		var data map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &data))

		// LiveKit should NOT be present when group calls are disabled
		_, hasLiveKit := data["livekit"]
		assert.False(t, hasLiveKit, "livekit config should NOT be exposed when group calls are disabled")
	})

	t.Run("no auth", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/calls/config", nil)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestCallHistory(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "historyuser", "history@test.com")

	t.Run("get call history", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/calls/history", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// ---------------------------------------------------------------------------
// Integration Tests — Matchmaking Endpoints
// ---------------------------------------------------------------------------

func TestMatchmaking(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "matcher", "matcher@test.com")

	t.Run("enter queue", func(t *testing.T) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/match/enter", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("leave queue", func(t *testing.T) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/match/leave", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("no auth", func(t *testing.T) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/match/enter", nil)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

// ---------------------------------------------------------------------------
// Integration Tests — Donation Endpoints
// ---------------------------------------------------------------------------

func TestDonationEndpoints(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "donor", "donor@test.com")

	t.Run("get badge tiers", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/badges/tiers", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("get donation history", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/donate/history", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// ---------------------------------------------------------------------------
// Integration Tests — Leaderboard
// ---------------------------------------------------------------------------

func TestLeaderboard(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "leaderuser", "leader@test.com")

	// Use alltime scope to avoid needing app_settings for monthly reset
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/leaderboard?scope=alltime", nil)
	req.Header.Set("Authorization", token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// BUG-001 fixed: u.picture → u.avatar_url — should always return 200 now.
	assert.Equal(t, http.StatusOK, resp.StatusCode, "leaderboard should return 200")
}
