package integration

import (
	"fmt"
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
// Arena Posts — CRUD
// ---------------------------------------------------------------------------

func TestCreatePost(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "poster", "poster@test.com")

	t.Run("success", func(t *testing.T) {
		body := `{"caption":"Hello Arena!"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/arena/posts", strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var post map[string]interface{}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &post))
		assert.Equal(t, "Hello Arena!", post["caption"])
		assert.Equal(t, "original", post["postType"])
		assert.Equal(t, "public", post["visibility"])
	})

	t.Run("friends visibility", func(t *testing.T) {
		body := `{"caption":"Friends only","visibility":"friends"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/arena/posts", strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var post map[string]interface{}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &post))
		assert.Equal(t, "friends", post["visibility"])
	})

	t.Run("empty caption and no media", func(t *testing.T) {
		body := `{"caption":""}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/arena/posts", strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("caption too long for free user", func(t *testing.T) {
		longCaption := strings.Repeat("a", 301) // FreeCaptionLength default = 300
		body := fmt.Sprintf(`{"caption":"%s"}`, longCaption)
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/arena/posts", strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("no auth", func(t *testing.T) {
		body := `{"caption":"No auth test"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/arena/posts", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestGetPost(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "getter", "getter@test.com")
	postID := testutil.SeedPost(t, userID, "My test post")

	t.Run("success", func(t *testing.T) {
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/arena/posts/%d", srv.URL, postID), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var post map[string]interface{}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &post))
		assert.Equal(t, "My test post", post["caption"])
	})

	t.Run("not found", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/arena/posts/999999", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("invalid id", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/arena/posts/abc", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func TestDeletePost(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "deleter", "deleter@test.com")
	_, otherToken := testutil.SeedUser(t, "other", "other@test.com")
	postID := testutil.SeedPost(t, userID, "Delete me")

	t.Run("not yours", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/arena/posts/%d", srv.URL, postID), nil)
		req.Header.Set("Authorization", otherToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("success", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/arena/posts/%d", srv.URL, postID), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// Verify it's gone
		getReq, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/arena/posts/%d", srv.URL, postID), nil)
		getReq.Header.Set("Authorization", token)
		getResp, err := http.DefaultClient.Do(getReq)
		require.NoError(t, err)
		defer getResp.Body.Close()
		assert.Equal(t, http.StatusNotFound, getResp.StatusCode)
	})
}

// ---------------------------------------------------------------------------
// Repost
// ---------------------------------------------------------------------------

func TestRepost(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, tokenA := testutil.SeedUser(t, "author", "author@test.com")
	_, tokenB := testutil.SeedUser(t, "reposter", "reposter@test.com")
	postID := testutil.SeedPost(t, userID, "Original post")

	t.Run("plain repost", func(t *testing.T) {
		body := `{"caption":""}`
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/repost", srv.URL, postID), strings.NewReader(body))
		req.Header.Set("Authorization", tokenB)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var post map[string]interface{}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &post))
		assert.Equal(t, "repost", post["postType"])
	})

	t.Run("duplicate repost", func(t *testing.T) {
		body := `{"caption":""}`
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/repost", srv.URL, postID), strings.NewReader(body))
		req.Header.Set("Authorization", tokenB)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusConflict, resp.StatusCode)
	})

	t.Run("quote repost", func(t *testing.T) {
		body := `{"caption":"My thoughts on this"}`
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/repost", srv.URL, postID), strings.NewReader(body))
		req.Header.Set("Authorization", tokenA) // author can quote their own post
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var post map[string]interface{}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &post))
		assert.Equal(t, "My thoughts on this", post["caption"])
		assert.Equal(t, "repost", post["postType"])
	})

	t.Run("repost nonexistent", func(t *testing.T) {
		body := `{"caption":""}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/arena/posts/999999/repost", strings.NewReader(body))
		req.Header.Set("Authorization", tokenB)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

// ---------------------------------------------------------------------------
// Pin / Unpin Post
// ---------------------------------------------------------------------------

func TestPinPost(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "pinner", "pinner@test.com")
	postID := testutil.SeedPost(t, userID, "Pin me")

	t.Run("pin", func(t *testing.T) {
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/pin", srv.URL, postID), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("unpin", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/arena/posts/%d/pin", srv.URL, postID), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// ---------------------------------------------------------------------------
// Arena Likes
// ---------------------------------------------------------------------------

func TestPostLikes(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "liker", "liker@test.com")
	postID := testutil.SeedPost(t, userID, "Like me")

	t.Run("like post", func(t *testing.T) {
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/like", srv.URL, postID), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("unlike post", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/arena/posts/%d/like", srv.URL, postID), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("get likers empty", func(t *testing.T) {
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/arena/posts/%d/likes", srv.URL, postID), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// ---------------------------------------------------------------------------
// Arena Bookmarks
// ---------------------------------------------------------------------------

func TestPostBookmarks(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "bookmarker", "bookmarker@test.com")
	postID := testutil.SeedPost(t, userID, "Bookmark me")

	t.Run("bookmark post", func(t *testing.T) {
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/bookmark", srv.URL, postID), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("bookmark idempotent", func(t *testing.T) {
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/bookmark", srv.URL, postID), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("get bookmarks", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/arena/bookmarks", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var posts []map[string]interface{}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &posts))
		assert.GreaterOrEqual(t, len(posts), 1)
	})

	t.Run("unbookmark", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/arena/posts/%d/bookmark", srv.URL, postID), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// ---------------------------------------------------------------------------
// Arena Comments
// ---------------------------------------------------------------------------

func TestComments(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "commenter", "commenter@test.com")
	postID := testutil.SeedPost(t, userID, "Comment on me")

	var commentID float64

	t.Run("create comment", func(t *testing.T) {
		body := `{"body":"Great post!"}`
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/comments", srv.URL, postID), strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var comment map[string]interface{}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &comment))
		assert.Equal(t, "Great post!", comment["body"])
		assert.Equal(t, float64(0), comment["depth"])
		commentID = comment["id"].(float64)
	})

	t.Run("reply to comment", func(t *testing.T) {
		body := fmt.Sprintf(`{"body":"I agree!","parentId":%d}`, int64(commentID))
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/comments", srv.URL, postID), strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var comment map[string]interface{}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &comment))
		assert.Equal(t, float64(1), comment["depth"])
	})

	t.Run("get comments", func(t *testing.T) {
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/arena/posts/%d/comments", srv.URL, postID), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var comments []map[string]interface{}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &comments))
		assert.GreaterOrEqual(t, len(comments), 1)
	})

	t.Run("empty body comment", func(t *testing.T) {
		body := `{"body":""}`
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/comments", srv.URL, postID), strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("comment too long for free user", func(t *testing.T) {
		longBody := strings.Repeat("x", 201) // FreeCommentLength default = 200
		body := fmt.Sprintf(`{"body":"%s"}`, longBody)
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/comments", srv.URL, postID), strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("comment on nonexistent post", func(t *testing.T) {
		body := `{"body":"Hello"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/arena/posts/999999/comments", strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("delete comment", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/arena/posts/%d/comments/%d", srv.URL, postID, int64(commentID)), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("delete someone elses comment", func(t *testing.T) {
		_, otherToken := testutil.SeedUser(t, "stranger", "stranger@test.com")

		// Create a comment first
		createBody := `{"body":"My comment"}`
		createReq, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/comments", srv.URL, postID), strings.NewReader(createBody))
		createReq.Header.Set("Authorization", token)
		createReq.Header.Set("Content-Type", "application/json")
		createResp, err := http.DefaultClient.Do(createReq)
		require.NoError(t, err)
		var created map[string]interface{}
		raw, _ := io.ReadAll(createResp.Body)
		createResp.Body.Close()
		require.NoError(t, json.Unmarshal(raw, &created))

		// Try to delete with other user
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/arena/posts/%d/comments/%d", srv.URL, postID, int64(created["id"].(float64))), nil)
		req.Header.Set("Authorization", otherToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

// ---------------------------------------------------------------------------
// Comment Likes
// ---------------------------------------------------------------------------

func TestCommentLikes(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "cmliker", "cmliker@test.com")
	postID := testutil.SeedPost(t, userID, "Like my comment")

	// Create a comment to like
	body := `{"body":"Like this comment"}`
	createReq, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/comments", srv.URL, postID), strings.NewReader(body))
	createReq.Header.Set("Authorization", token)
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	var comment map[string]interface{}
	raw, _ := io.ReadAll(createResp.Body)
	createResp.Body.Close()
	require.NoError(t, json.Unmarshal(raw, &comment))
	commentID := int64(comment["id"].(float64))

	t.Run("like comment", func(t *testing.T) {
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/comments/%d/like", srv.URL, commentID), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("unlike comment", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/arena/comments/%d/like", srv.URL, commentID), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// ---------------------------------------------------------------------------
// Arena Views (batch)
// ---------------------------------------------------------------------------

func TestRecordViews(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "viewer", "viewer@test.com")
	postID := testutil.SeedPost(t, userID, "View me")

	t.Run("success", func(t *testing.T) {
		body := fmt.Sprintf(`{"postIds":[%d]}`, postID)
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/arena/posts/views", strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	})

	t.Run("empty array", func(t *testing.T) {
		body := `{"postIds":[]}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/arena/posts/views", strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

// ---------------------------------------------------------------------------
// Arena Post Activity (owner-only analytics)
// ---------------------------------------------------------------------------

func TestPostActivity(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "analyst", "analyst@test.com")
	_, otherToken := testutil.SeedUser(t, "noowner", "noowner@test.com")
	postID := testutil.SeedPost(t, userID, "Analytics post")

	t.Run("owner sees activity", func(t *testing.T) {
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/arena/posts/%d/activity", srv.URL, postID), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var activity map[string]interface{}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &activity))
		assert.Equal(t, float64(postID), activity["postId"])
	})

	t.Run("non-owner gets 404", func(t *testing.T) {
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/arena/posts/%d/activity", srv.URL, postID), nil)
		req.Header.Set("Authorization", otherToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

// ---------------------------------------------------------------------------
// Arena Reposts Endpoint
// ---------------------------------------------------------------------------

func TestGetReposts(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "repostee", "repostee@test.com")
	postID := testutil.SeedPost(t, userID, "Get my reposts")

	t.Run("empty reposts", func(t *testing.T) {
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/arena/posts/%d/reposts", srv.URL, postID), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var data map[string]interface{}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &data))
		assert.Equal(t, float64(0), data["repostCount"])
		assert.Equal(t, float64(0), data["quoteCount"])
	})
}

// ---------------------------------------------------------------------------
// Report Post
// ---------------------------------------------------------------------------

func TestReportPost(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "reporter", "reporter@test.com")
	postID := testutil.SeedPost(t, userID, "Report me")

	t.Run("success", func(t *testing.T) {
		body := `{"reason":"Spam content"}`
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/report", srv.URL, postID), strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("missing reason", func(t *testing.T) {
		body := `{"reason":""}`
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/report", srv.URL, postID), strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

// ---------------------------------------------------------------------------
// Arena Feeds
// ---------------------------------------------------------------------------

func TestGlobalFeed(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "feeder", "feeder@test.com")
	testutil.SeedPost(t, userID, "Feed post 1")
	testutil.SeedPost(t, userID, "Feed post 2")

	t.Run("returns posts", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/arena/feed/global", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var posts []map[string]interface{}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &posts))
		assert.GreaterOrEqual(t, len(posts), 2)
	})

	t.Run("with limit", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/arena/feed/global?limit=1", nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var posts []map[string]interface{}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &posts))
		assert.Equal(t, 1, len(posts))
	})
}

func TestNetworkFeed(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID1, token1 := testutil.SeedUser(t, "netuser1", "netuser1@test.com")
	userID2, _ := testutil.SeedUser(t, "netuser2", "netuser2@test.com")

	// No friendship yet — should see no posts
	testutil.SeedPost(t, userID2, "Friend's post")

	t.Run("empty without friends", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/arena/feed/network", nil)
		req.Header.Set("Authorization", token1)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var posts []map[string]interface{}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &posts))
		assert.Equal(t, 0, len(posts))
	})

	t.Run("shows friend posts", func(t *testing.T) {
		testutil.SeedFriendship(t, userID1, userID2)

		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/arena/feed/network", nil)
		req.Header.Set("Authorization", token1)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var posts []map[string]interface{}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &posts))
		assert.GreaterOrEqual(t, len(posts), 1)
	})
}

func TestTrendingFeed(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	_, token := testutil.SeedUser(t, "trender", "trender@test.com")

	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/arena/feed/trending", nil)
	req.Header.Set("Authorization", token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestUserPosts(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "profiler", "profiler@test.com")
	testutil.SeedPost(t, userID, "My post")

	t.Run("own profile", func(t *testing.T) {
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/arena/users/%s/posts", srv.URL, userID), nil)
		req.Header.Set("Authorization", token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var posts []map[string]interface{}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &posts))
		assert.GreaterOrEqual(t, len(posts), 1)
	})

	t.Run("other user profile shows only public", func(t *testing.T) {
		_, otherToken := testutil.SeedUser(t, "viewer2", "viewer2@test.com")

		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/arena/users/%s/posts", srv.URL, userID), nil)
		req.Header.Set("Authorization", otherToken)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// ---------------------------------------------------------------------------
// Profile Click
// ---------------------------------------------------------------------------

func TestProfileClick(t *testing.T) {
	srv, _, cleanup := testutil.StartTestServer(t)
	defer cleanup()

	userID, token := testutil.SeedUser(t, "clicker", "clicker@test.com")
	postID := testutil.SeedPost(t, userID, "Click my profile")

	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/arena/posts/%d/profile-click", srv.URL, postID), nil)
	req.Header.Set("Authorization", token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}
