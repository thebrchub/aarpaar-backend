package services

import (
	"context"
	"log"
	"time"

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

// PushConfigured returns true if the push service was initialized.
func PushConfigured() bool { return pushSvc != nil }

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
	return nil
}

// ---------------------------------------------------------------------------
// PushPayload is the data-only payload sent with every push notification.
// ---------------------------------------------------------------------------

// PushPayload holds the key-value pairs for a push notification data message.
type PushPayload struct {
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
		return
	}

	tokens := getDeviceTokens(ctx, userID)
	if len(tokens) == 0 {
		return
	}

	if len(tokens) == 1 {
		resp, err := pushSvc.Send(ctx, &push.Notification{
			Token:    tokens[0],
			Data:     p.Data,
			Priority: p.Priority,
		})
		if err != nil {
			log.Printf("[push] Send failed user=%s: %v", userID, err)
			return
		}
		_ = resp
		return
	}

	// Multiple tokens → multicast
	resp, err := pushSvc.SendMulticast(ctx, &push.MulticastNotification{
		Tokens:   tokens,
		Data:     p.Data,
		Priority: p.Priority,
	})
	if err != nil {
		log.Printf("[push] SendMulticast failed user=%s: %v", userID, err)
		return
	}
	if resp.FailureCount > 0 {
		cleanStaleTokens(ctx, resp.FailedTokens)
	}
}

// SendPushToUsers sends a push notification to all devices of multiple users.
// No-op if push is not configured.
func SendPushToUsers(ctx context.Context, userIDs []string, p PushPayload) {
	if pushSvc == nil || len(userIDs) == 0 {
		return
	}

	tokens := getDeviceTokensMulti(ctx, userIDs)
	if len(tokens) == 0 {
		return
	}

	resp, err := pushSvc.SendMulticast(ctx, &push.MulticastNotification{
		Tokens:   tokens,
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
	key := "push:sent:" + roomID + ":" + userID
	set, err := redis.GetRawClient().SetNX(ctx, key, "1", pushDebounceTTL).Result()
	if err != nil {
		return true // On error, allow the push (fail open)
	}
	return set
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func getDeviceTokens(ctx context.Context, userID string) []string {
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
		if err := rows.Scan(&t); err == nil {
			tokens = append(tokens, t)
		}
	}
	return tokens
}

func getDeviceTokensMulti(ctx context.Context, userIDs []string) []string {
	if len(userIDs) == 0 {
		return nil
	}

	// Build parameterized IN clause
	query := `SELECT token FROM device_tokens WHERE user_id = ANY($1)`
	rows, err := postgress.GetRawDB().QueryContext(ctx, query, pgStringArray(userIDs))
	if err != nil {
		log.Printf("[push] query tokens multi failed: %v", err)
		return nil
	}
	defer rows.Close()

	var tokens []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err == nil {
			tokens = append(tokens, t)
		}
	}
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
	} else {
		log.Printf("[push] Cleaned %d stale device tokens", len(stale))
	}
}
