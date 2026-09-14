package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/config"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/models"
)

func newModelsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Measure a model on this machine and write a hardware profile",
		Long: "Hardware profiles are configuration, not code: context limits, memory budgets,\n" +
			"thread counts, offload layers, sampling defaults and packet sizes all live in a\n" +
			"profile, and the shipped reference profile is only a default value.\n\n" +
			"`le models bench` measures this machine and writes a profile from what it saw,\n" +
			"so the numbers the supervisor admits tasks against are measured rather than guessed.",
	}
	cmd.AddCommand(newModelsBenchCmd(), newModelsHealthCmd())
	return cmd
}

func newModelsBenchCmd() *cobra.Command {
	var (
		profileName string
		provider    string
		prompts     int
		promptSize  int
		outputSize  int
		contextSize int
		write       bool
		asJSON      bool
	)
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Measure prefill and decode throughput and peak memory, then write a profile",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			root, err := openRoot()
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			cfg, err := loadConfig(root)
			if err != nil {
				return err
			}
			f, err := llm.LoadProvidersFile(root.Layout().ConfigDir())
			if err != nil {
				return fmt.Errorf("no providers.yaml: run `le config init` first (%w)", err)
			}
			router, err := llm.NewRouter(f, cfg.Offline)
			if err != nil {
				return err
			}
			defer router.Close()

			p, err := router.For(llm.RoleCoding)
			if err != nil {
				return err
			}
			if provider != "" && p.Name() != provider {
				return fmt.Errorf("provider %q is not the one routed for the coding role (%q); "+
					"edit providers.yaml to change the routing", provider, p.Name())
			}

			fmt.Fprintf(cmd.ErrOrStderr(), "benchmarking %s (%s)…\n", p.Name(), p.Capabilities().Kind)
			ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
			defer cancel()

			result, err := models.Bench(ctx, p, models.BenchOptions{
				Iterations: prompts, PromptTokens: promptSize, OutputTokens: outputSize,
				Progress: func(msg string) { fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", msg) },
			})
			if err != nil {
				return err
			}
			if asJSON {
				return emitJSON(result)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "\n%s\n", strings.Repeat("─", 56))
			fmt.Fprintf(out, "model              %s\n", result.Model)
			fmt.Fprintf(out, "prefill            %.1f tok/s\n", result.PrefillTokensSec)
			fmt.Fprintf(out, "decode             %.1f tok/s\n", result.DecodeTokensSec)
			fmt.Fprintf(out, "first token        %.0f ms (p50), %.0f ms (p95)\n", result.TTFTp50MS, result.TTFTp95MS)
			fmt.Fprintf(out, "prompt cache reuse %.1f%%\n", result.CacheReusePct)
			fmt.Fprintf(out, "peak VRAM          %d MB of %d MB\n", result.PeakVRAMMB, result.TotalVRAMMB)
			fmt.Fprintf(out, "peak RAM           %d MB of %d MB\n", result.PeakRAMMB, result.TotalRAMMB)
			fmt.Fprintf(out, "rate source        %s\n", result.TimingSource)
			fmt.Fprintf(out, "%s\n", strings.Repeat("─", 56))

			profile := models.ProfileFrom(result, profileName, contextSize)
			if !write {
				fmt.Fprintf(out, "\nProposed profile %q (pass --write to save it):\n", profile.Name)
				fmt.Fprintf(out, "  context_tokens        %d\n", profile.ContextTokens)
				fmt.Fprintf(out, "  max_packet_tokens     %d\n", profile.MaxPacketTokens)
				fmt.Fprintf(out, "  reserved_output       %d\n", profile.ReservedOutput)
				fmt.Fprintf(out, "  max_concurrent_tasks  %d\n", profile.Concurrency)
				return nil
			}
			dir := profileDir(root)
			if err := config.SaveProfile(dir, profile); err != nil {
				return err
			}
			fmt.Fprintf(out, "\nwrote %s/%s.yaml\n", dir, profile.Name)
			fmt.Fprintf(out, "Set `profile: %s` in le.yaml to make it active.\n", profile.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&profileName, "profile-name", "", "name for the generated profile (default: derived from the hardware)")
	cmd.Flags().StringVar(&provider, "provider", "", "assert which provider is being measured")
	cmd.Flags().IntVar(&prompts, "iterations", 3, "measured iterations")
	cmd.Flags().IntVar(&promptSize, "prompt-tokens", 2000, "approximate prompt size per iteration")
	cmd.Flags().IntVar(&outputSize, "output-tokens", 256, "tokens to generate per iteration")
	cmd.Flags().IntVar(&contextSize, "context", 0, "context window to record (default: the provider's declared maximum)")
	cmd.Flags().BoolVar(&write, "write", false, "save the generated profile")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func newModelsHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Probe every declared provider",
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := openRoot()
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)
			cfg, _ := loadConfig(root)
			f, err := llm.LoadProvidersFile(root.Layout().ConfigDir())
			if err != nil {
				return fmt.Errorf("no providers.yaml: run `le config init` first (%w)", err)
			}
			router, err := llm.NewRouter(f, cfg.Offline)
			if err != nil {
				return err
			}
			defer router.Close()

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			failed := false
			for name, status := range router.Health(ctx) {
				if status != "ok" {
					failed = true
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-20s %s\n", name, status)
			}
			if failed {
				return fmt.Errorf("one or more providers are unreachable")
			}
			return nil
		},
	}
}
