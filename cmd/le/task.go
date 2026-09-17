package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/engine/native"
	"github.com/akynte/local-engineer/internal/ledger"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/memory"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/retrieval"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/supervisor"
	"github.com/akynte/local-engineer/internal/task"
	"github.com/akynte/local-engineer/internal/workspace"
)

func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Inspect the execution journal and recover interrupted tasks",
		Long: "Every model-visible action is journalled intent-first: the intent is written\n" +
			"before the side effect and the outcome after it. An operation with no outcome\n" +
			"is uncertain, and recovery classifies it by inspecting the worktree rather\n" +
			"than assuming either success or failure.",
	}
	cmd.AddCommand(newTaskListCmd(), newTaskJournalCmd(), newTaskRecoverCmd(),
		newTaskCreateCmd(), newTaskRunCmd(), newTaskVerifyCmd(), newTaskRetryCmd())
	return cmd
}

// engineFor builds the editing engine from the configured provider.
//
// When no provider is reachable the verification-only engine is used instead,
// and the caller is told which it got. Silently falling back would let someone
// believe a model had looked at their code when nothing had.
func engineFor(cmd *cobra.Command, root *store.Root, st *store.Store, ws *workspace.Workspace) (engine.Engine, error) {
	cfg, err := loadConfig(root)
	if err != nil {
		return nil, err
	}
	f, err := llm.LoadProvidersFile(root.Layout().ConfigDir())
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"no providers.yaml: running verification only. Run `le config init` and configure a model to make edits.\n")
		return engine.Verify{}, nil
	}
	router, err := llm.NewRouter(f, cfg.Offline)
	if err != nil {
		return nil, fmt.Errorf("providers.yaml is invalid: %w", err)
	}
	provider, err := router.For(llm.RoleCoding)
	if err != nil {
		return nil, err
	}

	probe, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
	defer cancel()
	if err := provider.Health(probe); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"provider %q is not reachable (%v): running verification only.\n", provider.Name(), err)
		return engine.Verify{}, nil
	}

	profile := loadProfile(root, cfg)
	opts := native.Options{
		Provider:  provider,
		Retriever: retrieverFor(st, ws),
		Graph:     graphFor(st),
		Logf:      func(f string, a ...any) { fmt.Fprintf(cmd.ErrOrStderr(), f+"\n", a...) },
	}
	// §9.3: none of these are hardcoded. They come from the active profile,
	// and the fallback below is the shipped default rather than a tuned value.
	if profile != nil {
		opts.MaxTools = profile.ToolSurfaceMax
		opts.MaxTokens = profile.ReservedOutput
		opts.Temperature = profile.Sampling.Temperature
		opts.Thinking = profile.Thinking
		opts.ContextTokens = profile.ContextTokens
		opts.MaxSteps = profile.MaxSteps
	}
	eng, err := native.New(opts)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"provider %q cannot drive edits (%v): running verification only.\n", provider.Name(), err)
		return engine.Verify{}, nil
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "engine: %s\n", eng.Name())
	return eng, nil
}

// runnerFor builds a task runner with the strongest available sandbox and the
// per-workspace caches, so a task's toolchain caches are never shared with
// another project's.
func runnerFor(cmd *cobra.Command, root *store.Root, st *store.Store, eng engine.Engine) (*task.Runner, error) {
	// The assembly lives in internal/supervisor so that the CLI and the MCP
	// adapter cannot end up with two different sets of controls. See that
	// package for why a second one would be dangerous rather than merely
	// duplicated.
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	repoRoot := cwd
	if ws, err := workspace.Open(cwd); err == nil {
		repoRoot = ws.Root
	}
	warn := func(format string, args ...any) {
		fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", args...)
	}
	return supervisor.Runner(cmd.Context(), root, st, eng, supervisor.Options{
		RepoRoot: repoRoot, Logf: warn, Warnf: warn,
	})
}

