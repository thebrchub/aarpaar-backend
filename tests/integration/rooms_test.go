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
		var rooms []map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &rooms))
		assert.GreaterOrEqual(t, len(rooms), 1)
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
}
