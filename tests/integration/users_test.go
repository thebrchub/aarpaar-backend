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
// Integration Tests — User Endpoints
// ---------------------------------------------------------------------------

func TestGetMe(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "alice", "alice@test.com")

	t.Run("success - authenticated user", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/users/me", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, _ := io.ReadAll(resp.Body)
		var data map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &data))
		assert.Equal(t, userID, data["id"])
		assert.Equal(t, "alice", data["name"])
	})

	t.Run("fail - 401 without auth token", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/users/me", nil)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("fail - 401 with invalid token", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/users/me", nil)
		req.Header.Set("Authorization", "Bearer invalid-token")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestCheckUsername(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUserWithUsername(t, "Bob", "bob@test.com", "bob123")

	t.Run("available username", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/users/check-username?username=newuser99", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, _ := io.ReadAll(resp.Body)
		var data map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &data))
		// API returns StatusMessage with status: "available" or "taken"
		assert.Equal(t, "available", data["status"])
	})

	t.Run("taken username", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/users/check-username?username=bob123", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, _ := io.ReadAll(resp.Body)
		var data map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &data))
		assert.Equal(t, "taken", data["status"])
	})
}

func TestSearchUsers(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUserWithUsername(t, "SearchUser", "search@test.com", "searchme")
	testutil.SeedUserWithUsername(t, "AnotherUser", "another@test.com", "another")

	t.Run("search existing username", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/users/search?query=searchme", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("search with SQL injection", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/users/search?query='+OR+1=1--", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		// Should NOT return 500 (which would indicate SQL error)
		assert.NotEqual(t, http.StatusInternalServerError, resp.StatusCode)
	})
}

func TestUpdateMe(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "updatable", "update@test.com")

	t.Run("update display name", func(t *testing.T) {
		body := `{"name":"Updated Name"}`
		req, _ := http.NewRequest("PATCH", srv.URL+"/api/v1/users/me", strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("no auth", func(t *testing.T) {
		body := `{"name":"Hacker"}`
		req, _ := http.NewRequest("PATCH", srv.URL+"/api/v1/users/me", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("verify update persists", func(t *testing.T) {
		// First update
		body := `{"name":"Verified Name"}`
		patchReq, _ := http.NewRequest("PATCH", srv.URL+"/api/v1/users/me", strings.NewReader(body))
		patchReq.Header.Set("Authorization", token)
		patchReq.Header.Set("Content-Type", "application/json")

		patchResp, err := http.DefaultClient.Do(patchReq)
		require.NoError(t, err)
		io.ReadAll(patchResp.Body)
		patchResp.Body.Close()
		require.Equal(t, http.StatusOK, patchResp.StatusCode)

		// Then verify via GET /me
		getReq, _ := http.NewRequest("GET", srv.URL+"/api/v1/users/me", nil)
		getReq.Header.Set("Authorization", token)

		getResp, err := http.DefaultClient.Do(getReq)
		require.NoError(t, err)
		defer getResp.Body.Close()

		assert.Equal(t, http.StatusOK, getResp.StatusCode)
		respBody, _ := io.ReadAll(getResp.Body)
		var data map[string]interface{}
		require.NoError(t, json.Unmarshal(respBody, &data))
		assert.Equal(t, "Verified Name", data["name"])
	})
}

func TestPutMe(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "putable", "putable@test.com")

	t.Run("full profile update", func(t *testing.T) {
		// username must match the existing one (immutable once set)
		body := `{"name":"Full Name","username":"putable","gender":"Male"}`
		req, _ := http.NewRequest("PUT", srv.URL+"/api/v1/users/me", strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("cannot change username", func(t *testing.T) {
		body := `{"name":"Full Name","username":"differentname","gender":"Male"}`
		req, _ := http.NewRequest("PUT", srv.URL+"/api/v1/users/me", strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusConflict, resp.StatusCode, "changing username should return 409")
	})

	t.Run("no auth", func(t *testing.T) {
		body := `{"name":"Hacker"}`
		req, _ := http.NewRequest("PUT", srv.URL+"/api/v1/users/me", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}
