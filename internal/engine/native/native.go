package native

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/config"
	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/models"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/retrieval"
	"github.com/akynte/local-engineer/internal/trust"
	"github.com/akynte/local-engineer/internal/workflow"
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
	// stops at a boundary when the estimate exceeds this budget. It never
	// removes earlier exchanges inside an attempt. Zero disables this check.
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

func (e *Engine) WorkflowProvider() llm.Provider { return e.Provider }

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

// Step runs one attempt: a bounded tool loop that ends when the model calls
// done, runs out of steps, or stops asking for tools.
func (e *Engine) Step(ctx context.Context, req engine.Request) (*engine.Response, error) {
	if req.Phase != "" && req.Phase != workflow.Edit {
		return nil, fmt.Errorf("native: tools are only available in EDIT")
	}
	transcript := req.Transcript
	if transcript == nil {
		transcript = &workflow.Transcript{}
	}
	req.Transcript = transcript
	if transcript.Pending {
		if err := e.reconcile(ctx, req); err != nil {
			return nil, err
		}
	}
	if transcript.Closed {
		return &engine.Response{Summary: transcript.Summary, ClaimsDone: transcript.ClaimsDone, BudgetExhausted: transcript.BudgetExhausted, Truncated: transcript.Truncated}, nil
	}
	messages := transcript.Messages
	if len(messages) == 0 {
		transcript.FenceToken = e.ensureFence().Token()
		messages = e.seed(req)
	} else {
		fence, err := trust.RestoreFence(transcript.FenceToken)
		if err != nil {
			return nil, err
		}
		e.fence = fence
	}
	persist := func() error {
		transcript.Messages = messages
		if req.SaveTranscript != nil {
			return req.SaveTranscript(ctx, transcript)
		}
		return nil
	}
	if err := persist(); err != nil {
		return nil, err
	}
	resp := &engine.Response{}
	finish := func() (*engine.Response, error) {
		transcript.Closed = true
		transcript.Summary, transcript.ClaimsDone = resp.Summary, resp.ClaimsDone
		transcript.BudgetExhausted, transcript.Truncated = resp.BudgetExhausted, resp.Truncated
		return resp, persist()
	}
	prog := newProgress()

	temp := e.Temperature
	maxSteps := e.MaxSteps
	if req.Budget.MaxSteps > 0 && req.Budget.MaxSteps < maxSteps {
		maxSteps = req.Budget.MaxSteps
	}
	for step := transcript.Steps + 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			resp.Summary = fmt.Sprintf("stopped after %d step(s): %v", step-1, err)
			return resp, nil
		}

		// Architecture review §7: never rewrite the phase transcript to fit.
		// Returning a boundary prevents both a cache-breaking trim and sending
		// an oversized initial packet when there is nothing left to trim.
		estimate := estimateTokens(messages, e.tools)
		outputLimit := e.MaxTokens
		if req.Budget.OutputTokens > 0 && (outputLimit <= 0 || req.Budget.OutputTokens < outputLimit) {
			outputLimit = req.Budget.OutputTokens
		}
		if outputLimit <= 0 {
			outputLimit = config.FallbackProfile().ReservedOutput
		}
		contextLimit := e.ContextTokens
		if req.Budget.ContextTokens > 0 && (contextLimit <= 0 || req.Budget.ContextTokens < contextLimit) {
			contextLimit = req.Budget.ContextTokens
		}
		if contextLimit > 0 && estimate+outputLimit > contextLimit {
			resp.BudgetExhausted = true
			resp.Summary = "EDIT context budget exhausted; a supervisor phase boundary is required"
			return finish()
		}
		if req.Budget.MaxTokens > 0 {
			remaining := req.Budget.MaxTokens - resp.TokensUsed - estimate
			if remaining <= 0 {
				resp.BudgetExhausted = true
				resp.Summary = "task token budget exhausted"
				return finish()
			}
			if outputLimit > remaining {
				outputLimit = remaining
			}
		}

		out, err := e.Provider.Chat(ctx, llm.ChatRequest{
			Messages:              messages,
			Tools:                 e.tools,
			ToolChoice:            "auto",
			Temperature:           &temp,
			MaxTokens:             outputLimit,
			Thinking:              e.Thinking,
			ReasoningBudgetTokens: min(req.Budget.ReasoningTokens, max(1, outputLimit-1)),
			// The system prompt and the packet are the stable prefix; the
			// tool exchange follows. §8.2: stable prefix first, so the
			// provider's prompt cache survives the loop.
			CachePrefixHint: 2,
		})
		if err != nil {
			return nil, fmt.Errorf("native: step %d: %w", step, err)
		}
		promptTokens, outputTokens := out.PromptTokens, out.OutputTokens
		if promptTokens <= 0 {
			promptTokens = estimate
		}
		if outputTokens <= 0 {
			outputTokens = estimateTokens([]llm.Message{{Role: "assistant", Content: out.Content + out.Reasoning, ToolCalls: out.ToolCalls}}, nil)
		}
		resp.TokensUsed += promptTokens + outputTokens
		transcript.Tokens += promptTokens + outputTokens
		transcript.Steps = step

		if !out.WantsTools() {
			messages = append(messages, llm.Message{Role: "assistant", Content: out.Content})
			// A response cut off at the output budget is not a decision to
			// stop. A reasoning model reaches this by spending the whole
			// budget thinking: FinishReason is "length", Content is empty and
			// the thinking is in Reasoning. Reporting that as "the model
			// stopped" would attribute a harness limit to the model.
			if out.FinishReason == "length" && strings.TrimSpace(out.Content) == "" {
				resp.Truncated = true
				resp.Summary = fmt.Sprintf(
					"the output budget of %d tokens ran out on step %d before the model "+
						"produced an answer or a tool call", outputLimit, step)
				if n := len(strings.TrimSpace(out.Reasoning)); n > 0 {
					resp.Summary += fmt.Sprintf("; it was spent on %d characters of reasoning, "+
						"so the budget is too small for this model's thinking", n)
				}
				return finish()
			}
			// No tool call means the model has nothing further to do. §10.1
			// rejects reflection without new evidence, so prodding it to
			// continue would be spending tokens on nothing.
			resp.Summary = strings.TrimSpace(out.Content)
			if resp.Summary == "" {
				resp.Summary = fmt.Sprintf("the model stopped after %d step(s) without calling a tool", step)
			}
			return finish()
		}

		if len(out.ToolCalls) > 1 {
			for _, call := range out.ToolCalls {
				if call.Name == ToolDone {
					return resp, fmt.Errorf("native: done must be the only call in its batch")
				}
			}
		}
		messages = append(messages, llm.Message{
			Role: "assistant", Content: out.Content, ToolCalls: out.ToolCalls,
		})

		learnedThisStep := false
		for callIndex, call := range out.ToolCalls {
			if transcript.Tools >= 40 || (call.Name == ToolRunRecipe && transcript.VerifyRuns >= 3) || transcript.InvalidCalls >= 3 {
				resp.BudgetExhausted = true
				resp.Summary = "EDIT tool, verification or invalid-call budget exhausted"
				for _, unused := range out.ToolCalls[callIndex:] {
					messages = append(messages, llm.Message{Role: "tool", ToolCallID: unused.ID, Name: unused.Name, Content: "Not executed: EDIT phase budget exhausted."})
				}
				transcript.Pending = false
				return finish()
			}
			transcript.Pending = true
			if err := persist(); err != nil {
				return resp, err
			}
			start := time.Now()
			res, err := e.exec(ctx, req, call)
			if err != nil {
				return resp, fmt.Errorf("native: journal tool %s: %w", call.Name, err)
			}
			e.logf("  %s%s (%s)", call.Name, failMark(res), time.Since(start).Round(time.Millisecond))

			// An edit changes the worktree, so the step produced something
			// whatever the call's answer was.
			v, times := prog.observe(call.Name, string(call.Arguments), res.Content)
			if v == learned || res.Edited || res.Done {
				learnedThisStep = true
			}
			// The supervisor's warning goes outside the fence: it is this
			// program speaking to the model, not content read out of the
			// repository, and fencing it would tell the model to treat its own
			// supervisor as data.
			var prefix string
			if v == repeated && times > 1 {
				prefix = repeatNote(call.Name, times)
			}

			// A tool result is repository content by another route: read_file
			// returns a file, search_code returns matching lines, git_log
			// returns commit messages someone else wrote. Fencing only the
			// packet would leave the larger channel open, and it is the one
			// the model reads most.
			messages = append(messages, llm.Message{
				Role: "tool", ToolCallID: call.ID, Name: call.Name,
				Content: prefix + e.ensureFence().Wrap("result of "+call.Name, res.Content),
			})
			// Keep the whole batch pending until every advertised call has a
			// result. A crash between calls must not send an orphaned batch.
			transcript.Pending = callIndex < len(out.ToolCalls)-1
			transcript.Tools++
			if call.Name == ToolRunRecipe {
				transcript.VerifyRuns++
			}
			if res.Invalid {
				transcript.InvalidCalls++
			}
			if res.Edited {
				resp.Edited = true
			}
			if res.Done {
				resp.Summary = res.Summary
				resp.ClaimsDone = true
				return finish()
			}
			if err := persist(); err != nil {
				return resp, err
			}
		}

		// Ending here rather than at the step limit is the point: a loop that
		// has stopped learning will not start again, and the budget is better
		// spent telling the operator what it kept doing. The attempt still
		// carries whatever edits it made, so verification judges the work
		// rather than the loop.
		prog.endOfStep(learnedThisStep)
		if stuck, why := prog.stuck(); stuck {
			resp.Summary = why
			e.logf("  loop guard: %s", why)
			return finish()
		}
	}

	resp.Summary = fmt.Sprintf("reached the step limit of %d without declaring completion", maxSteps)
	return finish()
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
	fmt.Fprintf(&b, "Write scope: %v (an empty scope permits no writes).\n", req.Access.WriteScope)
	// Stated once, above everything it governs. The objective is the only
	// instruction in this message; what follows it came out of the repository.
	b.WriteString("\n" + fence.Preamble() + "\n")
	if req.Plan != nil {
		fmt.Fprintf(&b, "\nValidated plan: %+v\n", *req.Plan)
	}
	if req.Attempt > 1 {
		fmt.Fprintf(&b, "\nThis is attempt %d. The previous attempt did not pass verification.\n", req.Attempt)
	}

	// Before the code, because these say why the work is being done and what
	// this repository has already learned — and because §8.2 wants the stable
	// part of the packet first, where the prompt cache can keep it.
	if req.Packet != nil {
		for _, note := range req.Packet.ProjectNotes {
			b.WriteString(fence.Wrap(fmt.Sprintf("memory file=%s repo=%s commit=%s source=%s", note.File, note.RepoID, note.Commit, note.Source), note.Text) + "\n")
		}
	}
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
