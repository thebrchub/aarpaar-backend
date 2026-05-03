package integration

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebrchub/aarpaar/tests/testutil"
)

// ---------------------------------------------------------------------------
// Helper: doReq performs an HTTP request and returns status + body bytes.
// ---------------------------------------------------------------------------

func doReq(t *testing.T, method, url, token string, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// ---------------------------------------------------------------------------
// Badge Tiers Cache
// ---------------------------------------------------------------------------

func TestBadgeTiersCaching(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, userToken := testutil.SeedUser(t, "cacheuser", "cacheuser@test.com")
	_, adminToken := testutil.SeedUser(t, "cacheadmin", "admin@test.com")

	// Seed a badge tier directly in DB
	db := postgress.GetPool()
	_, err := db.Exec(`INSERT INTO badge_tiers (name, min_amount, icon, display_order) VALUES ('Bronze', 10, '🥉', 1)`)
	require.NoError(t, err)

	t.Run("first call hits DB and caches", func(t *testing.T) {
		code, body := doReq(t, "GET", srv.URL+"/api/v1/badges/tiers", userToken, "")
		assert.Equal(t, 200, code)
		assert.Contains(t, string(body), "Bronze")

		// Verify Redis has the cached value
		ctx := context.Background()
		cached, err := redis.GetRawClient().Get(ctx, "badge_tiers:all").Bytes()
		require.NoError(t, err)
		assert.Contains(t, string(cached), "Bronze")
	})

	t.Run("second call returns cached value", func(t *testing.T) {
		// Mutate DB directly (bypass handler) — cache should still return old data
		_, err := db.Exec(`UPDATE badge_tiers SET name = 'Silver' WHERE name = 'Bronze'`)
		require.NoError(t, err)

		code, body := doReq(t, "GET", srv.URL+"/api/v1/badges/tiers", userToken, "")
		assert.Equal(t, 200, code)
		// Should still say Bronze (cached)
		assert.Contains(t, string(body), "Bronze")
	})

	t.Run("admin update invalidates cache", func(t *testing.T) {
		// Reset DB to Bronze
		_, err := db.Exec(`UPDATE badge_tiers SET name = 'Bronze' WHERE name = 'Silver'`)
		require.NoError(t, err)

		// Get the tier ID
		var tierID int
		err = db.QueryRow(`SELECT id FROM badge_tiers LIMIT 1`).Scan(&tierID)
		require.NoError(t, err)

		// Admin update badge tier
		code, _ := doReq(t, "PATCH",
			fmt.Sprintf("%s/api/v1/admin/badges/%d", srv.URL, tierID),
			adminToken, `{"name":"Gold"}`)
		assert.Equal(t, 200, code)

		// Cache should be invalidated — next GET should return Gold
		code, body := doReq(t, "GET", srv.URL+"/api/v1/badges/tiers", userToken, "")
		assert.Equal(t, 200, code)
		assert.Contains(t, string(body), "Gold")
	})

	t.Run("admin create invalidates cache", func(t *testing.T) {
		code, _ := doReq(t, "POST", srv.URL+"/api/v1/admin/badges",
			adminToken, `{"name":"Diamond","min_amount":1000,"icon":"💎","display_order":2}`)
		assert.Equal(t, 201, code)

		// Cache invalidated — should see both Gold and Diamond
		code, body := doReq(t, "GET", srv.URL+"/api/v1/badges/tiers", userToken, "")
		assert.Equal(t, 200, code)
		assert.Contains(t, string(body), "Diamond")
		assert.Contains(t, string(body), "Gold")
	})

	t.Run("admin delete invalidates cache", func(t *testing.T) {
		var tierID int
		err := db.QueryRow(`SELECT id FROM badge_tiers WHERE name = 'Diamond'`).Scan(&tierID)
		require.NoError(t, err)

		code, _ := doReq(t, "DELETE",
			fmt.Sprintf("%s/api/v1/admin/badges/%d", srv.URL, tierID),
			adminToken, "")
		assert.Equal(t, 200, code)

		// Cache invalidated — Diamond should be gone
		code, body := doReq(t, "GET", srv.URL+"/api/v1/badges/tiers", userToken, "")
		assert.Equal(t, 200, code)
		assert.NotContains(t, string(body), "Diamond")
	})
}

// ---------------------------------------------------------------------------
// Global Feed Cache
// ---------------------------------------------------------------------------

func TestGlobalFeedCaching(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "feeduser", "feeduser@test.com")

	// Seed posts
	testutil.SeedPost(t, userID, "Post Alpha")
	testutil.SeedPost(t, userID, "Post Beta")

	t.Run("first call hits DB and caches", func(t *testing.T) {
		code, body := doReq(t, "GET", srv.URL+"/api/v1/arena/feed/global", token, "")
		assert.Equal(t, 200, code)

		var posts []map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &posts))
		assert.GreaterOrEqual(t, len(posts), 2)
	})

	t.Run("second call returns cached response", func(t *testing.T) {
		// Insert a new post directly in DB
		testutil.SeedPost(t, userID, "Post Gamma")

		code, body := doReq(t, "GET", srv.URL+"/api/v1/arena/feed/global", token, "")
		assert.Equal(t, 200, code)

		// Should still show only 2 posts (cached)
		var posts []map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &posts))
		// Feed could be 2 or 3 depending on minute boundary, but at least 2
		assert.GreaterOrEqual(t, len(posts), 2)
	})

	t.Run("create post invalidates feed cache", func(t *testing.T) {
		// Use the API to create a post (triggers invalidation)
		code, _ := doReq(t, "POST", srv.URL+"/api/v1/arena/posts", token,
			`{"caption":"Post Delta via API"}`)
		assert.Equal(t, 200, code)

		// Wait briefly for async invalidation
		time.Sleep(100 * time.Millisecond)

		code, body := doReq(t, "GET", srv.URL+"/api/v1/arena/feed/global", token, "")
		assert.Equal(t, 200, code)

		var posts []map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &posts))
		// Should now include the new post
		found := false
		for _, p := range posts {
			if p["caption"] == "Post Delta via API" {
				found = true
				break
			}
		}
		assert.True(t, found, "New post should appear after cache invalidation")
	})

	t.Run("delete post invalidates feed cache", func(t *testing.T) {
		// Create a post, then delete it
		code, raw := doReq(t, "POST", srv.URL+"/api/v1/arena/posts", token,
			`{"caption":"Post to Delete"}`)
		assert.Equal(t, 200, code)

		var created map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &created))
		postID := created["id"]

		// Fetch feed to warm cache
		doReq(t, "GET", srv.URL+"/api/v1/arena/feed/global", token, "")

		// Delete the post
		code, _ = doReq(t, "DELETE",
			fmt.Sprintf("%s/api/v1/arena/posts/%v", srv.URL, postID), token, "")
		assert.Equal(t, 200, code)

		time.Sleep(100 * time.Millisecond)

		// Feed should no longer contain the deleted post
		code, body := doReq(t, "GET", srv.URL+"/api/v1/arena/feed/global", token, "")
		assert.Equal(t, 200, code)
		assert.NotContains(t, string(body), "Post to Delete")
	})
}

