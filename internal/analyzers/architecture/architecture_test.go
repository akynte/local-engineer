package architecture_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akynte/local-engineer/internal/analyzers/architecture"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

func analyze(t *testing.T, body string) index.Result {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ARCHITECTURE.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	a := architecture.New()
	a.Warnf = func(f string, args ...any) { t.Logf("warn: "+f, args...) }
	res, err := a.Analyze(context.Background(), root, []index.File{{Path: "ARCHITECTURE.md"}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func hasEdge(res index.Result, src, dst string, kind graph.EdgeKind, ev graph.Evidence) bool {
	for _, e := range res.Edges {
		if e.SrcFQN == src && e.DstFQN == dst && e.Kind == kind && e.Evidence == ev {
			return true
		}
	}
	return false
}

// §3.2: "layer annotations from ARCHITECTURE.md". The compiler can see that one
// package calls another; it cannot see that one is a handler layer and the
// other a repository layer. That is a human statement, and it belongs in the
// graph as `declared`.
func TestLayerAnnotationsBecomeDeclaredEdges(t *testing.T) {
	res := analyze(t, `# Architecture

| Layer | Packages |
|---|---|
| handler | internal/handler, internal/api |
| service | internal/service |
| repository | internal/repository |

Layer order: handler -> service -> repository
`)
	if !hasEdge(res, "layer:handler", "pkg:internal/handler", graph.EdgeContains, graph.Declared) {
		t.Errorf("the handler layer does not contain its package:\n%+v", res.Edges)
	}
	if !hasEdge(res, "layer:handler", "pkg:internal/api", graph.EdgeContains, graph.Declared) {
		t.Error("a second package on one row was lost")
	}
	// Consumer points at consumed, the same direction every analyzer uses:
	// changing the repository layer must find the service layer above it.
	if !hasEdge(res, "layer:handler", "layer:service", graph.EdgeDependsOn, graph.Declared) {
		t.Errorf("the declared call order is missing:\n%+v", res.Edges)
	}
	if !hasEdge(res, "layer:service", "layer:repository", graph.EdgeDependsOn, graph.Declared) {
		t.Error("the second hop of the order is missing")
	}
}

// The document is a node, so "what states this" is one hop rather than a guess.
func TestTheDocumentIsANode(t *testing.T) {
	res := analyze(t, "| handler | internal/handler |\n")
	var found bool
	for _, n := range res.Nodes {
		if n.FQN == "doc:ARCHITECTURE.md" && n.Kind == graph.KindDoc {
			found = true
		}
	}
	if !found {
		t.Errorf("ARCHITECTURE.md is not a node:\n%+v", res.Nodes)
	}
}

// Prose must not become edges. A table row whose second column is a sentence is
// documentation *about* the architecture, not an annotation of it, and turning
// it into nodes would fill the graph with names made of English.
func TestProseIsNotMistakenForAnnotation(t *testing.T) {
	res := analyze(t, `# Architecture

| Layer | Responsibility |
|---|---|
| handler | Accepts HTTP requests and validates them |
| service | Holds the business rules of the system |
`)
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeContains && e.DstKind == graph.KindPackage {
			t.Errorf("prose produced a package edge: %+v", e)
		}
	}
}

// Every edge records the assumption, because that is what it is: nothing
// verifies the code agrees with what the document says.
func TestEdgesRecordTheAssumption(t *testing.T) {
	res := analyze(t, "| handler | internal/handler |\n\nLayer order: handler -> service\n")
	for _, e := range res.Edges {
		if e.DstKind == graph.KindPackage || e.Kind == graph.EdgeDependsOn {
			if e.Attrs == "" {
				t.Errorf("edge %s -> %s records no assumption", e.SrcFQN, e.DstFQN)
			}
		}
	}
}

// A repository with no ARCHITECTURE.md gets nothing, quietly.
func TestNoDocumentIsNotAnError(t *testing.T) {
	a := architecture.New()
	res, err := a.Analyze(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 0 || len(res.Edges) != 0 {
		t.Errorf("something was produced from nothing: %+v", res)
	}
}

func TestHandlesOnlyArchitectureMd(t *testing.T) {
	a := architecture.New()
	for _, p := range []string{"ARCHITECTURE.md", "docs/architecture.md", "Architecture.MD"} {
		if !a.Handles(index.File{Path: p}) {
			t.Errorf("%s was rejected", p)
		}
	}
	for _, p := range []string{"README.md", "docs/index.md", "a.go"} {
		if a.Handles(index.File{Path: p}) {
			t.Errorf("%s was accepted", p)
		}
	}
}
