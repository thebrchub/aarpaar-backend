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
// Integration Tests — Room Endpoints
// ---------------------------------------------------------------------------

func TestGetRooms(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID1, token1 := testutil.SeedUser(t, "roomuser1", "room1@test.com")
	userID2, _ := testutil.SeedUser(t, "roomuser2", "room2@test.com")

	t.Run("user with no rooms", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/rooms", nil)
		req.Header.Set("Authorization", token1)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("user with rooms", func(t *testing.T) {
		// Create a friendship + DM room
		testutil.SeedFriendship(t, userID1, userID2)

		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/rooms", nil)
		req.Header.Set("Authorization", token1)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, _ := io.ReadAll(resp.Body)
		// Response shape: {"rooms":[...],"users":{...}}
		var result struct {
			Rooms []map[string]interface{} `json:"rooms"`
			Users map[string]interface{}   `json:"users"`
		}
		require.NoError(t, json.Unmarshal(body, &result))
		assert.GreaterOrEqual(t, len(result.Rooms), 1)
	})
}

func TestGetRoomMessages(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID1, token1 := testutil.SeedUser(t, "msguser1", "msg1@test.com")
	userID2, token2 := testutil.SeedUser(t, "msguser2", "msg2@test.com")
	roomID := testutil.SeedFriendship(t, userID1, userID2)

	// Seed some messages
	for i := 0; i < 5; i++ {
		testutil.SeedMessage(t, roomID, userID1, "Hello "+string(rune('A'+i)))
	}

	t.Run("valid room member", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/rooms/"+roomID+"/messages", nil)
		req.Header.Set("Authorization", token1)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, _ := io.ReadAll(resp.Body)
		var messages []map[string]interface{}
		// The response may be an array or may have a wrapper
		json.Unmarshal(body, &messages)
		assert.GreaterOrEqual(t, len(messages), 5)
	})

	t.Run("non-member user", func(t *testing.T) {
		_, nonMemberToken := testutil.SeedUser(t, "outsider", "outsider@test.com")

		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/rooms/"+roomID+"/messages", nil)
		req.Header.Set("Authorization", nonMemberToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	_ = token2 // token2 available for room access tests
}

func TestCreateDM(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token1 := testutil.SeedUserWithUsername(t, "dmcreator", "dmcreator@test.com", "dmcreator")
	_, _ = testutil.SeedUserWithUsername(t, "dmtarget", "dmtarget@test.com", "dmtarget")

	t.Run("create DM between users", func(t *testing.T) {
		body := `{"username":"dmtarget"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/rooms", strings.NewReader(body))
		req.Header.Set("Authorization", token1)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		// Should be 200 or 201
		assert.Contains(t, []int{http.StatusOK, http.StatusCreated}, resp.StatusCode)
	})

	t.Run("no auth", func(t *testing.T) {
		body := `{"username":"dmtarget"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/rooms", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("DM to self", func(t *testing.T) {
		body := `{"username":"dmcreator"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/rooms", strings.NewReader(body))
		req.Header.Set("Authorization", token1)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		// Should be rejected (400 or similar)
		assert.True(t, resp.StatusCode >= 400, "DM to self should be rejected")
	})

	t.Run("DM to nonexistent user", func(t *testing.T) {
		body := `{"username":"nobody_exists_here_12345"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/rooms", strings.NewReader(body))
		req.Header.Set("Authorization", token1)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.True(t, resp.StatusCode >= 400, "DM to nonexistent user should fail")
	})
}

// ---------------------------------------------------------------------------
// Integration Tests — DM Requests (accept/reject)
// ---------------------------------------------------------------------------

func TestDMRequests(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token1 := testutil.SeedUser(t, "dmreq1", "dmreq1@test.com")

	t.Run("get DM requests empty", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/rooms/requests", nil)
		req.Header.Set("Authorization", token1)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("accept non-existent room", func(t *testing.T) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/rooms/00000000-0000-0000-0000-000000000000/accept", nil)
		req.Header.Set("Authorization", token1)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.True(t, resp.StatusCode >= 400, "accept non-existent room should fail")
	})

	t.Run("reject non-existent room", func(t *testing.T) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/rooms/00000000-0000-0000-0000-000000000000/reject", nil)
		req.Header.Set("Authorization", token1)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.True(t, resp.StatusCode >= 400, "reject non-existent room should fail")
	})

	t.Run("no auth on requests", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/rooms/requests", nil)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}
