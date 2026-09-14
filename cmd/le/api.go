package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/acp"
	"github.com/akynte/local-engineer/internal/api"
	"github.com/akynte/local-engineer/internal/config"
	"github.com/akynte/local-engineer/internal/index"
	"github.com/akynte/local-engineer/internal/procman"
	"github.com/akynte/local-engineer/internal/sandbox"
	"github.com/akynte/local-engineer/internal/sandbox/bwrap"
	"github.com/akynte/local-engineer/internal/sandbox/landlock"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

func newAPICmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "api",
		Short: "Run the supervisor: children, health endpoints and dashboard",
		Long: "api is the container's entrypoint process. It migrates the data directory's\n" +
			"schemas forward, validates the configuration, selects the strongest available\n" +
			"sandbox, starts the supervised children, and serves /healthz and /readyz.\n\n" +
			"On SIGTERM it stops children in reverse order, flushes every SQLite WAL, and\n" +
			"exits within the configured grace period.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			log := slog.Default()

			root, err := openRoot()
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			cfg, err := loadConfig(root)
			if err != nil {
				return fmt.Errorf("configuration is invalid, refusing to start: %w", err)
			}
			if addr != "" {
				cfg.API.Addr = addr
			}
			profile := loadProfile(root, cfg)
			profileName := "(none)"
			if profile != nil {
				profileName = profile.Name
			}

			inContainer, containerWhy := sandbox.InContainer()
			if warn := config.ExposureWarning(cfg.API.Addr, inContainer); warn != "" {
				log.Warn("api exposure", "detail", warn, "container", containerWhy)
			}

			runner, sbReport := selectSandbox(ctx, cfg)
			log.Info("sandbox selected", "runner", sbReport.Runner, "layers", sbReport.Active)
			_ = runner

			procs := procman.New(func(f string, a ...any) { log.Info(fmt.Sprintf(f, a...)) })
			if err := registerChildren(procs, cfg, profile, root.Layout().ModelsDir()); err != nil {
				return err
			}
			go procs.Reap(ctx)
			if err := procs.Start(ctx); err != nil {
				return err
			}

			// §3.4: the file watcher marks scopes dirty so `le doctor` can
			// report index drift. It is started only when the supervisor is
			// running inside a workspace, because there is nothing to watch
			// otherwise — and it never re-indexes on its own: re-analysis costs
			// real time and belongs before a step that needs the graph, not in
			// the middle of an editor save.
			if cfg.Index.WatchEnabled {
				if stop, err := startIndexWatcher(ctx, root, cfg, log); err != nil {
					log.Warn("index watcher not started", "reason", err)
				} else if stop != nil {
					defer stop()
				}
			}

			// §4.3's ACP bridge. Off unless an address and an agent are
			// configured: DR-5's native engine does not speak ACP, so there is
			// nothing to carry by default, and an unused listening socket is a
			// surface nobody asked for.
			if cfg.API.ACPAddr != "" {
				if stop, err := startACPBridge(ctx, cfg, log); err != nil {
					return fmt.Errorf("the ACP bridge is configured but cannot start: %w", err)
				} else if stop != nil {
					defer stop()
				}
			}

			srv := api.New(cfg.API.Addr, api.Deps{
				Procs: procs, Root: root, Sandbox: &sbReport, Profile: profileName,
			}, log)

			grace := time.Duration(cfg.API.ShutdownGraceSeconds) * time.Second
			if grace <= 0 {
				grace = 30 * time.Second
			}
			log.Info("supervisor starting", "data", root.Layout().Root(),
				"profile", profileName, "inference", cfg.Inference.Mode, "grace", grace)

			serveErr := srv.Serve(ctx, grace)

			// §4.4 shutdown order: stop children, then flush WALs, then exit.
			log.Info("shutting down", "grace", grace)
			if err := procs.Stop(grace); err != nil {
				log.Warn("children did not stop cleanly", "error", err)
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = shutdownCtx
			if err := root.CloseAll(); err != nil {
				log.Warn("closing stores", "error", err)
			}
			return serveErr
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "listen address (default from le.yaml: 127.0.0.1:7777)")
	return cmd
}

// selectSandbox picks the strongest runner permitted by configuration and
// returns the report `le doctor` and /v1/sandbox both serve (DR-3).
func selectSandbox(ctx context.Context, cfg config.Config) (sandbox.Runner, sandbox.Report) {
	var candidates []sandbox.Runner
	ll, llErr := landlock.New()
	switch cfg.Sandbox.Mode {
	case "none":
	case "landlock":
		if llErr == nil {
			candidates = append(candidates, ll)
		}
	case "bwrap":
		if llErr == nil {
			candidates = append(candidates, bwrap.New(ll))
		}
	default:
		if llErr == nil {
			candidates = append(candidates, bwrap.New(ll), ll)
		}
	}
	candidates = append(candidates, sandbox.ContainerRunner{})
	runner, rep := sandbox.Select(ctx, candidates)
	return runner, rep
}

