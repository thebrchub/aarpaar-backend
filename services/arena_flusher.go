package services

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// Arena Engagement Flusher
//
// Buffers high-frequency engagement events (views, detail expands, profile
// clicks) in Redis SETs and periodically flushes them to Postgres in batch.
//
// Why Redis SETs:
//   - Natural deduplication: SADD("userId:postId") is idempotent
//   - O(1) writes from HTTP handlers (no Postgres round-trip)
//   - Atomic read-and-clear via Lua script (same pattern as chat flusher)
//   - At 10L+ users, this reduces Postgres writes from 100K+/s to ~100/s
//
// Flush cycle (every 5s):
//   1. Atomically grab all members from the Redis SET and delete it
//   2. Parse "userId:postId" pairs
//   3. Batch INSERT into tracking table (ON CONFLICT DO NOTHING)
//   4. Batch UPDATE posts SET counter = counter + N per post
// ---------------------------------------------------------------------------

// BufferView adds a view event to the Redis buffer.
// Called from RecordViewsHandler — O(1) per post ID.
func BufferView(ctx context.Context, userID string, postID int64) {
	rdb := redis.GetRawClient()
	rdb.SAdd(ctx, config.ARENA_VIEWS_BUFFER, userID+":"+strconv.FormatInt(postID, 10))
}

// BufferDetailExpand adds a detail-expand event to the Redis buffer.
// Called from GetPostHandler — O(1).
func BufferDetailExpand(ctx context.Context, userID string, postID int64) {
	rdb := redis.GetRawClient()
	rdb.SAdd(ctx, config.ARENA_EXPANDS_BUFFER, userID+":"+strconv.FormatInt(postID, 10))
}

// BufferProfileClick adds a profile-click event to the Redis buffer.
// Called from RecordProfileClickHandler — O(1).
func BufferProfileClick(ctx context.Context, userID string, postID int64) {
	rdb := redis.GetRawClient()
	rdb.SAdd(ctx, config.ARENA_PROFILE_CLICKS_BUF, userID+":"+strconv.FormatInt(postID, 10))
}

// flushArenaEngagement is called by the main flusher on each tick.
func flushArenaEngagement() {
	flushEngagementSet(config.ARENA_VIEWS_BUFFER, "post_views", "view_count")
	flushEngagementSet(config.ARENA_EXPANDS_BUFFER, "post_detail_expands", "detail_expand_count")
	flushEngagementSet(config.ARENA_PROFILE_CLICKS_BUF, "post_profile_clicks", "profile_click_count")
	flushLikes()
}

// flushEngagementSet atomically grabs all entries from a Redis SET,
// batch-inserts them into the tracking table, and increments the
// denormalized counter on the posts table.
func flushEngagementSet(redisKey, tableName, counterColumn string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rdb := redis.GetRawClient()

	// Atomically read all members and delete the set (same Lua script as chat flusher)
	result, err := atomicSMembersAndDel.Run(ctx, rdb, []string{redisKey}).StringSlice()
	if err != nil {
		if err != goredis.Nil {
			log.Printf("[arena-flusher] Lua script error reading %s: %v", redisKey, err)
		}
		return
	}
	if len(result) == 0 {
		return
	}

	// Parse "userId:postId" pairs and group by postId for counter updates
	type pair struct {
		userID string
		postID int64
	}
	pairs := make([]pair, 0, len(result))
	postCounts := make(map[int64]int, len(result))

	for _, entry := range result {
		// Format: "userId:postId" — userId is UUID (contains hyphens), postId after last colon
		lastColon := strings.LastIndex(entry, ":")
		if lastColon <= 0 || lastColon >= len(entry)-1 {
			continue
		}
		uid := entry[:lastColon]
		pid, err := strconv.ParseInt(entry[lastColon+1:], 10, 64)
		if err != nil {
			continue
		}
		pairs = append(pairs, pair{uid, pid})
		// Don't count yet — wait for INSERT to confirm which are truly new
	}

	if len(pairs) == 0 {
		return
	}

	// Batch INSERT into tracking table, RETURNING post_id for new rows only.
	// This naturally deduplicates — ON CONFLICT DO NOTHING skips existing rows.
	const chunkSize = 500
	for i := 0; i < len(pairs); i += chunkSize {
		end := i + chunkSize
		if end > len(pairs) {
			end = len(pairs)
		}
		chunk := pairs[i:end]

		values := make([]string, len(chunk))
		params := make([]any, 0, len(chunk)*2)
		for j, p := range chunk {
			params = append(params, p.userID, p.postID)
			values[j] = fmt.Sprintf("($%d::uuid, $%d::bigint)", j*2+1, j*2+2)
		}

		query := fmt.Sprintf(
			`INSERT INTO %s (user_id, post_id) VALUES %s ON CONFLICT DO NOTHING RETURNING post_id`,
			tableName, strings.Join(values, ", "),
		)

		rows, err := postgress.GetRawDB().QueryContext(ctx, query, params...)
		if err != nil {
			log.Printf("[arena-flusher] INSERT into %s failed: %v", tableName, err)
			// Re-queue the entries so they aren't lost
			reCtx, reCancel := context.WithTimeout(context.Background(), 5*time.Second)
			members := make([]any, len(chunk))
			for k, p := range chunk {
				members[k] = p.userID + ":" + strconv.FormatInt(p.postID, 10)
			}
			rdb.SAdd(reCtx, redisKey, members...)
			reCancel()
			continue
		}

		// Count newly inserted rows per post
		for rows.Next() {
			var pid int64
			if rows.Scan(&pid) == nil {
				postCounts[pid]++
			}
		}
		rows.Close()
	}

	if len(postCounts) == 0 {
		return
	}

	// Batch UPDATE posts SET counter = counter + N using a VALUES join.
	// This is ONE statement for all posts instead of N separate UPDATEs.
	updateValues := make([]string, 0, len(postCounts))
	updateParams := make([]any, 0, len(postCounts)*2)
	argID := 1
	for pid, cnt := range postCounts {
		updateValues = append(updateValues, fmt.Sprintf("($%d::bigint, $%d::int)", argID, argID+1))
		updateParams = append(updateParams, pid, cnt)
		argID += 2
	}

	updateQuery := fmt.Sprintf(
		`UPDATE posts SET %s = %s + v.cnt
		 FROM (VALUES %s) AS v(id, cnt)
		 WHERE posts.id = v.id`,
		counterColumn, counterColumn, strings.Join(updateValues, ", "),
	)

	if _, err := postgress.GetRawDB().ExecContext(ctx, updateQuery, updateParams...); err != nil {
		log.Printf("[arena-flusher] UPDATE %s failed: %v", counterColumn, err)
	}
}

