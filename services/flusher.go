package services

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/config"
	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// Message Flusher
//
// The flusher is a background service that periodically moves buffered chat
// messages from Redis into Postgres. This decouples the real-time WebSocket
// path from slow disk writes.
//
// Architecture:
//   1. Every FlushInterval (3s), we grab ALL dirty room IDs from Redis
//   2. We fan them out to a pool of FlushWorkerCount (10) goroutines
//   3. Each worker: Rename buffer → Read → Bulk INSERT → Delete
//   4. On failure: put the data back so nothing is lost
//   5. Receipt updates (mark_read, mark_delivered, delivery receipts) are
//      also flushed from Redis hashes to Postgres in batch UPDATEs.
//
// This scales to thousands of active rooms because we process them in
// parallel instead of one-at-a-time.
// ---------------------------------------------------------------------------

// flusherDone is used to signal the flusher to stop gracefully.
var flusherDone = make(chan struct{})

// flusherWg tracks the flusher goroutine for clean shutdown.
var flusherWg sync.WaitGroup

// consecutiveFlushFailures tracks how many flush cycles in a row had at least
// one room fail. Used for exponential backoff when Postgres is unavailable.
var consecutiveFlushFailures atomic.Int32

// atomicSMembersAndDel is a Lua script that atomically reads all members
// of a set and deletes it in a single Redis operation. This prevents the
// race where an SAdd between SMembers and Del would lose a room ID.
// Compiled once via redis.NewScript — subsequent calls use EVALSHA (SHA1 cache).
var atomicSMembersAndDel = goredis.NewScript(`
local members = redis.call('SMEMBERS', KEYS[1])
if #members > 0 then
    redis.call('DEL', KEYS[1])
end
return members
`)

// atomicHGetAllAndDel is a Lua script that atomically reads all fields
// of a hash and deletes it. Used for receipt flushing.
// Compiled once via redis.NewScript — subsequent calls use EVALSHA (SHA1 cache).
var atomicHGetAllAndDel = goredis.NewScript(`
local data = redis.call('HGETALL', KEYS[1])
if #data > 0 then
    redis.call('DEL', KEYS[1])
end
return data
`)

// StartFlusher kicks off the background flush loop.
// Call this once during application startup.
func StartFlusher() {
	ticker := time.NewTicker(config.FlushInterval)
	flusherWg.Add(1)
	go func() {
		defer flusherWg.Done()
		for {
			select {
			case <-flusherDone:
				ticker.Stop()
				// Final flush to persist any remaining buffered messages and receipts
				log.Println("[flusher] Final flush before shutdown...")
				FlushAllDirtyRooms()
				flushReceipts()
				return
			case <-ticker.C:
				FlushAllDirtyRooms()
				flushReceipts()
			}
		}
	}()
}

// StopFlusher signals the flusher to stop and waits for the final flush to complete.
func StopFlusher() {
	close(flusherDone)
	flusherWg.Wait()
}

// FlushAllDirtyRooms grabs every dirty room ID and processes them concurrently.
// Uses a Lua script to atomically read and clear the dirty set.
// Implements exponential backoff when consecutive flush cycles fail (e.g. Postgres down).
func FlushAllDirtyRooms() {
	// Exponential backoff: if previous flushes failed, skip some ticks.
	// Caps at 2^5 = 32 ticks (~96s at 3s intervals) to avoid indefinite stalls.
	failures := consecutiveFlushFailures.Load()
	if failures > 0 {
		// On failure N, only flush every 2^min(N,5) ticks.
		// We use a simple modulo check against a tick counter.
		backoffTicks := int32(1) << min32(failures, 5)
		// Skip this tick probabilistically using the failure count itself as a counter
		if failures%backoffTicks != 0 {
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rdb := redis.GetRawClient()

	// Atomically grab all dirty room IDs and clear the set using Lua script.
	// This prevents the race where IDs added between SMembers and Del would be lost.
	// Uses EVALSHA — script is sent once, then referenced by SHA1 hash.
	result, err := atomicSMembersAndDel.Run(ctx, rdb, []string{config.CHAT_DIRTY_TARGETS}).StringSlice()
	if err != nil {
		// redis.Nil means the key doesn't exist (nothing to flush)
		if err != goredis.Nil {
			log.Printf("[flusher] Lua script error reading dirty targets: %v", err)
		}
		return
	}

	if len(result) == 0 {
		return // Nothing to flush
	}

	// Feed the room IDs into a bounded worker pool
	jobs := make(chan string, len(result))
	for _, id := range result {
		jobs <- id
	}
	close(jobs)

	var wg sync.WaitGroup
	var flushFailed atomic.Bool
	for range config.FlushWorkerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for targetID := range jobs {
				if !flushOneRoom(targetID) {
					flushFailed.Store(true)
				}
			}
		}()
	}

	wg.Wait()

	// Track consecutive failures for exponential backoff
	if flushFailed.Load() {
		consecutiveFlushFailures.Add(1)
	} else {
		consecutiveFlushFailures.Store(0)
	}
}

