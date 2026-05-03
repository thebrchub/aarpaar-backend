package services

import (
	"context"
	"database/sql"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/models"
)

// ---------------------------------------------------------------------------
// Arena Limits Cache
//
// Loaded from app_settings.arena_limits on boot and refreshed periodically.
// Admin changes via PATCH /api/v1/admin/settings/arena_limits are picked up
// on the next refresh cycle (≤60s). The goroutine stops on context cancel.
// ---------------------------------------------------------------------------

const arenaLimitsCacheKey = config.CacheArenaLimits

var (
	arenaLimitsMu sync.RWMutex
	arenaLimits   models.ArenaLimits
	arenaLimitsOK bool
)

// RefreshArenaLimits reloads arena limits, VIP tier, app settings, and badge tiers.
// Exported for use by the cron scheduler.
func RefreshArenaLimits(_ context.Context) {
	loadArenaLimits()
	loadVIPMinTier()
	loadAppSettings()
	loadBadgeTiers()
}

func loadArenaLimits() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try Redis cache first
	var limits models.ArenaLimits
	found, _ := redis.Get(ctx, arenaLimitsCacheKey, &limits)
	if found {
		backfillArenaDefaults(&limits)
		arenaLimitsMu.Lock()
		arenaLimits = limits
		arenaLimitsOK = true
		arenaLimitsMu.Unlock()
		return
	}

	// Fall back to Postgres
	var raw []byte
	err := postgress.GetPool().QueryRow(ctx,
		`SELECT value FROM app_settings WHERE key = $1`, config.ArenaLimitsKey,
	).Scan(&raw)
	if err != nil {
		log.Printf("[arena] failed to load arena_limits: %v", err)
		setDefaultArenaLimits()
		return
	}

	if err := json.Unmarshal(raw, &limits); err != nil {
		log.Printf("[arena] failed to parse arena_limits: %v", err)
		setDefaultArenaLimits()
		return
	}

	backfillArenaDefaults(&limits)

	arenaLimitsMu.Lock()
	arenaLimits = limits
	arenaLimitsOK = true
	arenaLimitsMu.Unlock()

	// Cache in Redis for 2 minutes
	_ = redis.PutWithTTL(ctx, arenaLimitsCacheKey, limits, config.CacheTTLLong)
}

// backfillArenaDefaults fills in zero-valued fields with sensible defaults.
// This handles cases where the DB row was inserted before a field was added.
func backfillArenaDefaults(l *models.ArenaLimits) {
	if l.MaxPostsPerUser == 0 {
		l.MaxPostsPerUser = 1000
	}
	if l.MaxMediaPerPost == 0 {
		l.MaxMediaPerPost = 10
	}
	if l.MaxImageSizeKB == 0 {
		l.MaxImageSizeKB = config.ArenaMaxImageSizeKB
	}
	if l.MaxVideoSizeKB == 0 {
		l.MaxVideoSizeKB = config.ArenaMaxVideoSizeKB
	}
	if l.MaxCaptionLength == 0 {
		l.MaxCaptionLength = 2200
	}
	if l.MaxCommentLength == 0 {
		l.MaxCommentLength = 1000
	}
	if l.FreeCaptionLength == 0 {
		l.FreeCaptionLength = 300
	}
	if l.FreeCommentLength == 0 {
		l.FreeCommentLength = 200
	}
	if l.MaxBioLength == 0 {
		l.MaxBioLength = 500
	}
	if l.FreeBioLength == 0 {
		l.FreeBioLength = 200
	}
	if l.TrendingThreshold == 0 {
		l.TrendingThreshold = 50
	}
	if l.PresignPutMins == 0 {
		l.PresignPutMins = config.DefaultPresignPutMins
	}
	if l.PresignGetMins == 0 {
		l.PresignGetMins = config.DefaultPresignGetMins
	}
}

func setDefaultArenaLimits() {
	arenaLimitsMu.Lock()
	defer arenaLimitsMu.Unlock()
	if arenaLimitsOK {
		return // already set, don't overwrite with defaults
	}
	arenaLimits = models.ArenaLimits{
		MaxPostsPerUser:   1000,
		MaxMediaPerPost:   10,
		MaxImageSizeKB:    config.ArenaMaxImageSizeKB,
		MaxVideoSizeKB:    config.ArenaMaxVideoSizeKB,
		MaxCaptionLength:  2200,
		MaxCommentLength:  1000,
		FreeCaptionLength: 300,
		FreeCommentLength: 200,
		MaxBioLength:      500,
		FreeBioLength:     200,
		TrendingThreshold: 50,
		PresignPutMins:    config.DefaultPresignPutMins,
		PresignGetMins:    config.DefaultPresignGetMins,
	}
	arenaLimitsOK = true
}

