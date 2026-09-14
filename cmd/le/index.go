package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/index"
	"github.com/akynte/local-engineer/internal/retrieval"
)

func newIndexCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Build the source index, symbol index, graph and chunks",
		Long: "index walks every repository in the workspace and writes files, directories,\n" +
			"containment edges and lexical chunks into the workspace's index.db.\n" +
			"Language analyzers contribute the compiler-backed relations on top.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			ws, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			cfg, _ := loadConfig(root)
			ix := index.New(st, index.Options{
				MaxFileBytes: cfg.Index.MaxFileBytes,
				Excludes:     cfg.Index.Excludes,
				ChunkLines:   cfg.Index.ChunkLines,
			})

			results := map[string]index.Stats{}
			start := time.Now()
			for _, repo := range ws.Manifest.Repositories {
				if err := ix.RegisterRepository(ctx, repo); err != nil {
					return err
				}
				abs := filepath.Join(ws.Root, filepath.FromSlash(repo.Path))
				stats, err := ix.Repository(ctx, repo.ID, abs)
				if err != nil {
					return fmt.Errorf("index %s: %w", repo.Name, err)
				}
				results[repo.Name] = stats
			}

			if asJSON {
				return emitJSON(results)
			}
			out := cmd.OutOrStdout()
			for name, s := range results {
				fmt.Fprintf(out, "%s: %d files, %d chunks, %d nodes, %d edges (%d skipped) in %s\n",
					name, s.Files, s.Chunks, s.Nodes, s.Edges, s.Skipped, s.Duration.Round(time.Millisecond))
			}
			fmt.Fprintf(out, "total %s\n", time.Since(start).Round(time.Millisecond))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func newGraphCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Query the code relationship graph",
	}
	cmd.AddCommand(newGraphStatsCmd(), newGraphImpactCmd(), newGraphSearchCmd())
	return cmd
}

func newGraphStatsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Report node and edge counts by kind and evidence category",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			_, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			stats, err := graphFor(st).Stats(ctx)
			if err != nil {
				return err
			}
			if asJSON {
				return emitJSON(stats)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%d nodes, %d edges\n", stats.Nodes, stats.Edges)
			if len(stats.ByEdge) > 0 {
				fmt.Fprintln(out, "\nby relationship:")
				for k, n := range stats.ByEdge {
					fmt.Fprintf(out, "  %-14s %d\n", k, n)
				}
			}
			if len(stats.ByEvid) > 0 {
				fmt.Fprintln(out, "\nby evidence category:")
				for k, n := range stats.ByEvid {
					fmt.Fprintf(out, "  %-14s %d\n", k, n)
				}
			}
			if stats.DirtyKey > 0 {
				fmt.Fprintf(out, "\n%d index units are dirty; run `le index`.\n", stats.DirtyKey)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func newGraphImpactCmd() *cobra.Command {
	var changeKind string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "impact <symbol> [symbol...]",
		Short: "Report what a change to these symbols would affect",
		Long: "impact reports consumers with their evidence category, a deterministic\n" +
			"compatibility verdict and the migration step each one needs.\n\n" +
			"Consumers reached by inferred or unknown evidence are listed and treated as\n" +
			"present: a missing edge means \"not discovered\", never \"does not exist\".",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			kind, err := parseChange(changeKind)
			if err != nil {
				return err
			}
			g := graphFor(st)
			var ids []int64
			for _, sym := range args {
				nodes, err := g.NodesByName(ctx, sym, nil, 50)
				if err != nil {
					return err
				}
				if len(nodes) == 0 {
					return fmt.Errorf("no indexed symbol named %q; run `le index` or check the name", sym)
				}
				for _, n := range nodes {
					ids = append(ids, n.ID)
				}
			}
			imp, err := g.ImpactOf(ctx, ids, kind)
			if err != nil {
				return err
			}
			if asJSON {
				return emitJSON(imp)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s\n\n", imp.Summary())
			for _, c := range imp.Consumers {
				fmt.Fprintf(out, "  %-30s %-12s %-10s via %-12s depth %d\n",
					truncate(c.Node.FQN, 30), c.Verdict, c.Evidence, c.Via, c.Depth)
				if c.Migration != "" {
					fmt.Fprintf(out, "  %-30s   → %s\n", "", c.Migration)
				}
			}
			fmt.Fprintf(out, "\n%s\n", imp.Caveat)
			return nil
		},
	}
	cmd.Flags().StringVar(&changeKind, "change", "behaviour",
		"change kind: signature, behaviour, remove, rename, add_field, schema, config, route")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func newGraphSearchCmd() *cobra.Command {
	var expand int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Retrieve context the way a task step would: lexical anchors, then graph expansion",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			cfg, _ := loadConfig(root)
			budget := 0
			if p := loadProfile(root, cfg); p != nil {
				budget = p.MaxPacketTokens
			}
			pkt, err := retrieval.New(st).Build(ctx, retrieval.Request{
				Query: joinArgs(args), ExpandDepth: expand, TokenBudget: budget,
			})
			if err != nil {
				return err
			}
			if asJSON {
				return emitJSON(pkt)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%d slices, ~%d of %d tokens", len(pkt.Slices), pkt.Tokens, pkt.Budget)
			if pkt.Dropped > 0 {
				fmt.Fprintf(out, ", %d dropped over budget", pkt.Dropped)
			}
			fmt.Fprintln(out)
			for _, s := range pkt.Slices {
				fmt.Fprintf(out, "  %-16s %s:%d-%d %s\n", s.Origin, s.Path, s.StartLine, s.EndLine, s.Symbol)
			}
			for _, r := range pkt.Rejected {
				fmt.Fprintf(out, "  REJECTED: %s\n", r)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&expand, "expand", 1, "graph expansion depth from the lexical anchors (0 disables)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n+1:]
}

func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