func newTaskCreateCmd() *cobra.Command {
	var title, verify, requirement string
	var scope []string
	var attempts int

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a task",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			_, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			level, ok := recipe.ParseLevel(verify)
			if !ok {
				return fmt.Errorf("--verify must be one of low, standard, high (got %q)", verify)
			}
			if title == "" {
				return fmt.Errorf("--title is required")
			}

			t := task.Task{
				ID: task.NewID("t"), Title: title, RequirementID: requirement,
				Verification: level,
				Budget: task.Budget{
					MaxAttempts: attempts, MaxWallTime: 30 * time.Minute, Scope: scope,
				},
			}
			if err := task.NewStore(st).Create(ctx, t); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n", t.ID)
			fmt.Fprintf(cmd.ErrOrStderr(), "created: %s\nRun it with: le task run %s\n", t.Summary(), t.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "what the task is for (required)")
	cmd.Flags().StringVar(&verify, "verify", "standard", "verification level: low, standard, high")
	cmd.Flags().StringVar(&requirement, "requirement", "", "requirement id this task serves")
	cmd.Flags().StringSliceVar(&scope, "scope", nil,
		"path prefixes the task may change; a change outside them blocks acceptance")
	cmd.Flags().IntVar(&attempts, "attempts", 3, "how many engine attempts are allowed")
	return cmd
}

