package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/policy"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/supervisor"
	"github.com/akynte/local-engineer/internal/task"
)

// registerSupervision adds the tools that put an editor's work under the same
// engineering controls a CLI task gets.
//
// The division of labour is forced by the protocol rather than chosen. An MCP
// server cannot ask a user a question: OpenCode declares only the `roots`
// capability, not `elicitation`, so there is no channel for one. The agent
// talking to the user is the only component that can pause and ask — so the
// agent edits, and this supervises.
//
// What that buys is the whole point: the work is journalled before it happens,
// judged afterwards by the completion contract rather than by the agent's
// account of itself, and every result is tied to the exact content hash it
// describes. An agent that says "done" and an agent that is done become
// distinguishable, which is the property the CLI has always had and an editor
// session never did.
func (s *Server) registerSupervision(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "le_task_start",
		Description: "Open a supervised task before changing code. Returns the paths this " +
			"repository protects and the checks that will judge the work. Call this first " +
			"when asked to implement, fix, refactor or change anything; then edit normally.",
	}, s.taskStart)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "le_verify",
		Description: "Run this repository's verification recipes in a sandbox against the " +
			"current working tree and apply the completion contract. Returns each check and " +
			"whether the work is accepted. Pass the task_id from le_task_start so the result " +
			"is recorded against the task. Call after editing, and again after fixing what it " +
			"reports. This, not your own judgement, decides whether a task is done.",
	}, s.verify)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "le_task_answer",
		Description: "Record a question you had to ask the user and the answer they gave. " +
			"Call this whenever the user resolves an ambiguity you could not infer from the " +
			"codebase — a business rule, an architectural choice, a limit. The answer becomes " +
			"part of this project's record instead of being lost with the conversation.",
	}, s.taskAnswer)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "le_task_finish",
		Description: "Close a supervised task and produce its final review: what was asked, " +
			"what the user decided, which files changed, what was verified, and the verdict. " +
			"Call after le_verify reports ACCEPTED. Show the review to the user.",
	}, s.taskFinish)
}

// ----------------------------------------------------------- le_task_answer

type answerIn struct {
	TaskID   string `json:"task_id" jsonschema:"the id le_task_start returned"`
	Question string `json:"question" jsonschema:"what you asked the user"`
	Answer   string `json:"answer" jsonschema:"what they said"`
	Path     string `json:"path,omitempty" jsonschema:"a subdirectory of the open repository; defaults to its root"`
}

// taskAnswer is the division the protocol forces, made useful.
//
// Local Engineer cannot ask the user anything — it has no channel, and the
// agent holding the conversation does. But an answer about this project is a
// decision, and a decision that lives only in a chat transcript is gone by the
// next session. The agent owns the asking; this owns the remembering.
func (s *Server) taskAnswer(ctx context.Context, _ *mcp.CallToolRequest, in answerIn) (*mcp.CallToolResult, any, error) {
	sess, err := s.resolve(ctx, in.Path)
	if err != nil {
		return fail("%v", err), nil, nil
	}
	defer sess.Close() //nolint:contextcheck // cleanup must not take the request context: a cancelled call would then skip closing the databases.

	if err := supervisor.RecordAnswer(ctx, sess.Store, in.TaskID, in.Question, in.Answer); err != nil {
		return fail("recording the decision: %v", err), nil, nil
	}
	return text("Recorded. It will appear in the task's final review and in its journal."), nil, nil
}

// ----------------------------------------------------------- le_task_finish

type finishIn struct {
	TaskID string `json:"task_id" jsonschema:"the id le_task_start returned"`
	Path   string `json:"path,omitempty" jsonschema:"a subdirectory of the open repository; defaults to its root"`
}

