package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/eval"
)

// newEvalResultsCmd generates the published results document from saved runs.
//
// It exists so that no number in the repository's benchmark documentation was
// ever typed by a person. A figure that a human transcribed is a figure that
// can drift from the run behind it — usually in the flattering direction, and
// usually without anyone noticing. Regenerating is cheap; checking a hand-typed
// table against a JSON file is not, so it does not get done.
func newEvalResultsCmd() *cobra.Command {
	var (
		tasksDir   string
		outDir     string
		provenance string
		seed       int64
	)
	cmd := &cobra.Command{
		Use:   "results <results.json...>",
		Short: "Generate summary.json and RESULTS.md from saved runs",
		Long: "results reads saved run files and writes the published summary.\n\n" +
			"Everything in the output is computed here: per-arm rates with their intervals,\n" +
			"paired comparisons as clustered bootstraps, the component ladder with its\n" +
			"unmeasured rungs named as unmeasured, the retention decision for each measured\n" +
			"component, and the qualification gates.\n\n" +
			"The generated files carry a header saying not to edit them.",
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
			env := eval.CaptureEnvironment(cmd.Context(), seed)
			evidence := eval.EvidenceFrom(tasks, outcomes, env)
			q := eval.Qualify(evidence, eval.DefaultThresholds())
			results := eval.BuildResults(tasks, outcomes, env, q, seed)
			results.Provenance = provenance

			if outDir == "" {
				fmt.Fprint(cmd.OutOrStdout(), results.Markdown())
				return nil
			}
			if err := os.MkdirAll(outDir, 0o750); err != nil {
				return err
			}
			body, err := results.JSON()
			if err != nil {
				return err
			}
			summary := filepath.Join(outDir, "summary.json")
			if err := os.WriteFile(summary, append(body, '\n'), 0o600); err != nil {
				return err
			}
			document := filepath.Join(outDir, "RESULTS.md")
			if err := os.WriteFile(document, []byte(results.Markdown()), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\nwrote %s\n", summary, document)
			return nil
		},
	}
	cmd.Flags().StringVar(&tasksDir, "tasks", "evals/tasks", "the task set the results were produced from")
	cmd.Flags().StringVar(&outDir, "out", "", "write summary.json and RESULTS.md into this directory")
	cmd.Flags().StringVar(&provenance, "provenance", "",
		"a label rendered above every number, for example that these are pilot runs")
	cmd.Flags().Int64Var(&seed, "seed", 1, "the bootstrap seed, recorded in the output so a figure can be reproduced")
	return cmd
}
