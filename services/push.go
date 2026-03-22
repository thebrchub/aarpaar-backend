package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/goccy/go-json"
	goredis "github.com/redis/go-redis/v9"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/push"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/config"
)

// ---------------------------------------------------------------------------
// Push Notification Service
//
// Application-level wrapper around the go-starter-kit/push SDK.
// Handles: query device tokens → send via FCM → clean stale tokens.
// All public functions are safe to call when pushSvc is nil (no-op).
// ---------------------------------------------------------------------------

// pushSvc is the FCM push service singleton. Nil if FIREBASE_CREDENTIALS is not set.
var pushSvc push.PushService

// fcmSvc holds the concrete FCM service for Close() on shutdown.
var fcmSvc *push.FCMService

// PushConfigured returns true if the push service was initialized.
func PushConfigured() bool { return pushSvc != nil }

// GetPublicConfig returns the Firebase public config from the push SDK.
// Returns nil, error if push is not configured or public config is not set.
func GetPublicConfig() (*push.FirebasePublicConfig, error) {
	if pushSvc == nil {
		return nil, push.ErrNotConfigured
	}
	return pushSvc.GetPublicConfig()
}

// InitPush initializes the FCM push service. Call once at startup.
// Returns nil if credentials are not set (push disabled).
func InitPush() error {
	if config.FirebaseCredentials == "" {
		return nil
	}
	svc, err := push.NewFCMService(push.Config{
		CredentialsJSON: []byte(config.FirebaseCredentials),
	})
	if err != nil {
		return err
	}
	pushSvc = svc
	fcmSvc = svc
	return nil
}

// ---------------------------------------------------------------------------
// PushPayload is the data-only payload sent with every push notification.
// ---------------------------------------------------------------------------

// ClosePush closes the FCM service, releasing idle HTTP connections.
func ClosePush() {
	if fcmSvc != nil {
		fcmSvc.Close()
	}
}

// ---------------------------------------------------------------------------
// Notification Preference Check
// ---------------------------------------------------------------------------

