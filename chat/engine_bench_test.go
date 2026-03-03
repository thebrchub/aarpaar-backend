package chat

import (
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// Benchmarks — Engine Shard Hashing
// ---------------------------------------------------------------------------

func BenchmarkGetShard(b *testing.B) {
	e := NewEngine()
	defer e.Shutdown()

	roomIDs := make([]string, 1000)
	for i := range roomIDs {
		roomIDs[i] = fmt.Sprintf("room-%d", i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.getShard(roomIDs[i%len(roomIDs)])
	}
}

func BenchmarkGetShardParallel(b *testing.B) {
	e := NewEngine()
	defer e.Shutdown()

	roomIDs := make([]string, 1000)
	for i := range roomIDs {
		roomIDs[i] = fmt.Sprintf("room-%d", i)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			e.getShard(roomIDs[i%len(roomIDs)])
			i++
		}
	})
}
