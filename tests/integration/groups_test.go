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
