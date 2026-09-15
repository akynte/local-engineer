package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/recipe"
)

// newVerifyDeclaredCmd runs one repository-declared verification step.
//
// It is hidden because it is not an interface: it is how a recipe expresses a
// sequence. Neither declared kind is a single command — an integration step is
// up-then-test-then-down with the teardown guaranteed, and a generate check is
// snapshot-run-compare-restore — and a recipe is argv, never a shell string,
// because a shell inside the sandbox makes the argument boundary meaningless.
//
// It runs in the worktree, inside the sandbox the recipe runner built, with
// exactly the ports the declaration asked for.
func newVerifyDeclaredCmd() *cobra.Command {
	var kind, name, dir string
	c := &cobra.Command{
		Use:    "verify-declared",
		Short:  "Run one step from .le/verify.yaml (internal)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if kind == "" || name == "" {
				return fmt.Errorf("verify-declared needs --kind and --name")
			}
			worktree := dir
			if worktree == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				worktree = cwd
			}
			res, err := recipe.RunDeclared(cmd.Context(), worktree, kind, name)
			if err != nil {
				return err
			}
			if err := recipe.WriteStepResult(cmd.OutOrStdout(), res); err != nil {
				return err
			}
			// The exit code carries the verdict for anything that is not
			// reading the JSON, and the summarizer reads the JSON. A passing
			// step exits 0; everything else exits 1, and the JSON says whether
			// that was a failure of the code or of the check.
			if res.Status != string(recipe.Pass) {
				os.Exit(1)
			}
			return nil
		},
	}
	c.Flags().StringVar(&kind, "kind", "", "generate, integration or check")
	c.Flags().StringVar(&name, "name", "", "the step's name in .le/verify.yaml")
	c.Flags().StringVar(&dir, "dir", "", "worktree root (default: working directory)")
	return c
}
