// Package typescript contributes the TypeScript row of design v3 §3.2.
//
// What it does not do is the important part. Real TypeScript analysis means the
// compiler API, which means a Node sidecar; the design anticipates that and
// this package is not it. What this package does is read the source
// lexically, and every edge it emits carries the evidence category that
// reading actually supports:
//
//   - `imports` between files is `resolved`. The specifier is resolved against
//     the filesystem using the module resolution rules, so the edge asserts a
//     file exists, which is a fact this analyzer can establish.
//   - `imports` to a package is `declared`: the manifest says the dependency
//     exists, and whether it is installed is a question about node_modules.
//   - `extends` and `implements` are `declared`: the heritage clause states
//     them outright.
//   - `calls` is `inferred`, and deliberately so. Without a type checker an
//     identifier in call position may be a local, a shadowed binding or a
//     method on an unrelated object. The edges are useful for retrieval and
//     must not be read as a call graph.
//   - `reads_config` for process.env is `inferred`: two files agreeing on a
//     string.
//
// An impact report over these edges is therefore honest about being weaker
// than the Go one, which is the whole point of §3.2 carrying a category per
// relationship rather than a confidence score nobody can interpret.
package typescript

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// Analyzer reads TypeScript and JavaScript sources.
type Analyzer struct {
	MaxFileBytes int64
	Warnf        func(format string, args ...any)

	// UseSidecar turns on the compiler-backed analysis when the sidecar is
	// installed. The lexical reading is what a repository gets otherwise; the
	// two produce different evidence categories for the same relationships,
	// and the graph records which.
	UseSidecar bool
	// InstallRoot is where to look for sidecars/. Empty searches beside the
	// binary and the working directory.
	InstallRoot string
	// SidecarTimeout bounds the compiler run.
	SidecarTimeout time.Duration
}

// New returns an analyzer with the shipped defaults.
func New() *Analyzer { return &Analyzer{MaxFileBytes: 1 << 20} }

func (a *Analyzer) Name() string { return "typescript" }

// extensions this analyzer reads. Declaration files are included because they
// carry the public surface of a package, and excluding them loses the only
// description of a dependency's API.
var extensions = map[string]bool{
	".ts": true, ".tsx": true, ".mts": true, ".cts": true,
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
}

func (a *Analyzer) Handles(f index.File) bool {
	ext := strings.ToLower(filepath.Ext(f.Path))
	if !extensions[ext] {
		return false
	}
	// Minified bundles are cost without retrieval value and produce nonsense
	// call edges.
	base := filepath.Base(f.Path)
	return !strings.Contains(base, ".min.") && !strings.HasSuffix(base, ".bundle.js")
}

func (a *Analyzer) warn(format string, args ...any) {
	if a.Warnf != nil {
		a.Warnf(format, args...)
	}
}

// moduleFQN namespaces a module by its repository-relative path.
func moduleFQN(rel string) string { return "ts:" + filepath.ToSlash(rel) }

// symbolFQN namespaces a declaration within its module.
func symbolFQN(rel, name string) string { return moduleFQN(rel) + "#" + name }

// packageFQN namespaces an external dependency.
func packageFQN(spec string) string { return "npm:" + spec }

