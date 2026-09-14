// Package graph is the typed code-relationship graph of design v3 §3.
//
// DR-2: the graph lives in SQLite edge tables traversed with recursive CTEs.
// The public surface here is an interface so that the benchmark in
// evals/graph/ can justify a different backend later without touching callers.
//
// Two rules from §3.1 shape this API:
//   - the graph is a tool the deterministic layer queries, not a database the
//     model queries in a query language;
//   - a missing edge means "not discovered", never "does not exist" (§3.3).
package graph

import (
	"context"
	"fmt"

	"github.com/akynte/local-engineer/internal/workspace"
)

// Evidence is the provenance category of an edge (§3.2).
type Evidence string

const (
	// Resolved: a compiler, type checker or filesystem fact.
	Resolved Evidence = "resolved"
	// Declared: stated by a human or a manifest (catalog, ARCHITECTURE.md, compose).
	Declared Evidence = "declared"
	// Inferred: derived by heuristic (literal route prefixes, dynamic SQL).
	Inferred Evidence = "inferred"
	// Observed: seen at runtime or in commit history.
	Observed Evidence = "observed"
	// Unknown: the analyzer could not classify the relationship.
	Unknown Evidence = "unknown"
)

// AllEvidence lists the categories in decreasing strength.
var AllEvidence = []Evidence{Resolved, Declared, Inferred, Observed, Unknown}

// Valid reports whether e is one of the five categories.
func (e Evidence) Valid() bool {
	for _, c := range AllEvidence {
		if c == e {
			return true
		}
	}
	return false
}

// Certain reports whether the edge came from a source of truth rather than a
// heuristic. §3.3 requires that consumers reached by inferred or unknown edges
// are still treated as present, so this is used for reporting, never filtering.
func (e Evidence) Certain() bool { return e == Resolved || e == Declared }

// NodeKind enumerates the entities of §2.4.
type NodeKind string

const (
	KindFile          NodeKind = "file"
	KindDirectory     NodeKind = "directory"
	KindPackage       NodeKind = "package"
	KindModule        NodeKind = "module"
	KindService       NodeKind = "service"
	KindAPI           NodeKind = "api"
	KindRoute         NodeKind = "route"
	KindHandler       NodeKind = "handler"
	KindType          NodeKind = "type"
	KindFunction      NodeKind = "function"
	KindMethod        NodeKind = "method"
	KindClass         NodeKind = "class"
	KindInterface     NodeKind = "interface"
	KindField         NodeKind = "field"
	KindVariable      NodeKind = "variable"
	KindConstant      NodeKind = "constant"
	KindDependency    NodeKind = "dependency"
	KindConfigKey     NodeKind = "config_key"
	KindInfraResource NodeKind = "infra_resource"
	KindDeployment    NodeKind = "deployment"
	KindTest          NodeKind = "test"
	KindBuildTarget   NodeKind = "build_target"
	KindSchema        NodeKind = "schema"
	KindTable         NodeKind = "table"
	KindColumn        NodeKind = "column"
	KindCommit        NodeKind = "commit"
	KindDoc           NodeKind = "doc"
)

// EdgeKind enumerates the relationships of the coverage table in §3.2.
//
// # Direction invariant
//
// Every edge points from the consumer to the thing consumed: A -> B means
// "A depends on B, so a change to B may affect A".
//
// This is not a stylistic convention. Impact analysis is a *reverse*
// traversal: from the changed node it follows edges backwards to find what
// depends on it. An edge pointing the wrong way is therefore invisible to
// impact analysis, and the report is silently incomplete rather than wrong in
// any way a reader could notice.
//
// Two edges were originally written backwards and the consequence was exactly
// that: adding a method to an interface reported zero implementations, and a
// configuration key reported the manifests that set it but not the code that
// read it. TestEdgeDirectionInvariant guards against a third.
type EdgeKind string

const (
	EdgeContains  EdgeKind = "contains"   // directory/file/package containment
	EdgeImports   EdgeKind = "imports"    // import to dependency
	EdgeDependsOn EdgeKind = "depends_on" // module to module, package to package
	EdgeCalls     EdgeKind = "calls"      // function to function
	// EdgeImplements points from an implementation to the interface it
	// satisfies, so a change to the interface finds every type that must grow
	// a method.
	EdgeImplements EdgeKind = "implements"
	EdgeUsesType   EdgeKind = "uses_type"  // type to usage
	EdgeReferences EdgeKind = "references" // API to consumer
	EdgeRoutesTo   EdgeKind = "routes_to"  // route to handler
	EdgeHandles    EdgeKind = "handles"    // handler to service
	// EdgeReadsConfig points from whatever reads or sets a key to the key
	// itself, so a change to the key finds the code and the manifests alike.
	EdgeReadsConfig  EdgeKind = "reads_config"
	EdgeWritesSchema EdgeKind = "writes_schema" // migration to table
	EdgeReadsSchema  EdgeKind = "reads_schema"  // schema to application code
	EdgeTests        EdgeKind = "tests"         // test to implementation
	EdgeBuilds       EdgeKind = "builds"        // build target to dependency
	EdgeDeploys      EdgeKind = "deploys"       // deployment component to service
	EdgeProvisions   EdgeKind = "provisions"    // infrastructure resource to component
	EdgeTouches      EdgeKind = "touches"       // commit to file and symbol
	EdgeExtends      EdgeKind = "extends"
	EdgeEmbeds       EdgeKind = "embeds"
	EdgeReturns      EdgeKind = "returns"
	EdgeAccepts      EdgeKind = "accepts"
)

