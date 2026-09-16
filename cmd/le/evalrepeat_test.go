package main

import "testing"

// §Phase 5: repetitions are standard, not opt-in.
//
// The first real run of this harness changed verdict on 4 of 12 task/arm cells
// between passes. With `--repeat` defaulting to 1, the command that produced a
// publishable-looking table was the one that measured nothing, and the honest
// setting was the one an operator had to remember. This asserts the default
// stayed above 1 so that regression is a failing test rather than a quiet
// change of meaning in every number the harness prints.
func TestEvalRepeatsByDefault(t *testing.T) {
	cmd := newEvalRunCmd()
	f := cmd.Flags().Lookup("repeat")
	if f == nil {
		t.Fatal("le eval run has no --repeat flag")
	}
	if f.DefValue == "1" || f.DefValue == "0" {
		t.Errorf("--repeat defaults to %q: a single pass is one sample per cell, so the "+
			"default invocation of the evaluation would report a spread it never measured",
			f.DefValue)
	}
}
