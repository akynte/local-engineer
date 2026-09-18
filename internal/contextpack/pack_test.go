package contextpack_test

import (
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/contextpack"
	"github.com/akynte/local-engineer/internal/trust"
)

func newPack(t *testing.T, budget contextpack.Budget) *contextpack.Pack {
	t.Helper()
	fence, err := trust.NewFence()
	if err != nil {
		t.Fatal(err)
	}
	prefix := contextpack.Prefix{
		Policy:   "Operating policy: verify before claiming.",
		RepoCard: "local-engineer, Go, `go test ./...`",
		RepoMap:  "risk.OrderChecker#Check(ctx, Order) error",
		TaskCard: "Objective: evaluate account limit against total PnL.",
	}
	p, err := contextpack.New(prefix, fence, budget)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func big(budget contextpack.Budget) contextpack.Budget {
	if budget.Prefix == 0 {
		budget.Prefix = 7000
	}
	if budget.Log == 0 {
		budget.Log = 14000
	}
	return budget
}

// The whole point: the bytes before the divergence must not move. This is what
// decides whether a call costs a few hundred tokens of prefill or thirty
// thousand, so it is checked as an exact prefix rather than "looks similar".
func TestTheFrozenPrefixIsByteIdenticalAsTheLogGrows(t *testing.T) {
	p := newPack(t, big(contextpack.Budget{}))

	first, count := p.Messages(contextpack.Tail{Instruction: "Localize.", TokensRemaining: 9000})
	if count != 2 {
		t.Fatalf("prefix is %d messages, want 2", count)
	}
	frozen := []string{first[0].Content, first[1].Content}
	previous := first

	for i, body := range []string{"first evidence block", "second evidence block", "third"} {
		if err := p.Append(contextpack.Block{Kind: contextpack.KindEvidence, Origin: "source=file path=a.go", Body: body}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		next, nextCount := p.Messages(contextpack.Tail{
			Instruction:     "Localize.",
			TokensUsed:      100 * (i + 1),
			TokensRemaining: 9000 - 100*(i+1),
			Retry:           i,
		})
		if nextCount != count {
			t.Fatalf("prefix length changed from %d to %d", count, nextCount)
		}
		for j := range frozen {
			if next[j].Content != frozen[j] {
				t.Fatalf("frozen message %d changed after %d appends:\n%q\nvs\n%q", j, i+1, next[j].Content, frozen[j])
			}
		}
		// Everything already in the log must also be untouched, or the cache
		// is invalidated from the first changed entry onward. The log is every
		// message between the prefix and the tail.
		for j := count; j < count+i; j++ {
			if next[j].Content != previous[j].Content {
				t.Fatalf("log entry %d was rewritten on append %d", j-count, i+1)
			}
		}
		previous = next
	}
}

// A full log is a phase boundary, not a licence to drop the oldest entry.
func TestAFullLogIsAnErrorRatherThanASilentDrop(t *testing.T) {
	p := newPack(t, contextpack.Budget{Prefix: 7000, Log: 200})

	if err := p.Append(contextpack.Block{Kind: contextpack.KindEvidence, Body: strings.Repeat("x", 400)}); err != nil {
		t.Fatal(err)
	}
	before := p.Blocks()

	err := p.Append(contextpack.Block{Kind: contextpack.KindEvidence, Body: strings.Repeat("y", 4000)})
	if err == nil {
		t.Fatal("an oversized block was admitted")
	}
	if !strings.Contains(err.Error(), "phase boundary") {
		t.Fatalf("the error does not say what to do instead: %v", err)
	}
	if len(p.Blocks()) != len(before) {
		t.Fatal("a refused append changed the log")
	}
	if p.Fits(contextpack.Block{Kind: contextpack.KindEvidence, Body: strings.Repeat("y", 4000)}) {
		t.Fatal("Fits disagrees with Append")
	}
}

// Truncation happens once, at fetch time. A rule applied later would shorten a
// block that earlier calls already sent in full.
func TestCommandOutputIsTruncatedOnceAtFetchTime(t *testing.T) {
	var lines []string
	for i := range 500 {
		lines = append(lines, "line "+string(rune('a'+i%26)))
	}
	long := strings.Join(lines, "\n")

	out := contextpack.Truncate(contextpack.KindToolResult, long)
	if !strings.Contains(out, "lines omitted at fetch time") {
		t.Fatal("long command output was not truncated")
	}
	if strings.Count(out, "\n") >= strings.Count(long, "\n") {
		t.Fatal("truncation did not shorten the output")
	}
	if contextpack.Truncate(contextpack.KindToolResult, out) != out {
		t.Fatal("truncating an already-truncated block changed it again")
	}

	// A code range was bounded by whoever selected it. Cutting its middle out
	// produces something that reads as source and is not.
	if contextpack.Truncate(contextpack.KindEvidence, long) != long {
		t.Fatal("evidence was truncated by the command-output rule")
	}
}

// Repack is the one rebuild §7.1 allows, and P0–P2 have to survive it or the
// server's checkpoint no longer matches and the boundary costs a full prefill
// on top of the one it already pays.
func TestRepackKeepsThePolicyRepoCardAndMapAndStartsAnEmptyLog(t *testing.T) {
	p := newPack(t, big(contextpack.Budget{}))
	if err := p.Append(contextpack.Block{Kind: contextpack.KindEvidence, Body: "localization evidence"}); err != nil {
		t.Fatal(err)
	}
	before, _ := p.Messages(contextpack.Tail{})

	next, err := p.Repack("Objective: evaluate account limit against total PnL.\n\nPlan: change accountLimitExceeded.", contextpack.Budget{Prefix: 7000, Log: 16000})
	if err != nil {
		t.Fatal(err)
	}
	after, count := next.Messages(contextpack.Tail{})

	if after[0].Content != before[0].Content {
		t.Fatal("the policy changed across a phase boundary")
	}
	if !strings.Contains(after[1].Content, "OrderChecker") {
		t.Fatal("the repository map was lost across a phase boundary")
	}
	if !strings.Contains(after[1].Content, "Plan: change accountLimitExceeded") {
		t.Fatal("the new task card did not reach P3")
	}
	if len(after) != count {
		t.Fatalf("the log survived the boundary: %d messages after the prefix", len(after)-count)
	}
	if next.LogTokens() != 0 {
		t.Fatalf("log token count survived the boundary: %d", next.LogTokens())
	}
	// Repacking without a new card keeps the old one: a boundary that is not a
	// planning boundary should not disturb P3 at all.
	same, err := p.Repack("", contextpack.Budget{Prefix: 7000, Log: 16000})
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := same.Messages(contextpack.Tail{})
	if msgs[1].Content != before[1].Content {
		t.Fatal("a boundary with no new plan still changed the frozen prefix")
	}
}

// Repository content reaches the model inside the fence, with provenance. A
// model turn does not: it is not evidence and marking it as such would teach
// the model that its own words are repository data.
func TestEvidenceIsFencedWithProvenanceAndModelTurnsAreNot(t *testing.T) {
	p := newPack(t, big(contextpack.Budget{}))
	if err := p.Append(contextpack.Block{Kind: contextpack.KindEvidence, Origin: "source=file path=risk/order.go commit=abc123", Body: "func Check() error { return nil }"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Append(contextpack.Block{Kind: contextpack.KindModelTurn, Body: "I will change accountLimitExceeded."}); err != nil {
		t.Fatal(err)
	}
	msgs, count := p.Messages(contextpack.Tail{})

	evidence := msgs[count]
	if !strings.Contains(evidence.Content, "path=risk/order.go commit=abc123") {
		t.Fatalf("evidence lost its provenance: %q", evidence.Content)
	}
	if !strings.Contains(evidence.Content, "UNTRUSTED") {
		t.Fatalf("evidence was not fenced: %q", evidence.Content)
	}
	turn := msgs[count+1]
	if turn.Role != "assistant" {
		t.Fatalf("a model turn was replayed as role %q", turn.Role)
	}
	if strings.Contains(turn.Content, "UNTRUSTED") {
		t.Fatal("a model turn was fenced as repository evidence")
	}
}

func TestAPackRefusesToBuildWithoutAFenceOrWithAnOversizedPrefix(t *testing.T) {
	if _, err := contextpack.New(contextpack.Prefix{}, trust.Fence{}, big(contextpack.Budget{})); err == nil {
		t.Fatal("a pack was built with no fence, so evidence would arrive unmarked")
	}
	fence, err := trust.NewFence()
	if err != nil {
		t.Fatal(err)
	}
	huge := contextpack.Prefix{Policy: strings.Repeat("policy ", 5000)}
	if _, err := contextpack.New(huge, fence, contextpack.Budget{Prefix: 1000, Log: 1000}); err == nil {
		t.Fatal("a prefix over the phase budget was accepted")
	}
}

// The tail is rebuilt every call and must stay small, because it is the only
// part that is re-processed on every request.
func TestTheTailIsSmallAndCarriesTheBudgetNote(t *testing.T) {
	p := newPack(t, big(contextpack.Budget{}))
	msgs, count := p.Messages(contextpack.Tail{Instruction: "Plan the change.", TokensUsed: 4000, TokensRemaining: 12000, Retry: 1})
	tail := msgs[len(msgs)-1]
	if len(msgs) != count+1 {
		t.Fatalf("expected only the tail after the prefix, got %d messages", len(msgs)-count)
	}
	if !strings.Contains(tail.Content, "Plan the change.") {
		t.Fatal("the phase instruction is missing from the tail")
	}
	if !strings.Contains(tail.Content, "12000 remaining") || !strings.Contains(tail.Content, "attempt 2") {
		t.Fatalf("the budget note is wrong: %q", tail.Content)
	}
	if contextpack.EstimateTokens(tail.Content) > 300 {
		t.Fatalf("the tail is %d tokens; §7.1 budgets about 0.3K", contextpack.EstimateTokens(tail.Content))
	}
}

// A card assembled twice from the same state has to render identically, or P3
// moves without the task moving and the prefix stops being frozen.
func TestCompactionCardIsDeterministic(t *testing.T) {
	a := contextpack.Card{
		Objective:     "evaluate account limit against total PnL",
		FilesExamined: []string{"risk/order.go", "risk/pnl.go", "risk/order.go"},
		Symbols:       []string{"Check", "accountLimitExceeded"},
		OpenFailures:  []string{"fp-2", "fp-1"},
	}
	b := contextpack.Card{
		Objective:     "evaluate account limit against total PnL",
		FilesExamined: []string{"risk/pnl.go", "risk/order.go"},
		Symbols:       []string{"accountLimitExceeded", "Check"},
		OpenFailures:  []string{"fp-1", "fp-2", "fp-1"},
	}
	a.Normalize()
	b.Normalize()
	if a.Render() != b.Render() {
		t.Fatalf("the same state rendered two ways:\n%s\n---\n%s", a.Render(), b.Render())
	}
	if strings.Contains(a.Render(), "risk/order.go, risk/order.go") {
		t.Fatal("duplicates survived normalization")
	}
}

// The card is built from what tools established. A fact without evidence is a
// belief, and the rendering says which is which.
func TestCompactionCardNamesTheEvidenceForEachFact(t *testing.T) {
	c := contextpack.Card{
		Objective:      "fix the account limit basis",
		ConfirmedFacts: []contextpack.Fact{{Fact: "Check has two callers", Evidence: "scip:OrderChecker#Check()."}},
		Changes:        []contextpack.Change{{File: "risk/order.go", Symbol: "accountLimitExceeded", Summary: "use realized + unrealized"}},
		TestStatus:     contextpack.Tests{Ran: []string{"go test"}, Passed: 12, Failed: 1},
	}
	out := c.Render()
	for _, want := range []string{"scip:OrderChecker#Check().", "Established by tools", "12 passed, 1 failed", "accountLimitExceeded"} {
		if !strings.Contains(out, want) {
			t.Fatalf("card is missing %q:\n%s", want, out)
		}
	}
	if _, err := c.JSON(); err != nil {
		t.Fatal(err)
	}
}
