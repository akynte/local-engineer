package main

import (
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	lemcp "github.com/akynte/local-engineer/internal/mcp"
)

// newMCPCmd serves Local Engineer's tools to an MCP client over stdio.
//
// stdio is the transport OpenCode's "local" server type uses: it starts the
// command and speaks JSON-RPC over the pipe. That means stdout belongs to the
// protocol — anything written to it that is not a JSON-RPC frame corrupts the
// session — so this command prints nothing. Diagnostics go to stderr, which the
// client shows separately.
func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve Local Engineer's tools to an MCP client such as OpenCode",
		Long: "mcp speaks the Model Context Protocol over stdin and stdout, so an editor or\n" +
			"agent can ask about this repository without the operator typing CLI commands.\n\n" +
			"Register it with OpenCode by adding to opencode.jsonc:\n\n" +
			"  { \"mcp\": { \"local-engineer\": {\n" +
			"      \"type\": \"local\", \"command\": [\"le\", \"mcp\"], \"enabled\": true } } }\n\n" +
			"The tools are read-mostly: status, impact analysis, retrieval and re-indexing.\n" +
			"Running tasks and anything destructive stays on the CLI, where a person is\n" +
			"already watching.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			srv, err := lemcp.New(lemcp.Options{DataDir: g.dataDir, WorkDir: cwd})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(),
				"local-engineer mcp: serving %s over stdio\n", cwd)
			// Run blocks until the client disconnects or the context is
			// cancelled, which is what the signal handler in main does.
			return srv.Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
}