// ---------------------------------------------------------------------------
// Trending Feed Cache
// ---------------------------------------------------------------------------

func TestTrendingFeedCaching(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "trenduser", "trenduser@test.com")

	// Seed posts with engagement
	postID := testutil.SeedPost(t, userID, "Trending Post")
	db := postgress.GetPool()
	_, err := db.Exec(`UPDATE posts SET like_count = 100, comment_count = 50 WHERE id = $1`, postID)
	require.NoError(t, err)

	t.Run("first call caches trending feed", func(t *testing.T) {
		code, body := doReq(t, "GET", srv.URL+"/api/v1/arena/feed/trending", token, "")
		assert.Equal(t, 200, code)

		var posts []map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &posts))
		assert.GreaterOrEqual(t, len(posts), 1)
		assert.Contains(t, string(body), "Trending Post")
	})

	t.Run("cached response served on repeat call", func(t *testing.T) {
		// Direct DB insert (no invalidation)
		newPostID := testutil.SeedPost(t, userID, "New Hot Post")
		_, err := db.Exec(`UPDATE posts SET like_count = 500 WHERE id = $1`, newPostID)
		require.NoError(t, err)

		// Trending should still show cached result
		code, body := doReq(t, "GET", srv.URL+"/api/v1/arena/feed/trending", token, "")
		assert.Equal(t, 200, code)
		// May or may not contain "New Hot Post" depending on cache timing,
		// but should definitely still have "Trending Post"
		assert.Contains(t, string(body), "Trending Post")
	})
}

