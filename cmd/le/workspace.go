package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/workspace"
)

func newWorkspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workspace",
		Aliases: []string{"ws"},
		Short:   "Create, inspect and re-bind workspaces",
		Long: "A workspace is the unit of isolation. Its identity is pinned in\n" +
			".le/workspace.yaml inside the repository root, so the identity travels with\n" +
			"the code. Moving the directory without that file creates a new workspace on\n" +
			"purpose; after a move, `le workspace adopt` re-binds the existing id.",
	}
	cmd.AddCommand(newWorkspaceInitCmd(), newWorkspaceAdoptCmd(),
		newWorkspaceListCmd(), newWorkspaceShowCmd())
	return cmd
}

func newWorkspaceInitCmd() *cobra.Command {
	var name string
	var repos []string
	var force bool

	cmd := &cobra.Command{
		Use:   "init [path]",
		Short: "Create .le/workspace.yaml and register the workspace",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			ws, err := workspace.Init(path, workspace.InitOptions{
				Name: name, Repositories: repos, Force: force,
			})
			if err != nil {
				return err
			}
			root, err := openRoot()
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)
			st, err := root.OpenWorkspace(cmd.Context(), ws.ID())
			if err != nil {
				return err
			}
			if err := st.RecordWorkspace(ws); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "workspace %s\n", ws.ID())
			fmt.Fprintf(out, "  name:         %s\n", ws.Name())
			fmt.Fprintf(out, "  root:         %s\n", ws.Root)
			fmt.Fprintf(out, "  pinned in:    %s\n", workspace.MarkerPath(ws.Root))
			fmt.Fprintf(out, "  state under:  %s\n", st.Dir())
			for _, r := range ws.Manifest.Repositories {
				fmt.Fprintf(out, "  repository:   %s (%s) on %s\n", r.Name, r.ID, r.DefaultBranch)
			}
			fmt.Fprintf(out, "\nCommit %s so the identity travels with the repository.\n",
				workspace.MarkerDir+"/"+workspace.MarkerFile)
			fmt.Fprintln(out, "Next: `le index` to build the source index and graph.")
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "workspace name (default: the directory name)")
	cmd.Flags().StringSliceVar(&repos, "repo", nil,
		"repository path relative to the root; repeat for a multi-repository workspace")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing workspace.yaml")
	return cmd
}

func newWorkspaceAdoptCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "adopt [path]",
		Short: "Re-bind a pinned workspace id after the directory moved",
		Long: "adopt refreshes the recorded derivation and each repository's remote and\n" +
			"default branch. The pinned id never changes: keeping it is the entire point,\n" +
			"so the workspace's index, ledger and artifacts stay attached to the code.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			ws, err := workspace.Open(path)
			if err != nil {
				return err
			}
			before := ws.Manifest.DerivedFrom.CanonicalRoot
			if !ws.Moved() {
				fmt.Fprintf(cmd.OutOrStdout(), "workspace %s is already bound to %s; nothing to do\n",
					ws.ID(), ws.Root)
				return nil
			}
			if err := ws.Adopt(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "workspace %s re-bound\n  was: %s\n  now: %s\n",
				ws.ID(), before, ws.Root)
			return nil
		},
	}
}

func newWorkspaceListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every workspace known to this data directory",
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := openRoot()
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)
			records, err := root.ListWorkspaces()
			if err != nil {
				return err
			}
			if asJSON {
				return emitJSON(records)
			}
			if len(records) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(),
					"No workspaces yet. Run `le workspace init` in a repository root.")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tROOT\tLAST OPENED")
			for _, r := range records {
				last := "—"
				if !r.LastOpened.IsZero() {
					last = r.LastOpened.Format("2006-01-02 15:04")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.ID, r.Name, r.Root, last)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func newWorkspaceShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show the workspace containing the working directory",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			ws, err := workspace.Open(cwd)
			if err != nil {
				return err
			}
			if asJSON {
				return emitJSON(ws.Manifest)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "id:      %s\nname:    %s\nroot:    %s\nscheme:  v%d\n",
				ws.ID(), ws.Name(), ws.Root, ws.Manifest.SchemeVersion)
			if ws.Moved() {
				fmt.Fprintf(out, "\nThis workspace has moved (id derived at %s).\nRun `le workspace adopt` to re-bind it.\n",
					ws.Manifest.DerivedFrom.CanonicalRoot)
			}
			fmt.Fprintln(out, "\nrepositories:")
			for _, r := range ws.Manifest.Repositories {
				fmt.Fprintf(out, "  %-26s %s  branch=%s  remote=%s\n", r.ID, r.Path, r.DefaultBranch, r.Remote)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}
