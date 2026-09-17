package trust_test

import (
	"testing"

	"github.com/akynte/local-engineer/internal/trust"
)

// The fence costs tokens on every call, and this project's packet budget is
// the thing it spends most carefully. This records the cost rather than
// assuming it is small, and fails if the markers grow past a budget that a
// change would have to be deliberate to exceed.
func TestFencingOverheadIsBounded(t *testing.T) {
	f, err := trust.NewFence()
	if err != nil {
		t.Fatal(err)
	}
	body := "func Total(lines []Line) int { return 0 }"
	per := len(f.Wrap("internal/billing/total.go Total", body)) - len(body)
	pre := len(f.Preamble())

	const charsPerToken = 3.5
	// A 60-step run: roughly ten packet slices plus one tool result per step.
	total := pre + 70*per
	t.Logf("per fenced item: %d chars; preamble: %d chars", per, pre)
	t.Logf("a 70-item run: %d chars ≈ %.0f tokens, %.2f%% of a 65,536 window",
		total, float64(total)/charsPerToken, 100*float64(total)/charsPerToken/65536)

	if per > 160 {
		t.Errorf("a fenced item costs %d chars; the markers have grown", per)
	}
	if pre > 800 {
		t.Errorf("the preamble costs %d chars", pre)
	}
}
