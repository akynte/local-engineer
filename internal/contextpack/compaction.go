package contextpack

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Card is §20's compaction record: the task state rebuilt at a phase boundary.
//
// It is assembled from the ledger — facts a tool confirmed, decisions the
// supervisor recorded, changes actually on disk, failures still open — and
// never from the conversation. A summary of chat is a model's account of its
// own work, which is the thing this system exists not to trust: it drops the
// evidence and keeps the narrative, and the narrative is what goes wrong first.
//
// The old log is not lost. It stays in the trace database and can be fetched
// again by symbol name if the model asks for it.
type Card struct {
	Objective      string   `json:"objective"`
	ConfirmedFacts []Fact   `json:"confirmed_facts,omitempty"`
	FilesExamined  []string `json:"files_examined,omitempty"`
	Symbols        []string `json:"relevant_symbols,omitempty"`
	Hypothesis     string   `json:"hypothesis,omitempty"`
	Changes        []Change `json:"changes_made,omitempty"`
	TestStatus     Tests    `json:"test_status"`
	// OpenFailures are fingerprints, not messages. A fingerprint is what
	// distinguishes "the same failure again" from "a new one", which is the
	// distinction the failure ladder turns on.
	OpenFailures []string `json:"open_failures,omitempty"`
	Decisions    []string `json:"decisions,omitempty"`
	NextAction   string   `json:"next_action,omitempty"`
}

// Fact is something a tool established, with what established it.
type Fact struct {
	Fact string `json:"fact"`
	// Evidence names the source, for example `scip:OrderChecker#Check().`
	// or `verify:go test`. A fact with no evidence is a belief.
	Evidence string `json:"evidence"`
	Commit   string `json:"commit,omitempty"`
}

// Change is one edit that is actually on disk.
type Change struct {
	File    string `json:"file"`
	Symbol  string `json:"symbol,omitempty"`
	Summary string `json:"summary"`
}

// Tests is the verification state.
type Tests struct {
	Ran    []string `json:"ran,omitempty"`
	Passed int      `json:"passed"`
	Failed int      `json:"failed"`
}

// Render writes the card as the P3 task card: readable prose rather than the
// JSON it is stored as, because a model reading JSON spends tokens on syntax.
func (c Card) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Objective: %s\n", c.Objective)
	if c.Hypothesis != "" {
		fmt.Fprintf(&b, "Root cause so far: %s\n", c.Hypothesis)
	}
	if len(c.ConfirmedFacts) > 0 {
		b.WriteString("\nEstablished by tools (not by reasoning):\n")
		for _, f := range c.ConfirmedFacts {
			fmt.Fprintf(&b, "- %s [%s]\n", f.Fact, f.Evidence)
		}
	}
	if len(c.Symbols) > 0 {
		fmt.Fprintf(&b, "\nRelevant symbols: %s\n", strings.Join(c.Symbols, ", "))
	}
	if len(c.FilesExamined) > 0 {
		fmt.Fprintf(&b, "Files examined: %s\n", strings.Join(c.FilesExamined, ", "))
	}
	if len(c.Changes) > 0 {
		b.WriteString("\nChanges already on disk:\n")
		for _, ch := range c.Changes {
			where := ch.File
			if ch.Symbol != "" {
				where += " (" + ch.Symbol + ")"
			}
			fmt.Fprintf(&b, "- %s: %s\n", where, ch.Summary)
		}
	}
	if len(c.TestStatus.Ran) > 0 || c.TestStatus.Failed > 0 {
		fmt.Fprintf(&b, "\nVerification: %d passed, %d failed", c.TestStatus.Passed, c.TestStatus.Failed)
		if len(c.TestStatus.Ran) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(c.TestStatus.Ran, ", "))
		}
		b.WriteString("\n")
	}
	if len(c.OpenFailures) > 0 {
		fmt.Fprintf(&b, "Open failures: %s\n", strings.Join(c.OpenFailures, ", "))
	}
	if len(c.Decisions) > 0 {
		b.WriteString("\nDecisions:\n")
		for _, d := range c.Decisions {
			fmt.Fprintf(&b, "- %s\n", d)
		}
	}
	if c.NextAction != "" {
		fmt.Fprintf(&b, "\nNext: %s\n", c.NextAction)
	}
	return strings.TrimRight(b.String(), "\n")
}

// JSON is the stored form, for the trace.
func (c Card) JSON() ([]byte, error) { return json.Marshal(c) }

// Normalize sorts and deduplicates the list fields.
//
// A card assembled from the same state has to render identically every time,
// or P3 changes without the task changing and the frozen prefix stops being
// frozen — which is the whole cost this package exists to avoid.
func (c *Card) Normalize() {
	c.FilesExamined = sortUnique(c.FilesExamined)
	c.Symbols = sortUnique(c.Symbols)
	c.OpenFailures = sortUnique(c.OpenFailures)
	c.TestStatus.Ran = sortUnique(c.TestStatus.Ran)
}

func sortUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
