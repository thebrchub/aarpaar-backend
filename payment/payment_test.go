package payment

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Unit Tests — StubProvider
// ---------------------------------------------------------------------------

func TestStubProviderName(t *testing.T) {
	p := &StubProvider{}
	assert.Equal(t, "stub", p.Name())
}

func TestStubProviderCreateOrder(t *testing.T) {
	p := &StubProvider{}
	ctx := context.Background()

	resp, err := p.CreateOrder(ctx, CreateOrderRequest{
		Amount:   100.50,
		Currency: "INR",
		Metadata: map[string]string{"user_id": "123"},
	})

	require.NoError(t, err)
	assert.Contains(t, resp.OrderID, "ord_")
	assert.Equal(t, "stub", resp.ProviderRef)
}

func TestStubProviderVerifyPayment(t *testing.T) {
	p := &StubProvider{}
	ctx := context.Background()

	resp, err := p.VerifyPayment(ctx, VerifyPaymentRequest{
		OrderID:   "ord_123",
		PaymentID: "pay_456",
		Signature: "sig",
	})

	require.NoError(t, err)
	assert.True(t, resp.Verified)
	assert.Contains(t, resp.PaymentID, "pay_")
	assert.Equal(t, "captured", resp.Status)
}

func TestStubProviderRefundPayment(t *testing.T) {
	p := &StubProvider{}
	ctx := context.Background()

	resp, err := p.RefundPayment(ctx, RefundRequest{
		PaymentID: "pay_123",
		Amount:    50.00,
	})

	require.NoError(t, err)
	assert.Contains(t, resp.RefundID, "rfnd_")
	assert.Equal(t, "processed", resp.Status)
}

func TestStubProviderGetPaymentStatus(t *testing.T) {
	p := &StubProvider{}
	ctx := context.Background()

	status, err := p.GetPaymentStatus(ctx, "pay_123")

	require.NoError(t, err)
	assert.Equal(t, "pay_123", status.PaymentID)
	assert.Equal(t, "captured", status.Status)
	assert.Equal(t, "INR", status.Currency)
}

// ---------------------------------------------------------------------------
// Unit Tests — Manager
// ---------------------------------------------------------------------------

func TestManagerProcessDonation(t *testing.T) {
	mgr := NewManager(&StubProvider{})
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		paymentID, providerName, err := mgr.ProcessDonation(ctx, "user123", 100.0, "INR")
		require.NoError(t, err)
		assert.NotEmpty(t, paymentID)
		assert.Equal(t, "stub", providerName)
	})

	t.Run("negative amount", func(t *testing.T) {
		_, _, err := mgr.ProcessDonation(ctx, "user123", -10.0, "INR")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "amount must be positive")
	})

	t.Run("zero amount", func(t *testing.T) {
		_, _, err := mgr.ProcessDonation(ctx, "user123", 0, "INR")
		assert.Error(t, err)
	})

	t.Run("default currency", func(t *testing.T) {
		paymentID, _, err := mgr.ProcessDonation(ctx, "user123", 50.0, "")
		require.NoError(t, err)
		assert.NotEmpty(t, paymentID)
	})
}

func TestManagerProvider(t *testing.T) {
	stub := &StubProvider{}
	mgr := NewManager(stub)
	assert.Equal(t, stub, mgr.Provider())
}
