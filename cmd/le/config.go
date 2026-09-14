package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/config"
	"github.com/akynte/local-engineer/internal/llm"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and initialise configuration",
	}
	cmd.AddCommand(newConfigInitCmd(), newConfigShowCmd(), newConfigProfilesCmd())
	return cmd
}

func newConfigInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write default le.yaml and providers.yaml into the data directory",
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := openRoot()
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)
			dir := root.Layout().ConfigDir()

			if _, err := os.Stat(config.Path(dir)); err == nil && !force {
				return fmt.Errorf("%s already exists; pass --force to overwrite", config.Path(dir))
			}
			cfg := config.Default()
			if err := config.Save(dir, cfg); err != nil {
				return err
			}
			providers := llm.DefaultProvidersFile(
				fmt.Sprintf("http://127.0.0.1:%d", cfg.Inference.Port), "local")
			if err := llm.SaveProvidersFile(dir, providers); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\nwrote %s\n",
				config.Path(dir), filepath.Join(dir, "providers.yaml"))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return cmd
}

func newConfigShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration and role routing",
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := openRoot()
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)
			cfg, err := loadConfig(root)
			if err != nil {
				return err
			}
			out := map[string]any{"config": cfg}

			if f, err := llm.LoadProvidersFile(root.Layout().ConfigDir()); err == nil {
				if r, err := llm.NewRouter(f, cfg.Offline); err == nil {
					out["providers"] = r.Names()
					out["routing"] = r.Routing()
					r.Close()
				} else {
					out["providers_error"] = err.Error()
				}
			}
			if p := loadProfile(root, cfg); p != nil {
				out["profile"] = p
			} else {
				out["profile"] = "none loaded; conservative fallback values are in force"
			}
			return emitJSON(out)
		},
	}
	return cmd
}

func newConfigProfilesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "profiles",
		Short: "List available hardware profiles",
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := openRoot()
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)
			cfg, _ := loadConfig(root)

			dir := profileDir(root)
			names, err := config.ListProfiles(dir)
			if err != nil {
				return err
			}
			for _, n := range names {
				marker := " "
				if n == cfg.Profile {
					marker = "*"
				}
				origin := "shipped"
				if config.OnDisk(dir, n) {
					origin = "yours"
				}
				desc := ""
				if p, err := config.LoadProfile(dir, n); err == nil {
					desc = p.Description
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s %-32s %-8s %s\n", marker, n, origin, desc)
			}
			fmt.Fprintln(cmd.OutOrStdout(),
				"\n* is the active profile. Shipped profiles are starting points, not measurements:")
			fmt.Fprintln(cmd.OutOrStdout(),
				"  run `le models bench --write` to measure this machine.")
			return nil
		},
	}
}
