// Package doctor implements `le doctor` (design v3 §3.4, §4.4, §5.4, §6.1,
// §9.2). It is the single place that answers "what is actually in effect here",
// and DR-3 names it explicitly: "three layers with `le doctor` reporting which
// are active".
//
// Every check returns a status and, when not OK, a reason a human can act on.
// A check that cannot run says so rather than reporting OK.
package doctor

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/config"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/sandbox"
	"github.com/akynte/local-engineer/internal/sandbox/bwrap"
	"github.com/akynte/local-engineer/internal/sandbox/landlock"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/version"
	"github.com/akynte/local-engineer/internal/workspace"
)

// Level is a check outcome.
type Level string

const (
	OK      Level = "ok"
	Warn    Level = "warn"
	Fail    Level = "fail"
	Skipped Level = "skipped"
)

// Check is one diagnostic result.
type Check struct {
	Name   string `json:"name"`
	Level  Level  `json:"level"`
	Detail string `json:"detail"`
	// Fix is the action an operator should take. Empty when Level is OK.
	Fix string `json:"fix,omitempty"`
}

// Report is the full diagnostic output.
type Report struct {
	Version version.Info    `json:"version"`
	Checks  []Check         `json:"checks"`
	Sandbox *sandbox.Report `json:"sandbox,omitempty"`
	RanAt   time.Time       `json:"ran_at"`
}

// Worst returns the most severe level in the report.
func (r Report) Worst() Level {
	worst := OK
	for _, c := range r.Checks {
		switch c.Level {
		case Fail:
			return Fail
		case Warn:
			worst = Warn
		}
	}
	return worst
}

// Options selects what to check. Nil fields are skipped with a reason.
type Options struct {
	Config    *config.Config
	Profile   *config.Profile
	Root      *store.Root
	Workspace *workspace.Workspace
	// Deep runs PRAGMA integrity_check, which is slow on a large index (§5.4).
	Deep bool
}

// Run executes every applicable check.
func Run(ctx context.Context, opts Options) Report {
	rep := Report{Version: version.Current(), RanAt: time.Now().UTC()}
	add := func(c Check) { rep.Checks = append(rep.Checks, c) }

	add(checkContainer())
	sbReport, sandboxChecks := checkSandbox(ctx, opts.Config)
	rep.Sandbox = sbReport
	for _, c := range sandboxChecks {
		add(c)
	}
	add(checkFilesystem(opts.Root))
	for _, c := range checkProfile(ctx, opts.Profile) {
		add(c)
	}
	for _, c := range checkWorkspace(ctx, opts) {
		add(c)
	}
	return rep
}

// checkContainer reports DR-3 layer 1. Outside a container that layer is not
// in effect, and saying so is the point: §6.2's guarantee table assumes it.
func checkContainer() Check {
	in, why := sandbox.InContainer()
	if in {
		return Check{Name: "container boundary (DR-3 layer 1)", Level: OK, Detail: why}
	}
	return Check{
		Name:   "container boundary (DR-3 layer 1)",
		Level:  Warn,
		Detail: why,
		Fix: "The documented default is a container (§4.1). Host installs are supported for development " +
			"via scripts/install-bare-metal.sh (§13), but the host-isolation guarantees of §6.2 rest on " +
			"layer 1 and do not apply without it: nothing bounds the supervisor to the repositories you meant.",
	}
}

