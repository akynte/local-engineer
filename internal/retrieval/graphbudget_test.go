package retrieval_test

import (
	"context"
	"testing"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
	"github.com/akynte/local-engineer/internal/retrieval"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// wired builds a store with a symbol that has many neighbours, so expansion has
// far more to offer than the packet can hold.
func wired(t *testing.T, neighbours int) (*retrieval.Retriever, *store.Store) {
	t.Helper()
	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.CloseAll() })
	ctx := context.Background()
	st, err := root.OpenWorkspace(ctx, workspace.DeriveID("/graphbudget", "", t.Name()))
	if err != nil {
		t.Fatal(err)
	}
	// A node hangs off a repository; without one the foreign key refuses it.
	if err := index.New(st, index.Options{}).RegisterRepository(ctx, workspace.Repository{
		ID: "r", Name: "r", Path: ".", DefaultBranch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	g := graph.New(st)

	body := ""
	for range 60 {
		body += "// a line of context that costs tokens to carry\n"
	}
	root_, err := g.UpsertNode(ctx, graph.Node{
		WorkspaceID: st.ID(), RepositoryID: "r", Kind: graph.KindFunction,
		Name: "Seed", FQN: "pkg.Seed", Path: "seed.go", ContentHash: "h0",
		StartLine: 1, EndLine: 60, Signature: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	var edges []graph.Edge
	for i := range neighbours {
		id, err := g.UpsertNode(ctx, graph.Node{
			WorkspaceID: st.ID(), RepositoryID: "r", Kind: graph.KindFunction,
			Name: "N" + string(rune('A'+i%26)),
			FQN:  "pkg.N" + string(rune('A'+i%26)) + string(rune('0'+i/26)),
			Path: "n.go", ContentHash: "h", StartLine: 1, EndLine: 60, Signature: body,
		})
		if err != nil {
			t.Fatal(err)
		}
		edges = append(edges, graph.Edge{
			WorkspaceID: st.ID(), Src: root_, Dst: id, Kind: graph.EdgeCalls, Evidence: graph.Resolved, Source: "test",
		})
	}
	if err := g.UpsertEdges(ctx, edges); err != nil {
		t.Fatal(err)
	}
	return retrieval.New(st), st
}

// Expansion had no bound. It reached up to MaxNodes neighbours and appended
// them, so it filled whatever budget the anchors left — on every step of every
// task. The first measured run showed the cost: the arm with the graph spent
// 1.8x the tokens of the arm without it and solved no more.
func TestGraphExpansionCannotTakeMostOfThePacket(t *testing.T) {
	r, _ := wired(t, 80)
	const budget = 4000

	pkt, err := r.Build(context.Background(), retrieval.Request{
		Symbols: []string{"Seed"}, ExpandDepth: 1, TokenBudget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}

	var graphTokens int
	for _, s := range pkt.Slices {
		if s.Origin == retrieval.OriginGraph {
			graphTokens += s.TokenEstimate()
		}
	}
	if graphTokens == 0 {
		t.Fatal("expansion contributed nothing, so this proves nothing about the cap")
	}
	// The bound is stated independently of GraphBudgetFraction on purpose. An
	// assertion derived from the constant it is testing rises with it: setting
	// the fraction to 1.0 would raise the bar to the whole packet and the test
	// would still pass, which is how the first version of this passed against
	// the defect it was written for.
	if half := budget / 2; graphTokens > half {
		t.Errorf("expansion took %d tokens of a %d budget; it must not be most of the packet "+
			"(more than %d)", graphTokens, budget, half)
	}
	if pkt.Dropped == 0 {
		t.Error("nothing was dropped, so the cap never bit and the test is not exercising it")
	}
}

// The cap must not silence expansion entirely: a consumer in another package is
// exactly what lexical search cannot find, and that is the graph's whole case.
func TestExpansionStillReachesThePacket(t *testing.T) {
	r, _ := wired(t, 3)
	pkt, err := r.Build(context.Background(), retrieval.Request{
		Symbols: []string{"Seed"}, ExpandDepth: 1, TokenBudget: 20000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, s := range pkt.Slices {
		if s.Origin == retrieval.OriginGraph {
			n++
		}
	}
	if n == 0 {
		t.Error("no expanded slice reached the packet; the cap has disabled the graph")
	}
}
