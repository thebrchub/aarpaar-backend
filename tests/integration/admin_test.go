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
// Integration Tests — Admin Endpoints
// ---------------------------------------------------------------------------

func TestAdminStats(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	// Create admin user with the configured BENKI_ADMIN_EMAIL
	_, adminToken := testutil.SeedUser(t, "admin", "admin@test.com")

	t.Run("admin gets stats", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/admin/stats", nil)
		req.Header.Set("Authorization", adminToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("non-admin access", func(t *testing.T) {
		_, userToken := testutil.SeedUser(t, "nonadmin", "nonadmin@test.com")

		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/admin/stats", nil)
		req.Header.Set("Authorization", userToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("no auth", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/admin/stats", nil)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestAdminBanUnban(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, adminToken := testutil.SeedUser(t, "banadmin", "admin@test.com")
	targetID, targetToken := testutil.SeedUser(t, "bantarget", "bantarget@test.com")

	t.Run("ban user", func(t *testing.T) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/admin/users/"+targetID+"/ban", nil)
		req.Header.Set("Authorization", adminToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("banned user cannot access", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/users/me", nil)
		req.Header.Set("Authorization", targetToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("unban user", func(t *testing.T) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/admin/users/"+targetID+"/unban", nil)
		req.Header.Set("Authorization", adminToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("non-admin cannot ban", func(t *testing.T) {
		_, normalToken := testutil.SeedUser(t, "normie", "normie@test.com")

		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/admin/users/"+targetID+"/ban", nil)
		req.Header.Set("Authorization", normalToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}

func TestAdminUsers(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, adminToken := testutil.SeedUser(t, "useradmin", "admin@test.com")

	t.Run("list all users", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/admin/users", nil)
		req.Header.Set("Authorization", adminToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestAdminReports(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, adminToken := testutil.SeedUser(t, "reportadmin", "admin@test.com")

	t.Run("list reports", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/admin/reports", nil)
		req.Header.Set("Authorization", adminToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestAdminReportUser(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "reporter", "reporter@test.com")
	testutil.SeedUserWithUsername(t, "reported", "reported@test.com", "reported")

	t.Run("report user", func(t *testing.T) {
		// ReportUserHandler accepts {reported_username, reason}
		body := `{"reported_username":"reported","reason":"spam"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/match/report", strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}