// taskFinish produces the review artifact.
//
// It is not an approval. The agent has already edited the working tree, so
// there is nothing left to withhold and a gate that blocked here would block
// nothing. What this adds is the part git cannot reconstruct: the objective,
// the decisions the user made along the way, and what the contract concluded.
//
// The verdict comes from the verification actually on record. A task finished
// without one is reported UNVERIFIED rather than fine, because an agent's own
// account of its work is exactly what the contract exists not to trust.
func (s *Server) taskFinish(ctx context.Context, _ *mcp.CallToolRequest, in finishIn) (*mcp.CallToolResult, *supervisor.Review, error) {
	sess, err := s.resolve(ctx, in.Path)
	if err != nil {
		return fail("%v", err), nil, nil
	}
	defer sess.Close() //nolint:contextcheck // cleanup must not take the request context: a cancelled call would then skip closing the databases.

	rev, err := supervisor.FinishTask(ctx, sess.Store, sess.Workspace.Root, in.TaskID)
	if err != nil {
		return fail("finishing the task: %v", err), nil, nil
	}
	return text(rev.Format()), &rev, nil
}

// ------------------------------------------------------------ le_task_start

type startIn struct {
	Objective string `json:"objective" jsonschema:"what the user asked for, in one sentence"`
	Path      string `json:"path,omitempty" jsonschema:"a subdirectory of the open repository; defaults to its root"`
}

type startOut struct {
	TaskID        string   `json:"task_id"`
	ProtectedPath []string `json:"protected_paths,omitempty"`
	Checks        []string `json:"checks"`
}

