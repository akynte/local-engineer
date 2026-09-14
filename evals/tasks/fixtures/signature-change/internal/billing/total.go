package billing

// Line is one item on an invoice.
type Line struct {
	Description string
	PenceEach   int
	Quantity    int
}

// Total returns the invoice total in pence.
//
// It ignores currency entirely, which is the problem: the business now sells
// in more than one.
func Total(lines []Line) int {
	total := 0
	for _, l := range lines {
		total += l.PenceEach * l.Quantity
	}
	return total
}