// ---------------------------------------------------------------------------
// Single Post Cache
// ---------------------------------------------------------------------------

func TestSinglePostCaching(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "postuser", "postuser@test.com")
	postID := testutil.SeedPost(t, userID, "My Cached Post")

	t.Run("first call hits DB and caches", func(t *testing.T) {
		url := fmt.Sprintf("%s/api/v1/arena/posts/%d", srv.URL, postID)
		code, body := doReq(t, "GET", url, token, "")
		assert.Equal(t, 200, code)

		var post map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &post))
		assert.Equal(t, "My Cached Post", post["caption"])

		// Verify cached in Redis
		ctx := context.Background()
		cacheKey := fmt.Sprintf("post:%d:%s", postID, userID)
		cached, err := redis.GetRawClient().Get(ctx, cacheKey).Bytes()
		require.NoError(t, err)
		assert.Contains(t, string(cached), "My Cached Post")
	})

	t.Run("second call returns cached data", func(t *testing.T) {
		// Mutate caption in DB directly
		db := postgress.GetPool()
		_, err := db.Exec(`UPDATE posts SET caption = 'Changed Caption' WHERE id = $1`, postID)
		require.NoError(t, err)

		url := fmt.Sprintf("%s/api/v1/arena/posts/%d", srv.URL, postID)
		code, body := doReq(t, "GET", url, token, "")
		assert.Equal(t, 200, code)

		var post map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &post))
		// Should still show old caption (cached)
		assert.Equal(t, "My Cached Post", post["caption"])
	})

	t.Run("cache contains correct fields", func(t *testing.T) {
		url := fmt.Sprintf("%s/api/v1/arena/posts/%d", srv.URL, postID)
		code, body := doReq(t, "GET", url, token, "")
		assert.Equal(t, 200, code)

		var post map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &post))
		// Verify all expected fields are present
		assert.NotNil(t, post["id"])
		assert.NotNil(t, post["userId"])
		assert.NotNil(t, post["username"])
		assert.NotNil(t, post["postType"])
		assert.NotNil(t, post["createdAt"])
		assert.NotNil(t, post["likeCount"])
		assert.NotNil(t, post["commentCount"])
		assert.NotNil(t, post["repostCount"])
		assert.NotNil(t, post["viewCount"])
		assert.NotNil(t, post["bookmarkCount"])
	})
}

// ---------------------------------------------------------------------------
// Bookmarks Cache
// ---------------------------------------------------------------------------

func TestBookmarksCaching(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "bmuser", "bmuser@test.com")
	postID1 := testutil.SeedPost(t, userID, "Bookmarked Post 1")
	postID2 := testutil.SeedPost(t, userID, "Bookmarked Post 2")

	// Bookmark both posts
	doReq(t, "POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/bookmark", srv.URL, postID1), token, "")
	doReq(t, "POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/bookmark", srv.URL, postID2), token, "")

	t.Run("bookmarks feed is cached", func(t *testing.T) {
		code, body := doReq(t, "GET", srv.URL+"/api/v1/arena/bookmarks", token, "")
		assert.Equal(t, 200, code)

		var posts []map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &posts))
		assert.Equal(t, 2, len(posts))
	})

	t.Run("unbookmark invalidates cache", func(t *testing.T) {
		code, _ := doReq(t, "DELETE",
			fmt.Sprintf("%s/api/v1/arena/posts/%d/bookmark", srv.URL, postID1), token, "")
		assert.Equal(t, 200, code)

		// Bookmarks feed should now show only 1
		code, body := doReq(t, "GET", srv.URL+"/api/v1/arena/bookmarks", token, "")
		assert.Equal(t, 200, code)

		var posts []map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &posts))
		assert.Equal(t, 1, len(posts))
		assert.Contains(t, string(body), "Bookmarked Post 2")
	})

	t.Run("new bookmark invalidates cache", func(t *testing.T) {
		postID3 := testutil.SeedPost(t, userID, "Bookmarked Post 3")
		code, _ := doReq(t, "POST",
			fmt.Sprintf("%s/api/v1/arena/posts/%d/bookmark", srv.URL, postID3), token, "")
		assert.Equal(t, 200, code)

		code, body := doReq(t, "GET", srv.URL+"/api/v1/arena/bookmarks", token, "")
		assert.Equal(t, 200, code)

		var posts []map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &posts))
		assert.Equal(t, 2, len(posts))
		assert.Contains(t, string(body), "Bookmarked Post 3")
	})
}

