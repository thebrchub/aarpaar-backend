package handlers

import (
	"context"
	"log"
	"net/http"

	"github.com/goccy/go-json"
	"github.com/shivanand-burli/go-starter-kit/postgress"
	"github.com/thebrchub/aarpaar/config"
	"github.com/thebrchub/aarpaar/payment"
)

// PaymentMgr is the global payment manager, initialized in main.go.
var PaymentMgr *payment.Manager

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
