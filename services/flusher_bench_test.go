package services

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// Benchmarks — Message Flusher Insert Chunk Builder
//
// The insertMessageChunk function uses strconv.Itoa + strings.Builder to
// build bulk INSERT queries. This benchmark validates it stays fast at scale.
// ---------------------------------------------------------------------------

func BenchmarkInsertChunkBuilder(b *testing.B) {
	// Simulate building the SQL for a chunk of raw messages
	sizes := []int{10, 100, 500}
	for _, n := range sizes {
		msgs := make([]string, n)
		for i := range msgs {
			msgs[i] = fmt.Sprintf(`{"from":"user-%d","text":"message %d"}`, i, i)
		}

		b.Run(fmt.Sprintf("msgs_%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				buildInsertSQL("test-room", msgs)
			}
		})
	}
}

// buildInsertSQL mirrors the hot path of insertMessageChunk without DB calls.
func buildInsertSQL(roomID string, chunk []string) (string, []interface{}) {
	var sb strings.Builder
	var valueArgs []interface{}
	argID := 1
	first := true

	for _, rawMsg := range chunk {
		senderID := gjson.Get(rawMsg, "from").String()
		content := gjson.Get(rawMsg, "text").String()
		if senderID == "" || content == "" {
			continue
		}

		if !first {
			sb.WriteByte(',')
		}
		first = false

		sb.WriteString("($")
		sb.WriteString(strconv.Itoa(argID))
		sb.WriteString(",$")
		sb.WriteString(strconv.Itoa(argID + 1))
		sb.WriteString(",$")
		sb.WriteString(strconv.Itoa(argID + 2))
		sb.WriteByte(')')
		valueArgs = append(valueArgs, roomID, senderID, content)
		argID += 3
	}

	query := "INSERT INTO messages (room_id, sender_id, content) VALUES " + sb.String()
	return query, valueArgs
}

// ---------------------------------------------------------------------------
// Benchmarks — Arena Engagement Entry Parsing
//
// The arena flusher parses "userId:postId" entries from Redis SETs.
// At 10L+ users, thousands of entries are parsed each flush cycle.
// ---------------------------------------------------------------------------

func BenchmarkParseEngagementEntries(b *testing.B) {
	sizes := []int{100, 1000, 5000}
	for _, n := range sizes {
		entries := make([]string, n)
		for i := range entries {
			entries[i] = fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d:%d", i%10000, i+1)
		}

		b.Run(fmt.Sprintf("entries_%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				parseEntries(entries)
			}
		})
	}
}

type benchPair struct {
	userID string
	postID int64
}

func parseEntries(entries []string) []benchPair {
	pairs := make([]benchPair, 0, len(entries))
	for _, entry := range entries {
		lastColon := strings.LastIndex(entry, ":")
		if lastColon <= 0 || lastColon >= len(entry)-1 {
			continue
		}
		uid := entry[:lastColon]
		pid, err := strconv.ParseInt(entry[lastColon+1:], 10, 64)
		if err != nil {
			continue
		}
		pairs = append(pairs, benchPair{uid, pid})
	}
	return pairs
}

// ---------------------------------------------------------------------------
// Benchmarks — Like/Unlike Cancellation Parsing
//
// When a user likes then unlikes (or vice versa) within the same flush
// window, the flusher must parse and deduplicate entries efficiently.
// ---------------------------------------------------------------------------

func BenchmarkParseLikeEntries(b *testing.B) {
	// Mix of likes and unlikes for the same posts
	entries := make([]string, 2000)
	for i := range entries {
		entries[i] = fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d:%d", i%100, i%500+1)
	}

	b.Run("parse_2000", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			parseEntries(entries)
		}
	})
}

// ---------------------------------------------------------------------------
// Benchmarks — Receipt Flusher SQL Builder
//
// Receipt flushing builds batch UPDATEs using VALUES joins.
// This benchmark validates the string builder stays fast.
// ---------------------------------------------------------------------------

func BenchmarkReceiptSQLBuilder(b *testing.B) {
	sizes := []int{10, 100, 500}
	for _, n := range sizes {
		type entry struct {
			roomID string
			userID string
			ts     string
		}
		chunk := make([]entry, n)
		for i := range chunk {
			chunk[i] = entry{
				roomID: fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
				userID: fmt.Sprintf("660e8400-e29b-41d4-a716-44665544%04d", i),
				ts:     "2026-03-15T19:00:00.000Z",
			}
		}

		b.Run(fmt.Sprintf("receipts_%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				var sb strings.Builder
				args := make([]interface{}, 0, n*3)
				argID := 1
				for j, e := range chunk {
					if j > 0 {
						sb.WriteByte(',')
					}
					sb.WriteString("($")
					sb.WriteString(strconv.Itoa(argID))
					sb.WriteString("::uuid,$")
					sb.WriteString(strconv.Itoa(argID + 1))
					sb.WriteString("::uuid,$")
					sb.WriteString(strconv.Itoa(argID + 2))
					sb.WriteString("::timestamptz)")
					args = append(args, e.roomID, e.userID, e.ts)
					argID += 3
				}
				_ = sb.String()
				_ = args
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Benchmarks — Engagement Counter Aggregation
//
// After INSERT RETURNING, the flusher groups results by postID to build
// a single UPDATE with a VALUES join. This benchmark validates the map
// aggregation + SQL builder stays fast.
// ---------------------------------------------------------------------------

func BenchmarkCounterAggregation(b *testing.B) {
	// Simulate 1000 newly inserted view entries across 200 posts
	postIDs := make([]int64, 1000)
	for i := range postIDs {
		postIDs[i] = int64(i%200 + 1)
	}

	b.Run("aggregate_1000_entries", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			postCounts := make(map[int64]int, 200)
			for _, pid := range postIDs {
				postCounts[pid]++
			}

			// Build the VALUES join for UPDATE
			updateValues := make([]string, 0, len(postCounts))
			updateParams := make([]any, 0, len(postCounts)*2)
			argID := 1
			for pid, cnt := range postCounts {
				updateValues = append(updateValues, fmt.Sprintf("($%d::bigint, $%d::int)", argID, argID+1))
				updateParams = append(updateParams, pid, cnt)
				argID += 2
			}
			_ = strings.Join(updateValues, ", ")
			_ = updateParams
		}
	})
}
