package handlers

import (
	"context"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/goccy/go-json"
	"github.com/google/uuid"
	sdkmodels "github.com/shivanand-burli/go-starter-kit/models"
	sdkpay "github.com/shivanand-burli/go-starter-kit/payment"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/shivanand-burli/go-starter-kit/redis"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/services"
)

// PaymentSvc is the SDK payment service (Razorpay or nil), initialized in main.go.
var PaymentSvc sdkpay.PaymentService

// ---------------------------------------------------------------------------
// Donation History
// ---------------------------------------------------------------------------

// GetDonationHistoryHandler returns the authenticated user's donation history.
func GetDonationHistoryHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	limit, offset := parsePagination(r)

	ctx, cancel := pgCtx(r)
	defer cancel()

	query := `SELECT COALESCE(json_agg(t), '[]')::text FROM (
		SELECT id, amount, currency, payment_id, payment_provider, created_at
		FROM donations WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	) t`

	var raw []byte
	if err := postgress.GetRawDB().QueryRowContext(ctx, query, userID, limit, offset).Scan(&raw); err != nil {
		log.Printf("[donations] GetDonationHistory query failed user=%s: %v", userID, err)
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
}

// ---------------------------------------------------------------------------
// Badge Tiers (Public)
// ---------------------------------------------------------------------------

