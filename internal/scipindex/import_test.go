package scipindex_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
	"github.com/akynte/local-engineer/internal/scipindex"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestSCIPPersistsReferencesAndRejectsStaleEmbeddedSource(t *testing.T) {
	ctx := context.Background()
	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.CloseAll()
	st, err := root.OpenWorkspace(ctx, workspace.DeriveID("/scip", "", "test"))
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	body := "fn target() {}\nfn caller() { target(); }\n"
	if err := os.WriteFile(filepath.Join(repo, "lib.rs"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	ix := index.New(st, index.Options{})
	if err := ix.RegisterRepository(ctx, workspace.Repository{ID: "r", Name: "r", Path: "."}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Repository(ctx, "r", repo); err != nil {
		t.Fatal(err)
	}
	definition := &scip.Occurrence{Symbol: "scip-rust cargo demo 1 target().", SymbolRoles: int32(scip.SymbolRole_Definition)}
	definition.SetSourceRange(scip.Range{Start: scip.Position{Line: 0, Character: 3}, End: scip.Position{Line: 0, Character: 9}})
	ref := &scip.Occurrence{Symbol: definition.Symbol}
	ref.SetSourceRange(scip.Range{Start: scip.Position{Line: 1, Character: 14}, End: scip.Position{Line: 1, Character: 20}})
	doc := &scip.Document{RelativePath: "lib.rs", Text: body, Symbols: []*scip.SymbolInformation{{Symbol: definition.Symbol, DisplayName: "target", Kind: scip.SymbolInformation_Function}}, Occurrences: []*scip.Occurrence{definition, ref}}
	idx := &scip.Index{Documents: []*scip.Document{doc}}
	data, err := proto.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "index.scip")
	if err := os.WriteFile(file, data, 0644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		stats, err := scipindex.Import(ctx, st, "r", repo, file)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Symbols != 1 || stats.Occurrences != 2 {
			t.Fatalf("bad counts: %+v", stats)
		}
	}
	nodes, err := graph.New(st).NodesByName(ctx, "target", nil, 10)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes=%+v err=%v", nodes, err)
	}
	impact, err := graph.New(st).ImpactOf(ctx, []int64{nodes[0].ID}, graph.ChangeSignature)
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.Consumers) == 0 {
		t.Fatal("SCIP reference missing from impact graph")
	}
	if err := os.WriteFile(filepath.Join(repo, "lib.rs"), []byte(body+"// drift\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := scipindex.Import(ctx, st, "r", repo, file); err == nil {
		t.Fatal("stale embedded text accepted")
	}
}
