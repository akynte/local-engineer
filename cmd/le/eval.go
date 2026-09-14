package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/eval"
	"github.com/akynte/local-engineer/internal/index"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/sandbox"
)

func newEvalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Measure the system against a task set",
		Long: "eval runs a task set through one or more configurations and reports how often\n" +
			"each solved the task.\n\n" +
			"Two properties make the numbers mean something. A task's acceptance tests are\n" +
			"never in the worktree while the task runs, so a model cannot satisfy a test it\n" +
			"can read. And the system's own verdict is recorded separately from the ground\n" +
			"truth, so \"claimed success and was wrong\" is its own number rather than\n" +
			"something averaged away.",
	}
	cmd.AddCommand(newEvalRunCmd(), newEvalTasksCmd(), newEvalArmsCmd(), newEvalReportCmd())
	return cmd
}

func newEvalTasksCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "tasks",
		Short: "List and validate the task set",
		RunE: func(cmd *cobra.Command, _ []string) error {
			tasks, err := eval.LoadSet(dir)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			leak := map[eval.LeakRisk]int{}
			for _, t := range tasks {
				fmt.Fprintf(w, "%-24s %-14s %-10s %s\n", t.ID, t.Category, t.LeakRisk,
					truncateHead(t.Objective, 56))
				leak[t.LeakRisk]++
			}
			fmt.Fprintf(w, "\n%d task(s). Provenance:", len(tasks))
			risks := make([]string, 0, len(leak))
			for r, n := range leak {
				risks = append(risks, fmt.Sprintf(" %s=%d", r, n))
			}
			sort.Strings(risks)
			fmt.Fprintf(w, "%s\n", strings.Join(risks, ""))
			if leak[eval.LeakPublic] > 0 || leak[eval.LeakUnknown] > 0 {
				fmt.Fprintln(w,
					"\nSome tasks may have been seen in training. Results over those are an\n"+
						"upper bound, and the report says so.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "tasks", "evals/tasks", "the task set directory")
	return cmd
}

func newEvalArmsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "arms",
		Short: "Describe the configurations being compared and what each isolates",
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			for _, a := range eval.Arms() {
				fmt.Fprintf(w, "%s\n", a.Name)
				for _, line := range wrapText(a.Description, 72) {
					fmt.Fprintf(w, "  %s\n", line)
				}
				fmt.Fprintln(w)
			}
			fmt.Fprintln(w, "Questions this set is designed to answer:")
			for _, c := range eval.Comparisons() {
				fmt.Fprintf(w, "\n  %s\n    %s vs %s — isolates %s\n",
					c.Question, c.Baseline, c.Variant, c.WhatItIsolates)
			}
			return nil
		},
	}
}

