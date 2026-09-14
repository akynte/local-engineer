package billing

import "testing"

func TestTotal(t *testing.T) {
	lines := []Line{{PenceEach: 100, Quantity: 2}, {PenceEach: 50, Quantity: 1}}
	if got := Total(lines); got != 250 {
		t.Fatalf("got %d", got)
	}
}
