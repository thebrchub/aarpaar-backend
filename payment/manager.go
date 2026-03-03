package payment

import (
	"context"
	"fmt"
)

// ---------------------------------------------------------------------------
// Payment Manager
//
// Wraps the active PaymentProvider and exposes high-level convenience
// methods used by handlers. Swap the provider in main.go to switch from
// stub → real gateway without changing any handler code.
// ---------------------------------------------------------------------------

// Manager wraps a PaymentProvider and adds convenience methods.
type Manager struct {
	provider PaymentProvider
}

// NewManager creates a Manager with the given provider.
func NewManager(provider PaymentProvider) *Manager {
	return &Manager{provider: provider}
}

// Provider returns the underlying PaymentProvider.
func (m *Manager) Provider() PaymentProvider { return m.provider }

// ProcessDonation is a convenience method that creates an order and verifies
// payment in a single call. For the stub provider both steps auto-succeed.
// Returns the payment ID and provider name on success.
func (m *Manager) ProcessDonation(ctx context.Context, userID string, amount float64, currency string) (paymentID string, providerName string, err error) {
	if amount <= 0 {
		return "", "", fmt.Errorf("amount must be positive")
	}
	if currency == "" {
		currency = "INR"
	}

	// Step 1: Create order
	orderResp, err := m.provider.CreateOrder(ctx, CreateOrderRequest{
		Amount:   amount,
		Currency: currency,
		Metadata: map[string]string{
			"user_id": userID,
			"purpose": "donation",
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("create order failed: %w", err)
	}

	// Step 2: Verify payment
	verifyResp, err := m.provider.VerifyPayment(ctx, VerifyPaymentRequest{
		OrderID:   orderResp.OrderID,
		PaymentID: "", // Filled by real gateway callback
		Signature: "", // Filled by real gateway callback
	})
	if err != nil {
		return "", "", fmt.Errorf("verify payment failed: %w", err)
	}

	if !verifyResp.Verified {
		return "", "", fmt.Errorf("payment verification failed")
	}

	return verifyResp.PaymentID, m.provider.Name(), nil
}