// ---------------------------------------------------------------------------
// BufferLike / BufferUnlike — O(1) Redis SADD, flushed in batch
// ---------------------------------------------------------------------------

// BufferLike adds a like event to the Redis buffer.
func BufferLike(ctx context.Context, userID string, postID int64) {
	rdb := redis.GetRawClient()
	entry := userID + ":" + strconv.FormatInt(postID, 10)
	rdb.SAdd(ctx, config.ARENA_LIKES_BUFFER, entry)
	// Cancel a pending unlike for the same pair (idempotency)
	rdb.SRem(ctx, config.ARENA_UNLIKES_BUFFER, entry)
}

// BufferUnlike adds an unlike event to the Redis buffer.
func BufferUnlike(ctx context.Context, userID string, postID int64) {
	rdb := redis.GetRawClient()
	entry := userID + ":" + strconv.FormatInt(postID, 10)
	rdb.SAdd(ctx, config.ARENA_UNLIKES_BUFFER, entry)
	// Cancel a pending like for the same pair (idempotency)
	rdb.SRem(ctx, config.ARENA_LIKES_BUFFER, entry)
}

// flushLikes drains both like and unlike buffers and applies them in batch.
func flushLikes() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rdb := redis.GetRawClient()

	// Drain likes
	likeEntries, err := atomicSMembersAndDel.Run(ctx, rdb, []string{config.ARENA_LIKES_BUFFER}).StringSlice()
	if err != nil && err != goredis.Nil {
		log.Printf("[arena-flusher] Lua error reading likes: %v", err)
	}

	// Drain unlikes
	unlikeEntries, err := atomicSMembersAndDel.Run(ctx, rdb, []string{config.ARENA_UNLIKES_BUFFER}).StringSlice()
	if err != nil && err != goredis.Nil {
		log.Printf("[arena-flusher] Lua error reading unlikes: %v", err)
	}

	if len(likeEntries) == 0 && len(unlikeEntries) == 0 {
		return
	}

	type pair struct {
		userID string
		postID int64
		raw    string // original "userId:postId" for requeue
	}

	parsePairs := func(entries []string) []pair {
		out := make([]pair, 0, len(entries))
		for _, e := range entries {
			lastColon := strings.LastIndex(e, ":")
			if lastColon <= 0 || lastColon >= len(e)-1 {
				continue
			}
			pid, err := strconv.ParseInt(e[lastColon+1:], 10, 64)
			if err != nil {
				continue
			}
			out = append(out, pair{e[:lastColon], pid, e})
		}
		return out
	}

	likes := parsePairs(likeEntries)
	unlikes := parsePairs(unlikeEntries)

	// Cancel out overlapping entries (like + unlike for same pair = no-op)
	unlikeSet := make(map[string]bool, len(unlikes))
	for _, u := range unlikes {
		unlikeSet[u.raw] = true
	}
	filteredLikes := make([]pair, 0, len(likes))
	for _, l := range likes {
		if unlikeSet[l.raw] {
			delete(unlikeSet, l.raw) // cancel both
		} else {
			filteredLikes = append(filteredLikes, l)
		}
	}
	filteredUnlikes := make([]pair, 0, len(unlikeSet))
	for _, u := range unlikes {
		if unlikeSet[u.raw] {
			filteredUnlikes = append(filteredUnlikes, u)
		}
	}

	// Helper to extract raw strings for requeue
	rawEntries := func(ps []pair) []string {
		out := make([]string, len(ps))
		for i, p := range ps {
			out[i] = p.raw
		}
		return out
	}

	// Batch INSERT likes: ON CONFLICT DO NOTHING RETURNING post_id
	likeCounts := make(map[int64]int)
	const chunkSize = 500
	for i := 0; i < len(filteredLikes); i += chunkSize {
		end := i + chunkSize
		if end > len(filteredLikes) {
			end = len(filteredLikes)
		}
		chunk := filteredLikes[i:end]

		values := make([]string, len(chunk))
		params := make([]any, 0, len(chunk)*2)
		for j, p := range chunk {
			params = append(params, p.userID, p.postID)
			values[j] = fmt.Sprintf("($%d::uuid, $%d::bigint)", j*2+1, j*2+2)
		}

		rows, err := postgress.GetRawDB().QueryContext(ctx,
			fmt.Sprintf(`INSERT INTO post_likes (user_id, post_id) VALUES %s ON CONFLICT DO NOTHING RETURNING post_id`,
				strings.Join(values, ", ")),
			params...)
		if err != nil {
			log.Printf("[arena-flusher] INSERT likes failed: %v", err)
			requeue(rdb, config.ARENA_LIKES_BUFFER, rawEntries(chunk))
			continue
		}
		for rows.Next() {
			var pid int64
			if rows.Scan(&pid) == nil {
				likeCounts[pid]++
			}
		}
		rows.Close()
	}

	// Batch DELETE unlikes using CTE: returns affected post_ids
	unlikeCounts := make(map[int64]int)
	for i := 0; i < len(filteredUnlikes); i += chunkSize {
		end := i + chunkSize
		if end > len(filteredUnlikes) {
			end = len(filteredUnlikes)
		}
		chunk := filteredUnlikes[i:end]

		values := make([]string, len(chunk))
		params := make([]any, 0, len(chunk)*2)
		for j, p := range chunk {
			params = append(params, p.userID, p.postID)
			values[j] = fmt.Sprintf("($%d::uuid, $%d::bigint)", j*2+1, j*2+2)
		}

		rows, err := postgress.GetRawDB().QueryContext(ctx,
			fmt.Sprintf(`WITH del AS (
				DELETE FROM post_likes WHERE (user_id, post_id) IN (VALUES %s) RETURNING post_id
			) SELECT post_id FROM del`, strings.Join(values, ", ")),
			params...)
		if err != nil {
			log.Printf("[arena-flusher] DELETE unlikes failed: %v", err)
			requeue(rdb, config.ARENA_UNLIKES_BUFFER, rawEntries(chunk))
			continue
		}
		for rows.Next() {
			var pid int64
			if rows.Scan(&pid) == nil {
				unlikeCounts[pid]++
			}
		}
		rows.Close()
	}

	// Merge like and unlike deltas into a single UPDATE per post
	deltas := make(map[int64]int)
	for pid, cnt := range likeCounts {
		deltas[pid] += cnt
	}
	for pid, cnt := range unlikeCounts {
		deltas[pid] -= cnt
	}

	if len(deltas) == 0 {
		return
	}

	updateValues := make([]string, 0, len(deltas))
	updateParams := make([]any, 0, len(deltas)*2)
	argID := 1
	for pid, delta := range deltas {
		if delta == 0 {
			continue
		}
		updateValues = append(updateValues, fmt.Sprintf("($%d::bigint, $%d::int)", argID, argID+1))
		updateParams = append(updateParams, pid, delta)
		argID += 2
	}

	if len(updateValues) > 0 {
		_, err := postgress.GetRawDB().ExecContext(ctx,
			fmt.Sprintf(`UPDATE posts SET like_count = GREATEST(like_count + v.delta, 0)
				FROM (VALUES %s) AS v(id, delta)
				WHERE posts.id = v.id`, strings.Join(updateValues, ", ")),
			updateParams...)
		if err != nil {
			log.Printf("[arena-flusher] UPDATE like_count failed: %v", err)
		}
	}
}

// requeue pushes failed entries back into the Redis SET for retry.
func requeue(rdb goredis.Cmdable, key string, entries []string) {
	if len(entries) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	members := make([]any, len(entries))
	for i, e := range entries {
		members[i] = e
	}
	rdb.SAdd(ctx, key, members...)
}
