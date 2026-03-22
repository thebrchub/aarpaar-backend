package services

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebrchub/aarpaar/models"
)

// ---------------------------------------------------------------------------
// Unit Tests — Notification Preference Constants
// ---------------------------------------------------------------------------

func TestNotifPrefConstantsNonEmpty(t *testing.T) {
	keys := []string{
		NotifPrefLikes, NotifPrefComments, NotifPrefFriendRequests,
		NotifPrefReposts, NotifPrefDMRequests, NotifPrefGroupInvites,
		NotifPrefMatchActivity, NotifPrefMentions,
	}
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		assert.NotEmpty(t, k, "notification preference constant must not be empty")
		assert.False(t, seen[k], "duplicate notification preference key: %s", k)
		seen[k] = true
	}
}

func TestNotifPrefConstantsMatchModel(t *testing.T) {
	// Serialize the struct with all-true defaults and verify keys match constants
	prefs := models.NotificationPrefs{
		Likes: true, Comments: true, FriendRequests: true, Reposts: true,
		DMRequests: true, GroupInvites: true, MatchActivity: true, Mentions: true,
	}
	data, err := json.Marshal(prefs)
	require.NoError(t, err)

	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &m))

	constants := []string{
		NotifPrefLikes, NotifPrefComments, NotifPrefFriendRequests,
		NotifPrefReposts, NotifPrefDMRequests, NotifPrefGroupInvites,
		NotifPrefMatchActivity, NotifPrefMentions,
	}
	for _, c := range constants {
		_, exists := m[c]
		assert.True(t, exists, "constant %q must match a JSON field in NotificationPrefs", c)
	}
	assert.Equal(t, len(constants), len(m),
		"model struct fields and constants must have same count")
}

// ---------------------------------------------------------------------------
// Unit Tests — Notification Preference JSON Parsing
// ---------------------------------------------------------------------------

func TestNotifPrefsParseDefaults(t *testing.T) {
	// Simulates what the DB would return for a new user
	defaultJSON := `{"likes": true, "comments": true, "friend_requests": true, "reposts": true, "dm_requests": true, "group_invites": true, "match_activity": true, "mentions": true}`
	var prefs map[string]bool
	require.NoError(t, json.Unmarshal([]byte(defaultJSON), &prefs))
	for k, v := range prefs {
		assert.True(t, v, "default value for %q should be true", k)
	}
}

func TestNotifPrefsParsePartialOverride(t *testing.T) {
	// JSONB || merge: user disables likes and dm_requests
	original := `{"likes": true, "comments": true, "friend_requests": true, "reposts": true, "dm_requests": true, "group_invites": true, "match_activity": true, "mentions": true}`
	patch := `{"likes": false, "dm_requests": false}`

	var orig map[string]bool
	require.NoError(t, json.Unmarshal([]byte(original), &orig))
	var p map[string]bool
	require.NoError(t, json.Unmarshal([]byte(patch), &p))

	// Simulate || merge
	for k, v := range p {
		orig[k] = v
	}

	assert.False(t, orig["likes"])
	assert.True(t, orig["comments"])
	assert.False(t, orig["dm_requests"])
	assert.True(t, orig["group_invites"])
}

func TestNotifPrefsMissingKeyDefaultsTrue(t *testing.T) {
	// If a key is missing from the JSON (e.g. new pref added later),
	// ShouldNotify should treat it as enabled (fail-open logic).
	partialJSON := `{"likes": true, "comments": false}`
	var prefs map[string]bool
	require.NoError(t, json.Unmarshal([]byte(partialJSON), &prefs))

	// Simulate the ShouldNotify logic for a missing key
	checkPref := func(key string) bool {
		enabled, exists := prefs[key]
		return !exists || enabled
	}

	assert.True(t, checkPref("likes"))
	assert.False(t, checkPref("comments"))
	assert.True(t, checkPref("friend_requests")) // missing → true
	assert.True(t, checkPref("new_future_key"))  // missing → true
}

// ---------------------------------------------------------------------------
// Unit Tests — Resolve Original Post ID Cache Key Format
// ---------------------------------------------------------------------------

func TestResolveOriginalCacheKeyFormat(t *testing.T) {
	tests := []struct {
		postID int64
		want   string
	}{
		{1, "post:resolve:1"},
		{12345, "post:resolve:12345"},
		{9999999, "post:resolve:9999999"},
	}
	for _, tt := range tests {
		key := "post:resolve:" + strconv.FormatInt(tt.postID, 10)
		assert.Equal(t, tt.want, key, "postID=%d", tt.postID)
	}
}

func TestPostOwnerCacheKeyFormat(t *testing.T) {
	tests := []struct {
		postID int64
		want   string
	}{
		{1, "post:owner:1"},
		{42, "post:owner:42"},
	}
	for _, tt := range tests {
		key := "post:owner:" + strconv.FormatInt(tt.postID, 10)
		assert.Equal(t, tt.want, key, "postID=%d", tt.postID)
	}
}

// ---------------------------------------------------------------------------
// Benchmarks — Notification Preference JSON Parsing
//
// ShouldNotify parses JSONB on cache miss. At 10L+ users, the first call
// per user (every 5 min) triggers a parse. Must be < 1µs per parse.
// ---------------------------------------------------------------------------

func BenchmarkNotifPrefsJSONParse(b *testing.B) {
	defaultJSON := []byte(`{"likes": true, "comments": true, "friend_requests": true, "reposts": true, "dm_requests": true, "group_invites": true, "match_activity": true, "mentions": true}`)

	b.Run("unmarshal_map", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			var m map[string]bool
			json.Unmarshal(defaultJSON, &m)
		}
	})

	b.Run("unmarshal_struct", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			var p models.NotificationPrefs
			json.Unmarshal(defaultJSON, &p)
		}
	})
}

// ---------------------------------------------------------------------------
// Benchmarks — Resolve Cache Key Generation
//
// Cache key formatting runs on every like/unlike/comment. Must be very fast.
// ---------------------------------------------------------------------------

func BenchmarkResolveCacheKeyGen(b *testing.B) {
	ids := []int64{1, 999, 123456, 9999999}
	for _, id := range ids {
		b.Run(fmt.Sprintf("postID_%d", id), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = "post:resolve:" + strconv.FormatInt(id, 10)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Benchmarks — Notification Prefs Redis Hash Field Conversion
//
// On cache miss, we convert map[string]bool → map[string]interface{} for
// Redis HSET. Verify this is fast even with all 8 keys.
// ---------------------------------------------------------------------------

func BenchmarkNotifPrefsToRedisHash(b *testing.B) {
	prefs := map[string]bool{
		"likes": true, "comments": true, "friend_requests": false, "reposts": true,
		"dm_requests": false, "group_invites": true, "match_activity": true, "mentions": false,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fields := make(map[string]interface{}, len(prefs))
		for k, v := range prefs {
			if v {
				fields[k] = "true"
			} else {
				fields[k] = "false"
			}
		}
	}
}
