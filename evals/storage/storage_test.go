// Package storage holds the storage benchmark of design v3 §5.3.
//
// It builds a synthetic graph at realistic sizes and measures symbol lookup,
// three-hop traversal, FTS query, brute-force vector scan, and ledger append
// and checkpoint latency. The results are the evidence for keeping or
// replacing SQLite for any component (DR-1, DR-2) — the decision records point
// here rather than at an opinion.
//
//	go test -run '^$' -bench . -benchmem ./evals/storage
//	go test -run '^$' -bench . -benchtime=10x -count 6 ./evals/storage | benchstat -
package storage

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/ledger"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// Scales the benchmark builds. §5.3 names 10^5 and 10^6 edges.
const (
	smallEdges = 100_000
	largeEdges = 1_000_000
)

func openStore(b *testing.B) *store.Store {
	b.Helper()
	root, err := store.OpenRoot(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { root.CloseAll() })
	st, err := root.OpenWorkspace(context.Background(), workspace.DeriveID("/bench", "", "storage"))
	if err != nil {
		b.Fatal(err)
	}
	return st
}

// buildGraph creates a synthetic graph shaped like a real codebase: a power-law
// fan-in, so a few symbols have many callers and most have few. A uniform
// random graph would make traversal look better than it is.
func buildGraph(b *testing.B, st *store.Store, edges int) []int64 {
	b.Helper()
	ctx := context.Background()
	g := graph.New(st)

	nodeCount := edges / 8
	if nodeCount < 100 {
		nodeCount = 100
	}

	if err := st.Index().Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO repositories (repository_id, workspace_id, name, rel_path)
		                   VALUES ('bench', ?, 'bench', '.')`, st.ID().String())
		return err
	}); err != nil {
		b.Fatal(err)
	}

	nodes := make([]graph.Node, nodeCount)
	for i := range nodes {
		nodes[i] = graph.Node{
			RepositoryID: "bench", Kind: graph.KindFunction,
			Name: fmt.Sprintf("Fn%d", i),
			FQN:  fmt.Sprintf("pkg%d.Fn%d", i%200, i),
		}
	}
	ids := make([]int64, 0, nodeCount)
	for start := 0; start < len(nodes); start += 2000 {
		end := start + 2000
		if end > len(nodes) {
			end = len(nodes)
		}
		batch, err := g.UpsertNodes(ctx, nodes[start:end])
		if err != nil {
			b.Fatal(err)
		}
		ids = append(ids, batch...)
	}

	rng := rand.New(rand.NewSource(1))
	// Zipf gives the heavy-tailed fan-in a real call graph has: a few symbols
	// with very many callers, most with few. A uniform random graph would make
	// traversal look faster than it is, because the hot nodes are exactly the
	// ones impact analysis walks into.
	zipf := rand.NewZipf(rng, 1.2, 1, uint64(len(ids)-1))

	batch := make([]graph.Edge, 0, 5000)
	seen := make(map[[2]int64]bool, edges)
	attempts := 0
	for len(seen) < edges {
		attempts++
		if attempts > edges*40 {
			b.Fatalf("could not generate %d distinct edges over %d nodes; "+
				"the degree distribution is too concentrated", edges, len(ids))
		}
		src := ids[rng.Intn(len(ids))]
		dst := ids[zipf.Uint64()]
		if src == dst || seen[[2]int64{src, dst}] {
			continue
		}
		seen[[2]int64{src, dst}] = true
		batch = append(batch, graph.Edge{
			Src: src, Dst: dst, Kind: graph.EdgeCalls,
			Evidence: graph.Resolved, Source: "bench",
		})
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
	return ids
}

func BenchmarkSymbolLookup(b *testing.B) {
	for _, size := range []int{smallEdges, largeEdges} {
		b.Run(label(size), func(b *testing.B) {
			st := openStore(b)
			buildGraph(b, st, size)
			g := graph.New(st)
			ctx := context.Background()

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				name := fmt.Sprintf("Fn%d", i%(size/8))
				if _, err := g.NodesByName(ctx, name, nil, 10); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkThreeHopTraversal(b *testing.B) {
	for _, size := range []int{smallEdges, largeEdges} {
		b.Run(label(size), func(b *testing.B) {
			st := openStore(b)
			ids := buildGraph(b, st, size)
			g := graph.New(st)
			ctx := context.Background()

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				reached, err := g.Traverse(ctx, graph.Query{
					Start: []int64{ids[i%len(ids)]}, Dir: graph.Reverse, MaxDepth: 3, MaxNodes: 5000,
				})
				if err != nil {
					b.Fatal(err)
				}
				_ = reached
			}
		})
	}
}

func BenchmarkImpactAnalysis(b *testing.B) {
	st := openStore(b)
	ids := buildGraph(b, st, smallEdges)
	g := graph.New(st)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := g.ImpactOf(ctx, []int64{ids[i%len(ids)]}, graph.ChangeSignature); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFTSQuery(b *testing.B) {
	st := openStore(b)
	ctx := context.Background()

	const chunks = 100_000
	if err := st.Index().Tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`INSERT INTO chunks_fts (rowid, body, path, symbol) VALUES (?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i := 0; i < chunks; i++ {
			body := fmt.Sprintf("func GetUser%d(ctx context.Context, id string) (*User, error) { "+
				"return s.repo%d.Load(ctx, id) }", i, i%500)
			if _, err := stmt.Exec(i+1, body, fmt.Sprintf("pkg%d/file%d.go", i%200, i), "GetUser"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rows, err := st.Index().SQL().QueryContext(ctx,
			`SELECT rowid FROM chunks_fts WHERE chunks_fts MATCH ? ORDER BY bm25(chunks_fts) LIMIT 20`,
			fmt.Sprintf(`"GetUser%d"`, i%chunks))
		if err != nil {
			b.Fatal(err)
		}
		for rows.Next() {
		}
		rows.Close()
	}
}

// BenchmarkVectorScan measures the brute-force scan that is the default when
// semantic retrieval is enabled (§5.1: sqlite-vec is optional; brute force in
// Go is the default).
func BenchmarkVectorScan(b *testing.B) {
	const (
		chunks = 100_000
		dims   = 1024
	)
	rng := rand.New(rand.NewSource(2))
	vectors := make([][]float32, chunks)
	for i := range vectors {
		v := make([]float32, dims)
		for j := range v {
			v[j] = rng.Float32()
		}
		vectors[i] = v
	}
	query := make([]float32, dims)
	for j := range query {
		query[j] = rng.Float32()
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		best, bestScore := -1, float32(-1)
		for idx, v := range vectors {
			var dot float32
			for j := range v {
				dot += v[j] * query[j]
			}
			if dot > bestScore {
				best, bestScore = idx, dot
			}
		}
		_ = best
	}
}

// BenchmarkVectorDecode measures decoding stored vectors, which is the cost
// the brute-force scan pays when they are not already in memory.
func BenchmarkVectorDecode(b *testing.B) {
	const dims = 1024
	raw := make([]byte, dims*4)
	for i := 0; i < dims; i++ {
		binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(float32(i)))
	}
	out := make([]float32, dims)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for j := 0; j < dims; j++ {
			out[j] = math.Float32frombits(binary.LittleEndian.Uint32(raw[j*4:]))
		}
	}
}