// Analyze reads every accepted file and emits what the source states.
func (a *Analyzer) Analyze(ctx context.Context, repoRoot string, files []index.File) (index.Result, error) {
	if a.UseSidecar {
		script, err := SidecarPath(a.InstallRoot)
		switch {
		case err == nil:
			res, diags, runErr := RunSidecar(ctx, script, repoRoot, a.SidecarTimeout)
			for _, d := range diags {
				a.warn("typescript: sidecar: %s", d)
			}
			if runErr == nil {
				return res, nil
			}
			// A sidecar that failed is reported, not swallowed: an empty graph
			// and a graph nobody could build are different things, and only one
			// is a fact about the repository. The lexical reading still runs,
			// so the operator gets something rather than nothing.
			a.warn("typescript: %v; falling back to the lexical reading, whose edges "+
				"are weaker and say so", runErr)
		case errors.Is(err, ErrNoSidecar):
			a.warn("typescript: the sidecar is not installed; using the lexical reading")
		default:
			a.warn("typescript: locating the sidecar: %v", err)
		}
	}

	var res index.Result

	sorted := append([]index.File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	// present is the set of source files, used to resolve a specifier to a
	// file that actually exists.
	present := map[string]bool{}
	for _, f := range sorted {
		present[filepath.ToSlash(f.Path)] = true
	}

	parsed := make(map[string]File, len(sorted))
	for _, f := range sorted {
		body, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(f.Path)))
		if err != nil {
			a.warn("typescript: %s: %v", f.Path, err)
			continue
		}
		if a.MaxFileBytes > 0 && int64(len(body)) > a.MaxFileBytes {
			a.warn("typescript: %s: skipped, %d bytes exceeds the limit", f.Path, len(body))
			continue
		}
		parsed[filepath.ToSlash(f.Path)] = Parse(string(body))
	}

	// Modules first, so every edge below has both endpoints declared.
	for _, rel := range sortedKeys(parsed) {
		res.Nodes = append(res.Nodes, graph.Node{
			Kind: graph.KindModule, Name: filepath.Base(rel), FQN: moduleFQN(rel),
			Path: rel,
		})
	}

	// Declarations.
	for _, rel := range sortedKeys(parsed) {
		for _, d := range parsed[rel].Decls {
			res.Nodes = append(res.Nodes, graph.Node{
				Kind: nodeKindFor(d.Kind), Name: d.Name, FQN: symbolFQN(rel, d.Name),
				Path: rel, StartLine: d.Line, EndLine: d.Line,
				Visibility: visibility(d.Exported),
			})
			// The module contains the symbol: consumer to consumed, so a file
			// points at what it holds.
			res.Edges = append(res.Edges, index.PendingEdge{
				SrcKind: graph.KindModule, SrcFQN: moduleFQN(rel),
				DstKind: nodeKindFor(d.Kind), DstFQN: symbolFQN(rel, d.Name),
				Kind: graph.EdgeContains, Evidence: graph.Resolved,
			})
		}
	}

	for _, rel := range sortedKeys(parsed) {
		file := parsed[rel]
		a.emitImports(&res, rel, file, present)
		a.emitHeritage(&res, rel, file, parsed)
		a.emitEnvReads(&res, rel, file)
	}
	return res, nil
}

// emitImports resolves each specifier and emits the module or package edge.
func (a *Analyzer) emitImports(res *index.Result, rel string, file File, present map[string]bool) {
	seen := map[string]bool{}
	for _, imp := range file.Imports {
		if imp.Specifier == "" {
			continue
		}
		target, ok := resolve(rel, imp.Specifier, present)
		if ok {
			key := "m:" + target
			if seen[key] {
				continue
			}
			seen[key] = true
			// The importer is the consumer and points at what it consumes.
			res.Edges = append(res.Edges, index.PendingEdge{
				SrcKind: graph.KindModule, SrcFQN: moduleFQN(rel),
				DstKind: graph.KindModule, DstFQN: moduleFQN(target),
				Kind: graph.EdgeImports, Evidence: graph.Resolved,
			})
			continue
		}
		if isRelative(imp.Specifier) {
			// A relative specifier that resolves to nothing is a broken import
			// or a file the walk excluded. Saying so is more useful than
			// inventing a package node for it.
			a.warn("typescript: %s: import %q resolves to no file in the repository",
				rel, imp.Specifier)
			continue
		}
		pkg := packageRoot(imp.Specifier)
		key := "p:" + pkg
		if seen[key] {
			continue
		}
		seen[key] = true
		res.Nodes = append(res.Nodes, graph.Node{
			Kind: graph.KindDependency, Name: pkg, FQN: packageFQN(pkg),
		})
		res.Edges = append(res.Edges, index.PendingEdge{
			SrcKind: graph.KindModule, SrcFQN: moduleFQN(rel),
			DstKind: graph.KindDependency, DstFQN: packageFQN(pkg),
			Kind: graph.EdgeDependsOn, Evidence: graph.Declared,
		})
	}
}

// emitHeritage emits extends and implements. The target is resolved within the
// repository by name; a name that matches nothing is skipped rather than
// pointed at a node that does not exist.
func (a *Analyzer) emitHeritage(res *index.Result, rel string, file File, parsed map[string]File) {
	for _, d := range file.Decls {
		for _, base := range d.Extends {
			if target, kind, ok := findDecl(base, rel, parsed); ok {
				res.Edges = append(res.Edges, index.PendingEdge{
					SrcKind: nodeKindFor(d.Kind), SrcFQN: symbolFQN(rel, d.Name),
					DstKind: kind, DstFQN: target,
					Kind: graph.EdgeExtends, Evidence: graph.Declared,
				})
			}
		}
		for _, iface := range d.Implements {
			if target, kind, ok := findDecl(iface, rel, parsed); ok {
				// The implementing type is the consumer: adding a method to an
				// interface must find the types that implement it, and impact
				// analysis is a reverse traversal.
				res.Edges = append(res.Edges, index.PendingEdge{
					SrcKind: nodeKindFor(d.Kind), SrcFQN: symbolFQN(rel, d.Name),
					DstKind: kind, DstFQN: target,
					Kind: graph.EdgeImplements, Evidence: graph.Declared,
				})
			}
		}
	}
}