func newTaskRunCmd() *cobra.Command {
	var asJSON bool
	var showDiff bool

	cmd := &cobra.Command{
		Use:   "run <task-id>",
		Short: "Run a task to a terminal state",
		Long: "run gives the task its own git worktree, journals every action intent-first,\n" +
			"runs the verification recipes its level demands, and decides acceptance from\n" +
			"the evidence.\n\n" +
			"The engine's claim that it finished is an input to that decision, never the\n" +
			"decision itself. Your working copy is never touched: the task edits a worktree.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ws, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			eng, err := engineFor(cmd, root, st, ws)
			if err != nil {
				return err
			}
			defer eng.Close()

			r, err := runnerFor(cmd, root, st, eng)
			if err != nil {
				return err
			}

			repo := ws.Root
			if len(ws.Manifest.Repositories) > 0 {
				if p, err := ws.RepositoryPath(ws.Manifest.Repositories[0].ID); err == nil {
					repo = p
				}
			}

			out, err := r.Run(ctx, args[0], repo)
			if err != nil {
				return err
			}
			if asJSON {
				return emitJSON(out)
			}
			return printOutcome(cmd, out, showDiff)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	cmd.Flags().BoolVar(&showDiff, "diff", false, "print the diff the task produced")
	return cmd
}

func newTaskVerifyCmd() *cobra.Command {
	var verify string
	var asJSON, committed bool

	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Put the current worktree under the completion contract",
		Long: "verify creates a task, runs the verification recipes in a sandbox against a\n" +
			"fresh worktree, records every result as evidence tied to the exact content\n" +
			"hash it describes, and reports whether the completion contract is met.\n\n" +
			"It is how a change you made by hand gets the same treatment as one the\n" +
			"system made.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			ws, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			level, ok := recipe.ParseLevel(verify)
			if !ok {
				return fmt.Errorf("--verify must be one of low, standard, high (got %q)", verify)
			}
			t := task.Task{
				ID: task.NewID("verify"), Title: "verify " + ws.Name(), Verification: level,
				Kind: "verification", Budget: task.Budget{MaxAttempts: 1, MaxWallTime: 30 * time.Minute},
			}
			if err := task.NewStore(st).Create(ctx, t); err != nil {
				return err
			}
			r, err := runnerFor(cmd, root, st, engine.Verify{})
			if err != nil {
				return err
			}
			// Verify what the operator is actually looking at, not the last
			// commit. --committed opts into the other meaning explicitly.
			r.SyncUncommitted = !committed

			out, err := r.Run(ctx, t.ID, ws.Root)
			if err != nil {
				return err
			}
			if asJSON {
				return emitJSON(out)
			}
			if err := printOutcome(cmd, out, false); err != nil {
				return err
			}
			if !out.Accepted {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&verify, "verify", "standard", "verification level: low, standard, high")
	cmd.Flags().BoolVar(&committed, "committed", false,
		"verify the last commit instead of your uncommitted working tree")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func printOutcome(cmd *cobra.Command, out *task.Outcome, showDiff bool) error {
	w := cmd.OutOrStdout()
	verdict := "NOT ACCEPTED"
	if out.Accepted {
		verdict = "ACCEPTED"
	}
	fmt.Fprintf(w, "\n%s  %s (%d attempt(s), candidate %s)\n",
		verdict, out.Task.ID, out.Attempts, short(out.Candidate))
	if out.Verified != "" {
		fmt.Fprintf(w, "%s\n", out.Verified)
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nRECIPE\tKIND\tSTATUS\tSUMMARY")
	for _, res := range out.Results {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", res.Recipe, res.Kind, res.Status, res.Summary.Headline)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	for _, res := range out.Results {
		if len(res.Summary.Findings) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s:\n", res.Recipe)
		for _, f := range res.Summary.Findings {
			loc := f.File
			if f.Line > 0 {
				loc = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			if f.Test != "" {
				loc = strings.TrimSpace(loc + " " + f.Test)
			}
			fmt.Fprintf(w, "  %-40s %s\n", loc, f.Message)
		}
		if res.Summary.Truncated {
			fmt.Fprintf(w, "  … more findings omitted; full output: artifact %s\n", short(res.ArtifactHash))
		}
	}

	fmt.Fprintln(w, "\nWhy:")
	for _, r := range out.Reasons {
		fmt.Fprintf(w, "  - %s\n", r)
	}
	if out.Branch != "" && strings.TrimSpace(out.Diff) != "" {
		fmt.Fprintf(w, "\nThe change is on branch %s.\n", out.Branch)
	}
	if out.Gate != nil && out.Gate.Open() {
		fmt.Fprintf(w, "Answer the gate with:  le gate show %s\n", out.Gate.ID)
	}
	if showDiff && out.Diff != "" {
		fmt.Fprintf(w, "\n--- diff ---\n%s\n", out.Diff)
	}
	return nil
}

func newTaskListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List tasks in this workspace",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			_, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			rows, err := st.Ledger().SQL().QueryContext(ctx,
				`SELECT id, title, state, verification, COALESCE(worktree_id,'') FROM tasks ORDER BY created_at DESC`)
			if err != nil {
				return err
			}
			defer rows.Close()

			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATE\tVERIFY\tWORKTREE\tTITLE")
			n := 0
			for rows.Next() {
				var id, title, state, verify, wt string
				if err := rows.Scan(&id, &title, &state, &verify, &wt); err != nil {
					return err
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", id, state, verify, wt, title)
				n++
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if n == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No tasks in this workspace.")
				return nil
			}
			return tw.Flush()
		},
	}
}

func newTaskJournalCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "journal <task-id>",
		Short: "Print a task's operation journal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			ops, err := ledger.New(st).Operations(ctx, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return emitJSON(ops)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "SEQ\tKIND\tOUTCOME\tEVIDENCE")
			for _, op := range ops {
				outcome := "recorded"
				if op.Uncertain() {
					outcome = "UNCERTAIN"
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", op.Seq, op.Kind, outcome, op.EvidenceID)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func newTaskRecoverCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Reconcile every non-terminal task and report the resumable state",
		Long: "recover runs the recovery procedure: reconcile the worktree against the last\n" +
			"completed operation, classify every uncertain operation by inspection,\n" +
			"reconstruct the working state from the last checkpoint plus completed\n" +
			"operations, mark evidence produced against an older candidate as stale, and\n" +
			"name the next action.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			ws, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			states, err := ledger.New(st).Recover(ctx, func(taskID string) string {
				// Until per-task worktrees exist, a task's candidate is the
				// repository root itself.
				return filepath.Join(ws.Root)
			})
			if err != nil {
				return err
			}
			if asJSON {
				return emitJSON(states)
			}
			out := cmd.OutOrStdout()
			if len(states) == 0 {
				fmt.Fprintln(out, "No non-terminal tasks: nothing to recover.")
				return nil
			}
			for _, s := range states {
				fmt.Fprintf(out, "task %s — %s\n", s.TaskID, s.Objective)
				fmt.Fprintf(out, "  candidate:   %s\n", short(s.CurrentCandidate))
				fmt.Fprintf(out, "  drifted:     %v\n", s.Drifted)
				fmt.Fprintf(out, "  uncertain:   %d operation(s)\n", len(s.Uncertain))
				for _, u := range s.Uncertain {
					fmt.Fprintf(out, "    seq %d %s → %s (%s)\n",
						u.Operation.Seq, u.Operation.Kind, u.Applied, u.Detail)
				}
				stale := 0
				for _, v := range s.Validations {
					if v.Stale {
						stale++
					}
				}
				fmt.Fprintf(out, "  validations: %d, %d stale\n", len(s.Validations), stale)
				fmt.Fprintf(out, "  safe:        %v\n", s.SafeToResume())
				fmt.Fprintf(out, "  next:        %s\n\n", s.NextAction)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	if hash == "" {
		return "(none)"
	}
	return hash
}

func newTaskRetryCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "retry <task-id>",
		Short: "Return a failed task to pending so it can be run again",
		Long: "A failed task is not always work that could not be done. An output budget too\n" +
			"small for the model's reasoning, a request longer than the provider's timeout,\n" +
			"or a machine under memory pressure all produce a failed task whose work was\n" +
			"never really attempted — and fixing the cause does not help, because the task\n" +
			"refuses to run. Retry returns it to pending, keeping its id, its journal and\n" +
			"its worktree, so the record of what was already tried survives.\n\n" +
			"An accepted task is refused: its change has been through the completion\n" +
			"contract and may already be merged.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			store := task.NewStore(st)
			before, err := store.Get(ctx, args[0])
			if err != nil {
				return err
			}

			// Journalled before the transition, like every other decision that
			// changes what the system will do (§7.1). A retry with no record
			// leaves a task whose history says it failed and whose state says
			// otherwise.
			by := os.Getenv("USER")
			if by == "" {
				by = "operator"
			}
			h, err := ledger.New(st).Begin(ctx, before.ID, ledger.KindDecision, map[string]any{
				"decision": "retry", "from_state": string(before.State),
				"by": by, "reason": reason,
			}, "")
			if err != nil {
				return err
			}

			t, err := store.Reopen(ctx, args[0])
			if err != nil {
				// The decision was recorded and did not take effect; say so
				// rather than leaving an open operation implying it did.
				_ = h.Fail(ctx, err)
				return err
			}
			if err := h.Complete(ctx, map[string]any{"state": string(t.State)}, "", ""); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s -> %s\n", t.ID, before.State, t.State)
			fmt.Fprintf(cmd.ErrOrStderr(),
				"\nThe journal and worktree are kept. Run it with: le task run %s\n", t.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "",
		"what you changed so this run goes differently. Recorded in the journal")
	return cmd
}

// retrieverFor builds the retriever, attaching the repository's durable notes
// when there is a checkout to read them from.
//
// The notes live under `.le/memory/` inside the repository (§2.2) so they
// travel with it, which is why this needs the workspace: a store knows a
// workspace id, not where the code is.
func retrieverFor(st *store.Store, ws *workspace.Workspace) *retrieval.Retriever {
	r := retrieval.New(st)
	if ws == nil {
		return r
	}
	return r.WithMemory(memory.Open(ws.Root, memory.DefaultCaps()))
}
