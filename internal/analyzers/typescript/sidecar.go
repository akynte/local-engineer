package typescript

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// The sidecar (design v3 §1.2, §3.2).
//
// This package reads TypeScript lexically and labels every edge with what
// reading supports. The sidecar has the TypeScript compiler, so it can answer
// what reading cannot: which declaration a call actually reaches, which
// interface a class actually implements, where a tsconfig path alias points.
// Those edges are `resolved`.
//
// The relationship between the two is deliberate. The sidecar is used when it
// is present and working; the lexical reading is what a repository gets
// otherwise. Neither is a fallback in the sense of being a worse version of the
// other — they produce different evidence categories for the same
// relationships, and the graph records which, so a consumer can tell a
// compiler-backed edge from a read one.
//
// A sidecar that fails is not silently ignored. Its diagnostics are reported
// and the lexical result is used, because an empty graph and a graph nobody
// could build are different things and only one of them is a fact about the
// repository.

// SidecarDir is where the sidecar lives relative to the installation root.
const SidecarDir = "sidecars/typescript"

// EnvSidecarDir names the directory holding analyze.js, for installations that
// do not keep it beside the binary.
const EnvSidecarDir = "LE_TYPESCRIPT_SIDECAR_DIR"

// sidecarResult is the JSON contract documented in sidecars/typescript/README.md.
type sidecarResult struct {
	Nodes []struct {
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		FQN        string `json:"fqn"`
		Path       string `json:"path"`
		StartLine  int    `json:"start_line"`
		Visibility string `json:"visibility"`
	} `json:"nodes"`
	Edges []struct {
		SrcKind  string `json:"src_kind"`
		SrcFQN   string `json:"src_fqn"`
		DstKind  string `json:"dst_kind"`
		DstFQN   string `json:"dst_fqn"`
		Kind     string `json:"kind"`
		Evidence string `json:"evidence"`
	} `json:"edges"`
	Diagnostics []struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	} `json:"diagnostics"`
}

// ErrNoSidecar is returned when the sidecar is not installed. It is a named
// error because its absence is an ordinary condition, not a fault: a repository
// without Node gets the lexical reading and should not be told something broke.
var ErrNoSidecar = errors.New("typescript: the sidecar is not installed")

// SidecarPath finds analyze.js, looking beside the binary and then at the
// configured root. Returns ErrNoSidecar when there is none.
func SidecarPath(installRoot string) (string, error) {
	var candidates []string
	// An explicit override wins. The sidecar belongs to the installation, not
	// to the repository being analysed, so this is how a non-standard install
	// says where it put it.
	if dir := os.Getenv(EnvSidecarDir); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "analyze.js"))
	}
	if installRoot != "" {
		candidates = append(candidates, filepath.Join(installRoot, SidecarDir, "analyze.js"))
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, SidecarDir, "analyze.js"),
			filepath.Join(dir, "..", SidecarDir, "analyze.js"))
	}
	// The working directory is checked last and not searched upwards: walking
	// up would find a `sidecars/` belonging to the repository being analysed,
	// and running that would be running the analysed code.
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, SidecarDir, "analyze.js"))
	}
	// gosec reports G703 on both Stat calls because one candidate derives from
	// an environment variable. That variable is EnvSidecarDir, the documented
	// way a non-standard installation says where it put the sidecar: it is
	// part of the process environment, not repository input, and anyone who
	// can set it can already set PATH. The candidate from the *repository*
	// side — the working directory — is deliberately not searched upwards, a
	// few lines above, which is the traversal that would actually matter here.
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() { //nolint:gosec // installation-controlled path, see above
			// node_modules has to be there too, or the script fails on import
			// with a message about `typescript` that says nothing useful.
			if _, err := os.Stat(filepath.Join(filepath.Dir(c), "node_modules")); err == nil { //nolint:gosec // same
				return c, nil
			}
		}
	}
	return "", ErrNoSidecar
}

// RunSidecar analyses a repository with the TypeScript compiler.
func RunSidecar(ctx context.Context, script, repoRoot string, timeout time.Duration) (index.Result, []string, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return index.Result{}, nil, fmt.Errorf("%w: node is not on PATH", ErrNoSidecar)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	//nolint:gosec // script and repoRoot are resolved paths, not request input
	cmd := exec.CommandContext(ctx, node, script, repoRoot)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return index.Result{}, nil, fmt.Errorf("typescript: sidecar failed: %w: %s",
			err, truncate(stderr.String(), 400))
	}

	var out sidecarResult
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return index.Result{}, nil, fmt.Errorf("typescript: the sidecar's output is not readable: %w", err)
	}

	var res index.Result
	for _, n := range out.Nodes {
		res.Nodes = append(res.Nodes, graph.Node{
			Kind: graph.NodeKind(n.Kind), Name: n.Name, FQN: n.FQN, Path: n.Path,
			StartLine: n.StartLine, EndLine: n.StartLine, Visibility: n.Visibility,
		})
	}
	for _, e := range out.Edges {
		ev := graph.Evidence(e.Evidence)
		if ev == "" {
			// §3.2 requires a category on every relationship, and an omitted
			// one would read as certainty. Unknown is the honest default.
			ev = graph.Unknown
		}
		res.Edges = append(res.Edges, index.PendingEdge{
			SrcKind: graph.NodeKind(e.SrcKind), SrcFQN: e.SrcFQN,
			DstKind: graph.NodeKind(e.DstKind), DstFQN: e.DstFQN,
			Kind: graph.EdgeKind(e.Kind), Evidence: ev,
		})
	}

	var diags []string
	for _, d := range out.Diagnostics {
		diags = append(diags, d.Level+": "+d.Message)
	}
	return res, diags, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
