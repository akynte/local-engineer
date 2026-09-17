package main

import (
	"fmt"
	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/memory"
	"github.com/akynte/local-engineer/internal/opencode"
)

func newOpenCodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "opencode",
		Short: "Wire this repository into OpenCode",
	}
	cmd.AddCommand(newOpenCodeSetupCmd())
	return cmd
}

// newOpenCodeSetupCmd registers the MCP server and writes the project context
// OpenCode reads on its own.
//
// One command, run once, and afterwards a developer opens OpenCode in the
// directory and works normally. That is the whole point: a tool the user has to
// remember to invoke before asking a question is a tool they will stop using.
func newOpenCodeSetupCmd() *cobra.Command {
	var dataDir string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Register the MCP server and write AGENTS.md for this repository",
		Long: "setup makes Local Engineer part of an ordinary OpenCode session.\n\n" +
			"It registers `le mcp` in opencode.json, and writes a block into AGENTS.md —\n" +
			"which OpenCode reads into every session — telling the agent that a\n" +
			"compiler-backed index of this repository exists, which questions it answers\n" +
			"better than search, and what this repository has already recorded about\n" +
			"itself.\n\n" +
			"Re-run it after recording notes or re-indexing. It replaces only its own\n" +
			"block in AGENTS.md and merges into opencode.json, so anything you have\n" +
			"written in either file is left alone.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			ws, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)
			out := cmd.OutOrStdout()

			command := []string{"le", "mcp"}
			if dataDir != "" {
				command = append(command, "--data", dataDir)
			}
			cfgPath, cfgChanged, err := opencode.RegisterMCP(ws.Root, command)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s %s\n", verb(cfgChanged), cfgPath)

			facts := opencode.Facts{WorkspaceName: ws.Name()}
			if stats, err := graph.New(st).Stats(ctx); err == nil {
				facts.Nodes, facts.Edges = stats.Nodes, stats.Edges
			}
			if notes, err := memory.Open(ws.Root, memory.DefaultCaps()).All(); err == nil {
				facts.Notes = notes
			}
			agentsPath, agentsChanged, err := opencode.Apply(ws.Root, opencode.Render(facts))
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s %s\n", verb(agentsChanged), agentsPath)

			if facts.Nodes == 0 {
				fmt.Fprintf(out, "\nThis repository is not indexed yet. Run `le index` once, "+
					"then `le opencode setup` again so AGENTS.md reports the real graph.\n")
			}
			fmt.Fprintf(out, "\nOpen this directory in OpenCode and work normally.\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "",
		"pass an explicit --data to the registered `le mcp` command")
	return cmd
}

func verb(changed bool) string {
	if changed {
		return "wrote"
	}
	return "already current:"
}
