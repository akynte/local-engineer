// Package contextpack assembles every model request as a frozen prefix, an
// append-only log and a small tail (architecture review §7).
//
// The shape is forced by the inference hardware, not by taste. For the hybrid
// Gated DeltaNet models this system runs, llama.cpp cannot reuse a partial KV
// cache: a prompt is cheap only when it is an exact prefix extension of the
// previous one on the same slot. Any change earlier in the prompt — a re-ranked
// snippet, a dropped tool result, an edited system line — forces the whole
// prompt to be processed again. At a few hundred tokens per second that is one
// to two minutes per call, and over a hundred calls it is the working day.
//
// So nothing here rewrites. A tool result is truncated once, when it is
// fetched, and is never rewritten afterwards; the log is appended to and never
// reordered; and the only place the pack is rebuilt is a phase boundary, where
// one full prefill is paid deliberately and P0–P3 stay byte-identical so the
// checkpointed prefix still hits.
//
// The package is named contextpack rather than context, which §19's directory
// listing suggests, because every file that uses it also uses context.Context
// and one of the two would need an alias at every call site.
package contextpack

import (
	"fmt"
	"strings"

	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/trust"
)

// Prefix is frozen for the whole task: §7.1's P0 through P3.
//
// P3 is the one part that legitimately changes, once, when a plan first exists.
// That is a deliberate re-prefill at a phase boundary, not a rewrite inside a
// phase.
type Prefix struct {
	// Policy (P0) is the operating policy. It is identical for every task, so
	// it sits first and is the longest stretch any two tasks share.
	Policy string
	// RepoCard (P1) is what this repository is: languages, layout, the
	// verification commands actually confirmed, conventions.
	RepoCard string
	// RepoMap (P2) is the ranked repository map for this task, biased toward
	// the localization set.
	RepoMap string
	// TaskCard (P3) is the objective, acceptance criteria, constraints, and
	// the plan once one exists.
	TaskCard string
}

// BlockKind names what a log entry is, which decides how it is truncated.
type BlockKind string

const (
	// KindEvidence is repository content fetched deterministically: code
	// ranges, index results, test bodies.
	KindEvidence BlockKind = "evidence"
	// KindToolResult is the output of a command or tool call.
	KindToolResult BlockKind = "tool_result"
	// KindModelTurn is what the model said.
	KindModelTurn BlockKind = "model_turn"
)

// Block is one append-only log entry. Body is already truncated: Append stores
// what Truncate returned, so no later pass can shorten it and invalidate every
// token after it.
type Block struct {
	Kind BlockKind
	// Origin is the provenance header §9 requires on anything read out of the
	// repository, for example `source=file path=risk/order.go commit=abc123`.
	// It is empty for a model turn, which has no external provenance.
	Origin string
	Body   string
}

// Budget is §7.2's row for one phase.
type Budget struct {
	// Prefix is the ceiling for P0–P3 together.
	Prefix int
	// Log caps the append-only region.
	Log int
	// Output is reserved for the completion, thinking included.
	Output int
}

// Pack is one phase's context.
type Pack struct {
	prefix Prefix
	fence  trust.Fence
	budget Budget
	log    []Block
	// logTokens is the running estimate. It only ever grows within a phase,
	// which is the invariant that makes the prefix reusable.
	logTokens int
}

// New builds a pack for one phase. The fence marks repository content as data;
// a pack without a valid one refuses to build, because unfenced evidence is
// indistinguishable from an instruction.
func New(prefix Prefix, fence trust.Fence, budget Budget) (*Pack, error) {
	if !fence.Valid() {
		return nil, fmt.Errorf("contextpack: a pack needs a valid fence; unfenced evidence reads as instruction")
	}
	if budget.Prefix <= 0 || budget.Log <= 0 {
		return nil, fmt.Errorf("contextpack: phase budget must reserve prefix and log room")
	}
	p := &Pack{prefix: prefix, fence: fence, budget: budget}
	if used := p.PrefixTokens(); used > budget.Prefix {
		return nil, fmt.Errorf("contextpack: frozen prefix is %d tokens, over the phase budget of %d", used, budget.Prefix)
	}
	return p, nil
}

// ErrLogFull reports that the append-only region is full.
//
// It is an error rather than a silent drop on purpose. Dropping the oldest
// entry to make room is what every chat scaffold does, and on this hardware it
// invalidates the cache from that point on and costs a full re-prefill —
// having also thrown away evidence the model may still need. The caller takes
// a phase boundary instead, which is the one place a rebuild is affordable.
var ErrLogFull = fmt.Errorf("contextpack: append-only log is full; take a phase boundary")

// Append adds one block, truncating its body by fixed rules before storing it.
func (p *Pack) Append(b Block) error {
	b.Body = Truncate(b.Kind, b.Body)
	cost := EstimateTokens(p.render(b))
	if p.logTokens+cost > p.budget.Log {
		return ErrLogFull
	}
	p.log = append(p.log, b)
	p.logTokens += cost
	return nil
}

// Fits reports whether a block would be admitted, without admitting it. A
// caller that must choose between several pieces of evidence asks first rather
// than discovering the limit halfway through.
func (p *Pack) Fits(b Block) bool {
	b.Body = Truncate(b.Kind, b.Body)
	return p.logTokens+EstimateTokens(p.render(b)) <= p.budget.Log
}

// Tail is §7.1's T1 and T2: rebuilt on every call and deliberately tiny, so
// re-processing it costs nothing.
type Tail struct {
	// Instruction is the current phase's instruction and its allowed tools.
	Instruction string
	// TokensUsed, TokensRemaining and Retry are the budget note. A model that
	// cannot see its remaining allowance spends it on the wrong thing.
	TokensUsed      int
	TokensRemaining int
	Retry           int
}

