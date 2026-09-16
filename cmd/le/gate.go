package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/broker"
	"github.com/akynte/local-engineer/internal/store"
)

func newGateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Answer the decisions waiting for a person",
		Long: "A gate is a point where a decision leaves the system. Each one carries the\n" +
			"deterministic evidence for the decision — an impact report, a diff, the\n" +
			"verification findings — so answering it means reading what the supervisor\n" +
			"computed rather than trusting a summary of it.\n\n" +
			"Gates are journalled before they block, so an interrupted approval is a\n" +
			"pending gate on restart rather than a lost one.",
	}
	cmd.AddCommand(newGateListCmd(), newGateShowCmd(), newGateApproveCmd(), newGateRejectCmd())
	return cmd
}

// brokerFor builds a broker with the configured policy.
func brokerFor(root *store.Root, st *store.Store) *broker.Broker {
	cfg, _ := loadConfig(root)
	return broker.New(st, policyFrom(cfg.Gates))
}

func newGateListCmd() *cobra.Command {
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List gates waiting for a decision",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			_, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			b := brokerFor(root, st)
			gates, err := b.Pending(ctx)
			if err != nil {
				return err
			}
			if all {
				if gates, err = b.All(ctx); err != nil {
					return err
				}
			}
			if asJSON {
				return emitJSON(gates)
			}
			if len(gates) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No gates waiting.")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tKIND\tDECISION\tAGE\tQUESTION")
			for _, g := range gates {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", g.ID, g.Kind, g.Decision,
					time.Since(g.CreatedAt).Round(time.Second), truncateHead(g.Question, 60))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include gates that have been decided")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func newGateShowCmd() *cobra.Command {
	var asJSON, showDiff bool
	cmd := &cobra.Command{
		Use:   "show <gate-id>",
		Short: "Show a gate and the evidence behind it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			g, err := brokerFor(root, st).Get(ctx, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return emitJSON(g)
			}
			ev, err := g.Decoded()
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "%s\n\n%s\n\n", g.ID, g.Question)
			fmt.Fprintf(w, "task:     %s\nkind:     %s\ndecision: %s\nopened:   %s\n",
				g.TaskID, g.Kind, g.Decision, g.CreatedAt.Format(time.RFC3339))
			if g.DecidedAt != nil {
				fmt.Fprintf(w, "decided:  %s by %s\n", g.DecidedAt.Format(time.RFC3339), g.DecidedBy)
				if g.Note != "" {
					fmt.Fprintf(w, "note:     %s\n", g.Note)
				}
			}
			writeEvidence(w, ev, showDiff)
			if g.Open() {
				fmt.Fprintf(w, "\nAnswer with:\n  le gate approve %s --note \"...\"\n  le gate reject %s --note \"...\"\n",
					g.ID, g.ID)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	cmd.Flags().BoolVar(&showDiff, "diff", false, "print the full diff")
	return cmd
}

func newGateApproveCmd() *cobra.Command { return decideCmd("approve", broker.Approved) }
func newGateRejectCmd() *cobra.Command  { return decideCmd("reject", broker.Rejected) }

func decideCmd(verb string, decision broker.Decision) *cobra.Command {
	var note string
	cmd := &cobra.Command{
		Use:   verb + " <gate-id>",
		Short: strings.ToUpper(verb[:1]) + verb[1:] + " a gate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			by := os.Getenv("USER")
			if by == "" {
				by = "operator"
			}
			g, err := brokerFor(root, st).Decide(ctx, args[0], decision, by, note)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s by %s\n", g.ID, g.Decision, g.DecidedBy)
			if decision == broker.Approved {
				branch := "le/task/" + g.TaskID
				fmt.Fprintf(cmd.ErrOrStderr(),
					"\nThe change is on branch %s. Review and merge it with:\n"+
						"  git diff main..%s\n  git merge %s\n",
					branch, branch, branch)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"\nThe branch le/task/%s still holds the change if you want to look at it.\n"+
						"Delete it with: git branch -D le/task/%s\n", g.TaskID, g.TaskID)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&note, "note", "",
		"why. The reasoning is what this gate is worth six months from now, not the verdict")
	return cmd
}

// writeEvidence renders everything a gate carries.
//
// It is a function rather than a block inside the command because that is how
// this went wrong: the rendering lived inline and untested, two fields were
// added to broker.Evidence with doc comments explaining why an operator needs
// them, and neither was ever printed. A fresh-context review found three real
// defects in one change — including the one that made the fix a no-op — and
// nobody could see them. TestEveryEvidenceFieldIsRendered now fails when a
// field is added here and not shown.
func writeEvidence(w io.Writer, ev broker.Evidence, showDiff bool) {
	if ev.Summary != "" {
		fmt.Fprintf(w, "\n%s\n", ev.Summary)
	}
	if len(ev.Findings) > 0 {
		fmt.Fprintln(w, "\nVerification:")
		for _, f := range ev.Findings {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}
	if ev.Impact != nil {
		fmt.Fprintf(w, "\nImpact: %s\n", ev.Impact.Summary())
		for _, c := range ev.Impact.Consumers {
			if c.Verdict == "breaking" {
				fmt.Fprintf(w, "  breaking  %s\n", c.Node.FQN)
			}
		}
		fmt.Fprintf(w, "\n%s\n", ev.Impact.Caveat)
	}
	if len(ev.OutOfScope) > 0 {
		fmt.Fprintln(w, "\nChanged outside the declared scope:")
		for _, f := range ev.OutOfScope {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}
	// A protected path is a path someone wrote a rule about, and the
	// rule says why. Showing the path without the reason asks the
	// operator to judge a violation with nothing to judge on.
	if len(ev.PolicyReasons) > 0 {
		fmt.Fprintln(w, "\nProtected by repository policy:")
		for _, p := range ev.PolicyReasons {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
	// Last and labelled, as broker.Evidence says: everything above is
	// deterministic evidence and this is a model's opinion about work
	// a model did. It goes before the diff because a note printed
	// after forty thousand bytes of diff is a note nobody reads.
	if len(ev.ReviewConcerns) > 0 {
		fmt.Fprintln(w, "\nReview concerns (advisory — a model's reading, not evidence):")
		for _, c := range ev.ReviewConcerns {
			fmt.Fprintf(w, "  %s\n", c)
		}
	}
	if ev.Diff != "" {
		if showDiff {
			fmt.Fprintf(w, "\n--- diff ---\n%s\n", ev.Diff)
		} else {
			fmt.Fprintf(w, "\n(%d bytes of diff; pass --diff to see it)\n", len(ev.Diff))
		}
	}
}
