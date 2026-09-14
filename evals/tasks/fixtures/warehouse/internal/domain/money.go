// Package domain holds the types the rest of the service is written in terms
// of. Nothing here imports another package in this module: it is the bottom of
// the dependency graph, which is what makes it safe for everything else to
// depend on.
package domain

import (
	"errors"
	"fmt"
)

// Currency is an ISO 4217 code. The zero value is deliberately invalid so a
// Money that was never initialised cannot be mistaken for a valid zero amount.
type Currency string

const (
	GBP Currency = "GBP"
	EUR Currency = "EUR"
	USD Currency = "USD"
)

// ErrCurrencyMismatch is returned when two amounts in different currencies are
// combined. It is a distinct error because callers at the API boundary turn it
// into a 400 rather than a 500.
var ErrCurrencyMismatch = errors.New("domain: currency mismatch")

// Money is a minor-unit amount in a named currency. Amounts are integers
// because floating point cannot represent a penny exactly.
type Money struct {
	Minor    int64
	Currency Currency
}

// NewMoney builds an amount.
func NewMoney(minor int64, c Currency) Money { return Money{Minor: minor, Currency: c} }

// Valid reports whether the currency is one this service handles.
func (m Money) Valid() bool {
	switch m.Currency {
	case GBP, EUR, USD:
		return true
	}
	return false
}

// Add returns the sum, refusing to combine currencies.
func (m Money) Add(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.Currency, other.Currency)
	}
	return Money{Minor: m.Minor + other.Minor, Currency: m.Currency}, nil
}

// Sub returns the difference, refusing to combine currencies.
func (m Money) Sub(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.Currency, other.Currency)
	}
	return Money{Minor: m.Minor - other.Minor, Currency: m.Currency}, nil
}

// Times scales an amount by a whole number of units.
func (m Money) Times(n int) Money {
	return Money{Minor: m.Minor * int64(n), Currency: m.Currency}
}

// IsZero reports whether the amount is nil-valued.
func (m Money) IsZero() bool { return m.Minor == 0 }

// String renders the amount for logs and for the API.
func (m Money) String() string {
	sign := ""
	minor := m.Minor
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, minor/100, minor%100, m.Currency)
}
