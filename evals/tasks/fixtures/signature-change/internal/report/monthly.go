package report

import "example.com/billing/internal/billing"

// Monthly summarises a month of invoices.
func Monthly(invoices [][]billing.Line) int {
	sum := 0
	for _, lines := range invoices {
		sum += billing.Total(lines)
	}
	return sum
}
