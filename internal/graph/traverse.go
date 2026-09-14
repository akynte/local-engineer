package graph

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// evidenceRankSQL maps an evidence category to a strength rank, lower being
// stronger. A path is only as strong as its weakest hop, so the traversal
// carries max(rank) along each path.
const evidenceRankSQL = `CASE e.evidence
	WHEN 'resolved' THEN 0 WHEN 'declared' THEN 1 WHEN 'inferred' THEN 2
	WHEN 'observed' THEN 3 ELSE 4 END`

var rankToEvidence = map[int]Evidence{0: Resolved, 1: Declared, 2: Inferred, 3: Observed, 4: Unknown}

// Traverse walks the graph from the start nodes, one hop at a time.
//
// It expands level by level rather than in a single recursive CTE. A recursive
// CTE expands the *entire* reachable set before any LIMIT applies, so on a
// call graph — where fan-in is heavy-tailed and a few utility symbols have
// thousands of callers — a three-hop reverse walk from a hot node materialises
// most of the repository before returning the first row. Measured on a
// synthetic 100k-edge graph with a Zipf degree distribution, that was ~200 ms
// per traversal; the design commits to an interactive budget an order of
// magnitude below that (§5.3), and impact analysis runs before every edit.
//
// Level-wise expansion bounds the work instead of only the output: the walk
// stops as soon as the node cap is reached, and each level is a bounded
// indexed lookup against edges_forward or edges_reverse.
//
// DR-2 is unaffected — the graph is still SQLite edge tables — and the
// semantics are unchanged: each node is reported once, at its shortest depth,
// with the strongest evidence available along a path of that depth.
func (g *sqliteGraph) Traverse(ctx context.Context, q Query) ([]Reached, error) {
	if len(q.Start) == 0 {
		return nil, nil
	}
	if q.MaxDepth <= 0 {
		q.MaxDepth = 1
	}
	if q.MaxDepth > MaxTraversalDepth {
		return nil, fmt.Errorf("graph: max depth %d exceeds the supported bound of %d (DR-2 commits to shallow traversals)",
			q.MaxDepth, MaxTraversalDepth)
	}
	limit := q.MaxNodes
	if limit <= 0 {
		limit = DefaultMaxNodes
	}
	for _, ev := range q.MinEvidence {
		if !ev.Valid() {
			return nil, fmt.Errorf("graph: invalid evidence filter %q", ev)
		}
	}

	best := make(map[int64]pathState, limit)
	frontier := make([]int64, 0, len(q.Start))
	for _, id := range q.Start {
		if _, seen := best[id]; seen {
			continue
		}
		best[id] = pathState{depth: 0, rank: 0}
		frontier = append(frontier, id)
	}

	truncated := false
	for depth := 1; depth <= q.MaxDepth && len(frontier) > 0; depth++ {
		if len(best) >= limit {
			truncated = true
			break
		}
		next, hitCap, err := g.expand(ctx, frontier, q, depth, limit, best)
		if err != nil {
			return nil, err
		}
		if hitCap {
			truncated = true
		}
		frontier = next
	}
	if len(frontier) > 0 && len(best) >= limit {
		truncated = true
	}

	ids := make([]int64, 0, len(best))
	for id := range best {
		ids = append(ids, id)
	}
	nodes, err := g.loadNodes(ctx, ids)
	if err != nil {
		return nil, err
	}

	out := make([]Reached, 0, len(nodes))
	for _, n := range nodes {
		st := best[n.ID]
		ev := rankToEvidence[st.rank]
		if st.depth == 0 {
			// A start node is itself, not a claim about a relationship.
			ev = Resolved
		}
		out = append(out, Reached{Node: n, Depth: st.depth, Via: st.via, Evidence: ev})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Depth != out[j].Depth {
			return out[i].Depth < out[j].Depth
		}
		if out[i].Node.Kind != out[j].Node.Kind {
			return out[i].Node.Kind < out[j].Node.Kind
		}
		return out[i].Node.FQN < out[j].Node.FQN
	})
	if truncated && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// pathState is the best way found so far to reach one node: the shortest
// depth, and the strongest evidence along a path of that depth.
type pathState struct {
	depth int
	rank  int
	via   EdgeKind
}

// expandBatch is how many frontier nodes go into one IN clause. SQLite's
// default variable limit is well above this; the bound is about keeping each
// statement's plan small, not about the limit.
const expandBatch = 400