// GetBadgeTiersHandler returns all badge tiers sorted by display_order.
func GetBadgeTiersHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := pgCtx(r)
	defer cancel()

	// Try Redis cache first (badge tiers rarely change)
	const cacheKey = "badge_tiers:all"
	rdb := redis.GetRawClient()
	if cached, err := rdb.Get(ctx, cacheKey).Bytes(); err == nil && len(cached) > 0 {
		w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
		return
	}

	var raw []byte
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT COALESCE(json_agg(t), '[]')::text FROM (
			SELECT id, name, min_amount, icon, display_order
			FROM badge_tiers ORDER BY display_order ASC
		) t`,
	).Scan(&raw)
	if err != nil {
		log.Printf("[donations] GetBadgeTiers query failed: %v", err)
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Cache for 10 minutes
	rdb.Set(ctx, cacheKey, raw, 10*time.Minute)

	w.Header().Set(config.HeaderContentType, config.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
}

// ---------------------------------------------------------------------------
// Badge Computation Helpers
// ---------------------------------------------------------------------------

// BadgeInfo represents a user's computed badge.
type BadgeInfo struct {
	Name string  `json:"name"`
	Icon string  `json:"icon"`
	Tier int     `json:"tier"` // display_order
	Min  float64 `json:"min_amount"`
}

// computeBadgeFromDB loads badge tiers from the in-memory cache and returns the highest qualifying badge.
func computeBadgeFromDB(totalDonated float64) *BadgeInfo {
	name, icon, tier, min, ok := services.GetCachedBadge(totalDonated)
	if !ok {
		return nil
	}
	return &BadgeInfo{Name: name, Icon: icon, Tier: tier, Min: min}
}

// ---------------------------------------------------------------------------
// Create Donation Order (Razorpay Checkout — Step 1)
// ---------------------------------------------------------------------------

type CreateOrderRequest struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

func CreateDonationOrderHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req CreateOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Amount <= 0 {
		JSONError(w, "amount must be a positive number", http.StatusBadRequest)
		return
	}
	if req.Currency == "" {
		req.Currency = "INR"
	}

	if PaymentSvc == nil {
		JSONError(w, "Payment provider not configured", http.StatusServiceUnavailable)
		return
	}

	// Convert to smallest unit (paise/cents)
	amountSmallest := int64(req.Amount * 100)

	internalOrderID := uuid.New().String()

	order := &sdkmodels.BaseOrder{
		ID:         internalOrderID,
		Currency:   req.Currency,
		CustomerId: userID,
		Items: []sdkmodels.OrderItem{
			{
				BaseProduct: sdkmodels.BaseProduct{
					ID:        "donation",
					Name:      "Donation",
					UnitPrice: amountSmallest,
				},
				Quantity: 1,
			},
		},
	}

	resp, err := PaymentSvc.CheckoutSession(order)
	if err != nil {
		log.Printf("[donations] create order failed for user=%s: %v", userID, err)
		JSONError(w, "Failed to create payment order", http.StatusInternalServerError)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Save pending order for webhook mapping
	_, err = postgress.Exec(
		`INSERT INTO pending_orders (order_id, razorpay_order_id, user_id, amount, currency, status)
		 VALUES ($1, $2, $3, $4, $5, 'pending')`,
		internalOrderID, resp.SessionId, userID, amountSmallest, req.Currency,
	)
	if err != nil {
		log.Printf("[donations] failed to save pending order: %v", err)
		JSONError(w, "Failed to save order", http.StatusInternalServerError)
		return
	}
	_ = ctx

	JSONSuccess(w, map[string]interface{}{
		"order_id":          internalOrderID,
		"razorpay_order_id": resp.SessionId,
		"key_id":            config.RazorpayKeyID,
		"amount":            amountSmallest,
		"currency":          req.Currency,
	})
}

// ---------------------------------------------------------------------------
// Razorpay Webhook (Step 2 — payment verification)
// ---------------------------------------------------------------------------

func RazorpayWebhookHandler(w http.ResponseWriter, r *http.Request) {
	if PaymentSvc == nil {
		JSONError(w, "Payment provider not configured", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		JSONError(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	signature := r.Header.Get("X-Razorpay-Signature")
	if signature == "" {
		JSONError(w, "Missing signature", http.StatusBadRequest)
		return
	}

	resp, err := PaymentSvc.VerifyPayment(body, signature)
	if err != nil {
		log.Printf("[webhook] verification failed: %v", err)
		JSONError(w, "Verification failed", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	switch resp.PaymentStatus {
	case sdkpay.PaymentStatusCompleted:
		// Look up the pending order by our internal order ID (stored in notes)
		var userID string
		var amount int64
		var currency string
		err := postgress.GetRawDB().QueryRowContext(ctx,
			`SELECT user_id, amount, currency FROM pending_orders WHERE order_id = $1 AND status = $2`,
			resp.OrderId, config.OrderStatusPending,
		).Scan(&userID, &amount, &currency)
		if err != nil {
			log.Printf("[webhook] pending order not found for order_id=%s: %v", resp.OrderId, err)
			w.WriteHeader(http.StatusOK) // Ack to Razorpay
			return
		}

		// Record donation
		amountFloat := float64(amount) / 100.0
		_, err = postgress.Exec(
			`INSERT INTO donations (user_id, amount, currency, payment_id, payment_provider, razorpay_order_id)
			 VALUES ($1, $2, $3, $4, 'razorpay', $5)`,
			userID, amountFloat, currency, resp.SessionId, resp.OrderId,
		)
		if err != nil {
			log.Printf("[webhook] failed to record donation: %v", err)
		}

		// Update pending order status
		if _, err := postgress.Exec(
			`UPDATE pending_orders SET status = $1 WHERE order_id = $2`,
			config.OrderStatusCompleted, resp.OrderId,
		); err != nil {
			log.Printf("[webhook] update pending_orders completed failed order=%s: %v", resp.OrderId, err)
		}

	case sdkpay.PaymentStatusFailed:
		if _, err := postgress.Exec(
			`UPDATE pending_orders SET status = $1 WHERE order_id = $2`,
			config.OrderStatusFailed, resp.OrderId,
		); err != nil {
			log.Printf("[webhook] update pending_orders failed order=%s: %v", resp.OrderId, err)
		}
	}

	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// Get Donation Status (polling after checkout)
// ---------------------------------------------------------------------------

func GetDonationStatusHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	orderID := r.PathValue("orderId")
	if orderID == "" {
		JSONError(w, "orderId is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	var status string
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT status FROM pending_orders WHERE order_id = $1 AND user_id = $2`,
		orderID, userID,
	).Scan(&status)
	if err != nil {
		log.Printf("[donations] GetDonationStatus query failed order=%s user=%s: %v", orderID, userID, err)
		JSONError(w, "Order not found", http.StatusNotFound)
		return
	}

	JSONSuccess(w, map[string]interface{}{
		"order_id": orderID,
		"status":   status,
	})
}