// taskStart opens the journal entry before the work, not after it.
//
// §7.1's order is the reason this is a separate call rather than something
// le_verify infers: intent is recorded before the side effect, so an
// interrupted session leaves a state that can be reconciled rather than
// guessed at.
func (s *Server) taskStart(ctx context.Context, _ *mcp.CallToolRequest, in startIn) (*mcp.CallToolResult, startOut, error) {
	if strings.TrimSpace(in.Objective) == "" {
		return fail("objective is required"), startOut{}, nil
	}
	sess, err := s.resolve(ctx, in.Path)
	if err != nil {
		return fail("%v", err), startOut{}, nil
	}
	defer sess.Close() //nolint:contextcheck // cleanup must not take the request context: a cancelled call would then skip closing the databases.

	t := task.Task{
		ID:           task.NewID("oc"),
		Title:        strings.TrimSpace(in.Objective),
		Kind:         "supervised",
		Verification: recipe.Standard,
		Budget:       task.Budget{MaxAttempts: 1, MaxWallTime: 30 * time.Minute},
	}
	if err := task.NewStore(sess.Store).Create(ctx, t); err != nil {
		return fail("opening the task: %v", err), startOut{}, nil
	}

	out := startOut{TaskID: t.ID}
	for _, k := range recipe.Required(recipe.Standard) {
		out.Checks = append(out.Checks, string(k))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Task %s opened for: %s\n\n", t.ID, t.Title)

	// The protected paths are the part the agent most needs before it edits.
	// Reporting them afterwards, as a rejection, wastes the work.
	if set, err := policy.Load(sess.Workspace.Root + "/policies"); err == nil && len(set.Policies) > 0 {
		out.ProtectedPath = set.Paths()
		b.WriteString("This repository protects these paths — do not change them:\n")
		for _, p := range out.ProtectedPath {
			fmt.Fprintf(&b, "  %s\n", p)
		}
		b.WriteString("\n")
	}
	b.WriteString("Edit normally. When you believe the work is complete, call le_verify — " +
		"it runs the checks in a sandbox and decides acceptance from the evidence, so " +
		"there is no need to assert that it works.\n")
	return text(b.String()), out, nil
}

// ----------------------------------------------------------------- le_verify

type verifyIn struct {
	TaskID string `json:"task_id,omitempty" jsonschema:"the id le_task_start returned, so the result is recorded against that task"`
	Level  string `json:"level,omitempty" jsonschema:"low, standard or high. Defaults to standard"`
	Path   string `json:"path,omitempty" jsonschema:"a subdirectory of the open repository; defaults to its root"`
}

type verifyOut struct {
	Accepted   bool     `json:"accepted"`
	Candidate  string   `json:"candidate"`
	Reasons    []string `json:"reasons,omitempty"`
	OutOfScope []string `json:"out_of_scope,omitempty"`
}

// verify puts the working tree under the completion contract.
//
// It is the same path `le task verify` takes, through the same assembly in
// internal/supervisor, which is why the runner had to leave cmd/le: a second
// construction that forgot the sandbox would still compile and would run the
// repository's test suite unconfined.
//
// The call is slow — minutes on a real repository — and MCP calls are bounded
// by the client's timeout. `le opencode setup` writes a generous one for this
// reason, and a client that times out anyway loses the answer rather than the
// work: the journal and the evidence are already written.
func (s *Server) verify(ctx context.Context, _ *mcp.CallToolRequest, in verifyIn) (*mcp.CallToolResult, verifyOut, error) {
	level := recipe.Standard
	if in.Level != "" {
		parsed, ok := recipe.ParseLevel(in.Level)
		if !ok {
			return fail("level must be low, standard or high (got %q)", in.Level), verifyOut{}, nil
		}
		level = parsed
	}
	sess, err := s.resolve(ctx, in.Path)
	if err != nil {
		return fail("%v", err), verifyOut{}, nil
	}
	defer sess.Close() //nolint:contextcheck // cleanup must not take the request context: a cancelled call would then skip closing the databases.

	t := task.Task{
		ID: task.NewID("ocverify"), Title: "verify " + sess.Workspace.Name(),
		Kind: "verification", Verification: level,
		Budget: task.Budget{MaxAttempts: 1, MaxWallTime: 30 * time.Minute},
	}
	if err := task.NewStore(sess.Store).Create(ctx, t); err != nil {
		return fail("opening the verification task: %v", err), verifyOut{}, nil
	}

	// Discard progress: stdout carries the protocol, and this runs inside a
	// tool call where there is nowhere to stream it.
	r, err := supervisor.Runner(ctx, sess.Root, sess.Store, engine.Verify{}, supervisor.Options{
		RepoRoot: sess.Workspace.Root,
	})
	if err != nil {
		return fail("%v", err), verifyOut{}, nil
	}
	// Judge what the developer is actually looking at, not the last commit.
	r.SyncUncommitted = true

	outcome, err := r.Run(ctx, t.ID, sess.Workspace.Root)
	if err != nil {
		return fail("verification could not run: %v", err), verifyOut{}, nil
	}
	// Attach the result to the supervised task, so the final review reports
	// what was actually checked rather than what the agent says it checked.
	// Without this the chain breaks silently: every task would finish
	// UNVERIFIED however many times it had passed.
	if in.TaskID != "" {
		if err := supervisor.RecordVerification(ctx, sess.Store, in.TaskID, outcome); err != nil {
			return fail("recording the verification against %s: %v", in.TaskID, err), verifyOut{}, nil
		}
	}
	return text(renderOutcome(outcome)), verifyOut{
		Accepted:   outcome.Accepted,
		Candidate:  outcome.Candidate,
		Reasons:    outcome.Reasons,
		OutOfScope: outcome.OutOfScope,
	}, nil
}

func renderOutcome(o *task.Outcome) string {
	var b strings.Builder
	verdict := "NOT ACCEPTED"
	if o.Accepted {
		verdict = "ACCEPTED"
	}
	fmt.Fprintf(&b, "%s — candidate %s\n\n", verdict, short(o.Candidate))
	for _, res := range o.Results {
		fmt.Fprintf(&b, "  %-16s %-9s %s\n", res.Recipe, res.Status, res.Summary.Headline)
		// The findings are the part an agent can act on. A headline says a test
		// failed; a finding says which one and where.
		for _, f := range res.Summary.Findings {
			if f.File != "" {
				fmt.Fprintf(&b, "      %s:%d  %s\n", f.File, f.Line, f.Message)
				continue
			}
			fmt.Fprintf(&b, "      %s\n", f.Message)
		}
	}
	if len(o.OutOfScope) > 0 {
		b.WriteString("\nChanged outside the declared scope:\n")
		for _, p := range o.OutOfScope {
			fmt.Fprintf(&b, "  %s\n", p)
		}
	}
	if len(o.Reasons) > 0 {
		b.WriteString("\n")
		for _, r := range o.Reasons {
			fmt.Fprintf(&b, "%s\n", r)
		}
	}
	if !o.Accepted {
		b.WriteString("\nFix what failed above and call le_verify again. Do not report the " +
			"work as finished until this says ACCEPTED.\n")
	}
	return b.String()
}

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}
