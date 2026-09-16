package doctor

import (
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/config"
	"github.com/akynte/local-engineer/internal/llm"
)

// The measurement this repository actually ran: 416 tok/s prefill, 35 tok/s
// decode on a 35B MoE with CPU experts.
func measured() *config.Profile {
	return &config.Profile{
		Name: "test", ContextTokens: 65536, MaxPacketTokens: 16384, ReservedOutput: 12288,
		Measured: &config.Measurement{PrefillTokensSec: 416, DecodeTokensSec: 35},
	}
}

// Raising reserved_output_tokens from 8192 to 16384 on this hardware put one
// call at about 10.4 minutes against a 10-minute timeout. Nothing connected
// the two numbers, so it surfaced as "Client.Timeout exceeded while awaiting
// headers" — a network-shaped error for an arithmetic problem.
func TestABudgetThatExceedsTheTimeoutIsReported(t *testing.T) {
	p := measured()
	p.ReservedOutput = 16384

	c := checkRequestBudget(p, nil)

	if c.Level != Warn {
		t.Fatalf("a budget over the timeout must warn, got %v: %s", c.Level, c.Detail)
	}
	// The operator needs the number to set, not just the news that it is wrong.
	if !strings.Contains(c.Fix, "timeout_seconds") {
		t.Errorf("the fix must name the setting to change, got %q", c.Fix)
	}
	for _, want := range []string{"prefill", "decode"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("the detail must show the %s term so the arithmetic is checkable: %q", want, c.Detail)
		}
	}
}

// The value that was settled on after the failure has to read as healthy, or
// the check is noise that gets ignored.
func TestABudgetInsideTheTimeoutIsOK(t *testing.T) {
	if c := checkRequestBudget(measured(), nil); c.Level != OK {
		t.Errorf("a budget inside the timeout must be OK, got %v: %s", c.Level, c.Detail)
	}
}

// A declared timeout is what the check must measure against, not the default.
func TestADeclaredProviderTimeoutIsUsed(t *testing.T) {
	p := measured()
	// Comfortably inside the 10-minute default, well outside two minutes.
	providers := &llm.ProvidersFile{Providers: []llm.ProviderSpec{
		{Name: "local", TimeoutSeconds: 120},
	}}

	c := checkRequestBudget(p, providers)

	if c.Level != Warn {
		t.Fatalf("a provider's own short timeout must be what the budget is judged against, got %v: %s",
			c.Level, c.Detail)
	}
	if !strings.Contains(c.Detail, "local") {
		t.Errorf("the detail must say which provider set the timeout, got %q", c.Detail)
	}
}

// Without a measurement there is no arithmetic to do, and inventing rates
// would produce a confident answer from numbers nobody measured.
func TestAnUnmeasuredProfileIsSkippedRatherThanGuessed(t *testing.T) {
	p := measured()
	p.Measured = nil

	c := checkRequestBudget(p, nil)

	if c.Level != Skipped {
		t.Errorf("an unmeasured profile must skip, not guess, got %v", c.Level)
	}
	if !strings.Contains(c.Detail, "models bench") {
		t.Errorf("the skip must say how to make it checkable, got %q", c.Detail)
	}
}
