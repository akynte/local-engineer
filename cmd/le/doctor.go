package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/doctor"
	"github.com/akynte/local-engineer/internal/workspace"
)

func newDoctorCmd() *cobra.Command {
	var asJSON, deep bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Report what is actually in effect: isolation layers, storage, index, profile",
		Long: "doctor answers \"what is actually in effect here\".\n\n" +
			"It reports which of the three isolation layers are active (DR-3), whether the\n" +
			"data directory is on a filesystem SQLite can trust, how fresh the index is,\n" +
			"whether the active hardware profile fits this machine, and any leases left\n" +
			"behind by a crashed instance.\n\n" +
			"Exit status is 0 when every check passes, 1 when any check warns, and 2 when\n" +
			"any check fails.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			opts := doctor.Options{Deep: deep}

			root, err := openRoot()
			if err == nil {
				opts.Root = root
				defer root.CloseAll()
				cfg, cerr := loadConfig(root)
				if cerr == nil {
					opts.Config = &cfg
					opts.Profile = loadProfile(root, cfg)
				}
			}
			if cwd, err := os.Getwd(); err == nil {
				if ws, err := workspace.Open(cwd); err == nil {
					opts.Workspace = ws
				}
			}

			rep := doctor.Run(ctx, opts)
			if asJSON {
				if err := emitJSON(rep); err != nil {
					return err
				}
			} else {
				fmt.Fprint(cmd.OutOrStdout(), rep.Format())
			}
			switch rep.Worst() {
			case doctor.Fail:
				os.Exit(2)
			case doctor.Warn:
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	cmd.Flags().BoolVar(&deep, "deep", false, "run PRAGMA integrity_check on every database (slow)")
	return cmd
}