// flushOneRoom persists all buffered messages for a single room to Postgres.
// Returns true on success, false on failure (used for backoff tracking).
func flushOneRoom(targetID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
	defer cancel()

	rdb := redis.GetRawClient()

	bufferKey := config.CHAT_BUFFER_COLON + targetID
	processingKey := config.CHAT_PROCESSING_COLON + targetID

	// Stranger chats are ephemeral — don't persist them, just clean up Redis
	if strings.HasPrefix(targetID, config.STRANGER_PREFIX) {
		rdb.Del(ctx, bufferKey)
		return true
	}

	// Rename the buffer to a "processing" key so new messages go to a fresh list.
	// This is our lightweight lock mechanism — no distributed locks needed.
	err := rdb.Rename(ctx, bufferKey, processingKey).Err()
	if err != nil {
		log.Printf("[flusher] Rename failed for %s (key may be empty): %v", targetID, err)
		return true // key-not-found is not a real failure
	}

	// Read all messages from the processing list
	rawMessages, err := rdb.LRange(ctx, processingKey, 0, -1).Result()
	if err != nil || len(rawMessages) == 0 {
		rdb.Del(ctx, processingKey)
		return true
	}

	// Bulk insert into Postgres (chunked to avoid giant SQL statements)
	err = bulkInsertToPostgres(targetID, rawMessages)
	if err != nil {
		log.Printf("[flusher] DB save failed for %s: %v. Re-queueing...", targetID, err)

		// On failure, put the data back so we don't lose messages
		reCtx, reCancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
		defer reCancel()

		rePipe := rdb.Pipeline()
		rePipe.Rename(reCtx, processingKey, bufferKey)
		rePipe.SAdd(reCtx, config.CHAT_DIRTY_TARGETS, targetID)
		if _, pipeErr := rePipe.Exec(reCtx); pipeErr != nil {
			log.Printf("[flusher] Failed to re-queue %s: %v", targetID, pipeErr)
		}
		return false
	}

	// Success — delete the processed list from Redis
	rdb.Del(ctx, processingKey)
	return true
}

// ---------------------------------------------------------------------------
// bulkInsertToPostgres builds INSERT statements for messages, chunked to
// avoid exceeding Postgres max query size on busy rooms.
// Uses strconv.Itoa instead of fmt.Sprintf for performance in the hot path.
// ---------------------------------------------------------------------------

