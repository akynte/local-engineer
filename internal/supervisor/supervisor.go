// Package supervisor assembles the task runner that governs a piece of work.
//
// A runner is not a constructor call. It carries the sandbox verification runs
// in, the repository's protected paths, the gate policy, the analyzers a
// freshness check re-runs, and the environment a build is given — and every one
// of those is a control rather than a setting. A second assembly that forgot the
// sandbox would still compile, still run, and would run unconfined.
//
// It lived in cmd/le, which meant the completion contract was reachable only
// from a terminal. That was fine while the CLI was the only interface. It stopped
// being fine when an editor needed to put work under the same contract: the
// choice was to duplicate two hundred lines of security-relevant wiring, or to
// move it somewhere both callers could reach. This is the second.
package supervisor

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/akynte/local-engineer/internal/analyzers/architecture"
	"github.com/akynte/local-engineer/internal/analyzers/deploy"
	"github.com/akynte/local-engineer/internal/analyzers/gitlog"
	"github.com/akynte/local-engineer/internal/analyzers/golang"
	"github.com/akynte/local-engineer/internal/analyzers/protoavro"
	sqlan "github.com/akynte/local-engineer/internal/analyzers/sql"
	"github.com/akynte/local-engineer/internal/analyzers/terraform"
	"github.com/akynte/local-engineer/internal/analyzers/typescript"
	"github.com/akynte/local-engineer/internal/broker"
	"github.com/akynte/local-engineer/internal/config"
	"github.com/akynte/local-engineer/internal/critic"
	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/index"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/policy"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/sandbox"
	"github.com/akynte/local-engineer/internal/sandbox/bwrap"
	"github.com/akynte/local-engineer/internal/sandbox/landlock"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/task"
)

// Options are what differs between callers. Everything else is loaded here, so
// two interfaces cannot end up with two different sets of controls.
type Options struct {
	// RepoRoot is the repository whose policies apply and whose graph a
	// freshness check re-analyses.
	RepoRoot string
	// Logf reports progress. Nil discards it.
	Logf func(format string, args ...any)
	// Warnf reports analyzer problems. Nil discards them.
	Warnf func(format string, args ...any)
}

// Runner builds a task runner with every control in place.
//
// It refuses rather than degrades when no sandbox is available. Verification
// runs commands chosen by a model over the operator's code; running those
// unconfined because confinement was unavailable is the one outcome worth
// failing for.
func Runner(ctx context.Context, root *store.Root, st *store.Store, eng engine.Engine, o Options) (*task.Runner, error) {
	logf := o.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	warnf := o.Warnf
	if warnf == nil {
		warnf = func(string, ...any) {}
	}

	cfg, _ := Config(root)
	sb, report := SelectSandbox(ctx, cfg)
	if sb == nil {
		return nil, fmt.Errorf("no sandbox runner is available; verification must not run unconfined")
	}
	logf("sandbox: %s (%v)", report.Runner, report.Active)

	holder, _ := os.Hostname()
	r, err := task.NewRunner(st, eng, sb, fmt.Sprintf("%s/%d", holder, os.Getpid()))
	if err != nil {
		return nil, err
	}
	r.Logf = logf
	r.Broker = broker.New(st, GatePolicy(cfg.Gates))

	// Repository-wide rules (§6.2), loaded from the repository being worked on
	// rather than from the data directory: a rule about what may not change
	// belongs beside the thing it protects.
	policies, err := policy.Load(filepath.Join(o.RepoRoot, "policies"))
	if err != nil {
		return nil, fmt.Errorf("the repository's policies are invalid: %w", err)
	}
	if len(policies.Policies) > 0 {
		logf("policies: %d rule(s) protecting %d path pattern(s)",
			len(policies.Policies), len(policies.Paths()))
	}
	r.Policies = policies

	// §3.4: a repository the watcher has marked dirty is re-analysed before a
	// step consults the graph, with the analyzers a full index would use.
	r.Freshener = index.New(st, index.Options{
		MaxFileBytes: cfg.Index.MaxFileBytes,
		Excludes:     cfg.Index.Excludes,
		ChunkLines:   cfg.Index.ChunkLines,
		Analyzers:    Analyzers(warnf),
	})

	// §10.1's out-of-conversation calls need structured output, so a provider
	// that cannot constrain its answers does not get them: DR-4 refuses rather
	// than degrading, and a review parsed out of prose loses concerns silently.
	if provider, err := ReviewProvider(root); err == nil && provider != nil &&
		provider.Capabilities().StructuredOutput {
		r.Critic = &critic.Critic{Provider: provider, MaxTokens: 2048, Temperature: 0.1}
		if profile := Profile(root, cfg); profile != nil {
			r.Critic.Thinking = profile.Thinking
			r.Critic.MaxTokens = profile.ReservedOutput
		}
	} else if err == nil && provider != nil {
		logf("review and diagnosis are off: %s does not declare structured output", provider.Name())
	}

	dirs, err := st.TaskDirs()
	if err != nil {
		return nil, err
	}
	r.SandboxSpec = sandbox.Spec{
		ReadOnly: cfg.Sandbox.ReadOnlyPaths,
		TmpDir:   dirs.Tmp,
		Env:      recipe.GoEnv(dirs.GoBuildCache, dirs.GoModCache, dirs.Tmp),
		// A test suite binds port 0 and connects to whatever the kernel
		// returns, so no allowlist can name those ports in advance. The range
		// holds no services, and TCPDeny keeps it that way.
		AllowEphemeralTCP: true,
		TCPDeny:           ServicePorts(cfg),
	}
	for _, port := range cfg.Sandbox.AllowedTCPConnect {
		r.SandboxSpec.TCPConnect = append(r.SandboxSpec.TCPConnect, uint16(port)) //nolint:gosec // operator-configured port
	}
	if cfg.Inference.Mode == config.ModeEmbedded {
		r.SandboxSpec.TCPConnect = append(r.SandboxSpec.TCPConnect, uint16(cfg.Inference.Port)) //nolint:gosec // operator-configured port
	}
	return r, nil
}

