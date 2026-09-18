package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/eval"
)

// newEvalQualifyCmd answers one question: is there enough evidence yet to stop
// saying the system is unproven?
//
// It is deliberately hard to pass and it reads only artefacts. Every gate is
// derived from task files and saved runs on disk, so the command cannot be
// talked into a pass — the corresponding work has to have been done and left a
// trace. A FAIL is the expected and useful output for most of a project's life:
// it says exactly which piece of evidence is missing next.
func newEvalQualifyCmd() *cobra.Command {
	var (
		tasksDir   string
		asJSON     bool
		reproduced string
		evaluation string
		agrees     bool
	)
	cmd := &cobra.Command{
		Use:   "qualify <results.json...>",
		Short: "Report whether the evidence yet supports dropping the \"unproven\" limitation",
		Long: "qualify checks the saved evidence against the bar this project set for itself\n" +
			"before any of it was measured.\n\n" +
			"It does not decide whether the system is good. It decides whether anyone is\n" +
			"entitled to an opinion yet: enough held-out tasks, repeats on the finalists,\n" +
			"acceptance tests the agent never saw, a baseline to compare against, ablations\n" +
			"for the components that are claimed to matter, false acceptance actually\n" +
			"measured, paired statistics, raw runs kept, the environment recorded, and an\n" +
			"independent reproduction that agrees.\n\n" +
			"A failing gate is not a bug. It is the next piece of work.",
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			outcomes, err := eval.LoadOutcomes(args...)
			if err != nil {
				return err
			}
			tasks, err := eval.LoadSet(tasksDir)
			if err != nil {
				return err
			}
			env := eval.CaptureEnvironment(cmd.Context(), 0)
			evidence := eval.EvidenceFrom(tasks, outcomes, env)
			evidence.EvaluationRunID = evaluation
			evidence.ReproductionRunID = reproduced
			evidence.ReproductionAgrees = agrees

			q := eval.Qualify(evidence, eval.DefaultThresholds())
			w := cmd.OutOrStdout()
			if asJSON {
				body, err := q.JSON()
				if err != nil {
					return err
				}
				fmt.Fprintln(w, string(body))
				return nil
			}

			fmt.Fprintf(w, "Qualification: %s\n\n", strings.ToUpper(q.Status))
			for _, g := range q.Gates {
				mark := "FAIL"
				if g.Met {
					mark = "ok  "
				}
				fmt.Fprintf(w, "%s  %-26s  required %-22s actual %s\n", mark, g.Name, g.Required, g.Actual)
			}
			fmt.Fprintf(w, "\n%s\n", q.Recommendation)
			for _, g := range q.Gates {
				if !g.Met && g.Why != "" {
					fmt.Fprintf(w, "\n%s:\n  %s\n", g.Name, g.Why)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&tasksDir, "tasks", "evals/tasks", "the task set the results were produced from")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the qualification as JSON")
	cmd.Flags().StringVar(&evaluation, "evaluation-run", "", "the run id of the evaluation being qualified")
	cmd.Flags().StringVar(&reproduced, "reproduction-run", "",
		"the run id of an independent reproduction from a clean checkout")
	cmd.Flags().BoolVar(&agrees, "reproduction-agrees", false,
		"assert that the reproduction's headline numbers match within their intervals")
	return cmd
}