func (t Tail) render() string {
	var b strings.Builder
	b.WriteString(t.Instruction)
	if t.TokensUsed > 0 || t.TokensRemaining > 0 || t.Retry > 0 {
		fmt.Fprintf(&b, "\n\nBudget: %d tokens used, %d remaining, attempt %d.",
			t.TokensUsed, t.TokensRemaining, t.Retry+1)
	}
	return b.String()
}

// Messages renders the request, and reports how many leading messages are the
// frozen prefix so the caller can set CachePrefixHint.
//
// The prefix is two messages — the policy as a system turn, then P1–P3 as one
// user turn — and both are byte-identical for every call in a task. The log
// follows in the order it was appended, and the tail is last.
func (p *Pack) Messages(tail Tail) (msgs []llm.Message, prefixCount int) {
	msgs = append(msgs, llm.Message{Role: "system", Content: p.prefix.Policy + "\n\n" + p.fence.Preamble()})
	msgs = append(msgs, llm.Message{Role: "user", Content: p.frozenUser()})
	prefixCount = len(msgs)

	for _, b := range p.log {
		role := "user"
		if b.Kind == KindModelTurn {
			role = "assistant"
		}
		msgs = append(msgs, llm.Message{Role: role, Content: p.render(b)})
	}
	if rendered := tail.render(); strings.TrimSpace(rendered) != "" {
		msgs = append(msgs, llm.Message{Role: "user", Content: rendered})
	}
	return msgs, prefixCount
}

// frozenUser renders P1–P3. Sections a task has not filled are omitted rather
// than rendered empty, so the text is stable instead of carrying placeholders
// that change when they are filled.
func (p *Pack) frozenUser() string {
	var b strings.Builder
	for _, section := range []struct{ heading, body string }{
		{"REPOSITORY", p.prefix.RepoCard},
		{"REPOSITORY MAP", p.prefix.RepoMap},
		{"TASK", p.prefix.TaskCard},
	} {
		if strings.TrimSpace(section.body) == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("## " + section.heading + "\n")
		b.WriteString(section.body)
	}
	return b.String()
}

// render wraps a block for the prompt. Repository content goes inside the
// fence with its provenance; a model turn does not, because it is not evidence.
func (p *Pack) render(b Block) string {
	if b.Kind == KindModelTurn {
		return b.Body
	}
	origin := b.Origin
	if origin == "" {
		origin = string(b.Kind)
	}
	return p.fence.Wrap(origin, b.Body)
}

// PrefixTokens estimates the frozen region.
func (p *Pack) PrefixTokens() int {
	return EstimateTokens(p.prefix.Policy) + EstimateTokens(p.fence.Preamble()) + EstimateTokens(p.frozenUser())
}

// LogTokens reports what the append-only region currently costs.
func (p *Pack) LogTokens() int { return p.logTokens }

// Blocks returns the log in order. The slice is a copy: a caller that could
// reorder the log in place could invalidate the cache from the first changed
// entry onward.
func (p *Pack) Blocks() []Block { return append([]Block(nil), p.log...) }

// Prefix returns the frozen region, for a caller persisting it across a phase
// boundary.
func (p *Pack) Prefix() Prefix { return p.prefix }

// Repack starts a new phase: the same frozen prefix, an empty log, and the
// phase's own budget.
//
// This is the only rebuild §7.1 allows, and P0–P2 are carried over unchanged so
// the server's checkpointed prefix still matches. A caller that has a plan now
// passes the new task card, which is the one legitimate P3 change and costs one
// deliberate re-prefill.
func (p *Pack) Repack(taskCard string, budget Budget) (*Pack, error) {
	next := p.prefix
	if strings.TrimSpace(taskCard) != "" {
		next.TaskCard = taskCard
	}
	return New(next, p.fence, budget)
}

const (
	// headLines and tailLines are §7.1's fixed truncation for command output:
	// the beginning says what ran, the end says how it failed, and the middle
	// of a ten-thousand-line test log is where neither is.
	headLines = 60
	tailLines = 40
	// markerLines is what the omission notice adds: a blank line, the notice,
	// and another blank line.
	markerLines = 3
)

// Truncate applies the fixed rules, at fetch time and once.
//
// Evidence is not truncated here: a code range was already bounded by whoever
// selected it, and cutting the middle out of a function body produces something
// that looks like source and is not.
func Truncate(kind BlockKind, body string) string {
	if kind != KindToolResult {
		return body
	}
	lines := strings.Split(body, "\n")
	// The marker itself costs three lines, so the threshold has to include
	// them: at exactly headLines+tailLines the result would be longer than the
	// bound that produced it, and truncating a truncated block would rewrite
	// content earlier calls already sent — the one thing this package forbids.
	if len(lines) <= headLines+tailLines+markerLines {
		return body
	}
	head := lines[:headLines]
	tail := lines[len(lines)-tailLines:]
	omitted := len(lines) - headLines - tailLines
	return strings.Join(head, "\n") +
		fmt.Sprintf("\n\n... %d lines omitted at fetch time ...\n\n", omitted) +
		strings.Join(tail, "\n")
}

// EstimateTokens approximates a token count from bytes.
//
// It is an estimate and is used only for admission, never for accounting: what
// the provider reports is what the ledger records. Four bytes per token is
// close for English prose and pessimistic for source, which is the direction an
// admission check should err in.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return len(s)/4 + 1
}