// registerChildren wires the §4.3 process model. `le api` itself is this
// process, so only the inference server is a child in the default
// configuration; `opencode serve` is started per task, not here.
func registerChildren(m *procman.Manager, cfg config.Config, p *config.Profile, modelsDir string) error {
	if cfg.Inference.Mode != config.ModeEmbedded {
		return nil
	}
	// §4.3: the embedded server is started "with the active profile". The
	// profile's runtime block is where §9.3 puts thread counts, offload layers,
	// batch sizes and cache types, so building the command line anywhere else
	// would put them back in the code.
	args, err := config.LlamaArgs(cfg, p, modelsDir)
	if err != nil {
		return err
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", cfg.Inference.Port)
	return m.Add(procman.Child{
		Name:      "llama-server",
		Essential: true,
		Build: func(ctx context.Context) (*exec.Cmd, error) {
			// The binary and arguments come from le.yaml and the active
			// profile, both operator configuration under /data/config — the
			// same trust level as the supervisor itself. An operator who can
			// edit them can already run anything in this container.
			return exec.CommandContext(ctx, cfg.Inference.Binary, args...), nil //nolint:gosec // see above
		},
		Health: func(ctx context.Context) error {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("llama-server /health returned %d", resp.StatusCode)
			}
			return nil
		},
		// Model load on a cold GGUF is slow; the timeout is per-child and
		// configurable rather than a global guess (§9.3).
		StartTimeout: time.Duration(cfg.Inference.StartTimeoutSeconds) * time.Second,
	})
}

// startIndexWatcher starts the §3.4 watcher for the workspace the supervisor
// was launched in, if any. It returns a stop function, or a nil one when there
// is no workspace to watch — which is not an error: the supervisor is often
// started outside a repository.
func startIndexWatcher(ctx context.Context, root *store.Root, cfg config.Config,
	log *slog.Logger) (func(), error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	ws, err := workspace.Open(cwd)
	if err != nil {
		return nil, fmt.Errorf("not started inside a workspace: %w", err)
	}
	st, err := root.OpenWorkspace(ctx, ws.ID())
	if err != nil {
		return nil, err
	}
	ix := index.New(st, index.Options{
		MaxFileBytes: cfg.Index.MaxFileBytes,
		Excludes:     cfg.Index.Excludes,
		ChunkLines:   cfg.Index.ChunkLines,
	})
	w, err := index.NewWatcher(ix, ws, index.WatchOptions{
		Logf: func(f string, a ...any) { log.Warn(fmt.Sprintf(f, a...)) },
		OnDirty: func(repositoryID string, paths int) {
			log.Info("index marked dirty", "repository", repositoryID, "paths", paths)
		},
	})
	if err != nil {
		return nil, err
	}

	watchCtx, cancel := context.WithCancel(ctx)
	go func() {
		if err := w.Run(watchCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Warn("index watcher stopped", "error", err)
		}
	}()
	log.Info("index watcher started", "workspace", ws.ID(), "root", ws.Root)
	return cancel, nil
}

// startACPBridge listens for editor connections and hands each one its own
// agent process. It returns a stop function.
func startACPBridge(ctx context.Context, cfg config.Config, log *slog.Logger) (func(), error) {
	ln, err := acp.Listen(cfg.API.ACPAddr)
	if err != nil {
		return nil, err
	}
	if warn := config.ExposureWarning(cfg.API.ACPAddr, false); warn != "" {
		// The bridge hands a connection a process. Binding it beyond loopback
		// is worth saying out loud, and for the same reason as the API.
		log.Warn("acp bridge exposure", "detail", warn)
	}

	b := &acp.Bridge{
		Command: cfg.API.ACPCommand,
		Logf:    func(f string, a ...any) { log.Info(fmt.Sprintf(f, a...)) },
	}
	bridgeCtx, cancel := context.WithCancel(ctx)
	go func() {
		if err := b.Serve(bridgeCtx, ln); err != nil && !errors.Is(err, context.Canceled) {
			log.Warn("acp bridge stopped", "error", err)
		}
	}()
	log.Info("acp bridge listening", "addr", ln.Addr().String(), "agent", cfg.API.ACPCommand[0])
	return func() { cancel(); _ = ln.Close() }, nil
}