// Config loads the operator configuration.
func Config(root *store.Root) (config.Config, error) {
	return config.Load(root.Layout().ConfigDir())
}

// Profile loads the active hardware profile, or nil when none is set or it
// cannot be read. A missing profile is a degraded default, not a failure.
func Profile(root *store.Root, cfg config.Config) *config.Profile {
	if cfg.Profile == "" {
		return nil
	}
	p, err := config.LoadProfile(filepath.Join(root.Layout().ConfigDir(), "profiles"), cfg.Profile)
	if err != nil {
		return nil
	}
	return &p
}

// SelectSandbox picks the strongest confinement the host actually permits.
func SelectSandbox(ctx context.Context, cfg config.Config) (sandbox.Runner, sandbox.Report) {
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
	return sandbox.Select(ctx, candidates)
}

// GatePolicy turns gate configuration into the broker's policy.
func GatePolicy(g config.GateConfig) broker.Policy {
	return broker.Policy{
		RequireForBreaking:   g.Breaking,
		RequireForOutOfScope: g.OutOfScope,
		RequireForApply:      g.Apply,
		RequireForPlan:       g.Plan,
		Timeout:              time.Duration(g.TimeoutMinutes) * time.Minute,
	}
}

// ReviewProvider returns the provider routed to the review role, or nil when
// none is configured. No providers is not a fault: review is an addition.
func ReviewProvider(root *store.Root) (llm.Provider, error) {
	cfg, err := Config(root)
	if err != nil {
		return nil, err
	}
	f, err := llm.LoadProvidersFile(root.Layout().ConfigDir())
	if err != nil {
		return nil, nil //nolint:nilerr // no providers configured is not a fault
	}
	router, err := llm.NewRouter(f, cfg.Offline)
	if err != nil {
		return nil, err
	}
	return router.For(llm.RoleReview)
}

// ServicePorts lists the ports this installation's own services listen on, so
// a sandbox can deny them even inside the ephemeral range.
func ServicePorts(cfg config.Config) []uint16 {
	var out []uint16
	add := func(p int) {
		if p > 0 && p <= 65535 {
			out = append(out, uint16(p)) //nolint:gosec // bounds checked above
		}
	}
	if _, portStr, err := net.SplitHostPort(cfg.API.Addr); err == nil {
		if p, err := strconv.Atoi(portStr); err == nil {
			add(p)
		}
	}
	add(cfg.Inference.Port)
	add(cfg.Egress.DepsPort)
	add(cfg.Egress.DocsPort)
	return out
}

// Analyzers builds the language and infrastructure analyzers.
func Analyzers(warnf func(string, ...any)) []index.Analyzer {
	goa := golang.New()
	goa.Warnf = warnf
	sqla := sqlan.New()
	sqla.Warnf = warnf
	dep := deploy.New()
	dep.Warnf = warnf
	tf := terraform.New()
	tf.Warnf = warnf
	gitl := gitlog.New()
	gitl.Warnf = warnf
	ts := typescript.New()
	ts.Warnf = warnf
	// The sidecar gives compiler-backed edges when installed; without it the
	// lexical reading runs and its edges say they are weaker.
	ts.UseSidecar = true
	pa := protoavro.New()
	pa.Warnf = warnf
	arch := architecture.New()
	arch.Warnf = warnf
	return []index.Analyzer{goa, ts, sqla, pa, dep, tf, arch, gitl}
}
