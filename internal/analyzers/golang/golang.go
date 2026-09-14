// Package golang is the Go language analyzer (design v3 §3.2).
//
// It contributes the compiler-backed rows of the coverage table: the module
// and package graph, caller-to-callee edges, interface satisfaction, type
// usage, imports, tests, configuration reads and HTTP routes.
//
// Everything here is derived from a real type-checked program, not from
// regular expressions over source text. That is what lets these edges carry
// `resolved` evidence — and it is why the one place that cannot be resolved,
// interface dispatch, is labelled `inferred` with its assumption recorded on
// the edge rather than being quietly presented as fact.
package golang

import (
	"context"
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// Analyzer implements index.Analyzer for Go.
type Analyzer struct {
	// Timeout bounds a whole analysis run. Loading a large module type-checks
	// every dependency, and an unbounded run would block indexing entirely.
	Timeout time.Duration
	// MaxPackages caps how many packages are analysed. A monorepo with
	// thousands of packages would otherwise exhaust memory during load.
	MaxPackages int
	// Tests includes test files in the load, which is what produces the
	// test-to-implementation edges of §3.2.
	Tests bool
	// Warnf reports non-fatal analysis problems. Nil discards them.
	Warnf func(format string, args ...any)
}

// New returns an analyzer with the shipped defaults.
func New() *Analyzer {
	return &Analyzer{Timeout: 5 * time.Minute, MaxPackages: 2000, Tests: true}
}

func (a *Analyzer) Name() string { return "go" }

// Handles accepts Go source and module files. The analyzer works on whole
// modules rather than individual files, so this only decides whether the
// analyzer runs at all.
func (a *Analyzer) Handles(f index.File) bool {
	return f.Lang == "go" || f.Lang == "gomod"
}

func (a *Analyzer) warn(format string, args ...any) {
	if a.Warnf != nil {
		a.Warnf(format, args...)
	}
}

// Analyze loads every module reachable from the accepted files and emits the
// nodes and edges for it.
func (a *Analyzer) Analyze(ctx context.Context, repoRoot string, files []index.File) (index.Result, error) {
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}

	roots := moduleRoots(repoRoot, files)
	if len(roots) == 0 {
		// Go files with no go.mod: nothing to type-check against. Say so
		// rather than emitting guesses.
		a.warn("go: no go.mod found under %s; skipping Go analysis", repoRoot)
		return index.Result{}, nil
	}

	b := newBuilder(a)
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return index.Result{}, err
		}
		if err := b.analyzeModule(ctx, root); err != nil {
			// One unloadable module must not lose the others.
			a.warn("go: %s: %v", root, err)
		}
	}
	return b.result(), nil
}

// moduleRoots returns the directories holding a go.mod, nearest-first.
func moduleRoots(repoRoot string, files []index.File) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		if filepath.Base(f.Path) != "go.mod" {
			continue
		}
		dir := filepath.Join(repoRoot, filepath.FromSlash(filepath.Dir(f.Path)))
		if !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
	}
	// A repository can hold nested modules; analysing the outer one does not
	// cover the inner ones, so all of them are loaded.
	sort.Strings(out)
	return out
}

// builder accumulates nodes and edges across modules, de-duplicating by FQN.
type builder struct {
	a *Analyzer

	nodes   []graph.Node
	nodeSet map[string]bool
	edges   []index.PendingEdge
	edgeSet map[string]bool

	// pkgCount bounds total work across modules.
	pkgCount int
}

func newBuilder(a *Analyzer) *builder {
	return &builder{a: a, nodeSet: map[string]bool{}, edgeSet: map[string]bool{}}
}

func (b *builder) result() index.Result {
	return index.Result{Nodes: b.nodes, Edges: b.edges}
}

func (b *builder) addNode(n graph.Node) {
	key := string(n.Kind) + "\x00" + n.FQN
	if b.nodeSet[key] {
		return
	}
	b.nodeSet[key] = true
	b.nodes = append(b.nodes, n)
}

