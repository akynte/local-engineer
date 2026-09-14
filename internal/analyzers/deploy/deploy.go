package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/akynte/local-engineer/internal/index"
)

// Analyzer reads deployment, build and infrastructure declarations.
type Analyzer struct {
	// MaxFileBytes skips files too large to be hand-written manifests. A
	// multi-megabyte YAML is generated output, and parsing it costs more than
	// it tells anyone.
	MaxFileBytes int64
	Warnf        func(format string, args ...any)
}

// New returns an analyzer with the shipped defaults.
func New() *Analyzer { return &Analyzer{MaxFileBytes: 2 << 20} }

func (a *Analyzer) Name() string { return "deploy" }

// Handles accepts the files this analyzer knows how to read.
func (a *Analyzer) Handles(f index.File) bool {
	if isDockerfile(f.Path) || isMakefile(f.Path) || isComposeFile(f.Path) {
		return true
	}
	// A YAML file might be a Kubernetes manifest; deciding needs its content,
	// which Analyze checks.
	return f.Lang == "yaml"
}

func (a *Analyzer) warn(format string, args ...any) {
	if a.Warnf != nil {
		a.Warnf(format, args...)
	}
}

// Analyze reads each accepted file and dispatches on what it turns out to be.
func (a *Analyzer) Analyze(ctx context.Context, repoRoot string, files []index.File) (index.Result, error) {
	var res index.Result

	sorted := append([]index.File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	for _, f := range sorted {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if a.MaxFileBytes > 0 && f.Size > a.MaxFileBytes {
			continue
		}
		abs := f.AbsPath
		if abs == "" {
			abs = filepath.Join(repoRoot, filepath.FromSlash(f.Path))
		}
		body, err := os.ReadFile(abs) //nolint:gosec // a path the indexer walked inside the repository
		if err != nil {
			a.warn("deploy: %s: %v", f.Path, err)
			continue
		}

		switch {
		case isDockerfile(f.Path):
			a.analyzeDockerfile(f.Path, body, &res)
		case isMakefile(f.Path):
			a.analyzeMakefile(f.Path, body, &res)
		case isComposeFile(f.Path) && looksLikeCompose(body):
			a.analyzeCompose(f.Path, body, &res)
		case f.Lang == "yaml" && looksLikeKubernetes(body):
			a.analyzeKubernetes(f.Path, body, &res)
		}
	}
	return dedupe(res), nil
}

// looksLikeCompose confirms a compose-named file actually is one. Plenty of
// YAML files are named compose-ish and are not.
func looksLikeCompose(body []byte) bool {
	return strings.Contains(string(body), "services:")
}

// dedupe removes repeated nodes and edges. Several files legitimately declare
// the same configuration key or service, and the graph wants one node each.
func dedupe(res index.Result) index.Result {
	seenNode := map[string]bool{}
	nodes := res.Nodes[:0]
	for _, n := range res.Nodes {
		key := string(n.Kind) + "\x00" + n.FQN
		if seenNode[key] {
			continue
		}
		seenNode[key] = true
		nodes = append(nodes, n)
	}

	seenEdge := map[string]bool{}
	edges := res.Edges[:0]
	for _, e := range res.Edges {
		key := strings.Join([]string{
			string(e.SrcKind), e.SrcFQN, string(e.DstKind), e.DstFQN, string(e.Kind), e.Attrs,
		}, "\x00")
		if seenEdge[key] {
			continue
		}
		seenEdge[key] = true
		edges = append(edges, e)
	}
	return index.Result{Nodes: nodes, Edges: edges}
}

// attrs renders a JSON attribute object, dropping empty values.
func attrs(kv map[string]string) string {
	keys := make([]string, 0, len(kv))
	for k, v := range kv {
		if v != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return "{}"
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q:%q", k, kv[k])
	}
	b.WriteByte('}')
	return b.String()
}