// ShouldNotify returns true if the target user has the given notification type
// enabled. The prefKey must match a JSON key in users.notification_prefs
// (e.g. "likes", "comments", "friend_requests", "dm_requests", etc.).
// Returns true on any error (fail open — better to over-notify than miss).
func ShouldNotify(ctx context.Context, userID string, prefKey string) bool {
	// Try Redis cache first (5-min TTL)
	cacheKey := config.CacheNotifPrefs + userID
	rdb := redis.GetRawClient()
	cached, err := rdb.HGet(ctx, cacheKey, prefKey).Result()
	if err == nil {
		return cached != "false"
	}

	// Cache miss — load from Postgres and populate full hash
	var prefsJSON []byte
	err = postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT notification_prefs FROM users WHERE id = $1`, userID,
	).Scan(&prefsJSON)
	if err != nil {
		return true // fail open
	}

	var prefs map[string]bool
	if err := json.Unmarshal(prefsJSON, &prefs); err != nil {
		return true
	}

	// Cache all prefs as a Redis hash (atomic HSet + Expire via pipeline)
	fields := make(map[string]interface{}, len(prefs))
	for k, v := range prefs {
		if v {
			fields[k] = "true"
		} else {
			fields[k] = "false"
		}
	}
	if len(fields) > 0 {
		pipe := rdb.Pipeline()
		pipe.HSet(ctx, cacheKey, fields)
		pipe.Expire(ctx, cacheKey, config.CacheTTLNotifPrefs)
		pipe.Exec(ctx)
	}

	enabled, exists := prefs[prefKey]
	return !exists || enabled // default to true if key missing
}

// InvalidateNotifPrefsCache removes the cached notification preferences.
// Called after a user updates their preferences.
func InvalidateNotifPrefsCache(ctx context.Context, userID string) {
	rdb := redis.GetRawClient()
	rdb.Del(ctx, config.CacheNotifPrefs+userID)
}

// Notification preference keys (must match JSON keys in users.notification_prefs)
const (
	NotifPrefLikes          = "likes"
	NotifPrefComments       = "comments"
	NotifPrefFriendRequests = "friend_requests"
	NotifPrefReposts        = "reposts"
	NotifPrefDMRequests     = "dm_requests"
	NotifPrefGroupInvites   = "group_invites"
	NotifPrefMatchActivity  = "match_activity"
	NotifPrefMentions       = "mentions"
)

// PushPayload holds the key-value pairs for a push notification data message.
type PushPayload struct {
	Title    string // visible notification title (empty = data-only)
	Body     string // visible notification body
	Data     map[string]string
	Priority push.Priority
}

// ---------------------------------------------------------------------------
// Send helpers
// ---------------------------------------------------------------------------

// SendPushToUser sends a push notification to all devices of a single user.
// No-op if push is not configured.
func SendPushToUser(ctx context.Context, userID string, p PushPayload) {
	if pushSvc == nil {
		// log.Printf("[push] pushSvc is nil — push not configured, skipping user=%s", userID)
		return
	}

	// Stamp every outgoing push so clients can detect stale notifications
	if p.Data == nil {
		p.Data = make(map[string]string)
	}
	p.Data["sentAt"] = fmt.Sprintf("%d", time.Now().UnixMilli())

	tokens := getDeviceTokens(ctx, userID)
	if len(tokens) == 0 {
		// log.Printf("[push] No device tokens found for user=%s — cannot send push", userID)
		return
	}

	if len(tokens) == 1 {
		_, err := pushSvc.Send(ctx, &push.Notification{
			Token:    tokens[0],
			Title:    p.Title,
			Body:     p.Body,
			Data:     p.Data,
			Priority: p.Priority,
		})
		if err != nil {
			log.Printf("[push] Send failed user=%s: %v", userID, err)
			if errors.Is(err, push.ErrInvalidToken) {
				// log.Printf("[push] Single token stale user=%s, cleaning", userID)
				cleanStaleTokens(ctx, []push.FailedToken{{Token: tokens[0], IsStale: true}})
			}
			return
		}
		return
	}

	// Multiple tokens → multicast
	resp, err := pushSvc.SendMulticast(ctx, &push.MulticastNotification{
		Tokens:   tokens,
		Title:    p.Title,
		Body:     p.Body,
		Data:     p.Data,
		Priority: p.Priority,
	})
	if err != nil {
		log.Printf("[push] SendMulticast failed user=%s: %v", userID, err)
		return
	}
	if resp.FailureCount > 0 {
		for _, ft := range resp.FailedTokens {
			tokenPrefix := ft.Token
			if len(tokenPrefix) > 20 {
				tokenPrefix = tokenPrefix[:20]
			}
			log.Printf("[push]   FAILED token=%s... stale=%v err=%s", tokenPrefix, ft.IsStale, ft.Error)
		}
		cleanStaleTokens(ctx, resp.FailedTokens)
	}
}

// SendPushToUsers sends a push notification to all devices of multiple users.
// No-op if push is not configured.
func SendPushToUsers(ctx context.Context, userIDs []string, p PushPayload) {
	if pushSvc == nil || len(userIDs) == 0 {
		return
	}

	if p.Data == nil {
		p.Data = make(map[string]string)
	}
	p.Data["sentAt"] = fmt.Sprintf("%d", time.Now().UnixMilli())

	tokens := getDeviceTokensMulti(ctx, userIDs)
	if len(tokens) == 0 {
		return
	}

	resp, err := pushSvc.SendMulticast(ctx, &push.MulticastNotification{
		Tokens:   tokens,
		Title:    p.Title,
		Body:     p.Body,
		Data:     p.Data,
		Priority: p.Priority,
	})
	if err != nil {
		log.Printf("[push] SendMulticast failed users=%d: %v", len(userIDs), err)
		return
	}
	if resp.FailureCount > 0 {
		cleanStaleTokens(ctx, resp.FailedTokens)
	}
}

// BroadcastToTopic sends a push notification to all devices subscribed to a topic.
// No-op if push is not configured.
func BroadcastToTopic(ctx context.Context, topic string, data map[string]string) error {
	if pushSvc == nil {
		return nil
	}
	_, err := pushSvc.SendToTopic(ctx, &push.TopicNotification{
		Topic: topic,
		Data:  data,
	})
	return err
}

// SubscribeToTopic subscribes tokens to a topic.
func SubscribeToTopic(ctx context.Context, topic string, tokens []string) {
	if pushSvc == nil || len(tokens) == 0 {
		return
	}
	if err := pushSvc.SubscribeToTopic(ctx, topic, tokens); err != nil {
		log.Printf("[push] SubscribeToTopic failed topic=%s: %v", topic, err)
	}
}

// ---------------------------------------------------------------------------
// Debounce: prevents notification spam for message bursts
// ---------------------------------------------------------------------------

const pushDebounceTTL = 30 * time.Second

// ShouldPushMessage checks if a push should be sent for this room+user combo.
// Returns true if no push was sent recently (within debounceTTL).
// Sets the debounce key atomically so concurrent calls don't double-send.
func ShouldPushMessage(ctx context.Context, roomID, userID string) bool {
	key := config.CachePushSent + roomID + ":" + userID
	set, err := redis.GetRawClient().SetNX(ctx, key, "1", pushDebounceTTL).Result()
	if err != nil {
		log.Printf("[push] ShouldPushMessage Redis SetNX failed room=%s user=%s: %v", roomID, userID, err)
		return true // On error, allow the push (fail open)
	}
	return set
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// deviceTokenCacheTTL is how long device tokens are cached in Redis.
const deviceTokenCacheTTL = config.CacheTTLPushTokens

func getDeviceTokens(ctx context.Context, userID string) []string {
	// Try Redis cache first
	cacheKey := config.CachePushTokens + userID
	cached, err := redis.GetRawClient().Get(ctx, cacheKey).Result()
	if err == nil && cached != "" {
		return strings.Split(cached, ",")
	}

	rows, err := postgress.GetRawDB().QueryContext(ctx,
		`SELECT token FROM device_tokens WHERE user_id = $1`, userID,
	)
	if err != nil {
		log.Printf("[push] query tokens failed user=%s: %v", userID, err)
		return nil
	}
	defer rows.Close()

	var tokens []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			log.Printf("[push] Scan device token failed user=%s: %v", userID, err)
			continue
		}
		tokens = append(tokens, t)
	}

	// Cache in Redis
	if len(tokens) > 0 {
		redis.GetRawClient().Set(ctx, cacheKey, strings.Join(tokens, ","), deviceTokenCacheTTL)
	}
	return tokens
}

func getDeviceTokensMulti(ctx context.Context, userIDs []string) []string {
	if len(userIDs) == 0 {
		return nil
	}

	// Try per-user Redis cache via pipeline (same keys as getDeviceTokens)
	rdb := redis.GetRawClient()
	pipe := rdb.Pipeline()
	cmds := make([]*goredis.StringCmd, len(userIDs))
	for i, uid := range userIDs {
		cmds[i] = pipe.Get(ctx, config.CachePushTokens+uid)
	}
	_, _ = pipe.Exec(ctx)

	var tokens []string
	var missingIDs []string
	for i, cmd := range cmds {
		if val, err := cmd.Result(); err == nil && val != "" {
			tokens = append(tokens, strings.Split(val, ",")...)
		} else {
			missingIDs = append(missingIDs, userIDs[i])
		}
	}

	if len(missingIDs) == 0 {
		return tokens
	}

	// Fallback: DB query only for cache misses
	query := `SELECT user_id, token FROM device_tokens WHERE user_id = ANY($1)`
	rows, err := postgress.GetRawDB().QueryContext(ctx, query, pgStringArray(missingIDs))
	if err != nil {
		log.Printf("[push] query tokens multi failed: %v", err)
		return tokens
	}
	defer rows.Close()

	// Collect tokens per user for caching
	perUser := make(map[string][]string)
	for rows.Next() {
		var uid, t string
		if err := rows.Scan(&uid, &t); err != nil {
			continue
		}
		tokens = append(tokens, t)
		perUser[uid] = append(perUser[uid], t)
	}

	// Cache each user's tokens
	cachePipe := rdb.Pipeline()
	for uid, toks := range perUser {
		cachePipe.Set(ctx, config.CachePushTokens+uid, strings.Join(toks, ","), deviceTokenCacheTTL)
	}
	_, _ = cachePipe.Exec(ctx)

	return tokens
}

// pgStringArray converts a []string to a Postgres text array literal.
type pgTextArray []string

func pgStringArray(s []string) pgTextArray { return pgTextArray(s) }

// Scan implements sql.Scanner (not needed here but good practice).
func (a pgTextArray) Value() (interface{}, error) {
	if len(a) == 0 {
		return "{}", nil
	}
	result := "{"
	for i, s := range a {
		if i > 0 {
			result += ","
		}
		// Simple quoting — device tokens are alphanumeric, no special chars
		result += `"` + s + `"`
	}
	result += "}"
	return result, nil
}

func cleanStaleTokens(ctx context.Context, failed []push.FailedToken) {
	var stale []string
	for _, f := range failed {
		if f.IsStale {
			stale = append(stale, f.Token)
		}
	}
	if len(stale) == 0 {
		return
	}

	_, err := postgress.GetRawDB().ExecContext(ctx,
		`DELETE FROM device_tokens WHERE token = ANY($1)`, pgStringArray(stale))
	if err != nil {
		log.Printf("[push] stale token cleanup failed: %v", err)
	}
}