func checkSandbox(ctx context.Context, cfg *config.Config) (*sandbox.Report, []Check) {
	var checks []Check
	mode := "auto"
	if cfg != nil && cfg.Sandbox.Mode != "" {
		mode = cfg.Sandbox.Mode
	}

	llRunner, llErr := landlock.New()
	var candidates []sandbox.Runner
	switch mode {
	case "none":
		candidates = []sandbox.Runner{sandbox.ContainerRunner{}}
	case "landlock":
		if llErr == nil {
			candidates = append(candidates, llRunner)
		}
		candidates = append(candidates, sandbox.ContainerRunner{})
	case "bwrap":
		if llErr == nil {
			candidates = append(candidates, bwrap.New(llRunner))
		}
		candidates = append(candidates, sandbox.ContainerRunner{})
	default: // auto: strongest first
		if llErr == nil {
			candidates = append(candidates, bwrap.New(llRunner), llRunner)
		}
		candidates = append(candidates, sandbox.ContainerRunner{})
	}
	_, rep := sandbox.Select(ctx, candidates)

	// Landlock ABI, reported as a number because §6.1's network rules need 4+.
	if abi, err := landlock.ABI(); err != nil {
		checks = append(checks, Check{
			Name: "landlock (DR-3 layer 2)", Level: Warn, Detail: err.Error(),
			Fix: "If this is a container, confirm the runtime's seccomp profile permits landlock_create_ruleset, " +
				"landlock_add_rule and landlock_restrict_self (§16 verify item 1).",
		})
	} else {
		lvl, detail := OK, fmt.Sprintf("ABI %d", abi)
		if ok, reason := landlock.SupportsNetwork(); !ok {
			lvl, detail = Warn, detail+"; "+reason
		} else {
			detail += "; TCP rules enforced"
		}
		checks = append(checks, Check{Name: "landlock (DR-3 layer 2)", Level: lvl, Detail: detail,
			Fix: map[Level]string{Warn: "Port restrictions degrade to best-effort. Egress control then rests on the " +
				"container's network configuration alone: run with --network none, or on a user-defined bridge " +
				"reaching only the inference endpoint (§6.1). The allowlisting proxy §6.1 describes is not built."}[lvl]})
	}

	// Bubblewrap, the optional layer.
	bw := bwrap.New(nil)
	if ok, reason := bw.Available(ctx); ok {
		checks = append(checks, Check{Name: "bubblewrap (DR-3 layer 3)", Level: OK,
			Detail: "available: mount and PID namespaces active"})
	} else {
		checks = append(checks, Check{Name: "bubblewrap (DR-3 layer 3)", Level: Warn, Detail: reason,
			Fix: "Optional. Without it, concurrent tasks share a PID view (§6.2). " +
				"Run the container with --security-opt seccomp=unconfined --security-opt apparmor=unconfined to enable it."})
	}

	// Multipath TCP is a documented hole, not a bug; report it every time so
	// nobody builds a guarantee on top of the port rules alone.
	checks = append(checks, Check{
		Name: "network containment", Level: Warn,
		Detail: "Landlock TCP rules do not cover Multipath TCP sockets, and Go's net.Listen uses MPTCP by default. " +
			"Port rules are therefore augmented, not relied on alone (§6.1).",
		Fix: "Use --network none plus an in-container inference route for fully offline mode. " +
			"Provisioning goes through the §6.1 allowlisting proxy, which a task sandbox cannot reach.",
	})

	checks = append(checks, checkEgress(cfg))

	return &rep, checks
}

// checkEgress reports the state of §6.1's allowlisting proxy.
//
// Three states worth distinguishing, because they are three different
// machines: no egress at all, egress through an allowlist, and an allowlist
// wide enough not to be one.
func checkEgress(cfg *config.Config) Check {
	if cfg == nil {
		return Check{Name: "egress proxy (§6.1)", Level: Skipped, Detail: "no configuration loaded"}
	}
	if cfg.Offline {
		return Check{Name: "egress proxy (§6.1)", Level: OK,
			Detail: "offline: no route out, and the provisioning lanes do not exist"}
	}
	if !cfg.Egress.Enabled {
		return Check{Name: "egress proxy (§6.1)", Level: OK,
			Detail: "disabled: nothing in this container has a provisioned route out",
			Fix: "A task sandbox never gets egress either way. Enable egress.enabled in le.yaml " +
				"only if you need `le deps` or `le docs` to fetch; review egress.allowlist first."}
	}
	if err := cfg.Egress.Allowlist.Validate(); err != nil {
		return Check{Name: "egress proxy (§6.1)", Level: Fail, Detail: err.Error(),
			Fix: "Fix egress.allowlist in le.yaml, or set egress.enabled to false."}
	}
	rules := cfg.Egress.Allowlist.Rules
	var wildcards int
	for _, r := range rules {
		if strings.HasPrefix(strings.TrimSpace(r.Host), "*.") {
			wildcards++
		}
	}
	detail := fmt.Sprintf("enabled: deps on 127.0.0.1:%d, docs on 127.0.0.1:%d, %d allowlist rule(s)",
		cfg.Egress.DepsPort, cfg.Egress.DocsPort, len(rules))
	if wildcards > 0 {
		// Not a fault, but the thing an operator should look at twice: a
		// wildcard is the entry most likely to be broader than intended.
		return Check{Name: "egress proxy (§6.1)", Level: Warn,
			Detail: fmt.Sprintf("%s, %d of them wildcards", detail, wildcards),
			Fix:    "Check that each `*.` rule is as narrow as you meant. Exact hosts are preferred."}
	}
	return Check{Name: "egress proxy (§6.1)", Level: OK, Detail: detail}
}

