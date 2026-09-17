// Command le is the local-engineer supervisor and CLI.
//
// Inside the container it is PID 1 (via tini or --init) and supervises the
// long-lived children of design v3 §4.3. On a developer host it is an ordinary
// CLI that talks to the same data directory.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/sandbox/landlock"
)

func main() {
	// The Landlock helper must run before anything else touches the world: it
	// applies the ruleset to itself and execs the real target (§6.1 layer 2).
	if len(os.Args) > 1 && os.Args[1] == landlock.HelperCommand {
		args := os.Args[2:]
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		if err := landlock.Helper(args); err != nil {
			fmt.Fprintln(os.Stderr, "le:", err)
			os.Exit(126)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "le:", err)
		os.Exit(1)
	}
}

// globals are the flags every subcommand shares.
type globals struct {
	dataDir string
	logJSON bool
	verbose bool
	quiet   bool
}

var g globals

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "le",
		Short: "local-engineer: a supervised local coding engineer",
		Long: "local-engineer supervises a local model through a deterministic harness:\n" +
			"a per-workspace code graph, an intent-first execution journal, evidence-backed\n" +
			"verification, and layered sandboxing.\n\n" +
			"Every workspace is isolated: its own index, ledger, telemetry, cache, artifacts\n" +
			"and engine directories, with no shared state and no cross-project memory.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			setupLogging()
		},
	}
	pf := root.PersistentFlags()
	pf.StringVar(&g.dataDir, "data", "", "data directory (default $LE_DATA, then /data)")
	pf.BoolVar(&g.logJSON, "log-json", false, "emit structured JSON logs")
	pf.BoolVarP(&g.verbose, "verbose", "v", false, "debug logging")
	pf.BoolVarP(&g.quiet, "quiet", "q", false, "errors only")

	root.AddCommand(
		newVersionCmd(),
		newDoctorCmd(),
		newWorkspaceCmd(),
		newIndexCmd(),
		newGraphCmd(),
		newAPICmd(),
		newBackupCmd(),
		newRestoreCmd(),
		newModelsCmd(),
		newConfigCmd(),
		newTaskCmd(),
		newGateCmd(),
		newPlanCmd(),
		newEvalCmd(),
		newMemoryCmd(),
		newLessonsCmd(),
		newDepsCmd(),
		newDocsCmd(),
		newTelemetryCmd(),
		newTUICmd(),
		newMCPCmd(),
		newVerifyDeclaredCmd(),
	)
	return root
}

func setupLogging() {
	level := slog.LevelInfo
	switch {
	case g.verbose:
		level = slog.LevelDebug
	case g.quiet:
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if g.logJSON {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}