// GetArenaLimits returns the cached arena limits.
func GetArenaLimits() models.ArenaLimits {
	arenaLimitsMu.RLock()
	defer arenaLimitsMu.RUnlock()
	return arenaLimits
}

// InvalidateArenaLimitsCache deletes the cached arena limits from Redis,
// forcing the next refresh to read from Postgres.
func InvalidateArenaLimitsCache() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = redis.Remove(ctx, arenaLimitsCacheKey)

	// Force immediate reload
	loadArenaLimits()
}

// ---------------------------------------------------------------------------
// VIP Min-Tier Cache
//
// Badge tiers change ~monthly via admin CRUD. IsUserVIP was hitting PG for
// MIN(min_amount) on every post/comment create. Now cached in memory with
// the same 60s refresh cycle as ArenaLimits.
// ---------------------------------------------------------------------------

var (
	vipMinTierMu sync.RWMutex
	vipMinTier   float64
)

func loadVIPMinTier() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var minTier float64
	err := postgress.GetPool().QueryRow(ctx,
		`SELECT COALESCE(MIN(min_amount), 0) FROM badge_tiers`,
	).Scan(&minTier)
	if err != nil {
		log.Printf("[vip] failed to load min badge tier: %v", err)
		return
	}
	vipMinTierMu.Lock()
	vipMinTier = minTier
	vipMinTierMu.Unlock()
}

// GetVIPMinTier returns the cached minimum donation amount for VIP status.
func GetVIPMinTier() float64 {
	vipMinTierMu.RLock()
	defer vipMinTierMu.RUnlock()
	return vipMinTier
}

// ---------------------------------------------------------------------------
// App Settings Cache
//
// App settings (premium_connect, leaderboard_config, group_capacity) are
// admin-only writes, but read on every friend request, DM, leaderboard, and
// group operation. Cached in memory with 60s refresh.
// ---------------------------------------------------------------------------

var (
	appSettingsMu sync.RWMutex
	appSettings   = make(map[string]json.RawMessage)
)

func loadAppSettings() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := postgress.GetPool().Query(ctx,
		`SELECT key, value FROM app_settings`,
	)
	if err != nil {
		log.Printf("[settings] failed to load app_settings: %v", err)
		return
	}
	defer rows.Close()

	newMap := make(map[string]json.RawMessage)
	for rows.Next() {
		var k string
		var v json.RawMessage
		if err := rows.Scan(&k, &v); err == nil {
			newMap[k] = v
		}
	}
	appSettingsMu.Lock()
	appSettings = newMap
	appSettingsMu.Unlock()
}

// GetCachedAppSetting reads a setting from the in-memory cache and unmarshals it.
func GetCachedAppSetting(key string, dest interface{}) bool {
	appSettingsMu.RLock()
	raw, ok := appSettings[key]
	appSettingsMu.RUnlock()
	if !ok {
		return false
	}
	return json.Unmarshal(raw, dest) == nil
}

// ---------------------------------------------------------------------------
// Badge Tiers Cache (for computeBadgeFromDB)
// ---------------------------------------------------------------------------

type badgeTierEntry struct {
	Name      string  `json:"name"`
	Icon      string  `json:"icon"`
	Order     int     `json:"display_order"`
	MinAmount float64 `json:"min_amount"`
}

var (
	badgeTiersMu sync.RWMutex
	badgeTiers   []badgeTierEntry
)

func loadBadgeTiers() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := postgress.GetPool().Query(ctx,
		`SELECT name, icon, display_order, min_amount FROM badge_tiers ORDER BY min_amount DESC`,
	)
	if err != nil {
		log.Printf("[badges] failed to load badge_tiers: %v", err)
		return
	}
	defer rows.Close()

	var tiers []badgeTierEntry
	for rows.Next() {
		var t badgeTierEntry
		if err := rows.Scan(&t.Name, &t.Icon, &t.Order, &t.MinAmount); err == nil {
			tiers = append(tiers, t)
		}
	}
	badgeTiersMu.Lock()
	badgeTiers = tiers
	badgeTiersMu.Unlock()
}