// checkFilesystem covers §5.4: SQLite needs a real filesystem with working
// fsync; the volume must not be an overlay layer or a network share.
func checkFilesystem(root *store.Root) Check {
	if root == nil {
		return Check{Name: "data directory", Level: Skipped, Detail: "no data directory open"}
	}
	dir := root.Layout().Root()
	fsType, err := filesystemType(dir)
	if err != nil {
		return Check{Name: "data directory", Level: Warn,
			Detail: fmt.Sprintf("%s: could not determine the filesystem type: %v", dir, err)}
	}
	switch fsType {
	case "overlayfs", "overlay", "aufs":
		return Check{Name: "data directory", Level: Fail,
			Detail: fmt.Sprintf("%s is on %s, a container overlay layer", dir, fsType),
			Fix: "Mount a named volume or a bind mount at /data. SQLite requires a real filesystem " +
				"with working fsync; an overlay layer also loses the data on container removal (§5.4)."}
	case "nfs", "nfs4", "cifs", "smb3", "fuse.sshfs":
		return Check{Name: "data directory", Level: Fail,
			Detail: fmt.Sprintf("%s is on %s, a network filesystem", dir, fsType),
			Fix:    "SQLite locking is unreliable on network shares. Use a local disk or a named volume (§5.4)."}
	}
	return Check{Name: "data directory", Level: OK, Detail: fmt.Sprintf("%s on %s", dir, fsType)}
}

// checkProfile covers §9.2: warn when the active profile's measured memory
// does not fit the host.
func checkProfile(ctx context.Context, p *config.Profile) []Check {
	if p == nil {
		return []Check{{Name: "hardware profile", Level: Warn,
			Detail: "no profile loaded; conservative fallback values are in force",
			Fix:    "Run `le models bench` to measure this machine and write a profile (§9.2)."}}
	}
	checks := []Check{{Name: "hardware profile", Level: OK,
		Detail: fmt.Sprintf("%s: context %d, packet cap %d", p.Name, p.ContextTokens, p.MaxPacketTokens)}}

	vram, ram := hostMemory(ctx)
	if ok, why := p.FitsHost(vram, ram); !ok {
		checks = append(checks, Check{Name: "profile fits host", Level: Warn, Detail: why,
			Fix: "Choose a smaller profile, or re-run `le models bench` on this machine (§9.2)."})
	} else if p.Measured == nil {
		checks = append(checks, Check{Name: "profile fits host", Level: Warn,
			Detail: "the profile carries no measurement, so admission limits are estimates",
			Fix:    "Run `le models bench` to replace the estimates with measured peak memory."})
	} else {
		checks = append(checks, Check{Name: "profile fits host", Level: OK,
			Detail: fmt.Sprintf("measured peak %d MB VRAM, %d MB RAM", p.PeakVRAMMB, p.PeakRAMMB)})
	}
	return checks
}

func checkWorkspace(ctx context.Context, opts Options) []Check {
	if opts.Root == nil || opts.Workspace == nil {
		return []Check{{Name: "workspace", Level: Skipped,
			Detail: "not run inside a workspace; `cd` to a repository with .le/workspace.yaml"}}
	}
	ws := opts.Workspace
	checks := []Check{{Name: "workspace", Level: OK,
		Detail: fmt.Sprintf("%s (%s) at %s", ws.Name(), ws.ID(), ws.Root)}}

	if ws.Moved() {
		checks = append(checks, Check{Name: "workspace location", Level: Warn,
			Detail: fmt.Sprintf("the id was derived at %s but the workspace is now at %s",
				ws.Manifest.DerivedFrom.CanonicalRoot, ws.Root),
			Fix: "Run `le workspace adopt` to re-bind the pinned id to this location (§2.1)."})
	}

	st, err := opts.Root.OpenWorkspace(ctx, ws.ID())
	if err != nil {
		return append(checks, Check{Name: "workspace storage", Level: Fail, Detail: err.Error()})
	}

	// §5.4: integrity check on startup, full check on demand.
	for _, db := range st.DBs() {
		if !opts.Deep {
			continue
		}
		problems, err := db.IntegrityCheck(ctx)
		switch {
		case err != nil:
			checks = append(checks, Check{Name: "integrity: " + db.Name(), Level: Fail, Detail: err.Error()})
		case len(problems) > 0:
			checks = append(checks, Check{Name: "integrity: " + db.Name(), Level: Fail,
				Detail: strings.Join(problems, "; "),
				Fix:    "Restore the most recent backup: `le restore --from /data/backups/<timestamp>`."})
		default:
			checks = append(checks, Check{Name: "integrity: " + db.Name(), Level: OK, Detail: "integrity_check ok"})
		}
	}

	// §3.4: index age and drift.
	checks = append(checks, checkIndexFreshness(ctx, st))

	// Leases left behind by a crashed instance (§7.2 step 4).
	checks = append(checks, checkLeases(ctx, st))
	return checks
}