func (b *builder) addEdge(e index.PendingEdge) {
	key := strings.Join([]string{
		string(e.SrcKind), e.SrcFQN, string(e.DstKind), e.DstFQN, string(e.Kind), e.Attrs,
	}, "\x00")
	if b.edgeSet[key] {
		return
	}
	b.edgeSet[key] = true
	b.edges = append(b.edges, e)
}

// loadMode is the minimum that still yields a type-checked program with
// syntax. Dropping any of these silently degrades the edges below from
// resolved to guesswork.
const loadMode = packages.NeedName |
	packages.NeedFiles |
	packages.NeedCompiledGoFiles |
	packages.NeedImports |
	packages.NeedDeps |
	packages.NeedTypes |
	packages.NeedSyntax |
	packages.NeedTypesInfo |
	packages.NeedModule

func (b *builder) analyzeModule(ctx context.Context, root string) error {
	cfg := &packages.Config{
		Mode:    loadMode,
		Context: ctx,
		Dir:     root,
		Tests:   b.a.Tests,
		// A hermetic load: no network, no implicit toolchain download. An
		// analysis that reaches the network would break the offline lane.
		Env: append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOTOOLCHAIN=local"),
	}

	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if len(pkgs) == 0 {
		return nil
	}

	// Type errors are normal in a repository mid-edit. Analyse what did check,
	// and report the rest rather than failing the whole run: a partially
	// broken build is exactly when someone needs the graph most.
	var errPkgs int
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if len(p.Errors) > 0 {
			errPkgs++
		}
	})
	if errPkgs > 0 {
		b.a.warn("go: %s: %d package(s) had type errors; their edges may be incomplete", root, errPkgs)
	}

	// Collect the in-repository packages. Dependencies outside the module are
	// represented as dependency nodes, not analysed.
	local := map[string]*packages.Package{}
	for _, p := range pkgs {
		if p.PkgPath == "" || p.Types == nil {
			continue
		}
		// packages.Load with Tests produces synthetic ".test" and "[...]"
		// variants; the real package is enough, and the external test package
		// carries the test functions.
		if strings.HasSuffix(p.PkgPath, ".test") {
			continue
		}
		if b.pkgCount >= b.a.MaxPackages {
			b.a.warn("go: package cap of %d reached; the graph is incomplete for %s", b.a.MaxPackages, root)
			break
		}
		if _, dup := local[p.PkgPath]; !dup {
			b.pkgCount++
		}
		local[p.PkgPath] = p
	}

	b.emitModule(pkgs, local)

	// A shared index of every named type and interface in the module, needed
	// for interface satisfaction and for resolving dynamic dispatch.
	idx := newTypeIndex(local)

	for _, p := range sortedPackages(local) {
		if err := ctx.Err(); err != nil {
			return err
		}
		b.emitPackage(p, local)
		b.emitDeclarations(p)
		b.emitImplements(p, idx)
		b.emitReferences(p, local, idx)
	}
	return nil
}

func sortedPackages(local map[string]*packages.Package) []*packages.Package {
	out := make([]*packages.Package, 0, len(local))
	for _, p := range local {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PkgPath < out[j].PkgPath })
	return out
}

// FQN conventions. Prefixes keep the namespaces disjoint, so a package and a
// function can never collide on the unique (kind, fqn) index.
func moduleFQN(path string) string  { return "mod:" + path }
func packageFQN(path string) string { return "pkg:" + path }
func configFQN(key string) string   { return "env:" + key }
func routeFQN(method, pattern string) string {
	if method == "" {
		return "route:" + pattern
	}
	return "route:" + method + " " + pattern
}

// objectFQN names a package-level object or a method.
func objectFQN(obj types.Object) string {
	if obj == nil || obj.Pkg() == nil {
		return ""
	}
	if fn, ok := obj.(*types.Func); ok {
		if recv := fn.Signature().Recv(); recv != nil {
			return obj.Pkg().Path() + "." + receiverName(recv.Type()) + "." + fn.Name()
		}
	}
	return obj.Pkg().Path() + "." + obj.Name()
}

func receiverName(t types.Type) string {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj().Name()
	}
	return types.TypeString(t, func(p *types.Package) string { return "" })
}
