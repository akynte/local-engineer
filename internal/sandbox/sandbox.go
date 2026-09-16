// Package sandbox implements the layered isolation of design v3 §6 and DR-3.
//
// Three layers, each documented with what it does and does not guarantee:
//
//  1. the container boundary (always),
//  2. Landlock per task (default inside the container),
//  3. bubblewrap per task (optional, when user namespaces are available).
//
// `le doctor` reports which layers are active. Nothing here claims a guarantee
// a layer does not provide: the guarantee table of §6.2 is encoded in
// Guarantees() so the honest statement and the code cannot drift apart.
package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
)

// Layer names the three layers of §6.1.
type Layer string

const (
	LayerContainer Layer = "container"
	LayerLandlock  Layer = "landlock"
	LayerBwrap     Layer = "bwrap"
)

// Spec describes one task's sandbox. Paths are absolute and already resolved
// by the caller; the runner never composes paths itself.
type Spec struct {
	// ReadOnly are toolchain and library paths the task may read.
	ReadOnly []string
	// ReadWrite are the task worktree, its tmp and its caches.
	ReadWrite []string
	// TCPConnect are the ports the task may dial: the inference proxy and
	// assigned test-service ports, and nothing else.
	TCPConnect []uint16
	// TCPBind are ports the task may listen on (test servers).
	TCPBind []uint16
	// AllowEphemeralTCP grants bind and connect on the host's ephemeral port
	// range, which a test suite needs and no allowlist can predict.
	//
	// Landlock's network rules name one port each — the kernel's rule struct
	// carries a single port, so a range cannot be expressed — and a server
	// bound to port 0 gets whatever the kernel picks. That is how every Go
	// test that uses httptest works, so without this the verification of any
	// repository with HTTP tests fails with "connect: permission denied" on a
	// port nobody chose. On this repository that was 22 tests across three
	// packages, failing regardless of the change under test.
	//
	// It does not weaken the guarantee §6.2 actually makes. The range holds no
	// services: by convention and by IANA's dynamic-port assignment, a service
	// listens below it, so the inference endpoint, the supervisor API and the
	// egress proxy stay denied. TCPDeny covers the case where an operator has
	// moved one into the range anyway.
	AllowEphemeralTCP bool
	// TCPDeny are ports excluded from the AllowEphemeralTCP grant: the
	// system's own service ports, in case an operator configured one inside
	// the ephemeral range. It has no effect on TCPConnect and TCPBind, which
	// are deliberate grants.
	TCPDeny []uint16
	// Env is the child's environment. XDG variables are set per task so that
	// OpenCode never sees another workspace's directories (§2.2).
	Env []string
	// Dir is the working directory.
	Dir string
	// TmpDir is the per-workspace tmp the sandbox exposes as the only tmp.
	TmpDir string
}

// Validate rejects a spec that would sandbox nothing, which is the failure
// mode most likely to pass unnoticed.
func (s Spec) Validate() error {
	if len(s.ReadWrite) == 0 {
		return fmt.Errorf("sandbox: spec grants no writable path; a task needs at least its worktree")
	}
	if s.Dir == "" {
		return fmt.Errorf("sandbox: spec has no working directory")
	}
	return nil
}

// Runner applies a sandbox to a child process. DR-3 keeps this an interface so
// a gVisor or microVM runner can be added for untrusted repositories without
// touching callers.
type Runner interface {
	// Name identifies the runner in `le doctor` output and in the journal.
	Name() string
	// Layers reports which layers this runner actually applies.
	Layers() []Layer
	// Command builds a sandboxed exec.Cmd. The command is not started.
	Command(ctx context.Context, spec Spec, argv ...string) (*exec.Cmd, error)
	// Available reports whether this runner can work on this host, and why not
	// when it cannot. The reason is user-visible: "unavailable" without a
	// reason is useless in a support conversation.
	//
	// It takes a context because probing can mean spawning a process, and a
	// probe that hangs would hang `le doctor` — the command people run when
	// something is already wrong.
	Available(ctx context.Context) (bool, string)
}

// Guarantee is one row of the §6.2 table.
type Guarantee struct {
	Statement string `json:"statement"`
	Container bool   `json:"container_only"`
	Landlock  bool   `json:"with_landlock"`
	Bwrap     bool   `json:"with_bwrap"`
	// Note carries the qualification the table states in prose.
	Note string `json:"note,omitempty"`
}

// Guarantees returns the §6.2 table verbatim. `le doctor` prints the rows for
// the layers actually active, so the product never claims more than it does.
func Guarantees() []Guarantee {
	return []Guarantee{
		{Statement: "Cannot touch host files outside mounts", Container: true, Landlock: true, Bwrap: true},
		{Statement: "Cannot read another workspace's data", Container: true, Landlock: true, Bwrap: true,
			Note: "container only: by file permissions and per-task Landlock rules; with Landlock: paths outside the task set are denied"},
		{Statement: "Cannot reach model-management endpoints", Container: true, Landlock: true, Bwrap: true,
			Note: "container only: via the §6.1 proxy allowlist when egress is enabled, and the container's " +
				"network configuration otherwise; with Landlock: TCP port rules. A task never gets the " +
				"proxy port either way"},
		{Statement: "Cannot see other tasks' processes", Container: false, Landlock: false, Bwrap: true,
			Note: "requires the PID namespace, which only the bubblewrap layer provides"},
		{Statement: "Out-of-scope writes in the worktree", Container: true, Landlock: true, Bwrap: true,
			Note: "detected by diff at every layer, not prevented"},
		{Statement: "Cannot modify policy, ledger, hidden tests", Container: true, Landlock: true, Bwrap: true,
			Note: "container only: file permissions and Landlock"},
	}
}

// ReadmeStatement is the honest summary §6.2 requires the README to carry.
const ReadmeStatement = "The default container gives strong isolation from your host and between " +
	"workspaces; process-level isolation between concurrent tasks requires the optional namespace mode."

// Report describes the active configuration for `le doctor`.
type Report struct {
	Runner     string      `json:"runner"`
	Active     []Layer     `json:"active_layers"`
	Inactive   []LayerNote `json:"inactive_layers"`
	Guarantees []Guarantee `json:"guarantees"`
	Statement  string      `json:"statement"`
}

// LayerNote explains why a layer is not active.
type LayerNote struct {
	Layer  Layer  `json:"layer"`
	Reason string `json:"reason"`
}

// Select picks the strongest available runner and reports what it chose and
// what it rejected. Order: bubblewrap (strongest) then Landlock then the
// container boundary alone.
func Select(ctx context.Context, candidates []Runner) (Runner, Report) {
	rep := Report{Guarantees: Guarantees(), Statement: ReadmeStatement}
	var chosen Runner
	for _, c := range candidates {
		ok, reason := c.Available(ctx)
		if ok && chosen == nil {
			chosen = c
			continue
		}
		if !ok {
			for _, l := range c.Layers() {
				if l == LayerContainer {
					continue
				}
				rep.Inactive = append(rep.Inactive, LayerNote{Layer: l, Reason: reason})
			}
		}
	}
	if chosen == nil {
		return nil, rep
	}
	rep.Runner = chosen.Name()
	rep.Active = chosen.Layers()
	sort.Slice(rep.Inactive, func(i, j int) bool { return rep.Inactive[i].Layer < rep.Inactive[j].Layer })
	return chosen, rep
}