// GetCachedBadge returns the highest qualifying badge for the given donation amount.
// Returns name, icon, tier, minAmount, ok.
func GetCachedBadge(totalDonated float64) (string, string, int, float64, bool) {
	if totalDonated <= 0 {
		return "", "", 0, 0, false
	}
	badgeTiersMu.RLock()
	defer badgeTiersMu.RUnlock()
	// Tiers sorted DESC by min_amount — first match is the highest qualifying
	for _, t := range badgeTiers {
		if totalDonated >= t.MinAmount {
			return t.Name, t.Icon, t.Order, t.MinAmount, true
		}
	}
	return "", "", 0, 0, false
}

// ---------------------------------------------------------------------------
// Post Owner Cache (Redis) — avoids repeated PG queries for viral post owners
// ---------------------------------------------------------------------------

const postOwnerTTL = 5 * time.Minute

// GetPostOwnerCached returns the owner user ID for a post, using redis.Fetch.
func GetPostOwnerCached(ctx context.Context, postID int64) string {
	key := config.CachePostOwner + strconv.FormatInt(postID, 10)
	ownerID, _ := redis.Fetch(ctx, key, postOwnerTTL, func(ctx context.Context) (string, error) {
		var id string
		_ = postgress.GetPool().QueryRow(ctx,
			`SELECT user_id FROM posts WHERE id = $1`, postID,
		).Scan(&id)
		return id, nil
	})
	return ownerID
}

// resolveOriginalTTL is the cache duration for plain-repost resolution.
const resolveOriginalTTL = config.CacheTTLPostResolve

// InvalidateNetworkFeedCache bumps a per-user generation counter so that
// existing cached network feed pages become stale. This is cheaper than
// SCAN+DEL of all matching keys and avoids Lua scripts.
func InvalidateNetworkFeedCache(ctx context.Context, userIDs ...string) {
	rdb := redis.GetRawClient()
	pipe := rdb.Pipeline()
	for _, uid := range userIDs {
		pipe.Incr(ctx, config.CacheNetworkGen+uid)
	}
	_, _ = pipe.Exec(ctx)
}

// GetNetworkFeedGen returns the current generation counter for a user's
// network feed cache. Returns "0" on miss (first load), which is fine.
func GetNetworkFeedGen(ctx context.Context, userID string) string {
	val, err := redis.GetRawClient().Get(ctx, config.CacheNetworkGen+userID).Result()
	if err != nil {
		return "0"
	}
	return val
}

// InvalidateRoomsCache bumps a per-user generation counter for the rooms list.
func InvalidateRoomsCache(ctx context.Context, userIDs ...string) {
	rdb := redis.GetRawClient()
	pipe := rdb.Pipeline()
	for _, uid := range userIDs {
		pipe.Incr(ctx, config.CacheRoomsGen+uid)
	}
	_, _ = pipe.Exec(ctx)
}

// GetRoomsGen returns the current generation counter for a user's rooms cache.
func GetRoomsGen(ctx context.Context, userID string) string {
	val, err := redis.GetRawClient().Get(ctx, config.CacheRoomsGen+userID).Result()
	if err != nil {
		return "0"
	}
	return val
}

// ResolveOriginalPostID returns the original post ID if the given post is a
// plain repost (no caption). For original posts or quote reposts, it returns
// the same postID unchanged. Uses redis.Fetch with singleflight dedup.
func ResolveOriginalPostID(ctx context.Context, postID int64) int64 {
	key := config.CachePostResolve + strconv.FormatInt(postID, 10)

	// Cache stores the resolved original ID. 0 = sentinel meaning "not a plain repost".
	cached, _ := redis.Fetch(ctx, key, resolveOriginalTTL, func(ctx context.Context) (int64, error) {
		var originalID sql.NullInt64
		err := postgress.GetPool().QueryRow(ctx,
			`SELECT original_post_id FROM posts
			 WHERE id = $1 AND post_type = 'repost' AND caption = ''
			   AND original_post_id IS NOT NULL`,
			postID,
		).Scan(&originalID)
		if err != nil || !originalID.Valid {
			return 0, nil // sentinel: not a plain repost
		}
		return originalID.Int64, nil
	})

	if cached == 0 {
		return postID
	}
	return cached
}
