# Aarpaar Backend — Performance, Memory, CPU, DB & Throughput Audit

> **Audit Date:** February 2026  
> **Codebase:** Go 1.25.5 | Postgres | Redis | WebSocket (gorilla/websocket)

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Architecture Overview](#2-architecture-overview)
3. [What's Already Well-Optimized](#3-whats-already-well-optimized)
4. [Performance Issues Found](#4-performance-issues-found)
   - [P0 — Critical (Production-Breaking)](#p0--critical)
   - [P1 — High (Scalability Limiters)](#p1--high)
   - [P2 — Medium (Efficiency Wins)](#p2--medium)
   - [P3 — Low (Minor Improvements)](#p3--low)
5. [CPU Analysis](#5-cpu-analysis)
6. [Memory Analysis](#6-memory-analysis)
7. [Database Write Analysis](#7-database-write-analysis)
8. [Throughput Analysis](#8-throughput-analysis)
9. [Redis Analysis](#9-redis-analysis)
10. [WebSocket Analysis](#10-websocket-analysis)
11. [Recommendations Summary](#11-recommendations-summary)

---

## 1. Executive Summary

The codebase is **architecturally sound** with many advanced patterns already in place (sharded locks, Lua scripts, zero-alloc SQL-to-JSON, batched flushes). However, there are **12 issues** across performance, memory, CPU, DB writes, and throughput that range from critical to minor.

| Severity | Count | Impact |
|----------|-------|--------|
| P0 Critical | 2 | Data loss, connection leak |
| P1 High | 4 | Scalability ceiling at ~5K concurrent users |
| P2 Medium | 4 | 10-30% efficiency gains |
| P3 Low | 2 | Minor polish |

---

## 2. Architecture Overview

```
Client WS ──► Engine (sharded rooms) ──► Redis Pub/Sub ──► All Servers
                                              │
                                    ┌─────────┴──────────┐
                                    ▼                    ▼
                              chat:buffer:*        chat:dirty_targets
                                    │                    │
                                    └────► Flusher (3s) ─┘
                                              │
                                              ▼
                                          Postgres
```

---

## 3. What's Already Well-Optimized

These patterns are **production-grade** — no changes needed:

| Pattern | Location | Why It's Good |
|---------|----------|---------------|
| **Sharded room locks (64 shards)** | `engine.go` L38-46 | Eliminates global mutex contention; 64 rooms can be written simultaneously |
| **FNV32a hash for shard selection** | `engine.go` L105-108 | Fast, non-crypto hash — perfect for load distribution |
| **Lua SMEMBERS+DEL atomicity** | `flusher.go` L49-54 | Prevents race between read and delete of dirty set |
| **Lua HGETALL+DEL atomicity** | `flusher.go` L59-64 | Same pattern for receipt hashes |
| **Rename-based buffer isolation** | `flusher.go` L141-145 | Writers get a fresh list while flusher processes the old one — no locking |
| **Bulk INSERT with chunking (500)** | `flusher.go` L173-184 | Prevents oversized SQL; balances batch size vs query cost |
| **`strconv.Itoa` over `fmt.Sprintf`** | `flusher.go` L191 | ~5x faster for int→string in hot path |
| **`gjson.GetManyBytes` single-pass** | `client.go` L116-117, `engine.go` L337-342 | One JSON scan instead of N separate lookups |
| **Zero-alloc SQL (json_agg)** | `rooms.go`, `messages.go`, `friends.go`, `users.go` | Postgres builds JSON; Go just pipes bytes to HTTP |
| **Pre-computed `fromPrefix`** | `client.go` L57, L281 | Zero-alloc sender stamping on every message |
| **GREATEST() for receipt UPDATEs** | `flusher.go` L258 | Multiple receipts for same user collapse into one UPDATE |
| **Covering index for auto-subscribe** | `migrate.go` L105-106 | Index-only scan on every WS connect |
| **UNION ALL for friend queries** | `engine.go` L555-558 | Enables index-only scans on both directions of friendships |
| **Distroless container** | `Dockerfile` L39 | Minimal attack surface, tiny image |
| **`-trimpath -ldflags="-s -w"`** | `Dockerfile` L31 | Stripped binary, no debug symbols |
| **WritePump message batching** | `client.go` L300-305 | Multiple queued messages sent in one WebSocket frame |
| **Pipeline for Redis batching** | `engine.go` L579-594 | N publishes in a single Redis round-trip |

---

## 4. Performance Issues Found

### P0 — Critical

#### P0-1: `defer cancel()` Inside a Loop Defers Until Function Return

**File:** `client.go` lines 267-268 (inside `readPump` → `MsgTypeSendMessage`)

```go
ctx, cancel := context.WithTimeout(context.Background(), config.RedisOpTimeout)
defer cancel()   // ← BUG: this defers until readPump() returns, NOT until loop iteration ends
```

**Impact:**  
- Every message creates a context that is **never canceled** until the WebSocket disconnects.
- For a user who sends 1,000 messages in a session, 1,000 context timers are leaked simultaneously.
- This causes **goroutine leaks** and **memory growth** proportional to messages × session length.
- Under load: OOM risk.

**Fix:**  
```go
func (c *Client) bufferMessage(ctx context.Context, roomID string, payload []byte) {
    ctx, cancel := context.WithTimeout(ctx, config.RedisOpTimeout)
    defer cancel()
    pipe := redis.GetRawClient().Pipeline()
    pipe.RPush(ctx, config.CHAT_BUFFER_COLON+roomID, payload)
    pipe.SAdd(ctx, config.CHAT_DIRTY_TARGETS, roomID)
    if _, err := pipe.Exec(ctx); err != nil {
        log.Printf("[client] Failed to buffer message: %v", err)
    }
}
```

---

#### P0-2: `enrichRoomsWithOnlineStatus` Deserializes + Re-serializes Full JSON

**File:** `rooms.go` lines 309-339

```go
func enrichRoomsWithOnlineStatus(raw []byte, currentUserID string) []byte {
    var rooms []roomInfo
    json.Unmarshal(raw, &rooms)  // full deserialization
    // ... set IsOnline flags ...
    enriched, _ := json.Marshal(rooms) // full re-serialization
    return enriched
}
```

**Impact:**  
- Postgres already built the JSON (zero-alloc pattern), but this function **undoes** that by deserializing into Go structs and re-marshaling.
- For a user with 50 rooms × 2 members each = 100 allocations for `roomMember` structs + the `roomInfo` slice.
- Contradicts the zero-alloc design philosophy of the rest of the codebase.
- At scale (50 rooms, 5K users calling `/rooms`), this generates ~500K allocations/sec → heavy GC.

**Fix:**  
Use `gjson` + `sjson` to surgically inject `"is_online":true` into the existing JSON bytes without full deserialization:
```go
func enrichRoomsWithOnlineStatus(raw []byte, currentUserID string) []byte {
    e := chat.GetEngine()
    if e == nil {
        return raw
    }
    result := raw
    gjson.GetBytes(raw, "#.members.#.id").ForEach(func(roomIdx, memberIDs gjson.Result) bool {
        memberIDs.ForEach(func(memberIdx, id gjson.Result) bool {
            path := fmt.Sprintf("%d.members.%d.is_online", roomIdx.Int(), memberIdx.Int())
            result, _ = sjson.SetBytes(result, path, e.IsUserOnline(id.String()))
            return true
        })
        return true
    })
    return result
}
```
Or better yet: compute `is_online` on the **client side** by maintaining a presence map (already pushed via `presence_online`/`presence_offline` events).

---

### P1 — High

#### P1-1: `broadcastPresence` Fires N Redis PUBLISHes Per Friend

**File:** `engine.go` lines 569-597

```go
for _, friendID := range friendIDs {
    envelope := map[string]interface{}{...}  // alloc per friend
    envBytes, _ := json.Marshal(envelope)    // marshal per friend
    pipe.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, envBytes)
}
```

**Impact:**  
- A user with 500 friends going online generates 500 `json.Marshal` calls + 500 map allocations.
- The pipeline batches the network round-trip, but the CPU/memory cost of marshaling is still O(N).
- At 1K users with 500 friends each going online simultaneously: 500K marshal calls.

**Fix:**  
Publish a **single** presence event with a list of target friend IDs, and let each server filter locally:
```go
// Single publish
payload := map[string]interface{}{
    "type":    "presence_online",
    "userId":  userID,
    "targets": friendIDs,  // server-side: deliver only to locally-connected targets
}
pipe.Publish(ctx, config.CHAT_GLOBAL_CHANNEL, envBytes)
```

---

#### P1-2: `getUserActiveRoomIDs` — One DB Query Per WebSocket Connect

**File:** `engine.go` lines 242-260

```go
func getUserActiveRoomIDs(userID string) []string {
    rows, err := postgress.GetRawDB().Query(
        `SELECT room_id FROM room_members WHERE user_id = $1 AND status = 'active'`, userID,
    )
    // ...
}
```

**Impact:**  
- Every WebSocket connection triggers a Postgres query. If 10K users reconnect after a deployment, that's 10K queries hitting Postgres simultaneously.
- The covering index (`idx_room_members_user_active`) helps, but connection storm is still a concern.

**Fix:**  
- Cache room memberships in Redis with a short TTL (e.g. `user:rooms:{userID}` → SET of room IDs, 5 min TTL).
- Invalidate on room create/delete/accept/reject.
- Falls back to Postgres on cache miss.

---

#### P1-3: `GetRoomsHandler` — N+1 Problem with Unread Count

**File:** `rooms.go` lines 84-101

The `LATERAL JOIN` for unread count runs a `COUNT(id)` per room:
```sql
LEFT JOIN LATERAL (
    SELECT COUNT(id)::int AS unread_count 
    FROM messages m 
    WHERE m.room_id = r.id AND m.created_at > rm.last_read_at
      AND m.sender_id != $1
) uc ON true
```

**Impact:**  
- For a user with 50 rooms, Postgres executes 50 correlated subqueries.
- Each `COUNT` scans `messages` from `last_read_at` to now — expensive for active rooms.
- With `idx_messages_room_id_created_at_desc`, each scan is b-tree-bounded but still O(unread).

**Fix:**  
Maintain an `unread_count` column in `room_members` and increment/decrement it atomically:
- On message INSERT trigger: `UPDATE room_members SET unread_count = unread_count + 1 WHERE room_id = NEW.room_id AND user_id != NEW.sender_id`
- On `mark_read`: `UPDATE room_members SET unread_count = 0, last_read_at = NOW() WHERE ...`
- This converts 50 COUNT queries into a single indexed lookup.

---

#### P1-4: No Connection Pooling Limits Visible

**File:** `main.go` line 43

```go
postgress.Init(config.PostgresConn, config.PGTimeout)
```

**Impact:**  
- The `go-starter-kit` is a black box — unclear if `SetMaxOpenConns`, `SetMaxIdleConns`, `SetConnMaxLifetime` are set.
- Without limits, Go's `database/sql` defaults to **unlimited** connections, which can overwhelm Postgres under load.
- Redis connection pooling is also opaque via the starter kit.

**Fix:**
```go
db := postgress.GetRawDB()
db.SetMaxOpenConns(50)        // Match Postgres max_connections / number_of_pods
db.SetMaxIdleConns(10)
db.SetConnMaxLifetime(5 * time.Minute)
```

---

### P2 — Medium

#### P2-1: `SearchUsersHandler` Uses `ILIKE '%..%'` — Full Table Scan

**File:** `users.go` lines 79-86

```sql
WHERE is_banned = false
  AND (username ILIKE '%' || $1 || '%' OR name ILIKE '%' || $1 || '%')
```

**Impact:**  
- Leading `%` prevents index usage. The `gin_trgm_ops` index on `username` and `name` CAN help with trigram matching, but `ILIKE` with leading wildcard may not use it depending on Postgres version and query planner.
- At 100K+ users, this becomes a sequential scan.

**Fix:**  
Use trigram similarity explicitly:
```sql
WHERE is_banned = false
  AND (username % $1 OR name % $1)
ORDER BY GREATEST(similarity(username, $1), similarity(name, $1)) DESC
LIMIT 20
```
Or switch to `websearch_to_tsquery` with a `tsvector` GIN index for proper full-text search.

---

#### P2-2: `ContainsProfanity` — Linear Scan of N Regex Patterns

**File:** `sanitize.go` lines 54-60

```go
func ContainsProfanity(s string) bool {
    lower := strings.ToLower(s)
    for _, re := range profanityRegexes {
        if re.MatchString(lower) {
            return true
        }
    }
    return false
}
```

**Impact:**  
- 12 compiled regexes evaluated sequentially per stranger message.
- Each regex does a full string scan. For a 4KB message: 12 × 4KB = 48KB of regex scanning.
- Hot path: called on every stranger chat message.

**Fix:**  
Compile all words into a **single** alternation regex:
```go
var profanityRegex = regexp.MustCompile(`(?i)\b(?:fuck|shit|bitch|asshole|...)\b`)

func ContainsProfanity(s string) bool {
    return profanityRegex.MatchString(strings.ToLower(s))
}
```
Single regex, single pass, ~12x faster. The Go regex engine handles alternation efficiently with its NFA.

---

#### P2-3: `enrichFriendsWithOnlineStatus` — Same Deser/Reser Issue

**File:** `friends.go` lines 482-503

Same problem as P0-2: unmarshal into `[]friendInfo`, set `IsOnline`, re-marshal.

**Impact:**
- Smaller than rooms (friends list is typically 10-200 items), but still unnecessary allocations.

**Fix:** Same as P0-2 — use `gjson`/`sjson` or move to client-side presence.

---

#### P2-4: Message Sanitization Rebuilds JSON When Text Changes

**File:** `client.go` lines 236-248

```go
if sanitized != rawText {
    parsed := gjson.ParseBytes(payload)
    fields := make(map[string]interface{})
    parsed.ForEach(func(key, value gjson.Result) bool {
        // ...rebuild all fields...
        return true
    })
    if rebuilt, err := json.Marshal(fields); err == nil {
        payload = rebuilt
    }
}
```

**Impact:**  
- Iterates all JSON fields, creates a `map[string]interface{}`, marshals it back.
- ~3 allocations + full JSON rebuild just to change one field.
- Only triggers when sanitization actually modifies text (e.g. strips HTML), so low frequency.

**Fix:**  
Use `sjson.SetBytes` for surgical replacement:
```go
if sanitized != rawText {
    payload, _ = sjson.SetBytes(payload, config.FieldText, sanitized)
}
```

---

### P3 — Low

#### P3-1: `json.NewEncoder(w).Encode(data)` Adds Trailing Newline

**File:** `response.go` lines 30, 37, 48

```go
json.NewEncoder(w).Encode(data)
```

**Impact:**  
- `json.Encoder.Encode` appends a `\n` after the JSON. Not a bug, but adds 1 byte to every response.
- More importantly, it allocates an encoder per call. For high-throughput endpoints, `json.Marshal` + `w.Write` is marginally faster.

**Fix (optional):**
```go
bytes, _ := json.Marshal(data)
w.Write(bytes)
```

---

#### P3-2: `htmlTagRegex` Match on Every Message (Even Non-HTML)

**File:** `sanitize.go` lines 17-20

```go
var htmlTagRegex = regexp.MustCompile(`<[^>]*>`)

func StripHTMLTags(s string) string {
    return htmlTagRegex.ReplaceAllString(s, "")
}
```

**Impact:**  
- Regex runs on every message even if there's no `<` character.
- Minimal CPU impact due to DFA optimization, but a fast-path `strings.IndexByte(s, '<')` check would skip 99%+ of messages.

**Fix:**
```go
func StripHTMLTags(s string) string {
    if strings.IndexByte(s, '<') == -1 {
        return s
    }
    return htmlTagRegex.ReplaceAllString(s, "")
}
```

---

## 5. CPU Analysis

| Hot Path | CPU Cost | Bottleneck |
|----------|----------|------------|
| `readPump` → `gjson.GetManyBytes` | **Low** | Single-pass JSON extraction — optimal |
| `readPump` → `SanitizeMessage` + `ContainsProfanity` | **Medium** | 12 regex scans per stranger message (P2-2) |
| `readPump` → `json.Marshal(fields)` for sanitized text | **Low** | Only triggers when HTML is stripped |
| `writePump` → batched writes | **Low** | Efficient frame batching |
| `listenToRedis` → `subscribeAndListen` | **Low** | Single goroutine, single-pass JSON routing |
| `broadcastPresence` → N × `json.Marshal` | **High at scale** | O(friends) marshaling (P1-1) |
| `FlushAllDirtyRooms` → worker pool | **Low** | 10 workers, chunked SQL — well-designed |
| `enrichRoomsWithOnlineStatus` | **Medium** | Full deser+reser negates zero-alloc SQL (P0-2) |

**Overall CPU profile estimate:**
- At <1K users: CPU is not a concern (~2-5% of a single core).
- At 10K users: `broadcastPresence` and `enrichRooms` become the dominant CPU consumers.
- At 50K users: profanity regex and presence broadcasts need optimization.

---

## 6. Memory Analysis

| Component | Memory Pattern | Risk |
|-----------|---------------|------|
| **Client.Send channel** | 128 × []byte per client | 128 × ~200B = ~25KB per connection. At 10K users: ~250MB. **Acceptable.** |
| **Client.JoinedRooms (sync.Map)** | ~48 bytes per entry | Negligible |
| **Room shard maps** | map[*Client]bool per room | ~64 bytes per subscription. 10K users × 20 rooms = ~12MB. **Fine.** |
| **Flusher rawMessages** | `LRange` loads all messages into memory | If a room has 100K buffered messages (Redis was down, flusher backed up), this is ~100MB in one goroutine. **Risky.** Consider paginated reads. |
| **Context leak (P0-1)** | Unbounded context accumulation | Each un-canceled context holds ~200 bytes + timer goroutine. 1K messages/session × 10K sessions = ~2GB leaked. **Critical.** |
| **Goroutines** | 2 per client (readPump + writePump) + 1 Redis listener + flusher | 10K users = 20K goroutines (~80MB stack). **Acceptable** for Go. |
| **enrichRooms/enrichFriends** | Temporary struct allocations | Survive until next GC. At high request rates, increases GC frequency. |

**Memory ceiling estimate:**
- 10K concurrent WebSocket users: ~500MB baseline
- With P0-1 leak fixed: stable at ~500MB
- Without fix: grows unboundedly with message volume

---

## 7. Database Write Analysis

### Write Paths

| Path | Frequency | Mechanism | Efficiency |
|------|-----------|-----------|------------|
| **Message INSERT** | Every 3s (flusher) | Bulk INSERT in chunks of 500 | **Excellent** — batched, chunked, no per-message INSERT |
| **Receipt UPDATE** | Every 3s (flusher) | Batch UPDATE with VALUES clause + GREATEST() | **Excellent** — collapses duplicates |
| **last_message_at trigger** | Per message INSERT | Row-level trigger on messages | **Good** — but fires per-row in bulk INSERT (500 trigger fires per chunk) |
| **last_seen_at UPDATE** | Per disconnect | Single UPDATE per user | **Fine** |
| **Room/member INSERT** | Per DM create / match | Transaction with 2-3 statements | **Fine** |
| **Friend request UPSERT** | Per friend action | Single statement | **Fine** |

### Trigger Concern: `update_room_last_message_at`

```sql
CREATE TRIGGER trg_update_room_last_message_at
    AFTER INSERT ON messages
    FOR EACH ROW EXECUTE FUNCTION update_room_last_message_at()
```

When the flusher inserts 500 messages for the same room in one chunk, this trigger fires 500 times, each executing:
```sql
UPDATE rooms SET last_message_at = NEW.created_at WHERE id = NEW.room_id;
```

**Impact:** 500 UPDATE statements to `rooms` for the same row = 500 row locks, 500 WAL entries. The final value is the last `created_at`, making the first 499 writes **wasted**.

**Fix:** Change to a `STATEMENT`-level trigger or update `rooms.last_message_at` directly in the flusher after the bulk INSERT:
```go
// After successful bulkInsertToPostgres:
postgress.Exec(`UPDATE rooms SET last_message_at = NOW() WHERE id = $1`, roomID)
```
And remove the per-row trigger.

### DB Write Volume Estimate

| Scenario | Messages/sec | DB Writes/3s |
|----------|-------------|--------------|
| 100 active rooms, 2 msg/s each | 200 | 1 bulk INSERT (~200 rows) + 1 receipt UPDATE |
| 1,000 active rooms, 5 msg/s each | 5,000 | 10 bulk INSERTs (~500 rows each) + receipt UPDATEs |
| 10,000 active rooms, 10 msg/s | 100,000 | 200 bulk INSERTs across 10 workers + receipts |

At 100K messages per 3s interval: Postgres can handle this with proper `shared_buffers` and `wal_buffers` tuning, **but the per-row trigger makes it 2x worse** (100K INSERTs + 100K trigger UPDATEs).

---

## 8. Throughput Analysis

### WebSocket Message Throughput

| Bottleneck | Throughput Limit |
|------------|-----------------|
| `readPump` per client | ~10K msg/s per goroutine (JSON parse + Redis publish) |
| **Redis Pub/Sub** | ~100K msg/s (single channel, single subscriber goroutine) |
| `writePump` batching | ~50K msg/s per client (batches queued messages into frames) |
| **Single `listenToRedis` goroutine** | **This is THE bottleneck** — one goroutine processes ALL messages from Redis |

**Critical insight:** The `subscribeAndListen` function runs in a **single goroutine** that processes all incoming Pub/Sub messages sequentially. At high volume, this becomes the system's throughput ceiling.

**Fix for >50K msg/s:**
```go
// Fan-out: N worker goroutines consuming from the Pub/Sub channel
ch := pubsub.Channel(goredis.WithChannelSize(10000))
for i := 0; i < runtime.NumCPU(); i++ {
    go func() {
        for msg := range ch {
            e.handlePubSubMessage(msg)
        }
    }()
}
```
Note: room ordering must be preserved — use consistent hashing to route messages for the same room to the same worker.

### HTTP API Throughput

| Endpoint | Bottleneck | Est. RPS |
|----------|------------|----------|
| `GET /rooms` | Postgres LATERAL JOINs + enrich | ~500 RPS |
| `GET /rooms/{id}/messages` | Postgres (indexed) | ~2,000 RPS |
| `POST /match/enter` | Redis SPOP + Postgres blocked check | ~1,000 RPS |
| `GET /friends` | Postgres + enrich | ~1,000 RPS |
| `GET /users/search` | Postgres ILIKE (P2-1) | ~200 RPS |

### Rate Limiter
- 10 req/s per IP, burst 20 — appropriate for a chat app API.
- WebSocket is not rate-limited per-message — a malicious client could send 10K msg/s. Consider adding per-client message rate limiting in `readPump`.

---

## 9. Redis Analysis

| Key Pattern | Type | Concern |
|-------------|------|---------|
| `chat:buffer:{roomId}` | List | Grows unboundedly if flusher is down. Add LTRIM or max-length check. |
| `chat:dirty_targets` | Set | Atomic via Lua — no issue. |
| `chat:read_receipts` | Hash | Grows with active rooms × users. Atomic flush — no issue. |
| `chat:delivery_receipts` | Hash | Same as above. |
| `stranger_members:{roomId}` | Set | 24h TTL — good. |
| `chat:closed:{roomId}` | String | 24h TTL — good. |
| `match_queue:Any_seeking_Any` | Set | No TTL — stale entries if users disconnect without calling `/match/leave`. Mitigated by `SRem` in Unregister, but only for this server's users. |
| `friend_req:{roomId}:{userId}` | String | 1h TTL — good. |

**Memory concern:** If flusher goes down and messages accumulate, each `chat:buffer:*` list grows without bound. Add a safety cap:
```go
// In readPump, before RPush:
length, _ := rdb.LLen(ctx, config.CHAT_BUFFER_COLON+roomID).Result()
if length > 10000 {
    sendError(c, "BUFFER_FULL", "Server is busy, please retry")
    continue
}
```

---

## 10. WebSocket Analysis

| Metric | Value | Assessment |
|--------|-------|------------|
| Read buffer | 4KB | Good for chat messages |
| Write buffer | 4KB | Good |
| Max message size | 4KB | Prevents abuse |
| Ping interval | 54s (90% of pongWait) | Standard |
| Pong timeout | 60s | Standard |
| Write timeout | 10s | Good |
| Send channel buffer | 128 messages | Good — large enough for bursts |
| Close handling | `sync.Once` on Send channel | Prevents double-close panics — excellent |

**Upgrade CheckOrigin:**
```go
CheckOrigin: func(r *http.Request) bool {
    if config.CORSOrigin == "*" {
        return true  // This is fine for dev, but in prod CORS_ORIGIN should be set
    }
    origin := r.Header.Get("Origin")
    return origin == config.CORSOrigin
},
```
In production, ensure `CORS_ORIGIN` is not `"*"` — otherwise any website can open WebSocket connections to your backend.

---

## 11. Recommendations Summary

### Immediate Fixes (Do Now)

| # | Issue | Fix | Impact |
|---|-------|-----|--------|
| P0-1 | Context leak in readPump | Extract buffer logic to separate function with proper `defer cancel()` | Prevents OOM |
| P0-2 | enrichRooms full deser/reser | Use gjson/sjson or move to client-side presence | -70% allocs on /rooms |

### Before 10K Users

| # | Issue | Fix | Impact |
|---|-------|-----|--------|
| P1-1 | N presence publishes per friend | Single publish with target list | -99% presence CPU |
| P1-2 | DB query per WS connect | Cache room IDs in Redis | -90% Postgres load on reconnect storms |
| P1-3 | COUNT per room for unread | Maintain counter column | -50x faster /rooms query |
| P1-4 | No connection pool limits | Set MaxOpenConns/MaxIdleConns | Prevents Postgres connection exhaustion |

### Before 50K Users

| # | Issue | Fix | Impact |
|---|-------|-----|--------|
| P2-1 | ILIKE full scan on search | Use trigram similarity operator (%) | 100x faster search |
| P2-2 | 12 regex scans for profanity | Single alternation regex | 12x faster profanity check |
| Throughput | Single Pub/Sub consumer goroutine | Fan out to N workers with consistent hashing | 4-8x Pub/Sub throughput |
| Trigger | Per-row trigger on messages | Statement-level or manual UPDATE in flusher | -50% DB write volume |

### Nice to Have

| # | Issue | Fix | Impact |
|---|-------|-----|--------|
| P2-3 | enrichFriends deser/reser | gjson/sjson | Minor alloc reduction |
| P2-4 | JSON rebuild for sanitized text | sjson.SetBytes | Minor alloc reduction |
| P3-1 | Encoder trailing newline | json.Marshal + w.Write | Negligible |
| P3-2 | Regex on every non-HTML message | Fast-path IndexByte check | Negligible |
| — | Redis buffer overflow protection | LLen cap before RPush | Prevents Redis OOM |
| — | Per-message rate limiting on WS | Token bucket in readPump | Prevents abuse |

---

*End of audit.*