func newEvalRunCmd() *cobra.Command {
	var (
		dir     string
		armList []string
		only    []string
		out     string
		asJSON  bool
		repeat  int
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the task set and report the results",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			_, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			tasks, err := eval.LoadSet(dir)
			if err != nil {
				return err
			}
			if len(only) > 0 {
				tasks = filterTasks(tasks, only)
				if len(tasks) == 0 {
					return fmt.Errorf("no task matched %v", only)
				}
			}

			arms := make([]eval.Arm, 0, len(armList))
			for _, name := range armList {
				a, err := eval.ArmByName(name)
				if err != nil {
					return err
				}
				arms = append(arms, a)
			}

			cfg, err := loadConfig(root)
			if err != nil {
				return err
			}
			providers, err := llm.LoadProvidersFile(root.Layout().ConfigDir())
			if err != nil {
				return fmt.Errorf("evaluation needs a model: run `le config init` and configure a provider (%w)", err)
			}
			router, err := llm.NewRouter(providers, cfg.Offline)
			if err != nil {
				return err
			}
			defer router.Close()

			sb, report := selectSandbox(ctx, cfg)
			if sb == nil {
				return fmt.Errorf("no sandbox runner is available; evaluation must not run unconfined")
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "sandbox: %s (%v)\n", report.Runner, report.Active)

			dirs, err := st.TaskDirs()
			if err != nil {
				return err
			}
			profile := loadProfile(root, cfg)
			solver := &eval.SystemSolver{
				Root: root, Store: st, Router: router, Sandbox: sb,
				// The same analyzers and index settings `le index` uses, so a
				// task copy is indexed the way a real repository would be.
				Analyzers: analyzers(cmd),
				IndexOptions: index.Options{
					MaxFileBytes: cfg.Index.MaxFileBytes,
					Excludes:     cfg.Index.Excludes,
					ChunkLines:   cfg.Index.ChunkLines,
				},
				SandboxSpec: sandbox.Spec{
					ReadOnly: cfg.Sandbox.ReadOnlyPaths,
					TmpDir:   dirs.Tmp,
					Env:      recipe.GoEnv(dirs.GoBuildCache, dirs.GoModCache, dirs.Tmp),
				},
				Logf: func(f string, a ...any) { fmt.Fprintf(cmd.ErrOrStderr(), f+"\n", a...) },
			}
			if profile != nil {
				solver.MaxTools = profile.ToolSurfaceMax
				solver.MaxTokens = profile.ReservedOutput
				solver.Temperature = profile.Sampling.Temperature
				solver.Thinking = profile.Thinking
			}

			runner := &eval.Runner{
				WorkDir: filepath.Join(dirs.Tmp, "eval"),
				Logf:    func(f string, a ...any) { fmt.Fprintf(cmd.ErrOrStderr(), f+"\n", a...) },
			}

			if repeat < 1 {
				repeat = 1
			}
			total := len(tasks) * len(arms) * repeat
			fmt.Fprintf(cmd.ErrOrStderr(), "running %d task(s) across %d arm(s)", len(tasks), len(arms))
			if repeat > 1 {
				fmt.Fprintf(cmd.ErrOrStderr(), ", %d times each", repeat)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), " = %d runs\n\n", total)

			var outcomes []eval.Outcome
			done := 0
			start := time.Now()
			// Repetitions are the outer loop so that an interrupted run still
			// covers every cell the same number of times, rather than leaving
			// the last arm with fewer samples than the first.
			for rep := 1; rep <= repeat; rep++ {
				for _, arm := range arms {
					for _, task := range tasks {
						done++
						fmt.Fprintf(cmd.ErrOrStderr(), "[%d/%d] %s / %s", done, total, arm.Name, task.ID)
						if repeat > 1 {
							fmt.Fprintf(cmd.ErrOrStderr(), " (pass %d/%d)", rep, repeat)
						}
						fmt.Fprint(cmd.ErrOrStderr(), "… ")
						o := runner.Run(ctx, task, arm, solver)
						o.Repetition = rep
						outcomes = append(outcomes, o)
						fmt.Fprintf(cmd.ErrOrStderr(), "%s (%s)\n", verdict(o), o.Duration.Round(time.Second))
						if ctx.Err() != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "\ninterrupted after %d run(s)\n", done)
							break
						}
					}
				}
			}

			rep := eval.Aggregate(outcomes, tasks)
			fmt.Fprintf(cmd.ErrOrStderr(), "\ncompleted in %s\n\n", time.Since(start).Round(time.Second))

			if out != "" {
				if err := rep.Save(out); err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s\n", out)
			}
			if asJSON {
				return emitJSON(rep)
			}
			fmt.Fprint(cmd.OutOrStdout(), rep.Format())
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "tasks", "evals/tasks", "the task set directory")
	cmd.Flags().StringSliceVar(&armList, "arms", []string{"unsupervised", "supervised"},
		"configurations to compare; `le eval arms` describes them")
	cmd.Flags().StringSliceVar(&only, "task", nil, "run only these task ids")
	cmd.Flags().IntVar(&repeat, "repeat", 1,
		"run the whole set this many times; one run of a cell is a sample, not a measurement")
	cmd.Flags().StringVar(&out, "out", "", "write the report as JSON to this path")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	return cmd
}

func newEvalReportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "report <results.json>",
		Short: "Render a saved result file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rep, err := eval.LoadReport(args[0])
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), rep.Format())
			return nil
		},
	}
}

func verdict(o eval.Outcome) string {
	switch {
	case o.Errored():
		return "ERROR"
	case o.Tampered:
		return "tampered"
	case o.FalseAccept:
		return "FALSE ACCEPT"
	case o.Solved:
		return "solved"
	case o.MissedSuccess:
		return "solved (unclaimed)"
	default:
		return "not solved"
	}
}

func filterTasks(tasks []eval.Task, ids []string) []eval.Task {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []eval.Task
	for _, t := range tasks {
		if want[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

func wrapText(s string, width int) []string {
	var lines []string
	var cur string
	for _, word := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = word
		case len(cur)+1+len(word) > width:
			lines = append(lines, cur)
			cur = word
		default:
			cur += " " + word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
