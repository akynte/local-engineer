// Package architecture reads the layer annotations §3.2 names as a source of
// truth: "layer annotations from ARCHITECTURE.md | resolved plus declared".
//
// The compiler can see that one package calls another. It cannot see that one
// is a handler layer and the other a repository layer, or that calls in that
// direction are intended and the reverse is not. That is a human statement
// about the system, and §3.2 asks for it to be in the graph as `declared` —
// alongside the resolved call edges, not instead of them.
//
// A layer violation is therefore answerable: a resolved call edge that runs
// against a declared layer order is a real finding, and neither half of it
// could produce that alone.
package architecture

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// Analyzer reads ARCHITECTURE.md.
type Analyzer struct {
	MaxFileBytes int64
	Warnf        func(format string, args ...any)
}

// New returns an analyzer with the shipped defaults.
func New() *Analyzer { return &Analyzer{MaxFileBytes: 1 << 20} }

func (a *Analyzer) Name() string { return "architecture" }

func (a *Analyzer) Handles(f index.File) bool {
	return strings.EqualFold(filepath.Base(f.Path), "ARCHITECTURE.md")
}

func (a *Analyzer) warn(format string, args ...any) {
	if a.Warnf != nil {
		a.Warnf(format, args...)
	}
}

func docFQN(rel string) string    { return "doc:" + filepath.ToSlash(rel) }
func layerFQN(name string) string { return "layer:" + strings.ToLower(name) }

// layerLine matches a table row or list item declaring a layer and its paths:
//
//	| handler | internal/handler, internal/api |
//	- handler: internal/handler, internal/api
var layerLine = regexp.MustCompile(`(?m)^\s*(?:\|\s*|[-*]\s+)` +
	`([A-Za-z][A-Za-z0-9 _-]*?)\s*(?:\||:)\s*` +
	"`?([^|\n`]+?)`?\\s*\\|?\\s*$")

// orderLine matches a declared call direction:
//
//	Layer order: handler -> service -> repository
var orderLine = regexp.MustCompile(`(?mi)^\s*(?:layers?\s+order|layering|call\s+order)\s*:\s*(.+)$`)

// Analyze reads the annotations a repository states about itself.
func (a *Analyzer) Analyze(_ context.Context, repoRoot string, files []index.File) (index.Result, error) {
	var res index.Result

	sorted := append([]index.File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	for _, f := range sorted {
		body, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(f.Path)))
		if err != nil {
			a.warn("architecture: %s: %v", f.Path, err)
			continue
		}
		if a.MaxFileBytes > 0 && int64(len(body)) > a.MaxFileBytes {
			continue
		}
		rel := filepath.ToSlash(f.Path)
		res.Nodes = append(res.Nodes, graph.Node{
			Kind: graph.KindDoc, Name: filepath.Base(rel), FQN: docFQN(rel), Path: rel,
		})
		a.readLayers(&res, rel, string(body))
	}
	return res, nil
}

func (a *Analyzer) readLayers(res *index.Result, rel, body string) {
	seen := map[string]bool{}

	for _, m := range layerLine.FindAllStringSubmatch(body, -1) {
		name := strings.TrimSpace(m[1])
		paths := splitPaths(m[2])
		if name == "" || len(paths) == 0 || isHeaderWord(name) {
			continue
		}
		// A "path" that is prose is a table header or a sentence, not a layer.
		if !looksLikePaths(paths) {
			continue
		}
		fqn := layerFQN(name)
		if !seen[fqn] {
			seen[fqn] = true
			res.Nodes = append(res.Nodes, graph.Node{
				Kind: graph.KindService, Name: name, FQN: fqn, Path: rel,
				Attrs: attrs(map[string]string{"declared_in": rel}),
			})
			// The document states the layer, so it contains it.
			res.Edges = append(res.Edges, index.PendingEdge{
				SrcKind: graph.KindDoc, SrcFQN: docFQN(rel),
				DstKind: graph.KindService, DstFQN: fqn,
				Kind: graph.EdgeContains, Evidence: graph.Declared,
			})
		}
		for _, p := range paths {
			// The layer contains the package: a package changing is a change
			// within a declared layer, which is what makes the annotation
			// reachable from code.
			res.Edges = append(res.Edges, index.PendingEdge{
				SrcKind: graph.KindService, SrcFQN: fqn,
				DstKind: graph.KindPackage, DstFQN: "pkg:" + p,
				Kind: graph.EdgeContains, Evidence: graph.Declared,
				Attrs: attrs(map[string]string{
					"assumption": "ARCHITECTURE.md states that " + p + " belongs to the " +
						name + " layer; nothing verifies the code agrees",
				}),
			})
		}
	}

	// A declared call order: handler -> service -> repository.
	for _, m := range orderLine.FindAllStringSubmatch(body, -1) {
		layers := splitArrow(m[1])
		for i := 0; i+1 < len(layers); i++ {
			from, to := layerFQN(layers[i]), layerFQN(layers[i+1])
			if from == to {
				continue
			}
			// Consumer points at consumed, the same direction every other
			// analyzer uses: changing the repository layer must find the
			// service layer above it.
			res.Edges = append(res.Edges, index.PendingEdge{
				SrcKind: graph.KindService, SrcFQN: from,
				DstKind: graph.KindService, DstFQN: to,
				Kind: graph.EdgeDependsOn, Evidence: graph.Declared,
				Attrs: attrs(map[string]string{
					"assumption": "declared layer order in " + rel +
						"; a resolved call in the opposite direction is a layering violation",
				}),
			})
		}
	}
}

func splitPaths(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' }) {
		p := strings.Trim(strings.TrimSpace(part), "`\"' ")
		if p != "" {
			out = append(out, strings.TrimSuffix(filepath.ToSlash(p), "/"))
		}
	}
	return out
}

func splitArrow(s string) []string {
	s = strings.NewReplacer("→", "->", "»", "->", "=>", "->").Replace(s)
	var out []string
	for _, part := range strings.Split(s, "->") {
		p := strings.Trim(strings.TrimSpace(part), "`*_.")
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// looksLikePaths keeps prose out. A table row whose second column is a sentence
// is documentation about the architecture, not an annotation of it, and turning
// it into edges would fill the graph with nodes named after English.
func looksLikePaths(paths []string) bool {
	for _, p := range paths {
		if strings.Contains(p, " ") || !strings.ContainsAny(p, "/.") {
			return false
		}
	}
	return true
}

// isHeaderWord skips the header row of a markdown table.
func isHeaderWord(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "layer", "layers", "name", "component", "package", "packages", "path", "paths", "---":
		return true
	}
	return strings.Trim(s, "-: ") == ""
}

func attrs(m map[string]string) string {
	body, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(body)
}
