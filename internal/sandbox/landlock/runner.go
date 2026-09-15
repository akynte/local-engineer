// Package landlock implements the per-task Landlock layer of design v3 §6.1,
// layer 2, and DR-3.
//
// Landlock restricts the calling process, and restrictions are inherited
// across execve. A parent therefore cannot restrict a child directly: the
// runner re-executes the `le` binary with a hidden helper subcommand, the
// helper applies the ruleset to itself, and then execs the real target. This
// is the same shape `landrun` uses.
//
// What this layer does NOT guarantee, stated here so no caller assumes
// otherwise (§6.2):
//   - it gives no PID namespace, so concurrent tasks still see each other's
//     processes; that needs the bubblewrap layer;
//   - Landlock's TCP rules do not cover Multipath TCP sockets, and Go's
//     net.Listen has defaulted to MPTCP since Go 1.24, so a sandboxed Go
//     program can still listen on an unlisted port. Network containment is
//     therefore augmented by the container's network configuration and, for
//     provisioning, by the §6.1 allowlisting proxy — never relied on alone. A
//     task's ruleset never includes the proxy port, which is what keeps the
//     provisioning lane out of a task's reach.
package landlock

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"

	ll "github.com/landlock-lsm/go-landlock/landlock"
	llsys "github.com/landlock-lsm/go-landlock/landlock/syscall"

	"github.com/akynte/local-engineer/internal/sandbox"
)

// HelperCommand is the hidden subcommand the runner re-executes. cmd/le wires
// it to Helper.
const HelperCommand = "__sandbox-exec"

// EnvSpec carries the JSON-encoded spec to the helper. It is an environment
// variable rather than an argument so it never appears in `ps` output of other
// containers sharing a host.
const EnvSpec = "LE_SANDBOX_SPEC"

// MinABI is the Landlock ABI the filesystem rules need. Network rules need
// ABI 4 (kernel 6.7+); the runner reports, rather than fails, when the running
// kernel is older (§6.1).
const MinABI = 1

// NetworkABI is the first ABI with TCP bind/connect rules.
const NetworkABI = 4

// Runner applies Landlock to each task process tree.
type Runner struct {
	// Self is the path of the `le` binary used as the re-exec helper.
	Self string
}

// New builds a runner that re-executes the running binary.
func New() (*Runner, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("landlock: locate self: %w", err)
	}
	return &Runner{Self: self}, nil
}

func (r *Runner) Name() string { return "landlock" }

// Layers reports the container boundary plus Landlock: the container is always
// present, and this runner adds the second layer.
func (r *Runner) Layers() []sandbox.Layer {
	return []sandbox.Layer{sandbox.LayerContainer, sandbox.LayerLandlock}
}

// ABI reports the kernel's Landlock ABI version, or an error explaining why
// Landlock is unavailable. `le doctor` prints this.
func ABI() (int, error) {
	if runtime.GOOS != "linux" {
		return 0, fmt.Errorf("landlock is a Linux LSM; this host runs %s", runtime.GOOS)
	}
	v, err := llsys.LandlockGetABIVersion()
	if err != nil {
		return 0, fmt.Errorf("landlock_create_ruleset probe failed: %w "+
			"(the kernel may lack Landlock, or the container's seccomp profile may block the three Landlock syscalls; "+
			"see docs/how-to/troubleshooting.md)", err)
	}
	return v, nil
}

// Available reports whether Landlock can be applied on this host.
func (r *Runner) Available(context.Context) (bool, string) {
	v, err := ABI()
	if err != nil {
		return false, err.Error()
	}
	if v < MinABI {
		return false, fmt.Sprintf("landlock ABI %d is below the required %d", v, MinABI)
	}
	if r.Self == "" {
		return false, "the re-exec helper binary path is unknown"
	}
	if _, err := os.Stat(r.Self); err != nil {
		return false, fmt.Sprintf("re-exec helper %s is not executable: %v", r.Self, err)
	}
	return true, ""
}

// SupportsNetwork reports whether TCP rules will actually be enforced.
func SupportsNetwork() (bool, string) {
	v, err := ABI()
	if err != nil {
		return false, err.Error()
	}
	if v < NetworkABI {
		return false, fmt.Sprintf("landlock ABI %d does not implement TCP rules; they need ABI %d (kernel 6.7+). "+
			"Port restrictions are best-effort and are silently skipped at this ABI", v, NetworkABI)
	}
	return true, ""
}