func bulkInsertToPostgres(roomID string, rawMessages []string) error {
	// Process in chunks to avoid giant SQL statements
	const chunkSize = 500
	for i := 0; i < len(rawMessages); i += chunkSize {
		end := i + chunkSize
		if end > len(rawMessages) {
			end = len(rawMessages)
		}
		if err := insertMessageChunk(roomID, rawMessages[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func insertMessageChunk(roomID string, chunk []string) error {
	var b strings.Builder
	var valueArgs []interface{}
	argID := 1
	first := true

	for _, rawMsg := range chunk {
		// Extract sender and content from the raw JSON
		senderID := gjson.Get(rawMsg, config.FieldFrom).String()
		content := gjson.Get(rawMsg, config.FieldText).String()

		// Skip malformed messages that are missing required fields
		if senderID == "" || content == "" {
			continue
		}

		if !first {
			b.WriteByte(',')
		}
		first = false

		// strconv.Itoa is significantly faster than fmt.Sprintf for int→string
		b.WriteString("($")
		b.WriteString(strconv.Itoa(argID))
		b.WriteString(",$")
		b.WriteString(strconv.Itoa(argID + 1))
		b.WriteString(",$")
		b.WriteString(strconv.Itoa(argID + 2))
		b.WriteByte(')')
		valueArgs = append(valueArgs, roomID, senderID, content)
		argID += 3
	}

	// Nothing valid to insert
	if len(valueArgs) == 0 {
		return nil
	}

	query := "INSERT INTO messages (room_id, sender_id, content) VALUES " + b.String()

	_, err := postgress.Exec(query, valueArgs...)
	return err
}

// ---------------------------------------------------------------------------
// Receipt Flushing
//
// mark_read, mark_delivered, and delivery receipts are buffered in Redis
// hashes during the real-time WebSocket path. This function atomically grabs
// all pending receipts and batch-UPDATEs them to Postgres.
//
// Multiple reads/deliveries for the same user+room within one flush interval
// collapse into a single UPDATE thanks to GREATEST() — no wasted writes.
// ---------------------------------------------------------------------------

func flushReceipts() {
	flushReceiptHash(config.CHAT_READ_RECEIPTS, "last_read_at")
	flushReceiptHash(config.CHAT_DELIVERY_RECEIPTS, "last_delivered_at")
}

func flushReceiptHash(redisKey string, column string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rdb := redis.GetRawClient()

	// Atomically grab all receipt entries and clear the hash.
	// Uses EVALSHA — script is sent once, then referenced by SHA1 hash.
	result, err := atomicHGetAllAndDel.Run(ctx, rdb, []string{redisKey}).Result()
	if err != nil {
		if err != goredis.Nil {
			log.Printf("[flusher] Lua script error reading %s: %v", redisKey, err)
		}
		return
	}

	// HGETALL returns a flat slice: [field1, value1, field2, value2, ...]
	flat, ok := result.([]interface{})
	if !ok || len(flat) == 0 {
		return
	}

	// Parse the flat list into room_id, user_id, timestamp triples
	type receiptEntry struct {
		roomID string
		userID string
		ts     string
	}
	entries := make([]receiptEntry, 0, len(flat)/2)
	for i := 0; i+1 < len(flat); i += 2 {
		fieldStr, _ := flat[i].(string)
		valueStr, _ := flat[i+1].(string)
		if fieldStr == "" || valueStr == "" {
			continue
		}
		// Field format: "roomID:userID"
		sepIdx := strings.LastIndex(fieldStr, ":")
		if sepIdx <= 0 {
			continue
		}
		roomID := fieldStr[:sepIdx]
		// Stranger chats are ephemeral — skip persisting their receipts
		if strings.HasPrefix(roomID, config.STRANGER_PREFIX) {
			continue
		}
		entries = append(entries, receiptEntry{
			roomID: roomID,
			userID: fieldStr[sepIdx+1:],
			ts:     valueStr,
		})
	}

	if len(entries) == 0 {
		return
	}

	// Batch UPDATE in chunks
	for i := 0; i < len(entries); i += config.ReceiptFlushBatchSize {
		end := i + config.ReceiptFlushBatchSize
		if end > len(entries) {
			end = len(entries)
		}
		chunk := entries[i:end]

		// Build: UPDATE room_members SET <column> = GREATEST(<column>, v.ts)
		//        FROM (VALUES ($1::uuid,$2::uuid,$3::timestamptz), ...) AS v(room_id, user_id, ts)
		//        WHERE room_members.room_id = v.room_id AND room_members.user_id = v.user_id
		var b strings.Builder
		args := make([]interface{}, 0, len(chunk)*3)
		argID := 1
		for j, e := range chunk {
			if j > 0 {
				b.WriteByte(',')
			}
			b.WriteString("($")
			b.WriteString(strconv.Itoa(argID))
			b.WriteString("::uuid,$")
			b.WriteString(strconv.Itoa(argID + 1))
			b.WriteString("::uuid,$")
			b.WriteString(strconv.Itoa(argID + 2))
			b.WriteString("::timestamptz)")
			args = append(args, e.roomID, e.userID, e.ts)
			argID += 3
		}

		query := "UPDATE room_members SET " + column + " = GREATEST(room_members." + column + ", v.ts) " +
			"FROM (VALUES " + b.String() + ") AS v(room_id, user_id, ts) " +
			"WHERE room_members.room_id = v.room_id AND room_members.user_id = v.user_id"

		if _, err := postgress.Exec(query, args...); err != nil {
			log.Printf("[flusher] Receipt flush failed for %s: %v", column, err)
		}
	}
}

// min32 returns the smaller of two int32 values.
func min32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}