// expand walks one hop from the frontier, recording newly reached nodes and
// returning the next frontier. hitCap reports whether the node cap stopped it.
func (g *sqliteGraph) expand(ctx context.Context, frontier []int64, q Query, depth, limit int,
	best map[int64]pathState) ([]int64, bool, error) {
	joinOn, step := "e.src_id", "e.dst_id"
	if q.Dir == Reverse {
		joinOn, step = "e.dst_id", "e.src_id"
	}

	var next []int64
	for start := 0; start < len(frontier); start += expandBatch {
		end := start + expandBatch
		if end > len(frontier) {
			end = len(frontier)
		}
		chunk := frontier[start:end]

		ph := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)+len(q.Kinds)+len(q.MinEvidence)+1)
		for i, id := range chunk {
			ph[i] = "?"
			args = append(args, id)
		}
		var filters strings.Builder
		if len(q.Kinds) > 0 {
			kph := make([]string, len(q.Kinds))
			for i, k := range q.Kinds {
				kph[i] = "?"
				args = append(args, string(k))
			}
			fmt.Fprintf(&filters, " AND e.kind IN (%s)", strings.Join(kph, ","))
		}
		if len(q.MinEvidence) > 0 {
			eph := make([]string, len(q.MinEvidence))
			for i, ev := range q.MinEvidence {
				eph[i] = "?"
				args = append(args, string(ev))
			}
			fmt.Fprintf(&filters, " AND e.evidence IN (%s)", strings.Join(eph, ","))
		}
		// Fetch at most as many edges as could still be useful. Without this a
		// single hot node can return a six-figure row set that is then mostly
		// discarded.
		remaining := limit - len(best)
		if remaining <= 0 {
			return next, true, nil
		}
		rowCap := remaining * 4
		args = append(args, rowCap)

		query := `SELECT ` + joinOn + `, ` + step + `, e.kind, ` + evidenceRankSQL + `
			FROM edges e WHERE ` + joinOn + ` IN (` + strings.Join(ph, ",") + `)` +
			filters.String() + ` LIMIT ?`

		rows, err := g.db.SQL().QueryContext(ctx, query, args...)
		if err != nil {
			return nil, false, fmt.Errorf("graph: expand depth %d: %w", depth, err)
		}
		fetched := 0
		for rows.Next() {
			var from, to int64
			var kind string
			var rank int
			if err := rows.Scan(&from, &to, &kind, &rank); err != nil {
				rows.Close()
				return nil, false, err
			}
			fetched++

			// A path is only as strong as its weakest hop.
			parent := best[from]
			if parent.rank > rank {
				rank = parent.rank
			}
			prev, seen := best[to]
			if seen && (prev.depth < depth || (prev.depth == depth && prev.rank <= rank)) {
				continue
			}
			if !seen && len(best) >= limit {
				rows.Close()
				return next, true, nil
			}
			best[to] = pathState{depth: depth, rank: rank, via: EdgeKind(kind)}
			if !seen {
				next = append(next, to)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, false, err
		}
		if fetched >= rowCap {
			return next, true, nil
		}
	}
	return next, false, nil
}

// loadNodes fetches node rows in batches, verifying each against the handle's
// workspace.
func (g *sqliteGraph) loadNodes(ctx context.Context, ids []int64) ([]Node, error) {
	out := make([]Node, 0, len(ids))
	for start := 0; start < len(ids); start += expandBatch {
		end := start + expandBatch
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		ph := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for i, id := range chunk {
			ph[i] = "?"
			args[i] = id
		}
		rows, err := g.db.SQL().QueryContext(ctx,
			`SELECT `+nodeColumns+nodeFrom+` WHERE n.node_id IN (`+strings.Join(ph, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			n, err := scanNode(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			if n, err = g.verify(n, nil); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, n)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ImpactOf implements §3.3: a reverse traversal from the changed nodes,
// reported with evidence categories, deterministic compatibility verdicts and
// required migration steps. Weak evidence is never filtered out.
func (g *sqliteGraph) ImpactOf(ctx context.Context, changed []int64, kind ChangeKind) (Impact, error) {
	if len(changed) == 0 {
		return Impact{Kind: kind, Counts: map[Verdict]int{}, Caveat: ImpactCaveat}, nil
	}
	changedNodes := make([]Node, 0, len(changed))
	for _, id := range changed {
		n, err := g.Node(ctx, id)
		if err != nil {
			return Impact{}, err
		}
		changedNodes = append(changedNodes, n)
	}

	const impactDepth = 3 // consumers of consumers, which is where the design's 2-4 hop claim sits
	limit := DefaultMaxNodes
	reached, err := g.Traverse(ctx, Query{
		Start:    changed,
		Dir:      Reverse,
		MaxDepth: impactDepth,
		MaxNodes: limit,
		// MinEvidence deliberately unset: §3.3 treats inferred and unknown
		// consumers as present.
	})
	if err != nil {
		return Impact{}, err
	}
	return buildImpact(changedNodes, kind, reached, len(reached) >= limit), nil
}

func (g *sqliteGraph) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	s.ByEdge = map[string]int64{}
	s.ByEvid = map[string]int64{}
	db := g.db.SQL()
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM nodes`).Scan(&s.Nodes); err != nil {
		return s, err
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM edges`).Scan(&s.Edges); err != nil {
		return s, err
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM index_keys WHERE dirty = 1`).Scan(&s.DirtyKey); err != nil {
		return s, err
	}
	for _, spec := range []struct {
		col string
		dst map[string]int64
	}{{"kind", s.ByEdge}, {"evidence", s.ByEvid}} {
		rows, err := db.QueryContext(ctx, `SELECT `+spec.col+`, count(*) FROM edges GROUP BY 1 ORDER BY 1`)
		if err != nil {
			return s, err
		}
		for rows.Next() {
			var k string
			var n int64
			if err := rows.Scan(&k, &n); err != nil {
				rows.Close()
				return s, err
			}
			spec.dst[k] = n
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return s, err
		}
	}
	return s, nil
}