func BenchmarkLedgerAppend(b *testing.B) {
	st := openStore(b)
	ctx := context.Background()
	l := ledger.New(st)

	if err := st.Ledger().Tx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		_, err := tx.Exec(`INSERT INTO tasks (id, workspace_id, title, state, created_at, updated_at)
		                   VALUES ('bench', ?, 'bench', 'running', ?, ?)`, st.ID().String(), now, now)
		return err
	}); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h, err := l.Begin(ctx, "bench", ledger.KindInspectFile,
			map[string]any{"path": fmt.Sprintf("pkg/file%d.go", i)}, "")
		if err != nil {
			b.Fatal(err)
		}
		if err := h.Complete(ctx, map[string]string{"status": "ok"}, "", ""); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLedgerCheckpoint(b *testing.B) {
	st := openStore(b)
	ctx := context.Background()
	l := ledger.New(st)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		err := l.Checkpoint(ctx, "bench", "running", map[string]any{
			"objective": "benchmark", "remaining_plan": []string{"a", "b"}, "next_action": "a",
		}, fmt.Sprintf("candidate-%d", i))
		if err != nil {
			b.Fatal(err)
		}
	}
}

func label(size int) string {
	if size >= 1_000_000 {
		return fmt.Sprintf("%dM_edges", size/1_000_000)
	}
	return fmt.Sprintf("%dK_edges", size/1000)
}
