// Package critic holds the three model calls that happen outside the editing
// conversation (design v3 §10.1).
//
// All three exist for the same reason and share the same constraint.
//
// The reason is that a conversation which has spent twenty steps building a
// change is the worst possible judge of it. §10.1 names the failure as
// "self-consistent errors": the context that produced a mistake contains every
// reason the mistake looked right. A fresh call sees the diff and the evidence
// and nothing else.
//
// The constraint is that **none of these decides anything**. §10.1 is explicit
// that candidates are "evidence-judged, not model-voted", and the completion
// contract accepts only when every required recipe has a passing result against
// the current candidate. A review can raise a concern for a human to read at
// the gate; it cannot accept, and it cannot reject. A diagnosis can change what
// the next attempt is told; it cannot change what counts as success.
//
// That is why nothing here returns a verdict. Review returns concerns,
// Diagnose returns a hypothesis, and the caller decides what to do with them.
package critic

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/recipe"
)

// Critic makes the out-of-conversation calls.
type Critic struct {
	Provider llm.Provider
	// MaxTokens bounds each call. These are meant to be cheap — §10.1 budgets
	// "one extra call per task" — so a critic that costs as much as the work is
	// not the technique being described.
	MaxTokens int
	// Temperature. Low: these calls are asked to notice things, not to be
	// creative about them.
	Temperature float64
	// Thinking follows the profile.
	Thinking string
}

// Concern is one thing a reviewer noticed.
type Concern struct {
	// Severity is the reviewer's own word. It is advisory and carries no
	// authority over acceptance; it orders what a person reads first.
	Severity string `json:"severity"`
	// Detail says what is wrong and where.
	Detail string `json:"detail"`
	// Path is the file, when the concern is about one.
	Path string `json:"path,omitempty"`
}

// ReviewResult is what a fresh-context review produced.
type ReviewResult struct {
	Concerns []Concern `json:"concerns"`
	// Summary is one line for the gate.
	Summary string `json:"summary"`
}

var reviewSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"summary":{"type":"string","description":"One line: what this change does."},
		"concerns":{
			"type":"array",
			"items":{
				"type":"object",
				"properties":{
					"severity":{"type":"string","enum":["low","medium","high"]},
					"detail":{"type":"string"},
					"path":{"type":"string"}
				},
				"required":["severity","detail"],
				"additionalProperties":false
			}
		}
	},
	"required":["summary","concerns"],
	"additionalProperties":false}`)

// Review looks at a finished change with no knowledge of how it was produced.
//
// It is given the diff and the verification evidence and nothing else: not the
// tool history, not the objective's restatements, not the attempts that failed.
// Handing it the conversation would reproduce the context whose self-consistency
// is the problem.
func (c *Critic) Review(ctx context.Context, objective, diff string, results []recipe.Result) (ReviewResult, error) {
	if c.Provider == nil {
		return ReviewResult{}, fmt.Errorf("critic: no provider")
	}
	if !c.Provider.Capabilities().StructuredOutput {
		// DR-4: refuse rather than degrade. A review parsed out of prose would
		// be a review whose concerns are sometimes silently dropped.
		return ReviewResult{}, &llm.UnsupportedError{
			Provider: c.Provider.Name(), Capability: "structured output",
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Objective: %s\n\n", objective)
	b.WriteString("Verification results:\n")
	for _, r := range results {
		fmt.Fprintf(&b, "  %s: %s — %s\n", r.Recipe, r.Status, r.Summary.Headline)
	}
	fmt.Fprintf(&b, "\nDiff:\n%s\n", truncate(diff, 20000))

	temp := c.Temperature
	resp, err := c.Provider.ChatStructured(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: reviewSystem},
			{Role: "user", Content: b.String()},
		},
		MaxTokens: c.MaxTokens, Temperature: &temp, Thinking: c.Thinking,
	}, reviewSchema)
	if err != nil {
		return ReviewResult{}, err
	}
	var out ReviewResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.Content)), &out); err != nil {
		return ReviewResult{}, fmt.Errorf("critic: review output is not the declared shape: %w", err)
	}
	return out, nil
}

const reviewSystem = `You are reviewing a finished change you did not write.

You are given the objective, the verification results, and the diff. You do not
have the history of how the change was made, and you do not need it.

Report concerns only. You are not deciding whether this is accepted: the
verification evidence decides that, and it has already been gathered. A concern
is something a careful reviewer would raise that the compiler and the tests
cannot see — a case not handled, an assumption not stated, a consumer likely
missed.

If the change looks right, return no concerns. An empty list is a useful answer
and padding it is not.`

// Hypothesis is a fresh reading of why attempts keep failing.
type Hypothesis struct {
	// Cause is what the diagnosis believes is actually wrong.
	Cause string `json:"cause"`
	// Suggestion is what to try instead.
	Suggestion string `json:"suggestion"`
	// Ruled out lists what the evidence already excludes, so the next attempt
	// does not spend itself re-testing them. §10.1 adopts persistent
	// cross-attempt state for exactly this.
	RuledOut []string `json:"ruled_out"`
}

var diagnoseSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"cause":{"type":"string"},
		"suggestion":{"type":"string"},
		"ruled_out":{"type":"array","items":{"type":"string"}}
	},
	"required":["cause","suggestion","ruled_out"],
	"additionalProperties":false}`)

// Diagnose reads repeated failures with fresh context.
//
// §10.1 names the problem as "stuck hypotheses": an attempt that failed the same
// way three times is not going to be fixed by a fourth attempt with the same
// context, because the context is what keeps producing the hypothesis. The one
// call this costs buys a different starting point.
func (c *Critic) Diagnose(ctx context.Context, objective string, failures []recipe.Result) (Hypothesis, error) {
	if c.Provider == nil {
		return Hypothesis{}, fmt.Errorf("critic: no provider")
	}
	if !c.Provider.Capabilities().StructuredOutput {
		return Hypothesis{}, &llm.UnsupportedError{
			Provider: c.Provider.Name(), Capability: "structured output",
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Objective: %s\n\nThe same work has now failed verification more than once.\n\n", objective)
	b.WriteString("What failed:\n")
	for _, r := range failures {
		fmt.Fprintf(&b, "\n%s (%s): %s\n", r.Recipe, r.Status, r.Summary.Headline)
		for i, f := range r.Summary.Findings {
			if i >= 10 {
				fmt.Fprintf(&b, "  … and %d more\n", len(r.Summary.Findings)-i)
				break
			}
			fmt.Fprintf(&b, "  %s:%d %s\n", f.File, f.Line, f.Message)
		}
	}

	temp := c.Temperature
	resp, err := c.Provider.ChatStructured(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: diagnoseSystem},
			{Role: "user", Content: b.String()},
		},
		MaxTokens: c.MaxTokens, Temperature: &temp, Thinking: c.Thinking,
	}, diagnoseSchema)
	if err != nil {
		return Hypothesis{}, err
	}
	var out Hypothesis
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.Content)), &out); err != nil {
		return Hypothesis{}, fmt.Errorf("critic: diagnosis output is not the declared shape: %w", err)
	}
	return out, nil
}

const diagnoseSystem = `You are diagnosing repeated failures on work you did not do.

You are given the objective and what verification reported, more than once. You
do not have the attempts themselves, which is deliberate: the reasoning that
produced the failures is what keeps producing them.

Name the cause you actually believe, and say what to try instead. List what the
evidence already rules out, so the next attempt does not spend itself
re-testing those.

If the failures point at the objective being wrong or ambiguous rather than the
code, say that. It is a more useful answer than a plausible fix.`

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n… (%d bytes truncated)", len(s)-n)
}
