package native

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/models"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/retrieval"
	"github.com/akynte/local-engineer/internal/trust"
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

	// ContextTokens is the model's context window in tokens. The engine
	// trims the conversation when the estimate exceeds this budget so that
	// Provider.Chat never receives a request larger than the window. Zero
	// disables trimming entirely.
	ContextTokens int

	// Logf reports each tool call. Nil discards them.
	Logf func(format string, args ...any)

	// fence marks repository content so it cannot be read as an instruction.
	// It carries a token generated per engine, which is what a document would
	// have to guess to close the fence and speak outside it.
	fence trust.Fence

	tools []llm.ToolDef
}

// Options configures an engine.
type Options struct {
	Provider      llm.Provider
	Retriever     *retrieval.Retriever
	Graph         graph.Graph
	Recipes       *recipe.Runner
	MaxSteps      int
	MaxTools      int
	Temperature   float64
	MaxTokens     int
	Thinking      string
	ContextTokens int
	Logf          func(string, ...any)
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
	// One fence per engine, so every message in a run shares a token and
	// content copied from an earlier task cannot forge a marker in this one.
	fence, err := trust.NewFence()
	if err != nil {
		return nil, err
	}
	e := &Engine{
		Provider: o.Provider, Retriever: o.Retriever, Graph: o.Graph, Recipes: o.Recipes,
		MaxSteps: o.MaxSteps, MaxTools: o.MaxTools, Temperature: o.Temperature,
		MaxTokens: o.MaxTokens, Thinking: o.Thinking, ContextTokens: o.ContextTokens,
		Logf: o.Logf, fence: fence,
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

// estimateTokens counts the approximate token footprint of messages and tool
// definitions. It accumulates character counts as an int, then converts ONCE
// at the end using float arithmetic so the untyped float constant 3.5 is
// never converted to int directly (which would be a compile error).
func estimateTokens(messages []llm.Message, tools []llm.ToolDef) int {
	const overhead = 16 // fixed per-message overhead for role + tool_call_id metadata
	chars := 0
	for _, m := range messages {
		chars += overhead
		chars += len(m.Role)
		chars += len(m.Content)
		chars += len(m.ToolCallID)
		for _, tc := range m.ToolCalls {
			chars += len(tc.ID) + len(tc.Name) + len(tc.Arguments)
		}
	}
	for _, t := range tools {
		chars += overhead
		chars += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	return int(float64(chars) / models.DefaultCharsPerToken)
}

// trimMessages removes the oldest exchange (one assistant message plus its
// tool-result children) while the estimate exceeds the budget. It never drops
// the first two messages (system prompt and user packet) and never drops the
// most recent exchange. It builds a new slice rather than mutating the caller's.
// Returns the trimmed slice and the number of messages dropped.
//
// The tools travel with the messages because they are part of the same request:
// the definitions are roughly 900 tokens on every call, and a budget that
// ignores them permits exactly the oversized request this function prevents.
func trimMessages(messages []llm.Message, tools []llm.ToolDef, budget int) ([]llm.Message, int) {
	if budget <= 0 {
		// Zero or negative budget: skip trimming entirely.
		return messages, 0
	}

	dropped := 0
	for {
		est := estimateTokens(messages, tools)
		if est <= budget {
			break
		}
		// Find the oldest exchange to drop: the first assistant message
		// (at index >= 2) and all tool messages that follow it until the
		// next assistant message or the end.
		// We must never drop the first two messages (system + user).
		// We must never drop the most recent exchange.
		// Strategy: scan from the back to find the most recent assistant
		// message, then scan from the front (after index 1) to find the
		// oldest assistant message. Drop that oldest exchange.

		// Find the last assistant message index.
		lastAssistant := -1
		for i := len(messages) - 1; i >= 2; i-- {
			if messages[i].Role == "assistant" {
				lastAssistant = i
				break
			}
		}
		if lastAssistant < 0 {
			// No assistant message to drop; nothing to trim.
			break
		}

		// Find the first assistant message at or after index 2.
		firstAssistant := -1
		for i := 2; i < len(messages); i++ {
			if messages[i].Role == "assistant" {
				firstAssistant = i
				break
			}
		}
		if firstAssistant < 0 {
			break
		}

		// If the first and last assistant are the same, there's only one
		// exchange and we must not drop it.
		if firstAssistant == lastAssistant {
			break
		}

		// Find the end of the first exchange: the message right before the
		// next assistant message, or the last message.
		endOfFirst := len(messages)
		for i := firstAssistant + 1; i < len(messages); i++ {
			if messages[i].Role == "assistant" {
				endOfFirst = i
				break
			}
		}

		// Count how many messages we're dropping.
		dropCount := endOfFirst - firstAssistant
		dropped += dropCount

		// Build a new slice without the dropped messages.
		newMsgs := make([]llm.Message, 0, len(messages)-dropCount)
		newMsgs = append(newMsgs, messages[:firstAssistant]...)
		newMsgs = append(newMsgs, messages[endOfFirst:]...)
		messages = newMsgs
	}

	return messages, dropped
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

		// §8.1: bound the tool transcript against the context window.
		// Trim the oldest exchanges when the estimate exceeds the budget,
		// so Provider.Chat never receives a request larger than the window.
		if e.ContextTokens > 0 {
			// The reply has to fit too, so the transcript's budget is what is
			// left after reserving the output allowance.
			budget := e.ContextTokens - e.MaxTokens
			if budget < 0 {
				budget = 0
			}
			var dropped int
			messages, dropped = trimMessages(messages, e.tools, budget)
			if dropped > 0 {
				resp.DroppedMessages += dropped
				e.logf("trimmed %d message(s) to fit the %d-token context window", dropped, e.ContextTokens)
			}
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

			// A tool result is repository content by another route: read_file
			// returns a file, search_code returns matching lines, git_log
			// returns commit messages someone else wrote. Fencing only the
			// packet would leave the larger channel open, and it is the one
			// the model reads most.
			messages = append(messages, llm.Message{
				Role: "tool", ToolCallID: call.ID, Name: call.Name,
				Content: e.ensureFence().Wrap("result of "+call.Name, res.Content),
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
	return e.briefWith(req, e.ensureFence())
}

// ensureFence returns this engine's fence, making one if it has none.
//
// New always sets one, so in production this is a no-op. It exists because an
// Engine built by struct literal — which tests do — would otherwise render
// unfenced content, and a defence that depends on the constructor being used is
// one an ordinary refactor can remove without failing anything.
func (e *Engine) ensureFence() trust.Fence {
	if !e.fence.Valid() {
		if made, err := trust.NewFence(); err == nil {
			e.fence = made
		}
	}
	return e.fence
}

// briefWith renders the opening message with a given fence, so a test can pin
// the token instead of matching a random one.
func (e *Engine) briefWith(req engine.Request, fence trust.Fence) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Objective: %s\n", req.Objective)
	// Stated once, above everything it governs. The objective is the only
	// instruction in this message; what follows it came out of the repository.
	b.WriteString("\n" + fence.Preamble() + "\n")
	if req.Attempt > 1 {
		fmt.Fprintf(&b, "\nThis is attempt %d. The previous attempt did not pass verification.\n", req.Attempt)
	}

	// Before the code, because these say why the work is being done and what
	// this repository has already learned — and because §8.2 wants the stable
	// part of the packet first, where the prompt cache can keep it.
	if req.Packet != nil && len(req.Packet.Notes) > 0 {
		b.WriteString("\nWhat this repository has recorded. These are notes, not code, " +
			"and each says where it came from:\n")
		for _, n := range req.Packet.Notes {
			src := n.Provenance.Source
			if src == "" {
				src = "unattributed"
			}
			// A note is written by whoever had commit access, which is not
			// necessarily the operator sitting at this task.
			b.WriteString(fence.Wrap(
				fmt.Sprintf("note %s from %s", n.Kind, src), n.Text) + "\n")
		}
		if req.Packet.NotesDropped > 0 {
			fmt.Fprintf(&b, "  (%d more note(s) did not fit)\n", req.Packet.NotesDropped)
		}
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
				fmt.Fprintf(&b, "  %s\n", fence.Neutralise(s.Signature))
			}
			if body := strings.TrimSpace(s.Body); body != "" {
				origin := s.Path
				if s.Symbol != "" && s.Symbol != s.Path {
					origin = fmt.Sprintf("%s %s", s.Path, s.Symbol)
				}
				b.WriteString(fence.Wrap(origin, body) + "\n")
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