// Node is one entity in the graph.
type Node struct {
	ID           int64        `json:"id"`
	WorkspaceID  workspace.ID `json:"workspace_id"`
	RepositoryID string       `json:"repository_id"`
	WorktreeID   string       `json:"worktree_id"`
	Kind         NodeKind     `json:"kind"`
	Name         string       `json:"name"`
	FQN          string       `json:"fqn"`
	FileID       int64        `json:"file_id,omitempty"`
	Path         string       `json:"path,omitempty"`
	StartLine    int          `json:"start_line,omitempty"`
	EndLine      int          `json:"end_line,omitempty"`
	Signature    string       `json:"signature,omitempty"`
	Visibility   string       `json:"visibility,omitempty"`
	ContentHash  string       `json:"content_hash,omitempty"`
	Attrs        string       `json:"attrs,omitempty"` // JSON
}

// Edge is one typed, evidence-tagged relationship.
type Edge struct {
	ID          int64        `json:"id"`
	WorkspaceID workspace.ID `json:"workspace_id"`
	Src         int64        `json:"src"`
	Dst         int64        `json:"dst"`
	Kind        EdgeKind     `json:"kind"`
	Evidence    Evidence     `json:"evidence"`
	Source      string       `json:"source"`
	Confidence  float64      `json:"confidence"`
	Attrs       string       `json:"attrs,omitempty"` // JSON, e.g. recorded VTA assumptions
}

// Validate checks an edge before it reaches the database. The CHECK
// constraints repeat these, but failing in Go gives the analyzer a usable
// error instead of a driver message.
func (e Edge) Validate() error {
	if e.Src == 0 || e.Dst == 0 {
		return fmt.Errorf("graph: edge %s has an unset endpoint", e.Kind)
	}
	if !e.Evidence.Valid() {
		return fmt.Errorf("graph: edge %s carries invalid evidence %q", e.Kind, e.Evidence)
	}
	if e.Source == "" {
		return fmt.Errorf("graph: edge %s from %d to %d has no source analyzer", e.Kind, e.Src, e.Dst)
	}
	return nil
}

// Direction selects which way a traversal walks.
type Direction string

const (
	// Forward follows src -> dst: "what does this depend on".
	Forward Direction = "forward"
	// Reverse follows dst -> src: "what depends on this". Impact analysis is
	// a reverse traversal.
	Reverse Direction = "reverse"
)

// Query describes a bounded traversal. Depth is capped because the design
// commits only to two-to-four-hop queries (§3.5, DR-2).
type Query struct {
	Start    []int64
	Dir      Direction
	Kinds    []EdgeKind // empty means every kind
	MaxDepth int        // 1..MaxTraversalDepth
	MaxNodes int        // hard cap on the result set; 0 means DefaultMaxNodes
	// MinEvidence drops weaker categories. Leave empty to keep all of them;
	// §3.3 requires impact analysis to keep inferred and unknown.
	MinEvidence []Evidence
}

// MaxTraversalDepth bounds recursive CTE expansion.
const MaxTraversalDepth = 6

// DefaultMaxNodes bounds a traversal result.
const DefaultMaxNodes = 5000

// Reached is one node found by a traversal, with how it was reached.
type Reached struct {
	Node Node `json:"node"`
	// Depth is the number of hops from the nearest start node.
	Depth int `json:"depth"`
	// Via is the edge kind of the last hop.
	Via EdgeKind `json:"via"`
	// Evidence is the weakest evidence category on the path, which is the
	// strongest claim that can honestly be made about the relationship.
	Evidence Evidence `json:"evidence"`
}

// Graph is the queryable code graph. DR-2 keeps this an interface so a
// different backend can be swapped in behind the same callers.
type Graph interface {
	// WorkspaceID reports the workspace this graph is scoped to. There is no
	// constructor that produces an unscoped graph (§2.3).
	WorkspaceID() workspace.ID

	UpsertNode(ctx context.Context, n Node) (int64, error)
	UpsertNodes(ctx context.Context, ns []Node) ([]int64, error)
	UpsertEdges(ctx context.Context, es []Edge) error

	Node(ctx context.Context, id int64) (Node, error)
	NodeByFQN(ctx context.Context, repositoryID string, kind NodeKind, fqn string) (Node, error)
	NodesByName(ctx context.Context, name string, kinds []NodeKind, limit int) ([]Node, error)

	Neighbors(ctx context.Context, id int64, dir Direction, kinds []EdgeKind) ([]Edge, error)
	Traverse(ctx context.Context, q Query) ([]Reached, error)

	// ImpactOf answers §3.3. It never filters by evidence: weak consumers are
	// reported, labelled, and treated as present.
	ImpactOf(ctx context.Context, changed []int64, kind ChangeKind) (Impact, error)

	Stats(ctx context.Context) (Stats, error)
}

// Stats is what `le doctor` prints about graph size.
type Stats struct {
	Nodes    int64            `json:"nodes"`
	Edges    int64            `json:"edges"`
	ByEdge   map[string]int64 `json:"by_edge_kind"`
	ByEvid   map[string]int64 `json:"by_evidence"`
	DirtyKey int64            `json:"dirty_index_keys"`
}
