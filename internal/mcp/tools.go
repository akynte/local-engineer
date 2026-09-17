package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
	"github.com/akynte/local-engineer/internal/memory"
	"github.com/akynte/local-engineer/internal/retrieval"
)

// register adds every tool.
//
// The surface is deliberately small. OpenCode's own documentation warns that
// "MCP servers add to your context, so you want to be careful with which ones
// you enable", and each tool spends context on its name, description and schema
// before the agent has done anything. These are the operations a developer
// performs while working; the rest of the CLI stays where it is.
//
// Nothing here runs a task or changes code. `le task run` drives a model
// through a bounded tool loop for minutes at a time, and nesting that inside
// another agent's tool call would put one agent's budget under another's
// control with no gate between them. Reading the repository's knowledge is
// what an agent in an editor actually needs.
func (s *Server) register(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "le_status",
		Description: "Report whether Local Engineer is set up for this repository and what " +
			"state it is in: workspace identity, the repositories it tracks, how fresh the " +
			"index is, and what the code graph currently holds. Read-only.",
	}, s.status)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "le_graph_impact",
		Description: "Report what a change to a symbol would affect: which consumers exist, " +
			"how each was discovered, a compatibility verdict and the migration each needs. " +
			"Use before changing a function signature. Read-only.",
	}, s.impact)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "le_search",
		Description: "Retrieve the code most relevant to a question, the way a task step " +
			"would: lexical anchors followed by graph expansion. Returns file, line range " +
			"and symbol for each result. Read-only.",
	}, s.search)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "le_note_add",
		Description: "Record something durable about this project that a future session " +
			"should not have to rediscover: a constraint, a decision and why it was made, or a " +
			"trap someone already fell into. Not a summary of work just done. Notes are kept " +
			"with the repository and are shown to every later session.",
	}, s.noteAdd)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "le_reindex",
		Description: "Re-analyse the repository so the index and code graph match the " +
			"working tree. Run after pulling changes or when le_status reports the index " +
			"is stale. Changes Local Engineer's own storage, never the repository.",
	}, s.reindex)
}

// ---------------------------------------------------------------- le_status

type statusIn struct {
	Path string `json:"path,omitempty" jsonschema:"a subdirectory of the open repository; defaults to its root"`
}

type statusOut struct {
	Initialised  bool     `json:"initialised"`
	WorkspaceID  string   `json:"workspace_id,omitempty"`
	Name         string   `json:"name,omitempty"`
	Root         string   `json:"root,omitempty"`
	Repositories []string `json:"repositories,omitempty"`
	IndexedAt    string   `json:"indexed_at,omitempty"`
	StaleScopes  int      `json:"stale_scopes"`
	Nodes        int64    `json:"nodes"`
	Edges        int64    `json:"edges"`
	Warnings     []string `json:"warnings,omitempty"`
}

func (s *Server) status(ctx context.Context, _ *mcp.CallToolRequest, in statusIn) (*mcp.CallToolResult, statusOut, error) {
	sess, err := s.resolve(ctx, in.Path)
	if err != nil {
		// Not being initialised is an ordinary answer to "what is the status",
		// not a failure: it is the answer the tool exists to give on a fresh
		// repository.
		return text(err.Error()), statusOut{Initialised: false}, nil
	}
	defer sess.Close() //nolint:contextcheck // cleanup must not take the request context: a cancelled call would then skip closing the databases.

	out := statusOut{
		Initialised: true,
		WorkspaceID: sess.Workspace.ID().String(),
		Name:        sess.Workspace.Manifest.Name,
		Root:        sess.Workspace.Root,
	}
	for _, r := range sess.Workspace.Manifest.Repositories {
		out.Repositories = append(out.Repositories, r.Name)
	}
	if sess.Workspace.Moved() {
		out.Warnings = append(out.Warnings,
			"this workspace was pinned at a different path; run `le workspace adopt` to re-bind it")
	}

	ix := index.New(sess.Store, index.Options{})
	if at := ix.LastIndexedAt(ctx); at > 0 {
		out.IndexedAt = time.Unix(at, 0).UTC().Format(time.RFC3339)
	} else {
		out.Warnings = append(out.Warnings,
			"this repository has never been indexed; run the le_reindex tool")
	}
	if n, err := ix.Dirty(ctx); err == nil && n > 0 {
		out.StaleScopes = n
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"%d scope(s) changed since the last index; run le_reindex for current answers", n))
	}
	if st, err := graph.New(sess.Store).Stats(ctx); err == nil {
		out.Nodes, out.Edges = st.Nodes, st.Edges
	}
	return text(renderStatus(out)), out, nil
}