// Command builds the sandboxed command. The returned *exec.Cmd runs the helper,
// which applies the ruleset and then execs argv.
func (r *Runner) Command(ctx context.Context, spec sandbox.Spec, argv ...string) (*exec.Cmd, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("landlock: no command to run")
	}
	body, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("landlock: encode spec: %w", err)
	}

	args := append([]string{HelperCommand, "--"}, argv...)
	// r.Self is this binary's own path, and argv is the task command the
	// sandbox exists to confine. The mitigation is the Landlock ruleset the
	// helper applies before exec, not argument filtering.
	cmd := exec.CommandContext(ctx, r.Self, args...) //nolint:gosec // see above
	cmd.Dir = spec.Dir
	cmd.Env = append(append([]string{}, spec.Env...), EnvSpec+"="+string(body))
	return cmd, nil
}

// Apply enforces a spec on the current process. It is exported so the helper
// and the tests use exactly the same code path.
//
// BestEffort() is deliberate: on an older kernel the strongest available
// subset is applied rather than failing the task outright. `le doctor` is what
// tells the operator which subset that is, so degradation is visible.
func Apply(spec sandbox.Spec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	cfg := ll.V10.BestEffort()

	// Landlock rules for a directory and for a regular file carry different
	// access rights, and applying the wrong one is an error rather than a
	// no-op. Classifying here means a caller can list paths without knowing
	// which is which — and a caller that has to know will eventually get it
	// wrong on someone else's filesystem layout.
	roDirs, roFiles := classify(spec.ReadOnly)
	rwDirs, rwFiles := classify(spec.ReadWrite)

	rules := make([]ll.Rule, 0, 8+len(spec.TCPConnect)+len(spec.TCPBind))
	// IgnoreIfMissing throughout: a toolchain path absent from a slim image
	// must not abort the task; the remaining rules still apply.
	if len(roDirs) > 0 {
		rules = append(rules, ll.RODirs(roDirs...).IgnoreIfMissing())
	}
	if len(roFiles) > 0 {
		rules = append(rules, ll.ROFiles(roFiles...).IgnoreIfMissing())
	}
	if len(rwDirs) > 0 {
		// WithRefer permits rename and link within the granted set, which a
		// build or a test needs for atomic file replacement.
		rules = append(rules, ll.RWDirs(rwDirs...).IgnoreIfMissing().WithRefer())
	}
	if len(rwFiles) > 0 {
		// WithIoctlDev: device nodes such as /dev/null and /dev/tty are
		// ioctl'd by ordinary programs, and denying that produces failures
		// far from their cause.
		rules = append(rules, ll.RWFiles(rwFiles...).IgnoreIfMissing().WithIoctlDev())
	}
	for _, p := range spec.TCPConnect {
		rules = append(rules, ll.ConnectTCP(p))
	}
	for _, p := range spec.TCPBind {
		rules = append(rules, ll.BindTCP(p))
	}

	if err := cfg.Restrict(rules...); err != nil {
		return fmt.Errorf("landlock: restrict: %w", err)
	}
	return nil
}

// classify splits paths into directories and non-directories.
//
// A path that does not exist is treated as a directory: the rule carries
// IgnoreIfMissing, so it is dropped either way, and guessing "directory" is
// the harmless choice.
func classify(paths []string) (dirs, files []string) {
	for _, p := range paths {
		if p == "" {
			continue
		}
		st, err := os.Stat(p)
		switch {
		case err != nil:
			dirs = append(dirs, p)
		case st.IsDir():
			dirs = append(dirs, p)
		default:
			files = append(files, p)
		}
	}
	return dirs, files
}

// Helper is the body of the hidden `__sandbox-exec` subcommand. It applies the
// spec from the environment and then execs argv, replacing itself so that no
// supervising process sits inside the sandbox.
func Helper(argv []string) error {
	raw := os.Getenv(EnvSpec)
	if raw == "" {
		return fmt.Errorf("landlock: %s is not set; this subcommand is internal", EnvSpec)
	}
	var spec sandbox.Spec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return fmt.Errorf("landlock: decode spec: %w", err)
	}
	if len(argv) == 0 {
		return fmt.Errorf("landlock: no command to exec")
	}
	if err := Apply(spec); err != nil {
		return err
	}

	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("landlock: %s: %w", argv[0], err)
	}
	env := os.Environ()
	cleaned := env[:0]
	for _, kv := range env {
		if len(kv) > len(EnvSpec) && kv[:len(EnvSpec)+1] == EnvSpec+"=" {
			continue
		}
		cleaned = append(cleaned, kv)
	}
	return syscall.Exec(bin, argv, cleaned)
}
