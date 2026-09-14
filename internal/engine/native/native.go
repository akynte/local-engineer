package native

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/retrieval"
	"github.com/akynte/local-engineer/prompts"
)

// llmToolCall is an alias kept local so exec.go reads without the package
// qualifier on every call.
type llmToolCall = llm.ToolCall

// Engine drives a bounded tool loop against a provider.
//
// The loop is deliberately the supervisor's, not the model's: it decides how
// many steps are allowed, what the model can see, and when to stop. §10.1
// rejects "think harder" reflection without new evidence, so every iteration
// here must bring something new — a tool result — or the loop ends.
type Engine struct {
	Provider  llm.Provider
	Retriever *retrieval.Retriever
	Graph     graph.Graph
	Recipes   *recipe.Runner

	// MaxSteps bounds one attempt. A model that loops calling read_file
	// forever must cost a bounded amount.
	MaxSteps int
	// MaxTools caps the tool surface; it comes from the hardware profile
	// (§9.3), because a small model given too many tools picks badly.
	MaxTools int
	// Temperature and friends come from the profile too.
	Temperature float64
	MaxTokens   int
	Thinking    string

	// Logf reports each tool call. Nil discards them.
	Logf func(format string, args ...any)

	tools []llm.ToolDef
}

// Options configures an engine.
type Options struct {
	Provider    llm.Provider
	Retriever   *retrieval.Retriever
	Graph       graph.Graph
	Recipes     *recipe.Runner
	MaxSteps    int
	MaxTools    int
	Temperature float64
	MaxTokens   int
	Thinking    string
	Logf        func(string, ...any)
}

// DefaultMaxSteps bounds one attempt when no profile says otherwise.
const DefaultMaxSteps = 20

// New builds an engine.
func New(o Options) (*Engine, error) {
	if o.Provider == nil {
		return nil, fmt.Errorf("native: no provider")
	}
	if !o.Provider.Capabilities().ToolCalling {
		// Refusing is the honest response. A model that cannot call tools
		// could be driven by parsing prose for edit blocks, but that turns
		// every malformed response into a silent no-op, and the design's
		// position is that a capability gap is declared rather than emulated
		// (DR-4).
		return nil, &llm.UnsupportedError{Provider: o.Provider.Name(), Capability: "tool calling"}
	}
	e := &Engine{
		Provider: o.Provider, Retriever: o.Retriever, Graph: o.Graph, Recipes: o.Recipes,
		MaxSteps: o.MaxSteps, MaxTools: o.MaxTools, Temperature: o.Temperature,
		MaxTokens: o.MaxTokens, Thinking: o.Thinking, Logf: o.Logf,
	}
	if e.MaxSteps <= 0 {
		e.MaxSteps = DefaultMaxSteps
	}
	e.refreshTools()
	return e, nil
}

// refreshTools recomputes the advertised surface from what is currently wired.
// It must run after anything that changes that, because the model is told the
// tool list once per call and will use whatever it is offered.
func (e *Engine) refreshTools() {
	e.tools = Definitions(e.MaxTools, Wired{
		Retrieval: e.Retriever != nil,
		Graph:     e.Graph != nil,
		Recipes:   e.Recipes != nil,
	})
}

func (e *Engine) Name() string { return "native/" + e.Provider.Name() }

func (e *Engine) Health(ctx context.Context) error { return e.Provider.Health(ctx) }

func (e *Engine) Close() error { return nil }

func (e *Engine) logf(format string, args ...any) {
	if e.Logf != nil {
		e.Logf(format, args...)
	}
}

