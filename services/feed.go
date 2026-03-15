package services

import (
	"context"
	"log"
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

const arenaLimitsCacheKey = "arena:limits"
const arenaLimitsRefresh = 60 * time.Second

var (
	arenaLimitsMu sync.RWMutex
	arenaLimits   models.ArenaLimits
	arenaLimitsOK bool
)

// StartArenaLimitsRefresher loads arena limits from Postgres and starts a
// background goroutine that refreshes them every 60s.
func StartArenaLimitsRefresher(ctx context.Context) {
	loadArenaLimits()
	go func() {
		ticker := time.NewTicker(arenaLimitsRefresh)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				loadArenaLimits()
			}
		}
	}()
}

func loadArenaLimits() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try Redis cache first
	var limits models.ArenaLimits
	found, err := redis.Get(ctx, arenaLimitsCacheKey, &limits)
	if err == nil && found {
		arenaLimitsMu.Lock()
		arenaLimits = limits
		arenaLimitsOK = true
		arenaLimitsMu.Unlock()
		return
	}

	// Fall back to Postgres
	var raw []byte
	err = postgress.GetRawDB().QueryRowContext(ctx,
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

	arenaLimitsMu.Lock()
	arenaLimits = limits
	arenaLimitsOK = true
	arenaLimitsMu.Unlock()

	// Cache in Redis for 2 minutes
	_ = redis.PutWithTTL(ctx, arenaLimitsCacheKey, limits, 2*time.Minute)
}

func setDefaultArenaLimits() {
	arenaLimitsMu.Lock()
	defer arenaLimitsMu.Unlock()
	if arenaLimitsOK {
		return // already set, don't overwrite with defaults
	}
	arenaLimits = models.ArenaLimits{
		MaxPostsPerUser:   50,
		MaxMediaPerPost:   10,
		MaxImageSizeKB:    config.ArenaMaxImageSizeKB,
		MaxVideoSizeKB:    config.ArenaMaxVideoSizeKB,
		MaxCaptionLength:  2200,
		MaxCommentLength:  1000,
		TrendingThreshold: 50,
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
