// Package money is the shared library. Both services depend on it, which is
// the relationship the example exists to make visible: changing Add's signature
// has consumers in two modules, and neither is in this one.
package money

import (
	"errors"
	"fmt"
)

// ErrCurrencyMismatch is returned when two amounts in different currencies are
// combined.
var ErrCurrencyMismatch = errors.New("money: currency mismatch")

// Amount is a minor-unit value in a named currency.
type Amount struct {
	Minor    int64
	Currency string
}

// Add returns the sum, refusing to combine currencies.
func Add(a, b Amount) (Amount, error) {
	if a.Currency != b.Currency {
		return Amount{}, fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, a.Currency, b.Currency)
	}
	return Amount{Minor: a.Minor + b.Minor, Currency: a.Currency}, nil
}

// String renders an amount.
func (a Amount) String() string {
	return fmt.Sprintf("%d.%02d %s", a.Minor/100, a.Minor%100, a.Currency)
}