// Step runs one attempt: a bounded tool loop that ends when the model calls
// done, runs out of steps, or stops asking for tools.
func (e *Engine) Step(ctx context.Context, req engine.Request) (*engine.Response, error) {
	messages := e.seed(req)
	resp := &engine.Response{}

	temp := e.Temperature
	for step := 1; step <= e.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			resp.Summary = fmt.Sprintf("stopped after %d step(s): %v", step-1, err)
			return resp, nil
		}

		out, err := e.Provider.Chat(ctx, llm.ChatRequest{
			Messages:    messages,
			Tools:       e.tools,
			ToolChoice:  "auto",
			Temperature: &temp,
			MaxTokens:   e.MaxTokens,
			Thinking:    e.Thinking,
			// The system prompt and the packet are the stable prefix; the
			// tool exchange follows. §8.2: stable prefix first, so the
			// provider's prompt cache survives the loop.
			CachePrefixHint: 2,
		})
		if err != nil {
			return nil, fmt.Errorf("native: step %d: %w", step, err)
		}
		resp.TokensUsed += out.PromptTokens + out.OutputTokens

		if !out.WantsTools() {
			// A response cut off at the output budget is not a decision to
			// stop. A reasoning model reaches this by spending the whole
			// budget thinking: FinishReason is "length", Content is empty and
			// the thinking is in Reasoning. Reporting that as "the model
			// stopped" would attribute a harness limit to the model.
			if out.FinishReason == "length" && strings.TrimSpace(out.Content) == "" {
				resp.Truncated = true
				resp.Summary = fmt.Sprintf(
					"the output budget of %d tokens ran out on step %d before the model "+
						"produced an answer or a tool call", e.MaxTokens, step)
				if n := len(strings.TrimSpace(out.Reasoning)); n > 0 {
					resp.Summary += fmt.Sprintf("; it was spent on %d characters of reasoning, "+
						"so the budget is too small for this model's thinking", n)
				}
				return resp, nil
			}
			// No tool call means the model has nothing further to do. §10.1
			// rejects reflection without new evidence, so prodding it to
			// continue would be spending tokens on nothing.
			resp.Summary = strings.TrimSpace(out.Content)
			if resp.Summary == "" {
				resp.Summary = fmt.Sprintf("the model stopped after %d step(s) without calling a tool", step)
			}
			return resp, nil
		}

		messages = append(messages, llm.Message{
			Role: "assistant", Content: out.Content, ToolCalls: out.ToolCalls,
		})

		for _, call := range out.ToolCalls {
			start := time.Now()
			res := e.Exec(ctx, req.Worktree, call)
			e.logf("  %s%s (%s)", call.Name, failMark(res), time.Since(start).Round(time.Millisecond))

			messages = append(messages, llm.Message{
				Role: "tool", ToolCallID: call.ID, Name: call.Name, Content: res.Content,
			})
			if res.Edited {
				resp.Edited = true
			}
			if res.Done {
				resp.Summary = res.Summary
				resp.ClaimsDone = true
				return resp, nil
			}
		}
	}

	resp.Summary = fmt.Sprintf("reached the step limit of %d without declaring completion", e.MaxSteps)
	return resp, nil
}

func failMark(r Result) string {
	if r.Failed {
		return " (rejected)"
	}
	return ""
}

// seed builds the opening conversation: a stable system prompt, then the
// objective with the retrieved packet and any feedback from the last attempt.
func (e *Engine) seed(req engine.Request) []llm.Message {
	return []llm.Message{
		{Role: "system", Content: prompts.EngineSystem()},
		{Role: "user", Content: e.brief(req)},
	}
}

func (e *Engine) brief(req engine.Request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Objective: %s\n", req.Objective)
	if req.Attempt > 1 {
		fmt.Fprintf(&b, "\nThis is attempt %d. The previous attempt did not pass verification.\n", req.Attempt)
	}

	if req.Packet != nil && len(req.Packet.Slices) > 0 {
		b.WriteString("\nRelevant code found by the supervisor's retrieval:\n")
		for _, s := range req.Packet.Slices {
			fmt.Fprintf(&b, "\n%s:%d-%d", s.Path, s.StartLine, s.EndLine)
			if s.Symbol != "" && s.Symbol != s.Path {
				fmt.Fprintf(&b, "  %s", s.Symbol)
			}
			b.WriteString("\n")
			if s.Signature != "" {
				fmt.Fprintf(&b, "  %s\n", s.Signature)
			}
			if body := strings.TrimSpace(s.Body); body != "" {
				fmt.Fprintf(&b, "%s\n", indent(body, "  "))
			}
		}
		if req.Packet.Impact != nil && len(req.Packet.Impact.Consumers) > 0 {
			fmt.Fprintf(&b, "\nImpact of changing this: %s\n", req.Packet.Impact.Summary())
		}
	}

	if len(req.Feedback) > 0 {
		b.WriteString("\nVerification findings from the previous attempt — fix these:\n")
		for _, f := range req.Feedback {
			fmt.Fprintf(&b, "\n%s", describe(f))
		}
	}
	return b.String()
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// SetRecipeRunner gives the engine the same sandboxed recipe runner the
// supervisor uses for the completion contract.
//
// It is set per task rather than at construction because the sandbox spec
// depends on which worktree the task got. Sharing the runner matters: a model
// checking its own work must see exactly what the contract will see, or it
// will declare victory against a different set of checks.
func (e *Engine) SetRecipeRunner(r *recipe.Runner) {
	e.Recipes = r
	// The recipe runner arrives after construction, so the tool surface has to
	// be recomputed: without this the engine either hides run_verification
	// from a run that has it, or keeps offering it to one that does not.
	e.refreshTools()
}
