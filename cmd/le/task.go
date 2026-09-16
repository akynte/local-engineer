package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/broker"
	"github.com/akynte/local-engineer/internal/config"
	"github.com/akynte/local-engineer/internal/critic"
	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/engine/native"
	"github.com/akynte/local-engineer/internal/ledger"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/policy"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/retrieval"
	"github.com/akynte/local-engineer/internal/sandbox"
	"github.com/akynte/local-engineer/internal/store"
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
		newTaskCreateCmd(), newTaskRunCmd(), newTaskVerifyCmd())
	return cmd
}

// engineFor builds the editing engine from the configured provider.
//
// When no provider is reachable the verification-only engine is used instead,
// and the caller is told which it got. Silently falling back would let someone
// believe a model had looked at their code when nothing had.
func engineFor(cmd *cobra.Command, root *store.Root, st *store.Store) (engine.Engine, error) {
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
		Retriever: retrieval.New(st),
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
	cfg, _ := loadConfig(root)
	sb, report := selectSandbox(cmd.Context(), cfg)
	if sb == nil {
		return nil, fmt.Errorf("no sandbox runner is available; verification must not run unconfined")
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "sandbox: %s (%v)\n", report.Runner, report.Active)

	holder, _ := os.Hostname()
	r, err := task.NewRunner(st, eng, sb, fmt.Sprintf("%s/%d", holder, os.Getpid()))
	if err != nil {
		return nil, err
	}
	r.Logf = func(format string, args ...any) {
		fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", args...)
	}
	r.Broker = broker.New(st, policyFrom(cfg.Gates))

	// Repository-wide rules (§6.2). Loaded from the workspace being worked on,
	// not from the data directory: they travel with the repository, and a rule
	// about what may not be changed belongs beside the thing it protects.
	policies, err := loadRepoPolicies()
	if err != nil {
		return nil, fmt.Errorf("the repository's policies are invalid: %w", err)
	}
	if len(policies.Policies) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "policies: %d rule(s) protecting %d path pattern(s)\n",
			len(policies.Policies), len(policies.Paths()))
	}
	r.Policies = policies

	// §10.1's out-of-conversation calls. They need structured output, so a
	// provider that cannot constrain its answers simply does not get them —
	// DR-4 refuses rather than degrading, and a review parsed out of prose is
	// a review whose concerns are sometimes silently lost.
	if provider, err := reviewProvider(root); err == nil && provider != nil &&
		provider.Capabilities().StructuredOutput {
		r.Critic = &critic.Critic{
			Provider: provider, MaxTokens: 2048, Temperature: 0.1,
		}
		if profile := loadProfile(root, cfg); profile != nil {
			r.Critic.Thinking = profile.Thinking
			r.Critic.MaxTokens = profile.ReservedOutput
		}
	} else if err == nil && provider != nil {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"review and diagnosis are off: %s does not declare structured output\n", provider.Name())
	}

	dirs, err := st.TaskDirs()
	if err != nil {
		return nil, err
	}
	r.SandboxSpec = sandbox.Spec{
		ReadOnly: cfg.Sandbox.ReadOnlyPaths,
		TmpDir:   dirs.Tmp,
		Env: recipe.GoEnv(
			dirs.GoBuildCache,
			dirs.GoModCache,
			dirs.Tmp),
		// A test suite binds port 0 and connects to whatever the kernel
		// returns, so no allowlist can name those ports in advance. The range
		// holds no services, and TCPDeny below keeps it that way even if an
		// operator has moved one into it.
		AllowEphemeralTCP: true,
		TCPDeny:           servicePorts(cfg),
	}
	for _, port := range cfg.Sandbox.AllowedTCPConnect {
		r.SandboxSpec.TCPConnect = append(r.SandboxSpec.TCPConnect, uint16(port)) //nolint:gosec // operator-configured port
	}
	if cfg.Inference.Mode == config.ModeEmbedded {
		r.SandboxSpec.TCPConnect = append(r.SandboxSpec.TCPConnect, uint16(cfg.Inference.Port)) //nolint:gosec // operator-configured port
	}
	return r, nil
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

			eng, err := engineFor(cmd, root, st)
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

// loadRepoPolicies reads the repository's own rules. They live beside the code
// they protect rather than in the data directory: a rule about what may not be
// changed travels with the thing it protects, and a clone carries it.
func loadRepoPolicies() (policy.Set, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return policy.Set{}, err
	}
	ws, err := workspace.Open(cwd)
	if err != nil {
		// Outside a workspace there is no repository to have policies.
		return policy.Set{}, nil //nolint:nilerr // not being in a workspace is not a failure to load
	}
	return policy.Load(filepath.Join(ws.Root, "policies"))
}

// reviewProvider resolves the provider for the review role. §10.1's
// out-of-conversation calls use it, and routing them separately is what makes
// "model routing (stronger slow lane for diagnosis)" a configuration change
// rather than a code one.
//
// A missing providers.yaml is not an error here: review and diagnosis are
// additions, and a task that can still verify should still run.
func reviewProvider(root *store.Root) (llm.Provider, error) {
	cfg, err := loadConfig(root)
	if err != nil {
		return nil, err
	}
	f, err := llm.LoadProvidersFile(root.Layout().ConfigDir())
	if err != nil {
		return nil, nil //nolint:nilerr // no providers configured is not a fault; review is an addition
	}
	router, err := llm.NewRouter(f, cfg.Offline)
	if err != nil {
		return nil, err
	}
	return router.For(llm.RoleReview)
}

// servicePorts lists the ports this installation's own services listen on, so
// the ephemeral grant never opens one.
//
// The supervisor API is the one that matters: it serves every workspace's
// status and the dashboard, and a task reaching it would cross the boundary
// §6.2 draws. The inference port is listed too — when inference is embedded it
// is granted deliberately through TCPConnect, and a deliberate grant is a
// different thing from one a range happened to cover.
func servicePorts(cfg config.Config) []uint16 {
	var out []uint16
	add := func(p int) {
		if p > 0 && p <= 65535 {
			out = append(out, uint16(p)) //nolint:gosec // bounds checked above
		}
	}
	if _, portStr, err := net.SplitHostPort(cfg.API.Addr); err == nil {
		if p, err := strconv.Atoi(portStr); err == nil {
			add(p)
		}
	}
	add(cfg.Inference.Port)
	add(cfg.Egress.DepsPort)
	add(cfg.Egress.DocsPort)
	return out
}
