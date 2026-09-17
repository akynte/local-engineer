package retrieval

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/memory"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/version"
	"github.com/akynte/local-engineer/internal/workspace"
)

// Retriever answers context requests for one workspace.
//
// The order is fixed by §8.2 and is deliberately not a model decision:
//  1. lexical anchors over FTS5,
//  2. graph expansion from those anchors,
//  3. mandatory slots filled by impact analysis so consumers and contracts are
//     never dropped.
type Retriever struct {
	st  *store.Store
	g   graph.Graph
	mem *memory.Store
}

// New binds a retriever to a workspace store.
func New(s *store.Store) *Retriever { return &Retriever{st: s, g: graph.New(s)} }

// WithMemory attaches the repository's durable notes (§8.2, §11).
//
// They live in the repository rather than the data directory so they travel
// with it, which is why the retriever cannot find them on its own: it knows a
// workspace, and the notes belong to a checkout.
func (r *Retriever) WithMemory(m *memory.Store) *Retriever {
	r.mem = m
	return r
}

// MemoryBudgetFraction is the share of a packet that durable notes may take.
//
// §8.2 adopts this memory at "low" cost and §11 flags the pattern as "good,
// easy to abuse": notes accumulate, and a playbook that grows until it crowds
// out the code is the failure mode. A note that does not fit is dropped and
// counted rather than silently trimmed, so the cap is visible when it bites.
const MemoryBudgetFraction = 0.15

// GraphBudgetFraction is the share of a packet that graph expansion may take.
//
// Expansion has the same failure mode the notes cap exists for, and had no
// equivalent bound: it reaches up to MaxNodes neighbours and appends them, so
// it filled whatever budget the anchors left, on every step of every task. The
// first measured run showed what that costs — the arm with the graph spent
// 1.8x the tokens of the arm without it and solved no more, which is what
// paying full price for a packet nobody read looks like.
//
// A third is deliberately generous. The graph's case is real: a consumer in
// another package is exactly what lexical search cannot find. The claim being
// bounded is not that expansion is worthless, only that it must compete for
// room rather than take what is left.
const GraphBudgetFraction = 0.33

// WorkspaceID reports the workspace this retriever serves.
func (r *Retriever) WorkspaceID() workspace.ID { return r.st.ID() }

// Request describes what the current step needs.
type Request struct {
	// Query is the lexical anchor text.
	Query string
	// Symbols are symbol names to look up directly.
	Symbols []string
	// MaxAnchors caps stage 1.
	MaxAnchors int
	// ExpandDepth caps stage 2. Zero disables graph expansion.
	ExpandDepth int
	// ExpandKinds restricts which edges are followed.
	ExpandKinds []graph.EdgeKind
	// ImpactOf, when set, fills the mandatory consumer slots for a planned
	// change to the named symbols.
	ImpactOf []string
	// ImpactChange classifies that planned change.
	ImpactChange graph.ChangeKind
	// TokenBudget caps the packet. Zero means DefaultTokenBudget.
	TokenBudget int
}

// DefaultTokenBudget is a conservative packet cap. §9.3 forbids hardcoding
// real limits: the operative value comes from the active hardware profile, and
// this constant is only the fallback when no profile is loaded.
const DefaultTokenBudget = 6000

// Packet is the assembled context for one step, plus the accounting that makes
// §8.3's metrics possible.
type Packet struct {
	WorkspaceID workspace.ID `json:"workspace_id"`
	Slices      []Slice      `json:"slices"`
	Tokens      int          `json:"tokens"`
	Budget      int          `json:"budget"`
	// Rejected counts slices dropped by Guard. Any non-zero value here is an
	// isolation incident and is surfaced, never swallowed (§2.3).
	Rejected []string `json:"rejected,omitempty"`
	// Dropped counts slices that did not fit the budget, which is the signal
	// that the packet cap is too small for the task (§8.3).
	Dropped int `json:"dropped"`
	// Impact is attached when the request asked for it, so the planner and the
	// human gate see the same report (§3.3).
	Impact *graph.Impact `json:"impact,omitempty"`
	// Notes are the repository's durable memory (§8.2): intent explaining why
	// work is being done, observations of what was seen, advice meant to steer
	// it. They are carried separately from Slices because they are not code and
	// must not be read as if they were.
	Notes []memory.Note `json:"notes,omitempty"`
	// NotesDropped counts notes that did not fit the memory budget. A playbook
	// quietly losing its tail is how advice stops matching what people think
	// the system was told.
	NotesDropped int `json:"notes_dropped,omitempty"`
}

