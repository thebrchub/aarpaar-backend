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
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/payment"
)

// PaymentMgr is the global payment manager (stub), initialized in main.go.
var PaymentMgr *payment.Manager

// PaymentSvc is the SDK payment service (Razorpay or nil), initialized in main.go.
var PaymentSvc sdkpay.PaymentService

// ---------------------------------------------------------------------------
// Donate
// ---------------------------------------------------------------------------

// DonateHandler processes a donation and records it.
//
// @Summary		Make a donation
// @Description	Processes a donation via the payment provider and records it. Returns the user's updated badge.
// @Tags		Donations
// @Accept		json
// @Produce		json
// @Param		body	body	DonateRequest	true	"Donation details"
// @Success		200	{object}	map[string]interface{}
// @Failure		400	{object}	StatusMessage
// @Failure		401	{object}	StatusMessage
// @Failure		500	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/donate [post]

type DonateRequest struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"` // defaults to "INR"
}

func DonateHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(config.UserIDKey).(string)
	if !ok || userID == "" {
		JSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req DonateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Amount <= 0 {
		JSONError(w, "amount must be a positive number", http.StatusBadRequest)
		return
	}
	if req.Currency == "" {
		req.Currency = "INR"
	}

	ctx, cancel := pgCtx(r)
	defer cancel()

	// Process via payment provider
	paymentID, providerName, err := PaymentMgr.ProcessDonation(ctx, userID, req.Amount, req.Currency)
	if err != nil {
		log.Printf("[donations] payment failed for user=%s amount=%.2f: %v", userID, req.Amount, err)
		JSONError(w, "Payment processing failed", http.StatusInternalServerError)
		return
	}

	// Record in DB
	_, err = postgress.Exec(
		`INSERT INTO donations (user_id, amount, currency, payment_id, payment_provider)
		 VALUES ($1, $2, $3, $4, $5)`,
		userID, req.Amount, req.Currency, paymentID, providerName,
	)
	if err != nil {
		log.Printf("[donations] failed to record donation: %v", err)
		JSONError(w, "Failed to record donation", http.StatusInternalServerError)
		return
	}

	// Compute updated badge
	totalDonated := GetUserTotalDonation(ctx, userID)
	badge := computeBadgeFromDB(ctx, totalDonated)

	JSONSuccess(w, map[string]interface{}{
		"status":        "success",
		"message":       "Thank you for your donation!",
		"amount":        req.Amount,
		"currency":      req.Currency,
		"total_donated": totalDonated,
		"badge":         badge,
		"payment_id":    paymentID,
	})
}

// ---------------------------------------------------------------------------
// Donation History
// ---------------------------------------------------------------------------

// GetDonationHistoryHandler returns the authenticated user's donation history.
//
// @Summary		Get donation history
// @Description	Returns paginated donation history for the current user.
// @Tags		Donations
// @Produce		json
// @Param		limit	query	int	false	"Page size (default 10, max 100)"
// @Param		offset	query	int	false	"Offset (default 0)"
// @Success		200	{array}		map[string]interface{}
// @Failure		401	{object}	StatusMessage
// @Security	BearerAuth
// @Router		/donate/history [get]
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
//
// @Summary		List badge tiers
// @Description	Returns all configurable badge tiers. Public endpoint.
// @Tags		Badges
// @Produce		json
// @Success		200	{array}	map[string]interface{}
// @Router		/badges/tiers [get]
func GetBadgeTiersHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := pgCtx(r)
	defer cancel()

	var raw []byte
	err := postgress.GetRawDB().QueryRowContext(ctx,
		`SELECT COALESCE(json_agg(t), '[]')::text FROM (
			SELECT id, name, min_amount, icon, display_order
			FROM badge_tiers ORDER BY display_order ASC
		) t`,
	).Scan(&raw)
	if err != nil {
		JSONError(w, "Database error", http.StatusInternalServerError)
		return
	}

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

// computeBadgeFromDB loads badge tiers from DB and returns the highest qualifying badge.
func computeBadgeFromDB(ctx context.Context, totalDonated float64) *BadgeInfo {
	if totalDonated <= 0 {
		return nil
	}

	rows, err := postgress.GetRawDB().Query(
		`SELECT name, icon, display_order, min_amount FROM badge_tiers
		 WHERE min_amount <= $1 ORDER BY min_amount DESC LIMIT 1`,
		totalDonated,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	if rows.Next() {
		var b BadgeInfo
		if err := rows.Scan(&b.Name, &b.Icon, &b.Tier, &b.Min); err == nil {
			return &b
		}
	}
	return nil
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
			`SELECT user_id, amount, currency FROM pending_orders WHERE order_id = $1 AND status = 'pending'`,
			resp.OrderId,
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
		_, _ = postgress.Exec(
			`UPDATE pending_orders SET status = 'completed' WHERE order_id = $1`,
			resp.OrderId,
		)

	case sdkpay.PaymentStatusFailed:
		_, _ = postgress.Exec(
			`UPDATE pending_orders SET status = 'failed' WHERE order_id = $1`,
			resp.OrderId,
		)
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
		JSONError(w, "Order not found", http.StatusNotFound)
		return
	}

	JSONSuccess(w, map[string]interface{}{
		"order_id": orderID,
		"status":   status,
	})
}
