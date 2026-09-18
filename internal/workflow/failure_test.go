package workflow_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/workflow"
)

func TestFailureFingerprintPreservesDiagnosticAndIgnoresNoise(t *testing.T) {
	a := recipe.Result{Recipe: "go test", Status: recipe.Fail, Summary: recipe.Summary{Headline: "failed in 1.23s", Findings: []recipe.Finding{{File: "/tmp/checkout/a.go", Line: 12, Test: "TestAdd", Message: "got 3 at 0x12345"}}}}
	b := a
	b.Summary.Findings = append([]recipe.Finding(nil), a.Summary.Findings...)
	b.Summary.Headline = "failed in 9.87s"
	b.Summary.Findings[0].Message = "got 3 at 0xabcde"
	if workflow.Fingerprint(a, "/tmp/checkout") != workflow.Fingerprint(b, "/tmp/checkout") {
		t.Fatal("volatile fields changed identity")
	}
	b.Summary.Findings[0].Line = 13
	if workflow.Fingerprint(a, "/tmp/checkout") == workflow.Fingerprint(b, "/tmp/checkout") {
		t.Fatal("source location was discarded")
	}
	baseline := []recipe.Result{{Recipe: "go test", Status: recipe.Pass}}
	if workflow.Classify(a, baseline, nil, "").Class != workflow.Regressed {
		t.Fatal("baseline regression missed")
	}
	seen := map[string]int{workflow.Fingerprint(a, ""): 1}
	if workflow.Classify(a, baseline, seen, "").Class != workflow.Same {
		t.Fatal("repeated failure missed")
	}
}

func TestPhaseTransitionsCannotBypassReview(t *testing.T) {
	if workflow.Transition(workflow.Edit, workflow.Finalize) == nil || workflow.Transition(workflow.Verify, workflow.Finalize) == nil {
		t.Fatal("review bypass allowed")
	}
	if err := workflow.Transition(workflow.Review, workflow.Finalize); err != nil {
		t.Fatal(err)
	}
}

// §11.1's primary_symbol. What it buys is discrimination and a query, not
// stability across a moved line: the review keeps line numbers in the hash on
// purpose, and this pins that so nobody later "fixes" it into a claim it does
// not make.
func TestFingerprintCarriesTheEnclosingSymbol(t *testing.T) {
	root := t.TempDir()
	source := "package risk\n\nfunc Check() error {\n\treturn nil\n}\n\nfunc Free() error {\n\treturn nil\n}\n"
	if err := os.WriteFile(filepath.Join(root, "order.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	failure := func(line int) recipe.Result {
		return recipe.Result{
			Recipe: "go build", Status: recipe.Fail, ExitCode: 1,
			Summary: recipe.Summary{
				Headline: "build failed",
				Findings: []recipe.Finding{{File: "order.go", Line: line, Message: "undefined: Unrealized"}},
			},
		}
	}

	// Line 4 is inside Check, line 8 inside Free. Same message, same file:
	// before the symbol these were distinguished only by the line number.
	inCheck, checkSymbols := workflow.FingerprintWithSymbols(failure(4), root)
	inFree, freeSymbols := workflow.FingerprintWithSymbols(failure(8), root)

	if len(checkSymbols) != 1 || checkSymbols[0] != "order.go:Check" {
		t.Fatalf("symbols for the failure in Check are %v", checkSymbols)
	}
	if len(freeSymbols) != 1 || freeSymbols[0] != "order.go:Free" {
		t.Fatalf("symbols for the failure in Free are %v", freeSymbols)
	}
	if inCheck == inFree {
		t.Fatal("two failures in different functions share a fingerprint")
	}

	// The record carries the symbol, because a repeated failure routed back to
	// localization arrives as a query and a line number is not one.
	record := workflow.Classify(failure(4), nil, map[string]int{}, root)
	if len(record.Symbols) != 1 || record.Symbols[0] != "order.go:Check" {
		t.Fatalf("the failure record lost the symbol: %+v", record)
	}

	// Determinism: the same failure hashes the same way twice.
	again, _ := workflow.FingerprintWithSymbols(failure(4), root)
	if again != inCheck {
		t.Fatal("the fingerprint is not deterministic")
	}
}

// A language with no grammar, a file that is gone, a finding with no location:
// each contributes no symbol and must not break the fingerprint, which is still
// well defined without one.
func TestFingerprintSurvivesAnUnresolvableLocation(t *testing.T) {
	root := t.TempDir()
	for _, f := range []recipe.Finding{
		{File: "", Line: 0, Message: "linker failed"},
		{File: "gone.go", Line: 12, Message: "undefined"},
		{File: "styles.css", Line: 4, Message: "unknown property"},
		{File: "app.vue", Line: 9, Message: "type error"},
	} {
		r := recipe.Result{Recipe: "build", Status: recipe.Fail, Summary: recipe.Summary{Findings: []recipe.Finding{f}}}
		fp, symbols := workflow.FingerprintWithSymbols(r, root)
		if fp == "" {
			t.Fatalf("no fingerprint for %+v", f)
		}
		if len(symbols) != 0 {
			t.Fatalf("a symbol was invented for %+v: %v", f, symbols)
		}
	}
}
