package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/broker"
	"github.com/akynte/local-engineer/internal/graph"
)

// fullEvidence populates every field of broker.Evidence with a value that is
// findable in the rendered output.
func fullEvidence() broker.Evidence {
	return broker.Evidence{
		Summary:       "SUMMARY-MARKER",
		Findings:      []string{"FINDING-MARKER"},
		Impact:        &graph.Impact{Caveat: "CAVEAT-MARKER"},
		BreakingCount: 1,
		OutOfScope:    []string{"OUTOFSCOPE-MARKER"},
		Diff:          "DIFF-MARKER",
		Plan:          map[string]string{"step": "PLAN-MARKER"},
		PolicyReasons: []string{"POLICY-MARKER"},
		// The one that started this: a fresh-context review found three real
		// defects in a change, including the one that made the fix a no-op,
		// and the gate never printed them.
		ReviewConcerns: []string{"CONCERN-MARKER"},
	}
}

// Every field on broker.Evidence exists because someone decided an operator
// needs it to answer the gate. A field that is populated and never rendered is
// worse than one that does not exist: the cost of computing it is paid, the
// runner logs that it "reaches the gate", and the person deciding sees
// nothing. This asserts the whole struct is accounted for, so the next field
// added cannot go silently unshown.
func TestEveryEvidenceFieldIsRendered(t *testing.T) {
	ev := fullEvidence()

	var out strings.Builder
	writeEvidence(&out, ev, true)
	got := out.String()

	// Fields whose value is not rendered verbatim, each with the reason.
	renderedIndirectly := map[string]string{
		// Impact prints its own Summary() and the breaking consumers, not the
		// raw struct.
		"Impact": "CAVEAT-MARKER",
		// BreakingCount is reported through the impact summary rather than as
		// a bare number.
		"BreakingCount": "",
		// Plan belongs to the plan gate, which renders it separately.
		"Plan": "",
	}

	rt := reflect.TypeOf(ev)
	for i := range rt.NumField() {
		name := rt.Field(i).Name
		if marker, indirect := renderedIndirectly[name]; indirect {
			if marker != "" && !strings.Contains(got, marker) {
				t.Errorf("%s is rendered indirectly but %q is missing from the output", name, marker)
			}
			continue
		}
		// Each marker is listed against its field by hand, so a renamed or
		// newly added field fails here rather than passing by accident.
		marker, known := map[string]string{
			"Summary": "SUMMARY-MARKER", "Findings": "FINDING-MARKER",
			"OutOfScope": "OUTOFSCOPE-MARKER", "Diff": "DIFF-MARKER",
			"PolicyReasons": "POLICY-MARKER", "ReviewConcerns": "CONCERN-MARKER",
		}[name]
		if !known {
			t.Fatalf("broker.Evidence gained the field %q and this test does not know how it "+
				"reaches the operator. Render it in writeEvidence, or add it to "+
				"renderedIndirectly with the reason.", name)
		}
		if !strings.Contains(got, marker) {
			t.Errorf("%s is populated but never rendered at the gate: %q is absent from the output",
				name, marker)
		}
	}
}

// The concerns are a model's opinion about work a model did, and everything
// else at the gate is deterministic evidence. Labelling them is what keeps the
// weakest input from reading like the rest.
func TestReviewConcernsAreLabelledAdvisory(t *testing.T) {
	var out strings.Builder
	writeEvidence(&out, fullEvidence(), false)
	got := out.String()

	idx := strings.Index(got, "CONCERN-MARKER")
	if idx < 0 {
		t.Fatal("the review concerns are not rendered")
	}
	header := got[:idx]
	if !strings.Contains(header, "advisory") {
		t.Error("review concerns must be labelled advisory, or a model's opinion reads as evidence")
	}
	// Deterministic evidence first: a reader who stops early should have read
	// the facts, not the opinion.
	if v := strings.Index(got, "FINDING-MARKER"); v > idx {
		t.Error("review concerns are printed before the verification findings")
	}
}

// A diff can be forty thousand bytes. A note printed after it is a note nobody
// reads, so the advisory section has to come first.
func TestConcernsArePrintedBeforeTheDiff(t *testing.T) {
	var out strings.Builder
	writeEvidence(&out, fullEvidence(), true)
	got := out.String()

	if strings.Index(got, "CONCERN-MARKER") > strings.Index(got, "DIFF-MARKER") {
		t.Error("the concerns are buried after the diff")
	}
}

// An empty field prints nothing: a gate with no concerns must not grow an
// empty heading that trains people to skip the section.
func TestEmptySectionsAreOmitted(t *testing.T) {
	var out strings.Builder
	writeEvidence(&out, broker.Evidence{Summary: "only a summary"}, false)
	got := out.String()

	for _, heading := range []string{"Review concerns", "Protected by repository policy", "Verification:"} {
		if strings.Contains(got, heading) {
			t.Errorf("an empty gate rendered the %q heading", heading)
		}
	}
}
