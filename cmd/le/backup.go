package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/workspace"
)

func newBackupCmd() *cobra.Command {
	var to string
	var all bool
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Write a consistent snapshot of a workspace's databases",
		Long: "backup uses SQLite's online snapshot path, so it is safe to run while the\n" +
			"supervisor is working. The three databases are written side by side under a\n" +
			"timestamped directory; restore reads the same layout.\n\n" +
			"Run this before every upgrade: migrations are forward-only and downgrades\n" +
			"across schema versions are not supported.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			root, err := openRoot()
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			stamp := time.Now().UTC().Format("20060102T150405Z")
			dest := to
			if dest == "" {
				dest = filepath.Join(root.Layout().BackupsDir(), stamp)
			}

			var ids []workspace.ID
			if all {
				records, err := root.ListWorkspaces()
				if err != nil {
					return err
				}
				for _, r := range records {
					ids = append(ids, r.ID)
				}
			} else {
				ws, _, _, err := openWorkspace(ctx)
				if err != nil {
					return err
				}
				ids = append(ids, ws.ID())
			}
			if len(ids) == 0 {
				return fmt.Errorf("no workspaces to back up")
			}

			for _, id := range ids {
				st, err := root.OpenWorkspace(ctx, id)
				if err != nil {
					return err
				}
				dir := filepath.Join(dest, id.String())
				if err := st.Backup(ctx, dir); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "backed up %s to %s\n", id, dir)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "destination directory (default: <data>/backups/<timestamp>)")
	cmd.Flags().BoolVar(&all, "all", false, "back up every workspace, not just the current one")
	return cmd
}

func newRestoreCmd() *cobra.Command {
	var from string
	var force bool
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore a workspace's databases from a backup directory",
		Long: "restore copies index.db, ledger.db and telemetry.db back into the workspace\n" +
			"directory. The supervisor must not be running against this data directory.\n\n" +
			"A database carries the id of the workspace that created it, so restoring into\n" +
			"the wrong workspace fails at open rather than serving another project's code.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if from == "" {
				return fmt.Errorf("--from is required: the backup directory holding index.db, ledger.db and telemetry.db")
			}
			ws, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			if err := st.Close(); err != nil {
				return err
			}
			restored, err := st.RestoreFrom(from, force)
			if err != nil {
				return err
			}
			for _, name := range restored {
				fmt.Fprintf(cmd.OutOrStdout(), "restored %s\n", name)
			}

			// Re-open to prove the restored files are valid and belong here.
			if _, err := root.OpenWorkspace(ctx, ws.ID()); err != nil {
				return fmt.Errorf("restored files failed validation: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "workspace %s restored and verified\n", ws.ID())
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "backup directory to restore from")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing databases")
	return cmd
}
