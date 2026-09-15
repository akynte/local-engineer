package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/telemetry"
	"github.com/akynte/local-engineer/internal/workspace"
)

// `le telemetry` — per-workspace counters, and the optional cross-workspace
// aggregate of §2.2.
//
// The aggregate is the one deliberate exception to "no cross-workspace query
// exists in the code", and §2.2 bounds it tightly: workspace ids and counters,
// never content. It is built by this command rather than written continuously,
// so nothing accumulates across your projects while you are not looking.

func newTelemetryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telemetry",
		Short: "Counters and timings, per workspace and — optionally — across them",
		Long: "Telemetry is per workspace: its own database, in its own directory, like\n" +
			"everything else (§2.2). The one exception §2.2 permits is an aggregate\n" +
			"holding \"workspace ids only\" and \"counters, never content\", and that is what\n" +
			"`aggregate` builds — on demand, never continuously.",
	}
	cmd.AddCommand(newTelemetryShowCmd(), newTelemetryAggregateCmd())
	return cmd
}

func newTelemetryShowCmd() *cobra.Command {
	var sinceHours int
	c := &cobra.Command{
		Use:   "show",
		Short: "Counters for the workspace containing the working directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _, st, err := openWorkspace(cmd.Context())
			if err != nil {
				return err
			}
			rec := telemetry.New(st)
			since := time.Now().Add(-time.Duration(sinceHours) * time.Hour)
			counters, err := rec.Counters(cmd.Context(), since)
			if err != nil {
				return err
			}
			if len(counters) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "nothing recorded in the last %dh\n", sinceHours)
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "KIND\tNAME\tCOUNT\tTOTAL")
			for _, c := range counters {
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", c.Kind, c.Name, c.Count,
					time.Duration(c.TotalMS)*time.Millisecond)
			}
			return w.Flush()
		},
	}
	c.Flags().IntVar(&sinceHours, "since-hours", 24*7, "how far back to count")
	return c
}

func newTelemetryAggregateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "aggregate",
		Short: "The optional cross-workspace counter store (§2.2)",
		Long: "The aggregate answers questions about volume — how many tasks, how much time,\n" +
			"across every workspace this data directory knows. It holds a workspace id, a\n" +
			"metric name, a day and two numbers. There is nowhere in its row shape to put a\n" +
			"path, a symbol or a task title, which is how §2.2's \"counters, never content\"\n" +
			"is enforced rather than promised.\n\n" +
			"It is derived and disposable: `build` replaces what it holds, and deleting the\n" +
			"file loses nothing that is not still in each workspace's own telemetry.",
	}
	cmd.AddCommand(newAggregateBuildCmd(), newAggregateShowCmd(), newAggregateForgetCmd())
	return cmd
}

func withAggregate(ctx context.Context, fn func(*store.Root, *telemetry.Aggregate) error) error {
	root, err := openRoot()
	if err != nil {
		return err
	}
	agg, err := telemetry.OpenAggregate(ctx, root)
	if err != nil {
		return err
	}
	defer agg.Close()
	return fn(root, agg)
}

func newAggregateBuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "build",
		Short: "Collect counters from every workspace into the aggregate",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withAggregate(cmd.Context(), func(root *store.Root, agg *telemetry.Aggregate) error {
				records, err := root.ListWorkspaces()
				if err != nil {
					return err
				}
				if len(records) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "no workspaces in this data directory")
					return nil
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "WORKSPACE\tNAME\tROWS")
				for _, rec := range records {
					st, err := root.OpenWorkspace(cmd.Context(), rec.ID)
					if err != nil {
						fmt.Fprintf(w, "%s\t%s\tskipped: %v\n", rec.ID, rec.Name, err)
						continue
					}
					n, err := agg.Collect(cmd.Context(), st)
					if err != nil {
						fmt.Fprintf(w, "%s\t%s\tfailed: %v\n", rec.ID, rec.Name, err)
						continue
					}
					fmt.Fprintf(w, "%s\t%s\t%d\n", rec.ID, rec.Name, n)
				}
				return w.Flush()
			})
		},
	}
}

func newAggregateShowCmd() *cobra.Command {
	var sinceDays int
	var name string
	c := &cobra.Command{
		Use:   "show",
		Short: "Report counters across workspaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withAggregate(cmd.Context(), func(_ *store.Root, agg *telemetry.Aggregate) error {
				q := telemetry.Query{Name: name}
				if sinceDays > 0 {
					q.Since = time.Now().AddDate(0, 0, -sinceDays)
				}
				rows, err := agg.Totals(cmd.Context(), q)
				if err != nil {
					return err
				}
				if len(rows) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(),
						"the aggregate is empty; run `le telemetry aggregate build` first")
					return nil
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "WORKSPACE\tKIND\tNAME\tCOUNT\tTOTAL")
				for _, r := range rows {
					fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", r.WorkspaceID, r.Kind, r.Name, r.Count,
						time.Duration(r.TotalMS)*time.Millisecond)
				}
				if err := w.Flush(); err != nil {
					return err
				}
				// The reminder matters: a stale aggregate looks exactly like a
				// current one, and it is derived rather than live.
				fmt.Fprintln(cmd.OutOrStdout(),
					"\nThese are the numbers as of the last `build`, not live.")
				return nil
			})
		},
	}
	c.Flags().IntVar(&sinceDays, "since-days", 0, "only days on or after this many days ago (0: everything)")
	c.Flags().StringVar(&name, "name", "", "only this metric")
	return c
}

func newAggregateForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget <workspace-id>",
		Short: "Remove one workspace from the aggregate",
		Long: "The aggregate is the only place one workspace's numbers sit beside another's,\n" +
			"so leaving it must be possible without deleting the whole file.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withAggregate(cmd.Context(), func(_ *store.Root, agg *telemetry.Aggregate) error {
				n, err := agg.Forget(cmd.Context(), workspace.ID(args[0]))
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "removed %d row(s) for %s\n", n, args[0])
				if n == 0 {
					fmt.Fprintln(os.Stderr, "(that workspace had nothing in the aggregate)")
				}
				return nil
			})
		},
	}
}
