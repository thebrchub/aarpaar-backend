package payment

import "context"

// ---------------------------------------------------------------------------
// Payment Provider Interface
//
// This is a generic payment abstraction that decouples the application from
// any specific payment gateway (Razorpay, Stripe, etc.). Handlers call the
// PaymentProvider methods without knowing the underlying implementation.
//
// To integrate a real gateway, implement this interface and swap it in
// main.go when initializing the PaymentManager.
// ---------------------------------------------------------------------------

// PaymentProvider defines the contract for any payment gateway integration.
type PaymentProvider interface {
	// CreateOrder initiates a new payment order.
	CreateOrder(ctx context.Context, req CreateOrderRequest) (CreateOrderResponse, error)

	// VerifyPayment verifies that a payment was completed successfully.
	VerifyPayment(ctx context.Context, req VerifyPaymentRequest) (VerifyPaymentResponse, error)

	// RefundPayment initiates a full or partial refund.
	RefundPayment(ctx context.Context, req RefundRequest) (RefundResponse, error)

	// GetPaymentStatus checks the current status of a payment.
	GetPaymentStatus(ctx context.Context, paymentID string) (PaymentStatus, error)

	// Name returns the provider name (e.g. "razorpay", "stripe", "stub").
	Name() string
}

// ---------------------------------------------------------------------------
// Request / Response Types
// ---------------------------------------------------------------------------

// CreateOrderRequest holds the data needed to create a payment order.
type CreateOrderRequest struct {
	Amount   float64           `json:"amount"`   // Amount in smallest unit (e.g. paise for INR)
	Currency string            `json:"currency"` // ISO 4217 code (e.g. "INR", "USD")
	Metadata map[string]string `json:"metadata"` // Arbitrary key-value pairs (user_id, purpose, etc.)
}

// CreateOrderResponse is returned after successfully creating an order.
type CreateOrderResponse struct {
	OrderID     string `json:"order_id"`     // Gateway-assigned order identifier
	ProviderRef string `json:"provider_ref"` // Any additional reference from the gateway
}

// VerifyPaymentRequest holds the data needed to verify a payment.
type VerifyPaymentRequest struct {
	OrderID   string `json:"order_id"`
	PaymentID string `json:"payment_id"`
	Signature string `json:"signature"` // HMAC signature for verification (provider-specific)
}

// VerifyPaymentResponse is returned after verifying a payment.
type VerifyPaymentResponse struct {
	Verified  bool   `json:"verified"`
	PaymentID string `json:"payment_id"`
	Status    string `json:"status"` // "captured", "authorized", etc.
}

// RefundRequest holds the data needed to initiate a refund.
type RefundRequest struct {
	PaymentID string  `json:"payment_id"`
	Amount    float64 `json:"amount"` // Partial refund amount (0 = full refund)
}

// RefundResponse is returned after initiating a refund.
type RefundResponse struct {
	RefundID string `json:"refund_id"`
	Status   string `json:"status"` // "processed", "pending", etc.
}

// PaymentStatus represents the current state of a payment.
type PaymentStatus struct {
	PaymentID string  `json:"payment_id"`
	OrderID   string  `json:"order_id"`
	Status    string  `json:"status"` // "created", "authorized", "captured", "refunded", "failed"
	Amount    float64 `json:"amount"`
	Currency  string  `json:"currency"`
}
