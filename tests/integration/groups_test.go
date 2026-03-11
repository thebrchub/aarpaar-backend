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
// Integration Tests — Group Endpoints
// ---------------------------------------------------------------------------

func TestCreateGroup(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "groupcreator", "groupcreator@test.com")

	t.Run("create public group", func(t *testing.T) {
		body := `{"name":"Test Group","visibility":"public"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups", strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Contains(t, []int{http.StatusOK, http.StatusCreated}, resp.StatusCode)

		respBody, _ := io.ReadAll(resp.Body)
		var data map[string]interface{}
		require.NoError(t, json.Unmarshal(respBody, &data))
		assert.Equal(t, "Test Group", data["name"])
	})

	t.Run("create private group", func(t *testing.T) {
		body := `{"name":"Private Group","visibility":"private"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups", strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Contains(t, []int{http.StatusOK, http.StatusCreated}, resp.StatusCode)
	})

	t.Run("no auth", func(t *testing.T) {
		body := `{"name":"Hacked Group"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestListGroups(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "grouplist", "grouplist@test.com")

	t.Run("list user groups", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/groups", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// createGroupHelper creates a group via the API and returns its room ID.
func createGroupHelper(t *testing.T, srvURL, token, name, visibility string) string {
	t.Helper()
	body := `{"name":"` + name + `","visibility":"` + visibility + `"}`
	req, _ := http.NewRequest("POST", srvURL+"/api/v1/groups", strings.NewReader(body))
	req.Header.Set("Authorization", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, resp.StatusCode)

	respBody, _ := io.ReadAll(resp.Body)
	var data map[string]interface{}
	require.NoError(t, json.Unmarshal(respBody, &data))

	// The field is "roomId" in the response
	if rid, ok := data["roomId"].(string); ok {
		return rid
	}
	if rid, ok := data["room_id"].(string); ok {
		return rid
	}
	if rid, ok := data["id"].(string); ok {
		return rid
	}
	t.Fatalf("no room_id in create group response: %s", string(respBody))
	return ""
}

func TestGetGroup(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "getgroup", "getgroup@test.com")
	groupID := createGroupHelper(t, srv.URL, token, "GetMe Group", "public")

	t.Run("get existing group", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/groups/"+groupID, nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("get non-existent group", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/groups/00000000-0000-0000-0000-000000000000", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.True(t, resp.StatusCode >= 400, "non-existent group should return error")
	})
}

func TestUpdateGroup(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, ownerToken := testutil.SeedUser(t, "groupowner", "groupowner@test.com")
	_, nonMemberToken := testutil.SeedUser(t, "nonmember", "nonmember@test.com")
	groupID := createGroupHelper(t, srv.URL, ownerToken, "Updatable Group", "public")

	t.Run("owner updates group name", func(t *testing.T) {
		body := `{"name":"Renamed Group"}`
		req, _ := http.NewRequest("PATCH", srv.URL+"/api/v1/groups/"+groupID, strings.NewReader(body))
		req.Header.Set("Authorization", ownerToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("non-member cannot update", func(t *testing.T) {
		body := `{"name":"Hacked"}`
		req, _ := http.NewRequest("PATCH", srv.URL+"/api/v1/groups/"+groupID, strings.NewReader(body))
		req.Header.Set("Authorization", nonMemberToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.True(t, resp.StatusCode >= 400, "non-member should not update group")
	})
}

func TestDeleteGroup(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, ownerToken := testutil.SeedUser(t, "delowner", "delowner@test.com")
	_, otherToken := testutil.SeedUser(t, "delother", "delother@test.com")
	groupID := createGroupHelper(t, srv.URL, ownerToken, "Deletable Group", "public")

	t.Run("non-creator cannot delete", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", srv.URL+"/api/v1/groups/"+groupID, nil)
		req.Header.Set("Authorization", otherToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.True(t, resp.StatusCode >= 400, "non-creator should not delete group")
	})

	t.Run("creator deletes group", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", srv.URL+"/api/v1/groups/"+groupID, nil)
		req.Header.Set("Authorization", ownerToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestJoinPublicGroup(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, creatorToken := testutil.SeedUser(t, "joincreator", "joincreator@test.com")
	_, joinerToken := testutil.SeedUser(t, "joiner", "joiner@test.com")
	groupID := createGroupHelper(t, srv.URL, creatorToken, "Joinable Group", "public")

	t.Run("join public group", func(t *testing.T) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups/"+groupID+"/join", nil)
		req.Header.Set("Authorization", joinerToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// ---------------------------------------------------------------------------
// Integration Tests — Group Calls Disabled by Default
// ---------------------------------------------------------------------------

func TestGroupCallsDisabledByDefault(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, creatorToken := testutil.SeedUser(t, "callcreator", "callcreator@test.com")
	groupID := createGroupHelper(t, srv.URL, creatorToken, "Call Group", "public")

	t.Run("start group call returns 403", func(t *testing.T) {
		body := `{"call_type":"video"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups/"+groupID+"/calls", strings.NewReader(body))
		req.Header.Set("Authorization", creatorToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode, "group calls should be disabled by default")
	})

	t.Run("join group call returns 403", func(t *testing.T) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups/"+groupID+"/calls/fake-call-id/join", nil)
		req.Header.Set("Authorization", creatorToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("leave group call returns 403", func(t *testing.T) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups/"+groupID+"/calls/fake-call-id/leave", nil)
		req.Header.Set("Authorization", creatorToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("get group call status returns 403", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/groups/"+groupID+"/calls/fake-call-id", nil)
		req.Header.Set("Authorization", creatorToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("mute participant returns 403", func(t *testing.T) {
		body := `{"userId":"someone","trackType":"audio","muted":true}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups/"+groupID+"/calls/fake-call-id/mute", strings.NewReader(body))
		req.Header.Set("Authorization", creatorToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("kick participant returns 403", func(t *testing.T) {
		body := `{"userId":"someone"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups/"+groupID+"/calls/fake-call-id/kick", strings.NewReader(body))
		req.Header.Set("Authorization", creatorToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("promote call admin returns 403", func(t *testing.T) {
		body := `{"userId":"someone"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups/"+groupID+"/calls/fake-call-id/admins", strings.NewReader(body))
		req.Header.Set("Authorization", creatorToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("force end call returns 403", func(t *testing.T) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups/"+groupID+"/calls/fake-call-id/end", nil)
		req.Header.Set("Authorization", creatorToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}