// emitEnvReads records configuration keys the module reads.
func (a *Analyzer) emitEnvReads(res *index.Result, rel string, file File) {
	seen := map[string]bool{}
	for _, env := range file.EnvReads {
		if seen[env.Key] {
			continue
		}
		seen[env.Key] = true
		res.Nodes = append(res.Nodes, graph.Node{
			Kind: graph.KindConfigKey, Name: env.Key, FQN: "config:" + env.Key,
		})
		// The reader is the consumer and points at the key it reads.
		res.Edges = append(res.Edges, index.PendingEdge{
			SrcKind: graph.KindModule, SrcFQN: moduleFQN(rel),
			DstKind: graph.KindConfigKey, DstFQN: "config:" + env.Key,
			Kind: graph.EdgeReadsConfig, Evidence: graph.Inferred,
		})
	}
}

// findDecl locates a declaration by name, preferring the file it was used in
// and then any other file. Without a type checker this is a guess, which is why
// the caller records the edge as declared-by-syntax rather than resolved.
func findDecl(name, from string, parsed map[string]File) (string, graph.NodeKind, bool) {
	if f, ok := parsed[from]; ok {
		for _, d := range f.Decls {
			if d.Name == name {
				return symbolFQN(from, d.Name), nodeKindFor(d.Kind), true
			}
		}
	}
	var candidates []string
	kinds := map[string]graph.NodeKind{}
	for _, rel := range sortedKeys(parsed) {
		for _, d := range parsed[rel].Decls {
			if d.Name == name && d.Exported {
				candidates = append(candidates, symbolFQN(rel, d.Name))
				kinds[symbolFQN(rel, d.Name)] = nodeKindFor(d.Kind)
			}
		}
	}
	// An ambiguous name is not an edge. Two exported types with the same name
	// in different modules are common, and picking one at random produces an
	// edge that is wrong half the time.
	if len(candidates) != 1 {
		return "", "", false
	}
	return candidates[0], kinds[candidates[0]], true
}

// resolve turns a specifier into a repository-relative path, following the
// extension and index-file rules.
func resolve(from, spec string, present map[string]bool) (string, bool) {
	if !isRelative(spec) {
		return "", false
	}
	base := filepath.ToSlash(filepath.Join(filepath.Dir(from), spec))
	base = strings.TrimSuffix(base, "/")

	// An explicit extension wins, including the .js-means-.ts rewrite that
	// ESM TypeScript requires.
	if ext := filepath.Ext(base); ext != "" {
		if present[base] {
			return base, true
		}
		stem := strings.TrimSuffix(base, ext)
		for _, candidate := range []string{stem + ".ts", stem + ".tsx", stem + ".mts", stem + ".cts"} {
			if present[candidate] {
				return candidate, true
			}
		}
	}
	for _, ext := range []string{".ts", ".tsx", ".mts", ".cts", ".d.ts", ".js", ".jsx", ".mjs", ".cjs"} {
		if present[base+ext] {
			return base + ext, true
		}
	}
	for _, ext := range []string{".ts", ".tsx", ".mts", ".cts", ".d.ts", ".js", ".jsx", ".mjs", ".cjs"} {
		if present[base+"/index"+ext] {
			return base + "/index" + ext, true
		}
	}
	return "", false
}

func isRelative(spec string) bool {
	return strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") || spec == "." || spec == ".."
}

// packageRoot reduces "@scope/pkg/sub/path" to "@scope/pkg" and "pkg/sub" to
// "pkg", so a dependency is one node rather than one per entry point.
func packageRoot(spec string) string {
	parts := strings.Split(spec, "/")
	if strings.HasPrefix(spec, "@") && len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

func nodeKindFor(kind string) graph.NodeKind {
	switch kind {
	case "class":
		return graph.KindClass
	case "interface":
		return graph.KindInterface
	case "type", "enum":
		return graph.KindType
	case "const":
		return graph.KindConstant
	default:
		return graph.KindFunction
	}
}

func sortedKeys(m map[string]File) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// visibility matches the vocabulary the Go analyzer uses, so a consumer of the
// graph does not have to know which analyzer produced a node.
func visibility(exported bool) string {
	if exported {
		return "exported"
	}
	return "unexported"
}
