package main

import (
	"fmt"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/ledger"
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
	cmd.AddCommand(newTaskListCmd(), newTaskJournalCmd(), newTaskRecoverCmd())
	return cmd
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
			defer root.CloseAll()

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
			defer root.CloseAll()

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
			defer root.CloseAll()

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