// Build assembles a packet. Every slice that leaves this function has passed
// Guard, so a packet can never carry another workspace's content.
func (r *Retriever) Build(ctx context.Context, req Request) (*Packet, error) {
	if req.MaxAnchors <= 0 {
		req.MaxAnchors = 20
	}
	budget := req.TokenBudget
	if budget <= 0 {
		budget = DefaultTokenBudget
	}

	var candidates []Slice

	// Stage 1: lexical anchors.
	if req.Query != "" {
		anchors, err := r.anchors(ctx, req.Query, req.MaxAnchors)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, anchors...)
	}
	for _, sym := range req.Symbols {
		nodes, err := r.g.NodesByName(ctx, sym, nil, 10)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			candidates = append(candidates, r.sliceFromNode(n, OriginExplicit, 1.0, 0, "", ""))
		}
	}

	// Stage 2: graph expansion from the anchors.
	if req.ExpandDepth > 0 {
		seeds := nodeIDs(candidates)
		if len(seeds) > 0 {
			reached, err := r.g.Traverse(ctx, graph.Query{
				Start: seeds, Dir: graph.Forward, Kinds: req.ExpandKinds,
				MaxDepth: req.ExpandDepth, MaxNodes: 500,
			})
			if err != nil {
				return nil, err
			}
			for _, rc := range reached {
				if rc.Depth == 0 {
					continue
				}
				candidates = append(candidates,
					r.sliceFromNode(rc.Node, OriginGraph, 0.5/float64(rc.Depth), rc.Depth, rc.Via, rc.Evidence))
			}
		}
	}

	pkt := &Packet{WorkspaceID: r.st.ID(), Budget: budget}

	// Stage 3: mandatory impact slots. These are added last but reserved
	// first: §8.2 requires that consumers and contracts are never dropped, so
	// they are placed into the packet before discretionary slices compete for
	// the remaining budget.
	var mandatory []Slice
	if len(req.ImpactOf) > 0 {
		imp, err := r.impact(ctx, req.ImpactOf, req.ImpactChange)
		if err != nil {
			return nil, err
		}
		pkt.Impact = imp
		for _, c := range imp.Consumers {
			mandatory = append(mandatory, r.sliceFromNode(c.Node, OriginImpact, 1.0, c.Depth, c.Via, c.Evidence))
		}
	}

	// Best first, within each origin. The score was computed for every slice
	// — bm25 for an anchor, inverse depth for an expanded node — and nothing
	// read it: the packet was filled in the order things happened to be
	// appended, so a depth-2 neighbour could displace a better one purely by
	// traversal order. Sorting inside an origin rather than across all of them
	// because bm25 and inverse depth are not the same scale, and comparing
	// them directly would rank by units rather than by relevance.
	sortByScore(candidates)

	kept, rejected := Guard(r.st.ID(), append(mandatory, candidates...))
	for _, err := range rejected {
		pkt.Rejected = append(pkt.Rejected, err.Error())
	}

	// Notes first: they are the stable part of the packet and §8.2 puts the
	// stable prefix first so the provider's prompt cache survives the loop.
	// Their cost comes out of the budget before code competes for it, bounded
	// so they can never be most of the packet.
	r.addNotes(pkt, int(float64(budget)*MemoryBudgetFraction))

	graphBudget := int(float64(budget) * GraphBudgetFraction)
	graphTokens := 0

	seen := map[int64]bool{}
	for _, s := range kept {
		if s.NodeID != 0 {
			if seen[s.NodeID] {
				continue
			}
			seen[s.NodeID] = true
		}
		cost := s.TokenEstimate()
		// Expansion competes for a bounded share, the way notes do. Without
		// this it takes whatever the anchors left, which is most of the packet
		// on most steps.
		if s.Origin == OriginGraph && graphTokens+cost > graphBudget {
			pkt.Dropped++
			continue
		}
		if s.Origin != OriginImpact && pkt.Tokens+cost > budget {
			pkt.Dropped++
			continue
		}
		if s.Origin == OriginGraph {
			graphTokens += cost
		}
		pkt.Slices = append(pkt.Slices, s)
		pkt.Tokens += cost
	}
	return pkt, nil
}

// anchors runs the FTS5 stage. bm25() is SQLite's built-in ranking function;
// lower is better, so it is negated into a score.
func (r *Retriever) anchors(ctx context.Context, query string, limit int) ([]Slice, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	rows, err := r.st.Index().SQL().QueryContext(ctx, `
		SELECT c.chunk_id, c.repository_id, f.path, c.start_line, c.end_line,
		       c.content_hash, c.index_version, COALESCE(f.worktree_id,''),
		       snippet(chunks_fts, 0, '', '', ' … ', 24) AS body,
		       -bm25(chunks_fts) AS score
		FROM chunks_fts
		JOIN chunks c ON c.chunk_id = chunks_fts.rowid
		JOIN files f ON f.file_id = c.file_id
		WHERE chunks_fts MATCH ?
		ORDER BY score DESC
		LIMIT ?`, match, limit)
	if err != nil {
		return nil, fmt.Errorf("retrieval: lexical anchors: %w", err)
	}
	defer rows.Close()

	var out []Slice
	for rows.Next() {
		var s Slice
		var chunkID int64
		if err := rows.Scan(&chunkID, &s.RepositoryID, &s.Path, &s.StartLine, &s.EndLine,
			&s.ContentHash, &s.IndexVersion, &s.WorktreeID, &s.Body, &s.Score); err != nil {
			return nil, err
		}
		s.WorkspaceID = r.st.ID()
		s.Origin = OriginAnchor
		s.Symbol = s.Path
		out = append(out, s)
	}
	return out, rows.Err()
}

