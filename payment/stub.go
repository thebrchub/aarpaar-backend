package payment

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Stub Payment Provider
//
// Returns success for every operation with generated IDs. Used for
// development and testing until a real payment gateway is integrated.
// ---------------------------------------------------------------------------

// StubProvider is a no-op payment provider for testing.
type StubProvider struct{}

func (s *StubProvider) Name() string { return "stub" }

func (s *StubProvider) CreateOrder(ctx context.Context, req CreateOrderRequest) (CreateOrderResponse, error) {
	orderID := fmt.Sprintf("ord_%s", uuid.New().String()[:12])
	log.Printf("[payment:stub] CreateOrder amount=%.2f currency=%s → orderID=%s", req.Amount, req.Currency, orderID)
	return CreateOrderResponse{
		OrderID:     orderID,
		ProviderRef: "stub",
	}, nil
}

func (s *StubProvider) VerifyPayment(ctx context.Context, req VerifyPaymentRequest) (VerifyPaymentResponse, error) {
	paymentID := fmt.Sprintf("pay_%s", uuid.New().String()[:12])
	log.Printf("[payment:stub] VerifyPayment orderID=%s → verified, paymentID=%s", req.OrderID, paymentID)
	return VerifyPaymentResponse{
		Verified:  true,
		PaymentID: paymentID,
		Status:    "captured",
	}, nil
}

func (s *StubProvider) RefundPayment(ctx context.Context, req RefundRequest) (RefundResponse, error) {
	refundID := fmt.Sprintf("rfnd_%s", uuid.New().String()[:12])
	log.Printf("[payment:stub] RefundPayment paymentID=%s amount=%.2f → refundID=%s", req.PaymentID, req.Amount, refundID)
	return RefundResponse{
		RefundID: refundID,
		Status:   "processed",
	}, nil
}

func (s *StubProvider) GetPaymentStatus(ctx context.Context, paymentID string) (PaymentStatus, error) {
	log.Printf("[payment:stub] GetPaymentStatus paymentID=%s → captured", paymentID)
	return PaymentStatus{
		PaymentID: paymentID,
		OrderID:   "stub_order",
		Status:    "captured",
		Amount:    0,
		Currency:  "INR",
	}, nil
}
