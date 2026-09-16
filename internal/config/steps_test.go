package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akynte/local-engineer/internal/config"
)

// §9.3 forbids hardcoding the limits a model's behaviour depends on. The step
// budget was the one that escaped that list: the engine's doc comment said it
// bounded an attempt "when no profile says otherwise", and no profile could
// say otherwise, so every task on every machine ran at exactly 20 steps.
func TestAProfileCanSetTheStepBudget(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`name: stepped
context_tokens: 32768
max_packet_tokens: 6000
reserved_output_tokens: 4096
max_tools_exposed: 12
max_steps: 60
`)
	if err := os.WriteFile(filepath.Join(dir, "stepped.yaml"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := config.LoadProfile(dir, "stepped")
	if err != nil {
		t.Fatalf("loading the profile: %v", err)
	}
	// A mistyped yaml tag parses as zero and the engine silently substitutes
	// its own default, which is the shape this defect had for its whole life:
	// a knob that reads as absent rather than as broken.
	if p.MaxSteps != 60 {
		t.Errorf("max_steps read as %d, want 60; the yaml key is not reaching the field", p.MaxSteps)
	}
}

// A negative budget would silently mean "the engine default" rather than being
// refused, which is how a typo becomes a behaviour nobody chose.
func TestANegativeStepBudgetIsRefused(t *testing.T) {
	p := config.FallbackProfile()
	p.MaxSteps = -1
	if err := p.Validate(); err == nil {
		t.Error("a negative max_steps must be refused by Validate")
	}
}
