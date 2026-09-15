package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/config"
	"github.com/akynte/local-engineer/internal/proxy"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/store"
)

// The provisioning lanes of design v3 §6.1.
//
// These are the only commands that reach the network on a task's behalf, and
// they are deliberately not part of a task. §6.1 says the proxy is "never for
// a task sandbox", so acquiring a dependency is something an operator does
// between tasks, with the diff to `go.mod` visible before any task runs
// against it.

func newDepsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deps",
		Short: "Fetch dependencies through the allowlisting proxy (§6.1)",
		Long: "The deps lane is a confined process that may reach the egress proxy and\n" +
			"nothing else — not the inference endpoint, not a test port. What the proxy\n" +
			"will connect to is the allowlist in le.yaml, and every decision is reported.\n\n" +
			"This is not part of a task. A task sandbox has no egress at all, so a change\n" +
			"that needs a new dependency is an operator fetching it first, with the go.mod\n" +
			"diff visible, and the task then verifying offline against what is present.",
	}
	cmd.AddCommand(newDepsSyncCmd(), newLaneRunCmd(proxy.LaneDeps))
	return cmd
}

func newDocsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "Fetch documentation through the allowlisting proxy (§6.1)",
		Long: "The docs lane reaches only the documentation hosts in the allowlist, and\n" +
			"writes what it fetches to stdout or a file. It is separate from the deps lane\n" +
			"so that a documentation host cannot be used to fetch code.",
	}
	cmd.AddCommand(newDocsFetchCmd(), newLaneRunCmd(proxy.LaneDocs))
	return cmd
}

// laneContext gathers everything a lane needs from configuration.
type laneContext struct {
	cfg   config.Config
	root  *store.Root
	lane  proxy.Lane
	port  int
	dir   string
	tmp   string
	cache string
}

func openLane(_ *cobra.Command, lane proxy.Lane) (*laneContext, func(), error) {
	root, err := openRoot()
	if err != nil {
		return nil, nil, err
	}
	closeRoot := func() {}
	cfg, err := loadConfig(root)
	if err != nil {
		closeRoot()
		return nil, nil, err
	}
	if cfg.Offline {
		closeRoot()
		return nil, nil, errors.New(
			"offline is set in le.yaml: there is no route out, so the provisioning lanes do not exist (§6.1)")
	}
	if !cfg.Egress.Enabled {
		closeRoot()
		return nil, nil, errors.New(
			"egress.enabled is false in le.yaml. The allowlisting proxy is off by default;\n" +
				"turn it on and review egress.allowlist before a lane can reach anything")
	}
	port := cfg.Egress.DepsPort
	if lane == proxy.LaneDocs {
		port = cfg.Egress.DocsPort
	}

	cwd, err := os.Getwd()
	if err != nil {
		closeRoot()
		return nil, nil, err
	}
	// The lane's caches live in the data directory, created by the store — a
	// lane never writes outside $LE_DATA except in its own working directory.
	// They are shared rather than per-workspace because a fetched module is
	// not workspace state: two projects needing the same version should not
	// download it twice, and a downloaded dependency says nothing about who
	// asked for it.
	cache, tmp, err := root.Layout().EnsureProvisioning(string(lane))
	if err != nil {
		closeRoot()
		return nil, nil, err
	}
	return &laneContext{
		cfg: cfg, root: root, lane: lane, port: port,
		dir: cwd, tmp: tmp, cache: cache,
	}, closeRoot, nil
}

func (lc *laneContext) spec() proxy.Spec {
	_, ro := recipe.GoSandboxPaths("", "", lc.tmp)
	return proxy.Spec{
		Lane:      lc.lane,
		ProxyPort: lc.port,
		Dir:       lc.dir,
		ReadWrite: []string{lc.cache},
		ReadOnly:  append(ro, lc.cfg.Sandbox.ReadOnlyPaths...),
		TmpDir:    lc.tmp,
		Env: []string{
			"HOME=" + lc.tmp,
			"PATH=/usr/local/go/bin:/opt/le/gotools/bin:/usr/local/bin:/usr/bin:/bin",
			"GOMODCACHE=" + filepath.Join(lc.cache, "go-mod"),
			"GOCACHE=" + filepath.Join(lc.cache, "go-build"),
			"GOTMPDIR=" + lc.tmp,
			"TMPDIR=" + lc.tmp,
			"NO_COLOR=1",
			"TERM=dumb",
		},
	}
}

