package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/broker"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/retrieval"
	"github.com/akynte/local-engineer/internal/task"
)

func newPlanCmd() *cobra.Command {
	var asJSON, apply bool
	var maxSteps int

	cmd := &cobra.Command{
		Use:   "plan <requirement>",
		Short: "Decompose a requirement into bounded child tasks",
		Long: "plan breaks one requirement into independent, verifiable steps, each with the\n" +
			"narrowest scope that contains its change.\n\n" +
			"The plan is produced by a model but is not trusted by one. A step that names a\n" +
			"scope escaping the repository, declares no scope at all, or depends on a step\n" +
			"that does not come before it is rejected here — before any of it runs — because\n" +
			"those are the failures that turn a long task into a long mess.\n\n" +
			"Nothing is created without --apply.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			cfg, err := loadConfig(root)
			if err != nil {
				return err
			}
			f, err := llm.LoadProvidersFile(root.Layout().ConfigDir())
			if err != nil {
				return fmt.Errorf("planning needs a model: run `le config init` and configure a provider (%w)", err)
			}
			router, err := llm.NewRouter(f, cfg.Offline)
			if err != nil {
				return err
			}
			defer router.Close()

			provider, err := router.For(llm.RolePlanning)
			if err != nil {
				return err
			}
			probe, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if err := provider.Health(probe); err != nil {
				return fmt.Errorf("provider %q is not reachable: %w", provider.Name(), err)
			}

			title := strings.Join(args, " ")
			req := task.Requirement{ID: task.NewID("req"), Title: title, State: "open"}

			store := task.NewStore(st)
			if err := store.CreateRequirement(ctx, req); err != nil {
				return err
			}

			planner := &task.Planner{
				Provider: provider, Retriever: retrieval.New(st), Graph: graphFor(st),
				MaxSteps: maxSteps,
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "planning with %s…\n", provider.Name())
			plan, err := planner.Plan(ctx, req)
			if err != nil {
				return err
			}
			if asJSON {
				return emitJSON(plan)
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "%s\n\n", plan.Objective)
			if plan.Rationale != "" {
				fmt.Fprintf(w, "%s\n\n", plan.Rationale)
			}
			for i, s := range plan.Steps {
				fmt.Fprintf(w, "%d. %s\n", i+1, s.Title)
				fmt.Fprintf(w, "   scope: %s   verify: %s", strings.Join(s.Scope, ", "), s.Verification)
				if len(s.DependsOn) > 0 {
					fmt.Fprintf(w, "   after: %v", s.DependsOn)
				}
				fmt.Fprintln(w)
			}
			if plan.Impact != nil && len(plan.Impact.Consumers) > 0 {
				fmt.Fprintf(w, "\nImpact of touching this area: %s\n", plan.Impact.Summary())
			}

			if !apply {
				fmt.Fprintf(w, "\nNothing was created. Re-run with --apply to create these as tasks.\n")
				return nil
			}

			// The plan itself may be gated: a decomposition that runs without
			// review is a lot of automated change on one person's behalf.
			b := broker.New(st, policyFrom(cfg.Gates))
			gate, err := b.Ask(ctx, req.ID, broker.KindPlan,
				fmt.Sprintf("Execute this %d-step plan?", len(plan.Steps)),
				broker.Evidence{Summary: plan.Objective, Plan: plan, Impact: plan.Impact})
			if err != nil {
				return err
			}
			if gate.Open() {
				fmt.Fprintf(w, "\nWaiting at a gate: %s\n  le gate show %s\n", gate.Question, gate.ID)
				return nil
			}
			if gate.Decision == broker.Rejected {
				fmt.Fprintf(w, "\nPlan rejected: %s\n", gate.Note)
				return nil
			}

			children, err := store.Materialise(ctx, plan, "")
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "\nCreated %d task(s):\n", len(children))
			for _, c := range children {
				fmt.Fprintf(w, "  %s  %s\n", c.ID, c.Title)
			}
			fmt.Fprintf(w, "\nRun them in order with `le task run <id>`.\n")
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "create the tasks (otherwise the plan is only printed)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	cmd.Flags().IntVar(&maxSteps, "max-steps", task.DefaultMaxPlanSteps, "cap the number of steps")
	return cmd
}