// ftsQuery turns free text into an FTS5 MATCH expression. Every term is
// quoted, so user input can never be read as FTS5 syntax.
func ftsQuery(q string) string {
	// Split on anything that is not an identifier character. Punctuation is a
	// separator rather than syntax, so no input can reach FTS5 as an operator.
	isTermRune := func(r rune) bool {
		return r == '_' || r == '.' || r == '/' || r == '-' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
	}
	fields := strings.FieldsFunc(q, func(r rune) bool { return !isTermRune(r) })
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 2 {
			continue
		}
		terms = append(terms, `"`+strings.ReplaceAll(f, `"`, `""`)+`"`)
	}
	return strings.Join(terms, " OR ")
}

func (r *Retriever) impact(ctx context.Context, symbols []string, kind graph.ChangeKind) (*graph.Impact, error) {
	if kind == "" {
		kind = graph.ChangeBehaviour
	}
	var ids []int64
	for _, sym := range symbols {
		nodes, err := r.g.NodesByName(ctx, sym, nil, 10)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			ids = append(ids, n.ID)
		}
	}
	imp, err := r.g.ImpactOf(ctx, ids, kind)
	if err != nil {
		return nil, err
	}
	return &imp, nil
}

func (r *Retriever) sliceFromNode(n graph.Node, origin Origin, score float64, depth int,
	via graph.EdgeKind, ev graph.Evidence) Slice {
	path := n.Path
	if path == "" {
		path = n.FQN
	}
	hash := n.ContentHash
	if hash == "" {
		// A node without its own content hash still needs provenance; the FQN
		// digest keeps Validate honest rather than inventing a file hash.
		hash = "fqn:" + n.FQN
	}
	return Slice{
		WorkspaceID: n.WorkspaceID, RepositoryID: n.RepositoryID, WorktreeID: n.WorktreeID,
		Path: path, Symbol: n.Name, ContentHash: hash, IndexVersion: version.IndexerVersion,
		NodeID: n.ID, Kind: n.Kind, StartLine: n.StartLine, EndLine: n.EndLine,
		Signature: n.Signature, Origin: origin, Score: score, Depth: depth, Via: via, Evidence: ev,
	}
}

func nodeIDs(slices []Slice) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, s := range slices {
		if s.NodeID != 0 && !seen[s.NodeID] {
			seen[s.NodeID] = true
			out = append(out, s.NodeID)
		}
	}
	return out
}

// CountRows is a maintenance helper used by the isolation tests to assert that
// no table in a workspace database holds a row stamped with another workspace.
func (r *Retriever) CountForeignRows(ctx context.Context) (int, error) {
	total := 0
	err := r.st.Index().ReadTx(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{"repositories", "files", "nodes", "edges", "chunks"} {
			var n int
			if err := tx.QueryRowContext(ctx,
				`SELECT count(*) FROM `+table+` WHERE workspace_id <> ?`, r.st.ID().String()).Scan(&n); err != nil {
				return fmt.Errorf("retrieval: scan %s: %w", table, err)
			}
			total += n
		}
		return nil
	})
	return total, err
}

// addNotes fills the packet's memory section within its own sub-budget.
//
// Kinds are kept in the order memory.Kinds gives, which is the order a reader
// should see them: intent explains why, observation records what was seen,
// advice tries to steer. Mixing them would let a one-off read as a rule, which
// is the drift the three kinds exist to prevent.
func (r *Retriever) addNotes(pkt *Packet, budget int) {
	if r.mem == nil || budget <= 0 {
		return
	}
	all, err := r.mem.All()
	if err != nil {
		// A repository with no notes is the common case and not a failure. A
		// malformed one should not take the task down either: the packet is
		// still usable without them, and `le memory list` is where a broken
		// file gets reported.
		return
	}
	spent := 0
	for _, kind := range memory.Kinds() {
		notes := all[kind]
		// Newest first: the caps drop the oldest, so the newest are the ones a
		// reader has not already seen play out.
		for i := len(notes) - 1; i >= 0; i-- {
			cost := noteTokens(notes[i])
			if spent+cost > budget {
				pkt.NotesDropped++
				continue
			}
			pkt.Notes = append(pkt.Notes, notes[i])
			spent += cost
			pkt.Tokens += cost
		}
	}
}

// noteTokens estimates a note the same way a slice is estimated, counting the
// provenance because that is rendered too — a rule whose source is invisible
// cannot be judged (§11).
func noteTokens(n memory.Note) int {
	return (len(n.Text)+len(n.Provenance.Source)+len(n.Kind))/4 + 8
}

// sortByScore orders slices best-first within each origin, leaving the relative
// order of the origins alone.
//
// Anchors stay ahead of expansion because a lexical hit on the query is a
// stronger signal than being adjacent to one, and the two scores are not on a
// common scale. What changes is that within each group the best now come first,
// which is what the score was computed for.
func sortByScore(slices []Slice) {
	sort.SliceStable(slices, func(i, j int) bool {
		if slices[i].Origin != slices[j].Origin {
			return false // keep the existing grouping
		}
		return slices[i].Score > slices[j].Score
	})
}
