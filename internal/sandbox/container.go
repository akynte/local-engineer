package sandbox

import (
	"context"
	"os"
	"os/exec"
)

// ContainerRunner is layer 1 of §6.1 on its own: the container boundary, with
// no per-task restriction inside it.
//
// It is always available, and it is the honest fallback when neither Landlock
// nor bubblewrap can be applied. It deliberately does not pretend to isolate
// tasks from one another; `le doctor` says so, and Guarantees() records which
// rows of the §6.2 table it actually satisfies.
type ContainerRunner struct{}

func (ContainerRunner) Name() string { return "container-only" }

func (ContainerRunner) Layers() []Layer { return []Layer{LayerContainer} }

// Available is always true: the boundary exists whether or not the process is
// actually inside a container. InContainer reports the latter.
func (ContainerRunner) Available(context.Context) (bool, string) { return true, "" }

func (ContainerRunner) Command(ctx context.Context, spec Spec, argv ...string) (*exec.Cmd, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	// Running the caller's command is this type's entire purpose. The
	// mitigation is not argument sanitisation — it is the container boundary
	// this runner names, which bounds whatever the command does (§6.1 layer 1).
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // see above
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	return cmd, nil
}

// InContainer reports whether this process is running inside a container, and
// which marker said so. It is a heuristic, and it is reported as one.
func InContainer() (bool, string) {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true, "/.dockerenv is present"
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true, "/run/.containerenv is present (Podman)"
	}
	if v := os.Getenv("LE_IN_CONTAINER"); v == "1" {
		return true, "LE_IN_CONTAINER=1 is set by the image entrypoint"
	}
	return false, "no container marker found; the container boundary of DR-3 layer 1 is NOT in effect"
}
