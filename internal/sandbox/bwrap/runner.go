// Package bwrap implements the optional bubblewrap layer of design v3 §6.1,
// layer 3, and DR-3.
//
// It adds mount and PID namespaces on top of Landlock, restoring the
// process-level isolation the container boundary alone does not give (§6.2:
// "Cannot see other tasks' processes" is the only row that needs this layer).
//
// It is optional because unprivileged user namespaces are commonly unavailable
// inside a container, and the runtime's seccomp profile may block namespace
// creation. Available() explains exactly which of those is the case, because
// "bwrap unavailable" on its own is useless in a support conversation.
package bwrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/akynte/local-engineer/internal/sandbox"
)

// Runner wraps a task process tree in bubblewrap.
type Runner struct {
	// Binary is the bwrap executable; empty means look it up on PATH.
	Binary string
	// Inner, when set, is applied inside the namespaces. The design layers
	// bwrap on top of Landlock rather than instead of it, so the usual value
	// is the Landlock runner's helper invocation.
	Inner sandbox.Runner
}

// New builds a bubblewrap runner layered over inner.
func New(inner sandbox.Runner) *Runner { return &Runner{Inner: inner} }

func (r *Runner) Name() string {
	if r.Inner != nil {
		return "bwrap+" + r.Inner.Name()
	}
	return "bwrap"
}

func (r *Runner) Layers() []sandbox.Layer {
	layers := []sandbox.Layer{sandbox.LayerContainer, sandbox.LayerBwrap}
	if r.Inner != nil {
		layers = append(layers, r.Inner.Layers()...)
	}
	return dedupe(layers)
}

func dedupe(in []sandbox.Layer) []sandbox.Layer {
	seen := map[sandbox.Layer]bool{}
	var out []sandbox.Layer
	for _, l := range in {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (r *Runner) binary() string {
	if r.Binary != "" {
		return r.Binary
	}
	return "bwrap"
}

// UserNamespacesAvailable reports whether unprivileged user namespaces can be
// created here. This is §16 verify item 2, answered at runtime rather than
// assumed.
func UserNamespacesAvailable() (bool, string) {
	// Debian and Ubuntu kernels expose this switch; absent means the kernel
	// has no such restriction and namespaces are governed by other policy.
	if body, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if strings.TrimSpace(string(body)) == "0" {
			return false, "kernel.unprivileged_userns_clone is 0: unprivileged user namespaces are disabled on this host"
		}
	}
	if body, err := os.ReadFile("/proc/sys/user/max_user_namespaces"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(body))); err == nil && n == 0 {
			return false, "user.max_user_namespaces is 0: unprivileged user namespaces are disabled on this host"
		}
	}
	if body, err := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); err == nil {
		if strings.TrimSpace(string(body)) == "1" {
			return false, "kernel.apparmor_restrict_unprivileged_userns is 1: AppArmor blocks unprivileged user namespaces " +
				"(Ubuntu 24.04 and later default). Run the container with --security-opt apparmor=unconfined to enable this layer"
		}
	}
	return true, ""
}

// Available reports whether the bubblewrap layer can be used.
func (r *Runner) Available() (bool, string) {
	path, err := exec.LookPath(r.binary())
	if err != nil {
		return false, fmt.Sprintf("%s is not on PATH: %v", r.binary(), err)
	}
	if ok, reason := UserNamespacesAvailable(); !ok {
		return false, reason
	}
	// A probe is the only honest test: the seccomp profile may permit the
	// binary and still deny clone(CLONE_NEWUSER).
	cmd := exec.Command(path, "--unshare-user", "--unshare-pid", "--dev-bind", "/", "/", "true")
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Sprintf("bwrap probe failed (%v): %s. "+
			"Inside a container this usually means the runtime's seccomp profile blocks namespace creation; "+
			"see DR-3 and docs/how-to/troubleshooting.md", err, strings.TrimSpace(string(out)))
	}
	if r.Inner != nil {
		if ok, reason := r.Inner.Available(); !ok {
			return false, "inner runner unavailable: " + reason
		}
	}
	return true, ""
}

// Command builds the bubblewrap invocation. Mount namespaces expose only the
// spec's paths; the PID namespace is what makes §6.2's "cannot see other
// tasks' processes" true.
func (r *Runner) Command(ctx context.Context, spec sandbox.Spec, argv ...string) (*exec.Cmd, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("bwrap: no command to run")
	}

	args := []string{
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try",
		"--die-with-parent", // the sandbox must not outlive the supervisor
		"--new-session",     // no shared terminal: prevents TIOCSTI injection into the parent's tty
		"--proc", "/proc",
		"--dev", "/dev",
	}
	for _, p := range spec.ReadOnly {
		args = append(args, "--ro-bind-try", p, p)
	}
	for _, p := range spec.ReadWrite {
		args = append(args, "--bind", p, p)
	}
	if spec.TmpDir != "" {
		// The task's tmp is the only tmp it can see (§2.2).
		args = append(args, "--bind", spec.TmpDir, "/tmp")
	} else {
		args = append(args, "--tmpfs", "/tmp")
	}
	args = append(args, "--chdir", spec.Dir, "--")

	// The network namespace is deliberately NOT unshared here: the design
	// routes egress through the container's network configuration and the
	// allowlisting proxy, and a task still needs to reach the inference
	// endpoint. Port-level restriction is the Landlock layer's job (§6.1).

	inner := argv
	if r.Inner != nil {
		cmd, err := r.Inner.Command(ctx, spec, argv...)
		if err != nil {
			return nil, err
		}
		inner = append([]string{cmd.Path}, cmd.Args[1:]...)
		args = append(args, inner...)
		full := exec.CommandContext(ctx, r.binary(), args...)
		full.Dir = spec.Dir
		full.Env = cmd.Env
		return full, nil
	}

	args = append(args, inner...)
	cmd := exec.CommandContext(ctx, r.binary(), args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	return cmd, nil
}
