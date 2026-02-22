package services

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

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
//
// This scales to thousands of active rooms because we process them in
// parallel instead of one-at-a-time.
// ---------------------------------------------------------------------------

// flusherDone is used to signal the flusher to stop gracefully.
var flusherDone = make(chan struct{})

// StartFlusher kicks off the background flush loop.
// Call this once during application startup.
func StartFlusher() {
	ticker := time.NewTicker(config.FlushInterval)
	go func() {
		for {
			select {
			case <-flusherDone:
				ticker.Stop()
				// Final flush to persist any remaining buffered messages
				log.Println("[flusher] Final flush before shutdown...")
				FlushAllDirtyRooms()
				return
			case <-ticker.C:
				FlushAllDirtyRooms()
			}
		}
	}()
}

// StopFlusher signals the flusher to stop and waits for the final flush.
func StopFlusher() {
	close(flusherDone)
	// Give the final flush a moment to complete
	time.Sleep(config.FlushInterval + 2*time.Second)
}

// FlushAllDirtyRooms grabs every dirty room ID and processes them concurrently.
// Uses a Redis pipeline to atomically read and clear the dirty set.
func FlushAllDirtyRooms() {
	ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
	defer cancel()

	rdb := redis.GetRawClient()

	// Atomically grab all dirty room IDs and clear the set in one pipeline.
	// This prevents the race condition where IDs added between SMembers and Del
	// would be lost.
	pipe := rdb.Pipeline()
	membersCmd := pipe.SMembers(ctx, config.CHAT_DIRTY_TARGETS)
	pipe.Del(ctx, config.CHAT_DIRTY_TARGETS)

	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("[flusher] Pipeline error reading dirty targets: %v", err)
		return
	}

	dirtyIDs, err := membersCmd.Result()
	if err != nil || len(dirtyIDs) == 0 {
		return // Nothing to flush
	}

	// Feed the room IDs into a bounded worker pool
	jobs := make(chan string, len(dirtyIDs))
	for _, id := range dirtyIDs {
		jobs <- id
	}
	close(jobs)

	var wg sync.WaitGroup
	for range config.FlushWorkerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for targetID := range jobs {
				flushOneRoom(targetID)
			}
		}()
	}

	wg.Wait()
}

// flushOneRoom persists all buffered messages for a single room to Postgres.
func flushOneRoom(targetID string) {
	ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
	defer cancel()

	rdb := redis.GetRawClient()

	bufferKey := config.CHAT_BUFFER_COLON + targetID
	processingKey := config.CHAT_PROCESSING_COLON + targetID

	// Stranger chats are ephemeral — don't persist them, just clean up Redis
	if strings.HasPrefix(targetID, config.STRANGER_PREFIX) {
		rdb.Del(ctx, bufferKey)
		return
	}

	// Rename the buffer to a "processing" key so new messages go to a fresh list.
	// This is our lightweight lock mechanism — no distributed locks needed.
	err := rdb.Rename(ctx, bufferKey, processingKey).Err()
	if err != nil {
		log.Printf("[flusher] Rename failed for %s (key may be empty): %v", targetID, err)
		return
	}

	// Read all messages from the processing list
	rawMessages, err := rdb.LRange(ctx, processingKey, 0, -1).Result()
	if err != nil || len(rawMessages) == 0 {
		rdb.Del(ctx, processingKey)
		return
	}

	// Bulk insert into Postgres
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
		return
	}

	// Success — delete the processed list from Redis
	rdb.Del(ctx, processingKey)
}

// ---------------------------------------------------------------------------
// bulkInsertToPostgres builds a single INSERT statement for all messages.
//
// Example output:
//   INSERT INTO messages (room_id, sender_id, content)
//   VALUES ($1,$2,$3), ($4,$5,$6), ...
//
// We use gjson to extract fields from the raw JSON without allocating
// Go structs. A strings.Builder is used to minimize memory allocations
// when constructing the query string.
// ---------------------------------------------------------------------------

func bulkInsertToPostgres(roomID string, rawMessages []string) error {
	var b strings.Builder
	var valueArgs []interface{}
	argID := 1
	first := true

	for _, rawMsg := range rawMessages {
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

		b.WriteString(fmt.Sprintf("($%d,$%d,$%d)", argID, argID+1, argID+2))
		valueArgs = append(valueArgs, roomID, senderID, content)
		argID += 3
	}

	// Nothing valid to insert
	if len(valueArgs) == 0 {
		return nil
	}

	query := fmt.Sprintf(
		`INSERT INTO messages (room_id, sender_id, content) VALUES %s`,
		b.String(),
	)

	_, err := postgress.Exec(query, valueArgs...)
	return err
}