func (lc *laneContext) runner(cmd *cobra.Command) (*proxy.LaneRunner, error) {
	sb, _ := selectSandbox(cmd.Context(), lc.cfg)
	if sb == nil {
		return nil, errors.New("no sandbox runner is available; a provisioning lane must be confined (§6.1)")
	}
	return &proxy.LaneRunner{
		Sandbox: sb,
		Timeout: 15 * time.Minute,
		Logf: func(f string, a ...any) {
			fmt.Fprintf(os.Stderr, f+"\n", a...)
		},
	}, nil
}

func newDepsSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Download the modules go.mod already requires",
		Long: "sync runs `go mod download` inside the deps lane. It downloads what go.mod\n" +
			"already names; it does not add a dependency, because adding one is an edit\n" +
			"and edits belong to a task.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			lc, done, err := openLane(cmd, proxy.LaneDeps)
			if err != nil {
				return err
			}
			defer done()
			if _, err := os.Stat(filepath.Join(lc.dir, "go.mod")); err != nil {
				return fmt.Errorf("no go.mod here: %s is not a Go module", lc.dir)
			}
			r, err := lc.runner(cmd)
			if err != nil {
				return err
			}
			res, err := r.Run(cmd.Context(), lc.spec(), "go", "mod", "download", "all")
			if err != nil {
				return err
			}
			return reportLane(cmd.OutOrStdout(), res)
		},
	}
}

func newDocsFetchCmd() *cobra.Command {
	var out string
	c := &cobra.Command{
		Use:   "fetch <url>",
		Short: "Fetch one documentation URL through the docs lane",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lc, done, err := openLane(cmd, proxy.LaneDocs)
			if err != nil {
				return err
			}
			defer done()
			r, err := lc.runner(cmd)
			if err != nil {
				return err
			}
			argv := []string{"curl", "-fsSL", "--proto", "=https", args[0]}
			if out != "" {
				argv = append(argv, "-o", out)
			}
			res, err := r.Run(cmd.Context(), lc.spec(), argv...)
			if err != nil {
				return err
			}
			if res.ExitCode == 0 && out == "" {
				fmt.Fprint(cmd.OutOrStdout(), res.Stdout)
				return nil
			}
			return reportLane(cmd.OutOrStdout(), res)
		},
	}
	c.Flags().StringVar(&out, "output", "", "write the body to this file instead of stdout")
	return c
}

// newLaneRunCmd is the escape hatch: an arbitrary command in the lane.
//
// It exists because the set of package managers is open and hardcoding each
// one would be a worse version of this. It is still confined and still
// allowlisted — the lane is the boundary, not the command.
func newLaneRunCmd(lane proxy.Lane) *cobra.Command {
	return &cobra.Command{
		Use:   "run -- <command> [args...]",
		Short: "Run a command inside the " + string(lane) + " lane",
		Long: "The command runs confined, with the proxy as its only route out. It can\n" +
			"reach exactly the hosts the allowlist names for this lane, and the proxy\n" +
			"reports every host it refused.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lc, done, err := openLane(cmd, lane)
			if err != nil {
				return err
			}
			defer done()
			r, err := lc.runner(cmd)
			if err != nil {
				return err
			}
			res, err := r.Run(cmd.Context(), lc.spec(), args...)
			if err != nil {
				return err
			}
			return reportLane(cmd.OutOrStdout(), res)
		},
	}
}

func reportLane(w io.Writer, res proxy.Result) error {
	if res.Stdout != "" {
		fmt.Fprint(w, res.Stdout)
	}
	if res.Stderr != "" {
		fmt.Fprint(os.Stderr, res.Stderr)
	}
	if res.ExitCode != 0 {
		// The most common cause of a failed fetch is a host the allowlist does
		// not name, and the proxy's log is where that shows. Say so rather
		// than leaving an exit code.
		return fmt.Errorf("%s exited %d in the %s lane; "+
			"if it could not reach a host, check the supervisor log for \"egress refused\" "+
			"and add the host to egress.allowlist with a reason",
			res.Argv[0], res.ExitCode, res.Lane)
	}
	return nil
}
