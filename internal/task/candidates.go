package task

import (
	"fmt"
	"sort"

	"github.com/akynte/local-engineer/internal/recipe"
)

// Candidate generation and ranking (design v3 §10.1).
//
// Two attempts from the same baseline, judged by acceptance evidence. The
// parenthetical in §10.1 is the whole design: "evidence-judged, not
// model-voted". Nothing here asks a model which candidate is better, and
// nothing here is allowed to accept a candidate the completion contract would
// refuse.
//
// It is "adopted, sparingly" for a reason that is easy to lose: it doubles
// generation cost, and on this hardware generation is the expensive part. It
// earns that only where the objective is genuinely ambiguous — two reasonable
// readings, both of which compile. Where the answer is determined, the second
// candidate is the same answer at twice the price.

// Candidate is one attempt's finished work, with what verification said.
type Candidate struct {
	// Label distinguishes the attempts in logs and at a gate.
	Label string
	// Manifest is the worktree content hash this describes.
	Manifest string
	// Results are the verification results produced against that manifest.
	Results []recipe.Result
	// OutOfScope lists changes outside the declared scope, including anything a
	// repository policy protects.
	OutOfScope []string
	// Branch is where the work was committed.
	Branch string
	// DiffBytes is how much it changed, used only to break an exact tie.
	DiffBytes int
}

// Ranking is the outcome of comparing candidates.
type Ranking struct {
	// Best is the chosen candidate, or nil when none is acceptable.
	Best *Candidate
	// Reasons explains the choice in the words a person reads at the gate.
	Reasons []string
	// Tied records that the evidence did not separate them and the tiebreak
	// decided. A tie is worth surfacing: it usually means the objective was
	// ambiguous, which is a fact about the request rather than the code.
	Tied bool
}

// Rank chooses between candidates on evidence alone.
//
// The order of the tests is the argument:
//
//  1. A candidate the completion contract would refuse is not a candidate. This
//     is checked first so that "better" can never mean "less unacceptable".
//  2. Fewer out-of-scope changes wins. A change that touches what it was not
//     asked to touch is worse than one that does not, regardless of how its
//     tests went.
//  3. More passing recipe kinds wins, because that is more evidence.
//  4. A smaller diff wins, and only as a tiebreak. It is a weak signal —
//     smaller is not better in general — which is why it never outranks
//     evidence.
func Rank(level recipe.Level, candidates []Candidate) Ranking {
	var out Ranking
	if len(candidates) == 0 {
		out.Reasons = append(out.Reasons, "no candidates were produced")
		return out
	}

	type scored struct {
		c          Candidate
		acceptable bool
		passing    int
		why        []string
	}
	var all []scored
	for _, c := range candidates {
		// Rule 6 is the runner's to enforce, not this function's: the runner
		// sees the worktree a candidate was built from, where "changed
		// nothing" is answerable, and this sees only the evidence. DiffBytes
		// is not that answer — it is documented as a weak tiebreak, and a
		// producer that left it unset would have every candidate silently
		// refused here.
		ok, reasons := Accept(level, c.Results, c.Manifest, c.OutOfScope,
			Effect{Made: true, Expected: true})
		s := scored{c: c, acceptable: ok, passing: passingKinds(c.Results), why: reasons}
		all = append(all, s)
	}

	// 1. Only acceptable candidates compete.
	var viable []scored
	for _, s := range all {
		if s.acceptable {
			viable = append(viable, s)
		}
	}
	if len(viable) == 0 {
		out.Reasons = append(out.Reasons,
			fmt.Sprintf("none of the %d candidate(s) satisfied the completion contract", len(all)))
		for _, s := range all {
			for _, r := range s.why {
				out.Reasons = append(out.Reasons, s.c.Label+": "+r)
			}
		}
		return out
	}

	sort.SliceStable(viable, func(i, j int) bool {
		a, b := viable[i], viable[j]
		// 2. Fewer out-of-scope changes.
		if len(a.c.OutOfScope) != len(b.c.OutOfScope) {
			return len(a.c.OutOfScope) < len(b.c.OutOfScope)
		}
		// 3. More passing kinds.
		if a.passing != b.passing {
			return a.passing > b.passing
		}
		// 4. Smaller diff, as a tiebreak only.
		return a.c.DiffBytes < b.c.DiffBytes
	})

	best := viable[0]
	out.Best = &best.c
	out.Reasons = append(out.Reasons, fmt.Sprintf(
		"%s chosen: %d passing verification kind(s), %d out-of-scope change(s), %d bytes of diff",
		best.c.Label, best.passing, len(best.c.OutOfScope), best.c.DiffBytes))

	if len(viable) > 1 {
		second := viable[1]
		if len(second.c.OutOfScope) == len(best.c.OutOfScope) && second.passing == best.passing {
			out.Tied = true
			out.Reasons = append(out.Reasons, fmt.Sprintf(
				"%s produced the same evidence; the smaller diff decided. Equal evidence "+
					"usually means the objective admitted more than one reading, which is "+
					"worth knowing about the request rather than the code.", second.c.Label))
		} else {
			out.Reasons = append(out.Reasons, fmt.Sprintf(
				"%s was rejected: %d passing kind(s), %d out-of-scope change(s)",
				second.c.Label, second.passing, len(second.c.OutOfScope)))
		}
	}
	for _, s := range all {
		if !s.acceptable {
			out.Reasons = append(out.Reasons, fmt.Sprintf(
				"%s did not satisfy the contract: %s", s.c.Label, firstReason(s.why)))
		}
	}
	return out
}

func passingKinds(results []recipe.Result) int {
	seen := map[recipe.Kind]bool{}
	for _, r := range results {
		if r.Status == recipe.Pass {
			seen[r.Kind] = true
		}
	}
	return len(seen)
}

func firstReason(reasons []string) string {
	if len(reasons) == 0 {
		return "no reason recorded"
	}
	return reasons[0]
}
