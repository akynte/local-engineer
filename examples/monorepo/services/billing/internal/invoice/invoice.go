// Package invoice totals the lines on an invoice.
package invoice

import (
	"example.com/libs/money"
)

// Line is one item.
type Line struct {
	Description string
	Unit        money.Amount
	Quantity    int
}

// Invoice is a set of lines in one currency.
type Invoice struct {
	ID       string
	Currency string
	Lines    []Line
}

// Total sums the lines. It is a consumer of money.Add in another module.
func Total(inv Invoice) (money.Amount, error) {
	total := money.Amount{Currency: inv.Currency}
	for _, l := range inv.Lines {
		line := money.Amount{Minor: l.Unit.Minor * int64(l.Quantity), Currency: l.Unit.Currency}
		sum, err := money.Add(total, line)
		if err != nil {
			return money.Amount{}, err
		}
		total = sum
	}
	return total, nil
}
