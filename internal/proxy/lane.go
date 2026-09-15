package proxy

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/sandbox"
)

// The provisioning lane (design v3 §6.1).
//
// A lane is a confined process that may reach the proxy and nothing else. It
// is not a task: it runs an operator-initiated command — fetch dependencies,
// fetch documentation — with a sandbox spec that differs from a task's in
// exactly one respect, the proxy port.
//
// Keeping it a separate entry point rather than a flag on a task is the whole
// design. §6.1 says the proxy is "never for a task sandbox", and a flag is a
// thing that gets set. A task runner has no way to construct a lane spec: the
// only function that grants the proxy port is here, and it takes a Lane.

// Spec describes one provisioning run.
type Spec struct {
	// Lane selects the allowlist subset and the proxy port.
	Lane Lane
	// ProxyPort is the loopback port for this lane's proxy.
	ProxyPort int
	// Dir is the working directory; the command may write only here and in
	// the caches below.
	Dir string
	// ReadWrite are the caches the fetch populates — the module cache, the
	// npm cache — plus Dir.
	ReadWrite []string
	// ReadOnly are toolchain paths.
	ReadOnly []string
	// TmpDir is the lane's only tmp.
	TmpDir string
	// Env is the base environment; the proxy variables are added here.
	Env []string
}

// SandboxSpec builds the confinement for a lane.
//
// The single difference from a task's spec is TCPConnect: the proxy port, and
// nothing else. Not the inference port — a dependency fetch has no business
// talking to the model — and not any test port.
func (s Spec) SandboxSpec() (sandbox.Spec, error) {
	if _, ok := ParseLane(string(s.Lane)); !ok {
		return sandbox.Spec{}, fmt.Errorf("proxy: %q is not a provisioning lane", s.Lane)
	}
	if s.ProxyPort <= 0 || s.ProxyPort > 65535 {
		return sandbox.Spec{}, fmt.Errorf("proxy: lane %s has no valid proxy port", s.Lane)
	}
	if s.Dir == "" {
		return sandbox.Spec{}, fmt.Errorf("proxy: lane %s has no working directory", s.Lane)
	}
	rw := append([]string{s.Dir}, s.ReadWrite...)
	if s.TmpDir != "" {
		rw = append(rw, s.TmpDir)
	}
	// Device nodes every ordinary program expects. Without them a fetch fails
	// in a way that points at the tool rather than at the sandbox: curl's
	// "Failure writing output to destination" for a denied /dev/null says
	// nothing about a missing grant. Read-write is correct — /dev/null is
	// written to constantly.
	rw = append(rw, DeviceFiles...)
	return sandbox.Spec{
		ReadOnly:   s.ReadOnly,
		ReadWrite:  rw,
		TCPConnect: []uint16{uint16(s.ProxyPort)}, //nolint:gosec // bounds checked above
		Dir:        s.Dir,
		TmpDir:     s.TmpDir,
		Env:        s.ProxyEnv(),
	}, nil
}

// ProxyEnv is the environment that points a tool at this lane's proxy.
//
// Both spellings are set because the ecosystem is split: Go reads HTTPS_PROXY,
// curl reads the lowercase form, and npm reads its own. NO_PROXY carries
// loopback so a tool that also talks to something local does not send that
// through the proxy and get refused by the allowlist.
func (s Spec) ProxyEnv() []string {
	addr := fmt.Sprintf("http://127.0.0.1:%d", s.ProxyPort)
	return append(append([]string{}, s.Env...),
		"HTTP_PROXY="+addr,
		"HTTPS_PROXY="+addr,
		"http_proxy="+addr,
		"https_proxy="+addr,
		"NO_PROXY=127.0.0.1,localhost,::1",
		"no_proxy=127.0.0.1,localhost,::1",
		// The lane exists so a fetch can happen; GOPROXY=off is the task
		// sandbox's rule, not this one. The default value is named
		// explicitly rather than inherited so that a GOPROXY=off in the
		// operator's environment does not silently make the lane useless.
		"GOFLAGS=-mod=mod",
		"GOPROXY=https://proxy.golang.org,direct",
	)
}

// DeviceFiles are the device nodes a provisioning lane needs. They mirror
// recipe.DeviceFiles; they are repeated rather than imported because
// internal/recipe is about verifying code and a lane is not, and a dependency
// between them would only exist to share six strings.
var DeviceFiles = []string{
	"/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom", "/dev/tty",
}

// LaneRunner executes a command inside a lane.
type LaneRunner struct {
	// Sandbox confines the command. Required: a provisioning lane that ran
	// unconfined would be a shell with network access.
	Sandbox sandbox.Runner
	// Timeout bounds a fetch.
	Timeout time.Duration
	// Logf reports what ran. Nil discards.
	Logf func(format string, args ...any)
}

// Result is one provisioning run.
type Result struct {
	Argv     []string      `json:"argv"`
	Lane     Lane          `json:"lane"`
	ExitCode int           `json:"exit_code"`
	Duration time.Duration `json:"duration"`
	Stdout   string        `json:"stdout,omitempty"`
	Stderr   string        `json:"stderr,omitempty"`
	// Decisions are what the proxy allowed and refused during this run. A
	// failed fetch is almost always a missing allowlist entry, and the
	// refusals say which.
	Decisions []Decision `json:"decisions,omitempty"`
}

// Run executes argv in the lane.
func (r *LaneRunner) Run(ctx context.Context, spec Spec, argv ...string) (Result, error) {
	res := Result{Argv: argv, Lane: spec.Lane}
	if len(argv) == 0 {
		return res, fmt.Errorf("proxy: no command to run in the %s lane", spec.Lane)
	}
	if r.Sandbox == nil {
		return res, fmt.Errorf("proxy: the %s lane has no sandbox; a provisioning lane must be confined", spec.Lane)
	}
	sb, err := spec.SandboxSpec()
	if err != nil {
		return res, err
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd, err := r.Sandbox.Command(runCtx, sb, argv...)
	if err != nil {
		return res, fmt.Errorf("proxy: confining the %s lane: %w", spec.Lane, err)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	start := time.Now()
	runErr := cmd.Run()
	res.Duration = time.Since(start)
	res.Stdout, res.Stderr = stdout.String(), stderr.String()

	var ee *exec.ExitError
	if runErr != nil {
		if ok := asExit(runErr, &ee); ok {
			res.ExitCode = ee.ExitCode()
		} else {
			return res, fmt.Errorf("proxy: running %s in the %s lane: %w", argv[0], spec.Lane, runErr)
		}
	}
	if r.Logf != nil {
		r.Logf("lane %s: %s exited %d in %s", spec.Lane, argv[0], res.ExitCode, res.Duration.Round(time.Millisecond))
	}
	if runCtx.Err() != nil {
		return res, fmt.Errorf("proxy: %s in the %s lane timed out after %s", argv[0], spec.Lane, timeout)
	}
	return res, nil
}

func asExit(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok { //nolint:errorlint // the direct type is what exec returns
		*target = ee
		return true
	}
	return false
}