func checkIndexFreshness(ctx context.Context, st *store.Store) Check {
	var updatedAt, dirty int64
	err := st.Index().SQL().QueryRowContext(ctx,
		`SELECT COALESCE(MAX(updated_at),0), COALESCE(SUM(dirty),0) FROM index_keys`).Scan(&updatedAt, &dirty)
	if err != nil {
		return Check{Name: "index freshness", Level: Fail, Detail: err.Error()}
	}
	if updatedAt == 0 {
		return Check{Name: "index freshness", Level: Warn, Detail: "nothing indexed yet",
			Fix: "Run `le index` to build the source index and graph."}
	}
	age := time.Since(time.Unix(updatedAt, 0)).Round(time.Minute)
	g := graph.New(st)
	stats, err := g.Stats(ctx)
	if err != nil {
		return Check{Name: "index freshness", Level: Warn, Detail: err.Error()}
	}
	detail := fmt.Sprintf("last indexed %s ago; %d nodes, %d edges", age, stats.Nodes, stats.Edges)
	if dirty > 0 {
		return Check{Name: "index freshness", Level: Warn,
			Detail: fmt.Sprintf("%s; %d units marked dirty", detail, dirty),
			Fix:    "Dirty units are re-analysed before any step that needs the graph (§3.4). Run `le index` to do it now."}
	}
	return Check{Name: "index freshness", Level: OK, Detail: detail}
}

func checkLeases(ctx context.Context, st *store.Store) Check {
	rows, err := st.Ledger().SQL().QueryContext(ctx,
		`SELECT worktree_id, COALESCE(holder,''), COALESCE(expires_at,0) FROM leases`)
	if err != nil {
		return Check{Name: "worktree leases", Level: Fail, Detail: err.Error()}
	}
	defer rows.Close()
	var expired, live int
	var details []string
	now := time.Now().UnixMilli()
	for rows.Next() {
		var wt, holder string
		var exp int64
		if err := rows.Scan(&wt, &holder, &exp); err != nil {
			return Check{Name: "worktree leases", Level: Fail, Detail: err.Error()}
		}
		if exp < now {
			expired++
			details = append(details, fmt.Sprintf("%s held by %s, expired", wt, holder))
		} else {
			live++
		}
	}
	if expired > 0 {
		return Check{Name: "worktree leases", Level: Warn,
			Detail: fmt.Sprintf("%d expired, %d live: %s", expired, live, strings.Join(details, "; ")),
			Fix:    "An expired lease is reclaimed automatically after reconciliation (§7.2 step 4)."}
	}
	return Check{Name: "worktree leases", Level: OK, Detail: fmt.Sprintf("%d live", live)}
}

// hostMemory reports (VRAM MB, RAM MB). Zero means unknown, and callers treat
// unknown as "cannot check" rather than "fits".
func hostMemory(ctx context.Context) (vramMB, ramMB int) {
	if body, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if kb, err := strconv.Atoi(fields[1]); err == nil {
						ramMB = kb / 1024
					}
				}
				break
			}
		}
	}
	vramMB = queryVRAM(ctx)
	return vramMB, ramMB
}

// Format renders a report as aligned text for the terminal.
func (r Report) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", r.Version)
	fmt.Fprintf(&b, "%s/%s, %d CPUs\n\n", runtime.GOOS, runtime.GOARCH, runtime.NumCPU())

	width := 0
	for _, c := range r.Checks {
		if len(c.Name) > width {
			width = len(c.Name)
		}
	}
	symbol := map[Level]string{OK: "ok  ", Warn: "warn", Fail: "FAIL", Skipped: "skip"}
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "[%s] %-*s  %s\n", symbol[c.Level], width, c.Name, c.Detail)
		if c.Fix != "" {
			for _, line := range wrap(c.Fix, 76) {
				fmt.Fprintf(&b, "       %-*s  %s\n", width, "", line)
			}
		}
	}
	if r.Sandbox != nil {
		fmt.Fprintf(&b, "\n%s\n", r.Sandbox.Statement)
	}
	return b.String()
}

func wrap(s string, width int) []string {
	words := strings.Fields(s)
	var lines []string
	var cur string
	for _, w := range words {
		if cur == "" {
			cur = w
			continue
		}
		if len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur += " " + w
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
