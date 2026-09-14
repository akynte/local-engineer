// Package graph holds the traversal comparison harness of design v3 §3.5 and
// DR-2: "a benchmark in evals/graph/ compares traversal latency and can
// justify a swap later."
//
// It measures the current level-wise implementation against the recursive-CTE
// strategy it replaced, at several depths and sizes. Keeping the old strategy
// here as a measured baseline — rather than deleting it — is what makes the
// comparison reproducible when someone next proposes a different backend.
//
//	go test -run '^$' -bench . -benchmem ./evals/graph
package graph

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"testing"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

func setup(b *testing.B, edges int) (*store.Store, []int64) {
	b.Helper()
	ctx := context.Background()
	root, err := store.OpenRoot(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { root.CloseAll() })
	st, err := root.OpenWorkspace(ctx, workspace.DeriveID("/bench", "", "graph"))
	if err != nil {
		b.Fatal(err)
	}

	if err := st.Index().Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO repositories (repository_id, workspace_id, name, rel_path)
		                   VALUES ('bench', ?, 'bench', '.')`, st.ID().String())
		return err
	}); err != nil {
		b.Fatal(err)
	}

	g := graph.New(st)
	nodeCount := edges / 8
	nodes := make([]graph.Node, nodeCount)
	for i := range nodes {
		nodes[i] = graph.Node{
			RepositoryID: "bench", Kind: graph.KindFunction,
			Name: fmt.Sprintf("Fn%d", i), FQN: fmt.Sprintf("pkg%d.Fn%d", i%200, i),
		}
	}
	ids := make([]int64, 0, nodeCount)
	for s := 0; s < len(nodes); s += 2000 {
		e := s + 2000
		if e > len(nodes) {
			e = len(nodes)
		}
		batch, err := g.UpsertNodes(ctx, nodes[s:e])
		if err != nil {
			b.Fatal(err)
		}
		ids = append(ids, batch...)
	}

	rng := rand.New(rand.NewSource(7))
	zipf := rand.NewZipf(rng, 1.2, 1, uint64(len(ids)-1))
	seen := make(map[[2]int64]bool, edges)
	batch := make([]graph.Edge, 0, 5000)
	for len(seen) < edges {
		src, dst := ids[rng.Intn(len(ids))], ids[zipf.Uint64()]
		if src == dst || seen[[2]int64{src, dst}] {
			continue
		}
		seen[[2]int64{src, dst}] = true
		batch = append(batch, graph.Edge{Src: src, Dst: dst, Kind: graph.EdgeCalls,
			Evidence: graph.Resolved, Source: "bench"})
		if len(batch) == cap(batch) {
			if err := g.UpsertEdges(ctx, batch); err != nil {
				b.Fatal(err)
			}
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := g.UpsertEdges(ctx, batch); err != nil {
			b.Fatal(err)
		}
	}
	return st, ids
}

// BenchmarkLevelwise measures the shipped implementation.
func BenchmarkLevelwise(b *testing.B) {
	for _, size := range []int{100_000, 1_000_000} {
		for _, depth := range []int{1, 2, 3, 4} {
			b.Run(fmt.Sprintf("%dK/depth%d", size/1000, depth), func(b *testing.B) {
				st, ids := setup(b, size)
				g := graph.New(st)
				ctx := context.Background()
				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := g.Traverse(ctx, graph.Query{
						Start: []int64{ids[i%len(ids)]}, Dir: graph.Reverse,
						MaxDepth: depth, MaxNodes: 5000,
					}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkRecursiveCTE measures the strategy the shipped implementation
// replaced. It is kept as a baseline, not as dead code: DR-2's replacement
// path says a swap must be justified by this harness, and a comparison needs
// something to compare against.
//
// The essential difference is that a recursive CTE expands the whole reachable
// set before any outer LIMIT applies, so the node cap bounds the output and
// not the work.
func BenchmarkRecursiveCTE(b *testing.B) {
	for _, size := range []int{100_000, 1_000_000} {
		for _, depth := range []int{1, 2, 3} {
			b.Run(fmt.Sprintf("%dK/depth%d", size/1000, depth), func(b *testing.B) {
				st, ids := setup(b, size)
				ctx := context.Background()
				const q = `
WITH RECURSIVE seed(node_id) AS (VALUES (?)),
reach(node_id, depth, via, evid_rank) AS (
	SELECT node_id, 0, '', 0 FROM seed
	UNION
	SELECT e.src_id, r.depth + 1, e.kind,
	       max(r.evid_rank, CASE e.evidence WHEN 'resolved' THEN 0 WHEN 'declared' THEN 1
	                                        WHEN 'inferred' THEN 2 WHEN 'observed' THEN 3 ELSE 4 END)
	FROM reach r JOIN edges e ON e.dst_id = r.node_id
	WHERE r.depth < ?
),
best AS (
	SELECT node_id, depth, via, evid_rank,
	       row_number() OVER (PARTITION BY node_id ORDER BY depth, evid_rank) AS rn
	FROM reach
)
SELECT n.node_id, n.fqn, b.depth FROM best b
JOIN nodes n ON n.node_id = b.node_id
WHERE b.rn = 1 ORDER BY b.depth LIMIT 5000`

				drain := func(i int) error {
					rows, err := st.Index().SQL().QueryContext(ctx, q, ids[i%len(ids)], depth)
					if err != nil {
						return err
					}
					defer rows.Close()
					for rows.Next() {
						var id int64
						var fqn string
						var d int
						if err := rows.Scan(&id, &fqn, &d); err != nil {
							return err
						}
					}
					return rows.Err()
				}

				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if err := drain(i); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// TestStrategiesAgree guards the swap: the two strategies must return the same
// node set at depth 2, or the benchmark is comparing different work.
func TestStrategiesAgree(t *testing.T) {
	// Smaller than the benchmarks: this test is about agreement, not about
	// cost, and it runs on every `make check`.
	const edges = 30_000
	b := &testing.B{}
	st, ids := setup(b, edges)
	ctx := context.Background()
	g := graph.New(st)

	// A cold node, so neither strategy hits the node cap and the comparison is
	// over a complete result rather than two different truncations.
	start := ids[len(ids)-1]

	reached, err := g.Traverse(ctx, graph.Query{
		Start: []int64{start}, Dir: graph.Reverse, MaxDepth: 2, MaxNodes: 100_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	levelwise := map[int64]int{}
	for _, r := range reached {
		levelwise[r.Node.ID] = r.Depth
	}

	rows, err := st.Index().SQL().QueryContext(ctx, `
WITH RECURSIVE reach(node_id, depth) AS (
	SELECT ?, 0
	UNION
	SELECT e.src_id, r.depth + 1 FROM reach r JOIN edges e ON e.dst_id = r.node_id WHERE r.depth < 2
)
SELECT node_id, min(depth) FROM reach GROUP BY node_id`, start)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	cte := map[int64]int{}
	for rows.Next() {
		var id int64
		var d int
		if err := rows.Scan(&id, &d); err != nil {
			t.Fatal(err)
		}
		cte[id] = d
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(levelwise) != len(cte) {
		t.Fatalf("the two strategies reached different node counts: level-wise %d, CTE %d",
			len(levelwise), len(cte))
	}
	for id, depth := range cte {
		got, ok := levelwise[id]
		if !ok {
			t.Fatalf("level-wise traversal missed node %d that the CTE reached", id)
		}
		if got != depth {
			t.Fatalf("node %d: level-wise depth %d, CTE depth %d", id, got, depth)
		}
	}
}
