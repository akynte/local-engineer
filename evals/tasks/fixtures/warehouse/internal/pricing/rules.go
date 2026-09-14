package pricing

import (
	"context"

	"example.com/warehouse/internal/domain"
)

// BulkDiscount takes a percentage off once an order passes a unit count.
type BulkDiscount struct {
	MinUnits       int
	PercentOffBase int
}

func (r BulkDiscount) Name() string { return "bulk" }

func (r BulkDiscount) Discount(_ context.Context, order domain.Order, subtotal domain.Money) (domain.Money, error) {
	if order.TotalUnits() < r.MinUnits {
		return domain.Money{Currency: subtotal.Currency}, nil
	}
	minor := subtotal.Minor * int64(r.PercentOffBase) / 100
	return domain.Money{Minor: minor, Currency: subtotal.Currency}, nil
}

// LoyaltyDiscount takes a flat amount off for known customers.
type LoyaltyDiscount struct {
	Members map[string]bool
	Amount  domain.Money
}

func (r LoyaltyDiscount) Name() string { return "loyalty" }

func (r LoyaltyDiscount) Discount(_ context.Context, order domain.Order, subtotal domain.Money) (domain.Money, error) {
	if !r.Members[order.CustomerID] {
		return domain.Money{Currency: subtotal.Currency}, nil
	}
	if r.Amount.Currency != subtotal.Currency {
		return domain.Money{Currency: subtotal.Currency}, nil
	}
	if r.Amount.Minor > subtotal.Minor {
		return subtotal, nil
	}
	return r.Amount, nil
}
