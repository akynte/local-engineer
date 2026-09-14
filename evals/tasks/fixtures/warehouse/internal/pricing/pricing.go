// Package pricing turns an order's lines into an amount to charge. It applies
// discounts, then shipping, then tax, in that order — changing the order
// changes the answer, so it is fixed here rather than left to callers.
package pricing

import (
	"context"
	"fmt"

	"example.com/warehouse/internal/domain"
	"example.com/warehouse/internal/store"
)

// Rule is a discount that may apply to an order.
type Rule interface {
	// Name identifies the rule in the breakdown shown to the customer.
	Name() string
	// Discount returns the amount to take off, which may be zero.
	Discount(ctx context.Context, order domain.Order, subtotal domain.Money) (domain.Money, error)
}

// Breakdown is what the customer is shown and what the API returns.
type Breakdown struct {
	Subtotal  domain.Money
	Discounts []LineDiscount
	Shipping  domain.Money
	Tax       domain.Money
	Total     domain.Money
}

// LineDiscount is one applied rule.
type LineDiscount struct {
	Rule   string
	Amount domain.Money
}

// TaxRate is basis points, so 2000 is 20%.
type TaxRate int

// Calculator prices orders.
type Calculator struct {
	rules   []Rule
	items   store.ItemStore
	taxRate TaxRate
}

// New builds a calculator.
func New(items store.ItemStore, taxRate TaxRate, rules ...Rule) *Calculator {
	return &Calculator{items: items, taxRate: taxRate, rules: rules}
}

// Price computes the breakdown for an order. Shipping is passed in because the
// shipping package owns that calculation and depends on this one for nothing.
func (c *Calculator) Price(ctx context.Context, order domain.Order, shipping domain.Money) (Breakdown, error) {
	subtotal, err := order.Subtotal()
	if err != nil {
		return Breakdown{}, fmt.Errorf("pricing: subtotal: %w", err)
	}

	b := Breakdown{Subtotal: subtotal, Shipping: shipping}
	running := subtotal
	for _, rule := range c.rules {
		amount, err := rule.Discount(ctx, order, subtotal)
		if err != nil {
			return Breakdown{}, fmt.Errorf("pricing: rule %s: %w", rule.Name(), err)
		}
		if amount.IsZero() {
			continue
		}
		next, err := running.Sub(amount)
		if err != nil {
			return Breakdown{}, err
		}
		running = next
		b.Discounts = append(b.Discounts, LineDiscount{Rule: rule.Name(), Amount: amount})
	}

	withShipping, err := running.Add(shipping)
	if err != nil {
		return Breakdown{}, err
	}
	b.Tax = c.tax(withShipping)
	total, err := withShipping.Add(b.Tax)
	if err != nil {
		return Breakdown{}, err
	}
	b.Total = total
	return b, nil
}

// tax applies the rate to an amount, rounding half up.
func (c *Calculator) tax(amount domain.Money) domain.Money {
	minor := (amount.Minor*int64(c.taxRate) + 5000) / 10000
	return domain.Money{Minor: minor, Currency: amount.Currency}
}
