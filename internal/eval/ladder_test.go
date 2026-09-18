package eval

import "testing"

// A ladder rung that names an arm which does not exist is a rung that can never
// be run, and a report built on it would describe a measurement nobody made.
func TestEveryWiredRungNamesARealArm(t *testing.T) {
	for _, r := range WiredLadder() {
		if _, err := ArmByName(r.Arm); err != nil {
			t.Fatalf("rung %s claims to be runnable but %v", r.Letter, err)
		}
	}
}

// The ladder's whole claim is attribution: a delta between two rungs belongs to
// the one component that changed. If two adjacent rungs differ by more than one
// toggle, that attribution is false, so the structure is asserted rather than
// assumed.
func TestAdjacentWiredRungsDifferByExactlyOneToggle(t *testing.T) {
	rungs := WiredLadder()
	for i := 1; i < len(rungs); i++ {
		lower, _ := ArmByName(rungs[i-1].Arm)
		upper, _ := ArmByName(rungs[i].Arm)
		changed := 0
		for _, differs := range []bool{
			lower.Supervised != upper.Supervised,
			lower.Graph != upper.Graph,
			lower.Verification != upper.Verification,
			lower.Role != upper.Role,
		} {
			if differs {
				changed++
			}
		}
		if changed != 1 {
			t.Fatalf("%s -> %s changes %d toggles, so a delta between them cannot be attributed to %q",
				rungs[i-1].Letter, rungs[i].Letter, changed, rungs[i].Adds)
		}
	}
}

// An unwired rung must not be counted as a ladder measurement, or the
// qualification gate passes on components nobody ablated.
func TestUnwiredRungsAreNotLadderArms(t *testing.T) {
	unwired := 0
	for _, r := range Ladder() {
		if r.Wired {
			continue
		}
		unwired++
		if r.Blocker == "" {
			t.Fatalf("rung %s is unwired but does not say what is missing", r.Letter)
		}
		if isLadderArm(r.Arm) {
			t.Fatalf("unwired rung %s counts toward the component-ladder gate", r.Letter)
		}
	}
	if unwired == 0 {
		t.Fatal("no rung is marked unwired; if that became true the blockers should be deleted deliberately")
	}
}

// Retention exists so a component is never dropped on impression. Absence of
// evidence must produce its own outcome, distinct from evidence of absence.
func TestRetentionDistinguishesNoEffectFromNoEvidence(t *testing.T) {
	th := DefaultRetention()
	cases := []struct {
		verdict Verdict
		want    string
	}{
		{VerdictPositive, "keep_enabled"},
		{VerdictNegative, "disable_by_default"},
		{VerdictNoMaterialDifference, "insufficient_evidence"},
		{VerdictInsufficient, "insufficient_evidence"},
	}
	for _, c := range cases {
		got := Decide("graph", HierarchicalDelta{Verdict: c.verdict}, th)
		if got.Decision != c.want {
			t.Fatalf("%s produced %q, want %q", c.verdict, got.Decision, c.want)
		}
	}
	// Negative evidence disables; it must never be described as deletion.
	d := Decide("graph", HierarchicalDelta{Verdict: VerdictNegative}, th)
	if d.Basis == "" || !contains(d.Basis, "not deleted") {
		t.Fatalf("the negative-evidence basis does not preserve the component: %q", d.Basis)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// A ladder with a hole in it must not pass as a ladder. Three arms that are not
// adjacent measure nothing attributable, and the gate's job is to notice.
func TestLadderChainRequiresAdjacency(t *testing.T) {
	rungs := WiredLadder()
	if len(rungs) < 4 {
		t.Skipf("the adjacency property needs at least four wired rungs, have %d", len(rungs))
	}
	runs := func(arms ...string) []Outcome {
		var out []Outcome
		for _, a := range arms {
			out = append(out, Outcome{Arm: a, Status: StatusCompleted})
		}
		return out
	}

	gapped := runs(rungs[0].Arm, rungs[2].Arm, rungs[3].Arm)
	if got := ladderChain(gapped); got != 2 {
		t.Fatalf("a ladder missing rung %s reported a chain of %d, want 2", rungs[1].Letter, got)
	}

	contiguous := runs(rungs[0].Arm, rungs[1].Arm, rungs[2].Arm)
	if got := ladderChain(contiguous); got != 3 {
		t.Fatalf("three adjacent rungs reported a chain of %d, want 3", got)
	}

	// A run that is not evidence cannot fill a gap: an arm that only ever
	// crashed has measured nothing.
	crashed := append(runs(rungs[0].Arm, rungs[2].Arm),
		Outcome{Arm: rungs[1].Arm, Status: StatusEnvironmentFailed})
	if got := ladderChain(crashed); got != 1 {
		t.Fatalf("a rung with only failed runs filled the gap: chain %d, want 1", got)
	}
}
