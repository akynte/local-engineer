package recipe_test

import (
	"testing"

	"github.com/akynte/local-engineer/internal/recipe"
)

// Includes decides which recipes a level runs; Required decides which results
// it demands. They are two lists that have to agree, and nothing made them.
// Requiring a kind the level never runs is an impossible contract: every task
// at that level fails with "requires X, which did not run", no matter what the
// work was. Raising KindFormat to Standard produced exactly that until
// Includes was raised with it.
func TestEveryRequiredKindIsAlsoRun(t *testing.T) {
	for _, level := range []recipe.Level{recipe.Low, recipe.Standard, recipe.High} {
		for _, kind := range recipe.Required(level) {
			if !level.Includes(kind) {
				t.Errorf("level %s requires %s but does not run it: "+
					"every task at this level would fail with \"requires %s, which did not run\"",
					level, kind, kind)
			}
		}
	}
}

// The levels are meant to be cumulative — a stronger level never checks less
// than a weaker one — which is what makes "raise the level" a safe instruction.
func TestLevelsAreCumulative(t *testing.T) {
	order := []recipe.Level{recipe.Low, recipe.Standard, recipe.High}
	for i := 1; i < len(order); i++ {
		weaker, stronger := order[i-1], order[i]
		for _, kind := range recipe.Required(weaker) {
			var found bool
			for _, k := range recipe.Required(stronger) {
				if k == kind {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s requires %s but %s does not; raising the level would check less",
					weaker, kind, stronger)
			}
		}
	}
}

// CI runs `make fmt-check` on every change. A contract that accepts
// unformatted code hands someone an acceptance that CI then overturns, which
// is worse than no acceptance at all. A task accepted at standard merged a
// test file with its imports out of order before this rule moved down.
func TestStandardChecksFormattingBecauseCIDoes(t *testing.T) {
	if !recipe.Standard.Includes(recipe.KindFormat) {
		t.Error("standard must run the format check")
	}
	var required bool
	for _, k := range recipe.Required(recipe.Standard) {
		if k == recipe.KindFormat {
			required = true
		}
	}
	if !required {
		t.Error("standard must require a format result, or an unformatted change is accepted")
	}
}
