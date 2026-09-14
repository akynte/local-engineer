package service

import (
	"context"
	"fmt"

	"example.com/payments/internal/repository"
)

// PaymentService holds the business rules. It depends on the Store interface
// rather than the concrete store, so a change to PostgresStore's signature is
// a breaking change for the interface and everything that satisfies it.
type PaymentService struct {
	store repository.Store
}

func NewPaymentService(store repository.Store) *PaymentService {
	return &PaymentService{store: store}
}

// Get loads a payment by id.
func (s *PaymentService) Get(ctx context.Context, id string) (*repository.Payment, error) {
	p, err := s.store.Load(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load payment %s: %w", id, err)
	}
	return p, nil
}

// Refund is the multi-step operation: exactly the shape where an impact report
// before an edit is worth having, because the state machine has consumers that
// a grep would miss.
func (s *PaymentService) Refund(ctx context.Context, id string, amount int64) error {
	p, err := s.store.Load(ctx, id)
	if err != nil {
		return err
	}
	if p.Status != "settled" {
		return fmt.Errorf("cannot refund a payment in state %q", p.Status)
	}
	if amount > p.Amount {
		return fmt.Errorf("refund %d exceeds payment amount %d", amount, p.Amount)
	}
	p.Status = "refunded"
	return s.store.Save(ctx, p)
}
