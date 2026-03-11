package chat

import (
	"testing"

	"github.com/shivanand-burli/go-starter-kit/middleware"
	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Unit Tests — Shard Hashing
// ---------------------------------------------------------------------------

func TestGetShard(t *testing.T) {
	e := &Engine{}
	for i := range e.roomShards {
		e.roomShards[i].rooms = make(map[string]map[*Client]bool)
	}

	t.Run("deterministic", func(t *testing.T) {
		shard1 := e.getShard("room_abc")
		shard2 := e.getShard("room_abc")
		assert.Equal(t, shard1, shard2, "same room ID must always map to same shard")
	})

	t.Run("different rooms can map to different shards", func(t *testing.T) {
		// With 64 shards, different room IDs should (usually) land in different shards.
		// We test a reasonable set to ensure distribution.
		shardSet := make(map[*roomShard]bool)
		for i := 0; i < 200; i++ {
			roomID := "room_" + string(rune('A'+i%26)) + string(rune('0'+i/26))
			shardSet[e.getShard(roomID)] = true
		}
		// With 200 rooms and 64 shards, we should hit at least 20 distinct shards
		assert.Greater(t, len(shardSet), 20, "shards should be well-distributed")
	})

	t.Run("shard index within bounds", func(t *testing.T) {
		for i := 0; i < 1000; i++ {
			roomID := "test_room_" + string(rune(i))
			shard := e.getShard(roomID)
			found := false
			for j := range e.roomShards {
				if shard == &e.roomShards[j] {
					found = true
					break
				}
			}
			assert.True(t, found, "shard must be one of the engine's shards")
		}
	})
}

// ---------------------------------------------------------------------------
// Unit Tests — AllowMessage (WS Rate Limiting)
// ---------------------------------------------------------------------------

func TestAllowMessage(t *testing.T) {
	e := &Engine{
		wsLimiter: middleware.NewIPRateLimiter(2, 3), // 2 req/sec, burst 3
	}
	defer e.wsLimiter.Close()

	t.Run("allows messages within burst", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			assert.True(t, e.AllowMessage("user-burst"), "message %d should be allowed within burst", i+1)
		}
	})

	t.Run("rejects messages exceeding burst", func(t *testing.T) {
		// Exhaust burst for a new user
		for i := 0; i < 3; i++ {
			e.AllowMessage("user-exceed")
		}
		// Next message should be rejected
		assert.False(t, e.AllowMessage("user-exceed"), "message after burst should be rejected")
	})

	t.Run("independent per user", func(t *testing.T) {
		// Exhaust burst for userA
		for i := 0; i < 3; i++ {
			e.AllowMessage("userA")
		}
		// userA should be rejected
		assert.False(t, e.AllowMessage("userA"), "userA should be rate limited")
		// userB should still have full burst available
		assert.True(t, e.AllowMessage("userB"), "userB should not be affected by userA's limit")
	})
}
