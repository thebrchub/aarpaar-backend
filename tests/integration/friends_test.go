package integration

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebrchub/aarpaar/tests/testutil"
)

// ---------------------------------------------------------------------------
// Integration Tests — Friends Endpoints
// ---------------------------------------------------------------------------

func TestGetFriends(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID1, token1 := testutil.SeedUser(t, "frienduser1", "friend1@test.com")
	userID2, _ := testutil.SeedUser(t, "frienduser2", "friend2@test.com")

	t.Run("no friends", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/friends", nil)
		req.Header.Set("Authorization", token1)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("with friends", func(t *testing.T) {
		testutil.SeedFriendship(t, userID1, userID2)

		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/friends", nil)
		req.Header.Set("Authorization", token1)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, _ := io.ReadAll(resp.Body)
		var friends []map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &friends))
		assert.GreaterOrEqual(t, len(friends), 1)
	})
}

func TestSendFriendRequest(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token1 := testutil.SeedUserWithUsername(t, "requester", "requester@test.com", "requester")
	_, _ = testutil.SeedUserWithUsername(t, "receiver", "receiver@test.com", "receiver")

	t.Run("send to valid user", func(t *testing.T) {
		body := `{"username":"receiver"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/friends/request", strings.NewReader(body))
		req.Header.Set("Authorization", token1)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("no auth", func(t *testing.T) {
		body := `{"username":"receiver"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/friends/request", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestGetFriendRequests(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "reqcheck", "reqcheck@test.com")

	t.Run("get pending requests", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/friends/requests", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestRemoveFriend(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID1, token1 := testutil.SeedUserWithUsername(t, "remover", "remover@test.com", "remover")
	userID2, _ := testutil.SeedUserWithUsername(t, "removee", "removee@test.com", "removee")

	// Create friendship first
	testutil.SeedFriendship(t, userID1, userID2)

	t.Run("remove existing friend", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", srv.URL+"/api/v1/friends/removee", nil)
		req.Header.Set("Authorization", token1)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("remove non-friend", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", srv.URL+"/api/v1/friends/nonexistent", nil)
		req.Header.Set("Authorization", token1)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}