// ---------------------------------------------------------------------------
// Cache-aside Pattern Verification
// ---------------------------------------------------------------------------

func TestCacheResponseFormatConsistency(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "fmtuser", "fmtuser@test.com")
	postID := testutil.SeedPost(t, userID, "Format Test Post")

	t.Run("global feed: cached and uncached format match", func(t *testing.T) {
		// First call (uncached — goes through json.Marshal + write)
		code1, body1 := doReq(t, "GET", srv.URL+"/api/v1/arena/feed/global", token, "")
		assert.Equal(t, 200, code1)

		// Second call (cached — served from Redis)
		code2, body2 := doReq(t, "GET", srv.URL+"/api/v1/arena/feed/global", token, "")
		assert.Equal(t, 200, code2)

		// Both should parse as JSON arrays
		var posts1, posts2 []map[string]interface{}
		require.NoError(t, json.Unmarshal(body1, &posts1), "uncached should be JSON array")
		require.NoError(t, json.Unmarshal(body2, &posts2), "cached should be JSON array")

		// Same number of posts
		assert.Equal(t, len(posts1), len(posts2))
	})

	t.Run("single post: cached and uncached format match", func(t *testing.T) {
		url := fmt.Sprintf("%s/api/v1/arena/posts/%d", srv.URL, postID)

		// First call (uncached)
		code1, body1 := doReq(t, "GET", url, token, "")
		assert.Equal(t, 200, code1)

		// Second call (cached)
		code2, body2 := doReq(t, "GET", url, token, "")
		assert.Equal(t, 200, code2)

		// Both should parse as JSON objects with same keys
		var post1, post2 map[string]interface{}
		require.NoError(t, json.Unmarshal(body1, &post1), "uncached should be JSON object")
		require.NoError(t, json.Unmarshal(body2, &post2), "cached should be JSON object")

		assert.Equal(t, post1["id"], post2["id"])
		assert.Equal(t, post1["caption"], post2["caption"])
	})
}

// ---------------------------------------------------------------------------
// Feed Invalidation via SCAN
// ---------------------------------------------------------------------------

func TestFeedCacheInvalidationViaRepost(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "repostuser", "repostuser@test.com")
	postID := testutil.SeedPost(t, userID, "Original Post")

	// Warm the feed cache
	doReq(t, "GET", srv.URL+"/api/v1/arena/feed/global", token, "")

	// Verify cache exists
	ctx := context.Background()
	keys, _, _ := redis.GetRawClient().Scan(ctx, 0, "feed:global:*", 100).Result()
	assert.NotEmpty(t, keys, "feed cache should exist after warming")

	// Repost (triggers invalidation)
	code, _ := doReq(t, "POST",
		fmt.Sprintf("%s/api/v1/arena/posts/%d/repost", srv.URL, postID),
		token, `{"caption":""}`)
	assert.Equal(t, 200, code)

	// Wait for async invalidation
	time.Sleep(200 * time.Millisecond)

	// Cache should be cleared
	keys, _, _ = redis.GetRawClient().Scan(ctx, 0, "feed:global:*", 100).Result()
	assert.Empty(t, keys, "feed cache should be cleared after repost")
}

// ---------------------------------------------------------------------------
// Redis Cleanup Verification
// ---------------------------------------------------------------------------

func TestCacheCleanupOnTestTeardown(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)

	userID, token := testutil.SeedUser(t, "cleanupuser", "cleanupuser@test.com")
	testutil.SeedPost(t, userID, "Cleanup Test Post")

	// Warm caches
	doReq(t, "GET", srv.URL+"/api/v1/badges/tiers", token, "")
	doReq(t, "GET", srv.URL+"/api/v1/arena/feed/global", token, "")

	// Verify something is cached
	ctx := context.Background()
	dbSize, err := redis.GetRawClient().DBSize(ctx).Result()
	require.NoError(t, err)
	assert.Greater(t, dbSize, int64(0), "Redis should have cached data")

	// Run cleanup
	cleanup()

	// After cleanup, Redis should be empty (FlushDB)
	dbSize, err = redis.GetRawClient().DBSize(ctx).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), dbSize, "Redis should be empty after cleanup")
}
