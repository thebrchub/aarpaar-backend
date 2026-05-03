package chat

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/helper"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/shivanand-burli/go-starter-kit/rtc"
	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// Bounded Background Worker Pool
// ---------------------------------------------------------------------------

// Pool is the bounded background worker pool for fire-and-forget tasks.
var Pool = helper.NewWorkerPool(500, "chat")

// droppedMessages counts messages dropped because client send buffers were full.
var droppedMessages atomic.Int64

// rtcPtr stores the LiveKit client, set once at startup by main.go.
// Uses atomic.Pointer for safe concurrent access from background goroutines.
var rtcPtr atomic.Pointer[rtc.RTCService]

// GetRTC returns the shared RTC client safely.
func GetRTC() rtc.RTCService {
	if p := rtcPtr.Load(); p != nil {
		return *p
	}
	return nil
}

// SetRTC stores the shared RTC client. Called once at startup.
func SetRTC(svc rtc.RTCService) {
	rtcPtr.Store(&svc)
}

// ---------------------------------------------------------------------------
// Group Call Orphan Scanner
//
// Periodically scans Redis for group_call:* keys that represent calls
// with 0 participants or calls older than maxGroupCallDuration.
// Cleans up stale state that could accumulate after server crashes.
// Runs alongside the existing P2P orphan call scanner.
// ---------------------------------------------------------------------------

const maxGroupCallDuration = 4 * time.Hour

// orphanGroupCallState is the typed struct matching models.GroupCallState.
// Duplicated here to avoid a circular import with the models package.
type orphanGroupCallState struct {
	CallID       string    `json:"callId"`
	InitiatedBy  string    `json:"initiatedBy"`
	StartedAt    time.Time `json:"startedAt"`
	CallType     string    `json:"callType"`
	LKRoomName   string    `json:"lkRoomName"`
	Participants []string  `json:"participants"`
	Admins       []string  `json:"admins"`
}

// ScanOrphanGroupCalls checks all active group call states and cleans up orphans.
// Called from the existing orphan scanner ticker in calls.go.
func ScanOrphanGroupCalls() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rdb := redis.GetRawClient()
	pattern := config.GROUP_CALL_COLON + "*"

	var cursor uint64
	for {
		keys, nextCursor, err := rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			log.Printf("[bgpool] Group call orphan scan error: %v", err)
			return
		}

		for _, key := range keys {
			val, err := rdb.Get(ctx, key).Result()
			if err != nil {
				continue
			}

			var state orphanGroupCallState
			if err := json.Unmarshal([]byte(val), &state); err != nil {
				// Corrupt state — clean up
				log.Printf("[bgpool] Corrupt group call state key=%s, deleting: %v", key, err)
				if err := rdb.Del(ctx, key).Err(); err != nil {
					log.Printf("[bgpool] Failed to delete corrupt key=%s: %v", key, err)
				}
				continue
			}

			// Check age (zero StartedAt means corrupt)
			if state.StartedAt.IsZero() {
				continue
			}

			// Clean up calls older than max duration OR with 0 participants
			if time.Since(state.StartedAt) > maxGroupCallDuration || len(state.Participants) == 0 {
				// log.Printf("[bgpool] Cleaning orphan group call: key=%s callId=%s age=%v participants=%d",
				// 	key, state.CallID, time.Since(state.StartedAt).Round(time.Second), len(state.Participants))

				// Destroy the LiveKit room to release server-side resources
				rtcSvc := GetRTC()
				if rtcSvc != nil && rtcSvc.IsConfigured() && state.LKRoomName != "" {
					if err := rtcSvc.DeleteRoom(ctx, state.LKRoomName); err != nil {
						log.Printf("[bgpool] RTC.DeleteRoom failed room=%s callId=%s: %v", state.LKRoomName, state.CallID, err)
					}
				}

				// Update call_logs
				if state.CallID != "" {
					duration := int(time.Since(state.StartedAt).Seconds())
					_, err := postgress.GetPool().Exec(ctx,
						`UPDATE call_logs SET ended_at = NOW(), duration_seconds = $2
						 WHERE call_id = $1 AND ended_at IS NULL`,
						state.CallID, duration,
					)
					if err != nil {
						log.Printf("[bgpool] Failed to update call_logs callId=%s: %v", state.CallID, err)
					}
				}

				if err := rdb.Del(ctx, key).Err(); err != nil {
					log.Printf("[bgpool] Failed to delete orphan group call key=%s: %v", key, err)
				}
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
}