func renderStatus(o statusOut) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Local Engineer is active for %s (%s)\n", o.Name, o.WorkspaceID)
	fmt.Fprintf(&b, "  root:         %s\n", o.Root)
	if len(o.Repositories) > 0 {
		fmt.Fprintf(&b, "  repositories: %s\n", strings.Join(o.Repositories, ", "))
	}
	if o.IndexedAt != "" {
		fmt.Fprintf(&b, "  indexed:      %s\n", o.IndexedAt)
	}
	fmt.Fprintf(&b, "  graph:        %d nodes, %d edges\n", o.Nodes, o.Edges)
	for _, w := range o.Warnings {
		fmt.Fprintf(&b, "  warning:      %s\n", w)
	}
	return b.String()
}

// ---------------------------------------------------------- le_graph_impact

type impactIn struct {
	Symbol string `json:"symbol" jsonschema:"the symbol whose consumers to report, for example Total"`
	Change string `json:"change,omitempty" jsonschema:"the kind of change: signature, behaviour, or removal. Defaults to signature"`
	Path   string `json:"path,omitempty" jsonschema:"a subdirectory of the open repository; defaults to its root"`
}

func (s *Server) impact(ctx context.Context, _ *mcp.CallToolRequest, in impactIn) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Symbol) == "" {
		return fail("symbol is required"), nil, nil
	}
	sess, err := s.resolve(ctx, in.Path)
	if err != nil {
		return fail("%v", err), nil, nil
	}
	defer sess.Close() //nolint:contextcheck // cleanup must not take the request context: a cancelled call would then skip closing the databases.

	kindName := in.Change
	if kindName == "" {
		kindName = "signature"
	}
	kind, err := graph.ParseChangeKind(kindName)
	if err != nil {
		return fail("%v", err), nil, nil
	}

	g := graph.New(sess.Store)
	nodes, err := g.NodesByName(ctx, in.Symbol, nil, 50)
	if err != nil {
		return fail("reading the graph: %v", err), nil, nil
	}
	if len(nodes) == 0 {
		return fail("no indexed symbol named %q. Run the le_reindex tool, or check the name.",
			in.Symbol), nil, nil
	}
	ids := make([]int64, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	imp, err := g.ImpactOf(ctx, ids, kind)
	if err != nil {
		return fail("computing impact: %v", err), nil, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", imp.Summary())
	for _, c := range imp.Consumers {
		fmt.Fprintf(&b, "  %s  %s  %s  via %s, depth %d\n",
			c.Node.FQN, c.Verdict, c.Evidence, c.Via, c.Depth)
		if c.Migration != "" {
			fmt.Fprintf(&b, "      → %s\n", c.Migration)
		}
	}
	// The caveat is not decoration: it is the difference between "nothing
	// depends on this" and "nothing was discovered", and dropping it would let
	// an absent edge read as a guarantee.
	fmt.Fprintf(&b, "\n%s\n", imp.Caveat)
	return text(b.String()), imp, nil
}

// --------------------------------------------------------------- le_search

type searchIn struct {
	Query string `json:"query" jsonschema:"what to look for, in words, for example how payments are reserved"`
	Depth int    `json:"depth,omitempty" jsonschema:"how far to expand through the graph from each anchor. Defaults to 1"`
	Path  string `json:"path,omitempty" jsonschema:"a subdirectory of the open repository; defaults to its root"`
}

func (s *Server) search(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Query) == "" {
		return fail("query is required"), nil, nil
	}
	sess, err := s.resolve(ctx, in.Path)
	if err != nil {
		return fail("%v", err), nil, nil
	}
	defer sess.Close() //nolint:contextcheck // cleanup must not take the request context: a cancelled call would then skip closing the databases.

	depth := in.Depth
	if depth <= 0 {
		depth = 1
	}
	pkt, err := retrieval.New(sess.Store).Build(ctx, retrieval.Request{
		Query: in.Query, ExpandDepth: depth,
	})
	if err != nil {
		return fail("retrieval: %v", err), nil, nil
	}
	if len(pkt.Slices) == 0 {
		return text("Nothing matched. The repository may not be indexed yet — " +
			"le_status will say."), pkt, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d result(s) for %q\n\n", len(pkt.Slices), in.Query)
	for _, sl := range pkt.Slices {
		fmt.Fprintf(&b, "  %s:%d-%d", sl.Path, sl.StartLine, sl.EndLine)
		if sl.Symbol != "" && sl.Symbol != sl.Path {
			fmt.Fprintf(&b, "  %s", sl.Symbol)
		}
		b.WriteString("\n")
	}
	// Paths and line numbers, not bodies: the client already has the
	// repository open and can read a file far more cheaply than this can
	// stream one through a tool result.
	return text(b.String()), pkt, nil
}

// -------------------------------------------------------------- le_reindex

type reindexIn struct {
	Path string `json:"path,omitempty" jsonschema:"a subdirectory of the open repository; defaults to its root"`
}

type reindexOut struct {
	Files    int    `json:"files"`
	Chunks   int    `json:"chunks"`
	Nodes    int    `json:"nodes"`
	Edges    int    `json:"edges"`
	Skipped  int    `json:"skipped"`
	Duration string `json:"duration"`
}

func (s *Server) reindex(ctx context.Context, _ *mcp.CallToolRequest, in reindexIn) (*mcp.CallToolResult, reindexOut, error) {
	sess, err := s.resolve(ctx, in.Path)
	if err != nil {
		return fail("%v", err), reindexOut{}, nil
	}
	defer sess.Close() //nolint:contextcheck // cleanup must not take the request context: a cancelled call would then skip closing the databases.

	repos := sess.Workspace.Manifest.Repositories
	if len(repos) == 0 {
		return fail("this workspace tracks no repositories"), reindexOut{}, nil
	}
	ix := index.New(sess.Store, index.Options{})
	var total reindexOut
	start := time.Now()
	for _, r := range repos {
		st, err := ix.Repository(ctx, r.ID, sess.Workspace.Root)
		if err != nil {
			// Partial progress is kept: the scopes that were re-analysed are
			// current, and saying which failed is more useful than discarding
			// the work and reporting one error.
			return fail("re-analysing %s: %v", r.Name, err), total, nil
		}
		total.Files += st.Files
		total.Chunks += st.Chunks
		total.Nodes += st.Nodes
		total.Edges += st.Edges
		total.Skipped += st.Skipped
	}
	total.Duration = time.Since(start).Round(time.Millisecond).String()
	return text(fmt.Sprintf(
			"Re-indexed %d repositor(y/ies) in %s: %d files, %d chunks, %d nodes, %d edges, %d skipped.",
			len(repos), total.Duration, total.Files, total.Chunks, total.Nodes, total.Edges, total.Skipped)),
		total, nil
}

// ------------------------------------------------------------ le_note_add

type noteIn struct {
	Text string `json:"text" jsonschema:"the note, in plain prose, one or two sentences"`
	Kind string `json:"kind,omitempty" jsonschema:"intent, observation, or advice. Defaults to observation"`
	Path string `json:"path,omitempty" jsonschema:"a subdirectory of the open repository; defaults to its root"`
}

type noteOut struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// noteAdd is the only tool here that writes anything, and it writes to the
// repository's own memory rather than to its code.
//
// It exists because continuity is the thing a coding agent loses between
// sessions. Without somewhere to put what it established, every session
// re-derives the same constraints from the same files. The store caps itself —
// fifty per kind, a kilobyte each — so this cannot grow into the essay §419
// warns about, and what reaches a prompt is capped again, harder.
func (s *Server) noteAdd(ctx context.Context, _ *mcp.CallToolRequest, in noteIn) (*mcp.CallToolResult, noteOut, error) {
	if strings.TrimSpace(in.Text) == "" {
		return fail("text is required"), noteOut{}, nil
	}
	kind := memory.Kind(in.Kind)
	if in.Kind == "" {
		kind = memory.KindObservation
	}
	if !kind.Valid() {
		return fail("kind %q is not one of: intent, observation, advice", in.Kind), noteOut{}, nil
	}
	sess, err := s.resolve(ctx, in.Path)
	if err != nil {
		return fail("%v", err), noteOut{}, nil
	}
	defer sess.Close() //nolint:contextcheck // cleanup must not take the request context: a cancelled call would then skip closing the databases.

	n, err := memory.Open(sess.Workspace.Root, memory.DefaultCaps()).Add(memory.Note{
		Kind: kind,
		Text: strings.TrimSpace(in.Text),
		// The source is the agent, stated plainly. A note whose provenance is
		// hidden reads later as something a person decided.
		Provenance: memory.Provenance{Source: "opencode"},
	})
	if err != nil {
		return fail("recording the note: %v", err), noteOut{}, nil
	}
	return text(fmt.Sprintf(
		"Recorded this as %s. Later sessions will see it once `le opencode setup` refreshes "+
			"AGENTS.md.", kind)), noteOut{ID: n.ID, Kind: string(n.Kind)}, nil
}
