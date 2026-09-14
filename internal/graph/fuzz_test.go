package graph

import "testing"

// Edge validation guards every write into the graph; it must never panic and
// must reject anything the database's CHECK constraints would reject, so the
// error a caller sees names the analyzer rather than the driver.
func FuzzEdgeValidate(f *testing.F) {
	f.Add(int64(1), int64(2), "calls", "resolved", "go-analyzer")
	f.Add(int64(0), int64(0), "", "", "")

	f.Fuzz(func(t *testing.T, src, dst int64, kind, evidence, source string) {
		e := Edge{Src: src, Dst: dst, Kind: EdgeKind(kind), Evidence: Evidence(evidence), Source: source}
		err := e.Validate()

		valid := src != 0 && dst != 0 && Evidence(evidence).Valid() && source != ""
		if valid && err != nil {
			t.Fatalf("a valid edge was rejected: %v", err)
		}
		if !valid && err == nil {
			t.Fatalf("an invalid edge was accepted: %+v", e)
		}
	})
}

// Every change kind must produce a verdict and, when breaking, a migration
// step. A change kind with no guidance is worse than useless at a human gate.
func FuzzVerdictTable(f *testing.F) {
	f.Add("signature", "calls", "resolved")
	f.Add("", "", "")

	f.Fuzz(func(t *testing.T, change, via, evidence string) {
		verdict, migration := verdictFor(ChangeKind(change), EdgeKind(via), Evidence(evidence))
		if verdict == "" {
			t.Fatalf("no verdict for (%q, %q, %q)", change, via, evidence)
		}
		if verdict == Breaking && migration == "" {
			t.Fatalf("a breaking verdict for (%q, %q, %q) names no migration step", change, via, evidence)
		}
	})
}
