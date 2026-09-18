package retrieval

import (
	"context"
	"github.com/akynte/local-engineer/internal/policy"
	"sort"
)

type MapEntry struct {
	Path      string  `json:"path"`
	Symbol    string  `json:"symbol"`
	Signature string  `json:"signature"`
	Line      int     `json:"line"`
	Rank      float64 `json:"rank"`
}

// RepoMap ranks declarations by incoming references and renders signatures only.
// Its token budget is independent of the full-source localization budget.
func (r *Retriever) RepoMap(ctx context.Context, budget int) ([]MapEntry, error) {
	if budget <= 0 {
		budget = 3000
	}
	entries, ids, err := r.mapDeclarations(ctx)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	edges, err := r.mapEdges(ctx, ids)
	if err != nil {
		return nil, err
	}
	ranks := pageRank(len(entries), edges)
	for i := range entries {
		entries[i].Rank = ranks[i]
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Rank != entries[j].Rank {
			return entries[i].Rank > entries[j].Rank
		}
		if entries[i].Path != entries[j].Path {
			return entries[i].Path < entries[j].Path
		}
		return entries[i].Symbol < entries[j].Symbol
	})
	var out []MapEntry
	spent := 0
	for _, e := range entries {
		cost := (len(e.Path)+len(e.Symbol)+len(e.Signature))/3 + 20
		if spent+cost > budget {
			continue
		}
		spent += cost
		out = append(out, e)
	}
	return out, nil
}

func pageRank(n int, edges [][2]int) []float64 {
	rank := make([]float64, n)
	degree := make([]int, n)
	if n == 0 {
		return rank
	}
	for i := range rank {
		rank[i] = 1 / float64(n)
	}
	for _, edge := range edges {
		degree[edge[0]]++
	}
	for step := 0; step < 40; step++ {
		next := make([]float64, n)
		dangling := 0.0
		for i, d := range degree {
			if d == 0 {
				dangling += rank[i]
			}
		}
		for i := range next {
			next[i] = (0.15 + 0.85*dangling) / float64(n)
		}
		for _, edge := range edges {
			next[edge[1]] += 0.85 * rank[edge[0]] / float64(degree[edge[0]])
		}
		rank = next
	}
	return rank
}

// mapDeclarations reads the declarations the map ranks, and the index each
// node_id occupies so edges can be resolved without a second lookup.
func (r *Retriever) mapDeclarations(ctx context.Context) ([]MapEntry, map[int64]int, error) {
	rows, err := r.st.Index().SQL().QueryContext(ctx, `SELECT n.node_id,f.path,n.name,n.signature,n.start_line FROM nodes n JOIN files f ON f.file_id=n.file_id WHERE n.workspace_id=? AND n.kind IN ('function','method','type','class','interface','test','schema') ORDER BY n.node_id LIMIT 50000`, r.st.ID().String())
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var entries []MapEntry
	ids := map[int64]int{}
	for rows.Next() {
		var id int64
		var entry MapEntry
		if err := rows.Scan(&id, &entry.Path, &entry.Symbol, &entry.Signature, &entry.Line); err != nil {
			return nil, nil, err
		}
		if policy.Sensitive(entry.Path) {
			continue
		}
		ids[id] = len(entries)
		entries = append(entries, entry)
	}
	return entries, ids, rows.Err()
}

// mapEdges reads the reference graph, keeping only edges whose endpoints are
// both declarations the map kept.
func (r *Retriever) mapEdges(ctx context.Context, ids map[int64]int) ([][2]int, error) {
	rows, err := r.st.Index().SQL().QueryContext(ctx, `SELECT src_id,dst_id FROM edges WHERE workspace_id=? AND kind IN ('calls','references','implements','uses_type','tests') ORDER BY edge_id LIMIT 200000`, r.st.ID().String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges [][2]int
	for rows.Next() {
		var src, dst int64
		if err := rows.Scan(&src, &dst); err != nil {
			return nil, err
		}
		a, okA := ids[src]
		b, okB := ids[dst]
		if okA && okB {
			edges = append(edges, [2]int{a, b})
		}
	}
	return edges, rows.Err()
}
