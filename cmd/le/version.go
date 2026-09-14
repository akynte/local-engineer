package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/version"
)

func newVersionCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print build, schema and indexer versions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := version.Current()
			if asJSON {
				return emitJSON(info)
			}
			fmt.Fprintln(cmd.OutOrStdout(), info)
			fmt.Fprintf(cmd.OutOrStdout(), "schemas: index=%d ledger=%d telemetry=%d\n",
				info.Schemas["index"], info.Schemas["ledger"], info.Schemas["telemetry"])
			fmt.Fprintf(cmd.OutOrStdout(), "indexer version: %d, workspace id scheme: v%d\n",
				info.IndexerV, info.WorkspaceIDScheme)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}
