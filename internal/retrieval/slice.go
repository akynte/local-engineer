// Package retrieval implements the lexical-anchors-then-graph-expansion order
// of design v3 §8.2 and the slice provenance contract of §2.3.
//
// Every retrieval result carries workspace_id, repository_id, worktree_id,
// path, symbol, content hash and index version. The context builder rejects
// slices whose workspace_id differs from the active task; that rejection is
// implemented in Guard and is exercised by the isolation tests.
package retrieval

import (
	"fmt"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/workspace"
)

// Origin records how a slice entered the result set, which is what makes the
// retrieval order auditable (§8.3: context-retrieval misses are a primary
// metric, so every slice must say why it is here).
type Origin string

const (
	// OriginAnchor: a lexical (FTS5/BM25) hit.
	OriginAnchor Origin = "lexical_anchor"
	// OriginGraph: reached by expanding the graph from an anchor.
	OriginGraph Origin = "graph_expansion"
	// OriginImpact: a mandatory slot filled by impact analysis, so that
	// consumers and contracts are never dropped (§8.2).
	OriginImpact Origin = "impact_mandatory"
	// OriginExplicit: named directly by the task or the operator.
	OriginExplicit Origin = "explicit"
)

// Slice is one unit of retrieved context. The provenance fields are not
// optional metadata: the packet builder refuses a slice missing any of them.
type Slice struct {
	WorkspaceID  workspace.ID `json:"workspace_id"`
	RepositoryID string       `json:"repository_id"`
	WorktreeID   string       `json:"worktree_id"`
	Path         string       `json:"path"`
	Symbol       string       `json:"symbol"`
	ContentHash  string       `json:"content_hash"`
	IndexVersion int          `json:"index_version"`

	NodeID    int64          `json:"node_id,omitempty"`
	Kind      graph.NodeKind `json:"kind,omitempty"`
	StartLine int            `json:"start_line"`
	EndLine   int            `json:"end_line"`

	// Signature is shown first; Body is fetched on demand (§8.2 progressive
	// disclosure: signatures first, bodies on demand).
	Signature string `json:"signature,omitempty"`
	Body      string `json:"body,omitempty"`

	Origin   Origin         `json:"origin"`
	Score    float64        `json:"score"`
	Depth    int            `json:"depth,omitempty"`
	Via      graph.EdgeKind `json:"via,omitempty"`
	Evidence graph.Evidence `json:"evidence,omitempty"`
}

// TokenEstimate is a cheap size proxy used to enforce the packet cap. It is
// deliberately conservative: four characters per token under-counts rarely
// enough for a hard cap set by the needle test (§8.3).
func (s Slice) TokenEstimate() int {
	n := len(s.Signature) + len(s.Body) + len(s.Path) + len(s.Symbol)
	return n/4 + 8
}

// Validate enforces the provenance contract of §2.3.
func (s Slice) Validate() error {
	switch {
	case s.WorkspaceID == "":
		return fmt.Errorf("retrieval: slice %s has no workspace_id", s.Path)
	case s.RepositoryID == "":
		return fmt.Errorf("retrieval: slice %s has no repository_id", s.Path)
	case s.Path == "":
		return fmt.Errorf("retrieval: slice for %s has no path", s.Symbol)
	case s.ContentHash == "":
		return fmt.Errorf("retrieval: slice %s has no content hash", s.Path)
	case s.IndexVersion == 0:
		return fmt.Errorf("retrieval: slice %s has no index version", s.Path)
	case s.Origin == "":
		return fmt.Errorf("retrieval: slice %s has no origin", s.Path)
	}
	return nil
}

// ForeignSliceError reports a slice that does not belong to the active task's
// workspace. It is a distinct type so that tests and telemetry can count these
// separately from ordinary validation failures.
type ForeignSliceError struct {
	Active workspace.ID
	Found  workspace.ID
	Path   string
}

func (e *ForeignSliceError) Error() string {
	return fmt.Sprintf("retrieval: slice %s belongs to workspace %s, active task is in %s; rejected",
		e.Path, e.Found, e.Active)
}

// Guard implements §2.3: "The context builder rejects slices whose
// workspace_id differs from the active task." It returns the accepted slices
// and every rejection, so a caller can both proceed and report.
//
// This is a hard boundary, not a filter with a fallback: a foreign slice is
// always dropped, and the caller is always told.
func Guard(active workspace.ID, in []Slice) (kept []Slice, rejected []error) {
	kept = make([]Slice, 0, len(in))
	for _, s := range in {
		if s.WorkspaceID != active {
			rejected = append(rejected, &ForeignSliceError{Active: active, Found: s.WorkspaceID, Path: s.Path})
			continue
		}
		if err := s.Validate(); err != nil {
			rejected = append(rejected, err)
			continue
		}
		kept = append(kept, s)
	}
	return kept, rejected
}
