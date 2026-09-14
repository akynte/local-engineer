package graph

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/version"
	"github.com/akynte/local-engineer/internal/workspace"
)

// ErrNotFound is returned when a node lookup misses.
var ErrNotFound = errors.New("graph: node not found")

// sqliteGraph is the DR-2 implementation: typed edge tables traversed with
// recursive CTEs. It holds a *store.DB, which is already bound to one
// workspace, so no query in this file can reach another workspace's data.
type sqliteGraph struct {
	db *store.DB
}

// New returns the graph for a workspace's index database. The only way to get
// one is to hand over a Store, which only OpenWorkspace can produce (§2.3).
func New(s *store.Store) Graph { return &sqliteGraph{db: s.Index()} }

func (g *sqliteGraph) WorkspaceID() workspace.ID { return g.db.WorkspaceID() }

func (g *sqliteGraph) UpsertNode(ctx context.Context, n Node) (int64, error) {
	ids, err := g.UpsertNodes(ctx, []Node{n})
	if err != nil {
		return 0, err
	}
	return ids[0], nil
}

func (g *sqliteGraph) UpsertNodes(ctx context.Context, ns []Node) ([]int64, error) {
	ids := make([]int64, len(ns))
	err := g.db.Tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO nodes (workspace_id, repository_id, worktree_id, kind, name, fqn,
			                   file_id, start_line, end_line, signature, visibility,
			                   content_hash, attrs, index_version)
			VALUES (?,?,?,?,?,?,NULLIF(?,0),?,?,?,?,?,?,?)
			ON CONFLICT (repository_id, worktree_id, kind, fqn) DO UPDATE SET
			  name = excluded.name, file_id = excluded.file_id,
			  start_line = excluded.start_line, end_line = excluded.end_line,
			  signature = excluded.signature, visibility = excluded.visibility,
			  content_hash = excluded.content_hash, attrs = excluded.attrs,
			  index_version = excluded.index_version
			RETURNING node_id`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i, n := range ns {
			if n.Kind == "" || n.FQN == "" {
				return fmt.Errorf("graph: node %d has no kind or fqn", i)
			}
			attrs := n.Attrs
			if attrs == "" {
				attrs = "{}"
			}
			// The workspace id on the row is always the handle's, never the
			// caller's: a caller cannot write a row attributed elsewhere.
			if err := stmt.QueryRowContext(ctx,
				g.db.WorkspaceID().String(), n.RepositoryID, n.WorktreeID, string(n.Kind), n.Name, n.FQN,
				n.FileID, n.StartLine, n.EndLine, n.Signature, n.Visibility,
				n.ContentHash, attrs, version.IndexerVersion,
			).Scan(&ids[i]); err != nil {
				return fmt.Errorf("graph: upsert node %s: %w", n.FQN, err)
			}
		}
		return nil
	})
	return ids, err
}

func (g *sqliteGraph) UpsertEdges(ctx context.Context, es []Edge) error {
	for _, e := range es {
		if err := e.Validate(); err != nil {
			return err
		}
	}
	return g.db.Tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO edges (workspace_id, src_id, dst_id, kind, evidence, source, confidence, attrs, index_version)
			VALUES (?,?,?,?,?,?,?,?,?)
			ON CONFLICT (src_id, dst_id, kind, source) DO UPDATE SET
			  evidence = excluded.evidence, confidence = excluded.confidence,
			  attrs = excluded.attrs, index_version = excluded.index_version`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, e := range es {
			attrs := e.Attrs
			if attrs == "" {
				attrs = "{}"
			}
			conf := e.Confidence
			if conf == 0 {
				conf = 1.0
			}
			if _, err := stmt.ExecContext(ctx, g.db.WorkspaceID().String(), e.Src, e.Dst,
				string(e.Kind), string(e.Evidence), e.Source, conf, attrs, version.IndexerVersion); err != nil {
				return fmt.Errorf("graph: upsert edge %s %d->%d: %w", e.Kind, e.Src, e.Dst, err)
			}
		}
		return nil
	})
}

const nodeColumns = `n.node_id, n.workspace_id, n.repository_id, n.worktree_id, n.kind, n.name, n.fqn,
	COALESCE(n.file_id,0), COALESCE(f.path,''), n.start_line, n.end_line, n.signature,
	n.visibility, n.content_hash, n.attrs`

const nodeFrom = ` FROM nodes n LEFT JOIN files f ON f.file_id = n.file_id `

func scanNode(rows interface{ Scan(...any) error }) (Node, error) {
	var n Node
	var ws string
	err := rows.Scan(&n.ID, &ws, &n.RepositoryID, &n.WorktreeID, &n.Kind, &n.Name, &n.FQN,
		&n.FileID, &n.Path, &n.StartLine, &n.EndLine, &n.Signature, &n.Visibility, &n.ContentHash, &n.Attrs)
	n.WorkspaceID = workspace.ID(ws)
	return n, err
}

func (g *sqliteGraph) Node(ctx context.Context, id int64) (Node, error) {
	row := g.db.SQL().QueryRowContext(ctx, `SELECT `+nodeColumns+nodeFrom+` WHERE n.node_id = ?`, id)
	n, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, fmt.Errorf("%w: id %d", ErrNotFound, id)
	}
	return g.verify(n, err)
}

func (g *sqliteGraph) NodeByFQN(ctx context.Context, repositoryID string, kind NodeKind, fqn string) (Node, error) {
	row := g.db.SQL().QueryRowContext(ctx, `SELECT `+nodeColumns+nodeFrom+
		` WHERE n.repository_id = ? AND n.kind = ? AND n.fqn = ?`, repositoryID, string(kind), fqn)
	n, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, fmt.Errorf("%w: %s %s", ErrNotFound, kind, fqn)
	}
	return g.verify(n, err)
}

func (g *sqliteGraph) NodesByName(ctx context.Context, name string, kinds []NodeKind, limit int) ([]Node, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT ` + nodeColumns + nodeFrom + ` WHERE n.name = ?`
	args := []any{name}
	if len(kinds) > 0 {
		ph := make([]string, len(kinds))
		for i, k := range kinds {
			ph[i] = "?"
			args = append(args, string(k))
		}
		q += ` AND n.kind IN (` + strings.Join(ph, ",") + `)`
	}
	q += ` ORDER BY n.kind, n.fqn LIMIT ?`
	args = append(args, limit)

	rows, err := g.db.SQL().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		if n, err = g.verify(n, nil); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// verify is the second half of §2.3's "the context builder rejects slices
// whose workspace_id differs from the active task": every row that leaves this
// package is checked against the handle it came from.
func (g *sqliteGraph) verify(n Node, err error) (Node, error) {
	if err != nil {
		return Node{}, err
	}
	if n.WorkspaceID != g.db.WorkspaceID() {
		return Node{}, fmt.Errorf("graph: row for workspace %s surfaced from the %s handle; refusing to serve it",
			n.WorkspaceID, g.db.WorkspaceID())
	}
	return n, nil
}

func (g *sqliteGraph) Neighbors(ctx context.Context, id int64, dir Direction, kinds []EdgeKind) ([]Edge, error) {
	col := "src_id"
	if dir == Reverse {
		col = "dst_id"
	}
	q := `SELECT edge_id, workspace_id, src_id, dst_id, kind, evidence, source, confidence, attrs
	      FROM edges WHERE ` + col + ` = ?`
	args := []any{id}
	if len(kinds) > 0 {
		ph := make([]string, len(kinds))
		for i, k := range kinds {
			ph[i] = "?"
			args = append(args, string(k))
		}
		q += ` AND kind IN (` + strings.Join(ph, ",") + `)`
	}
	q += ` ORDER BY kind, edge_id`

	rows, err := g.db.SQL().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Edge
	for rows.Next() {
		var e Edge
		var ws string
		if err := rows.Scan(&e.ID, &ws, &e.Src, &e.Dst, &e.Kind, &e.Evidence, &e.Source, &e.Confidence, &e.Attrs); err != nil {
			return nil, err
		}
		e.WorkspaceID = workspace.ID(ws)
		if e.WorkspaceID != g.db.WorkspaceID() {
			return nil, fmt.Errorf("graph: edge %d belongs to workspace %s, handle is %s", e.ID, e.WorkspaceID, g.db.WorkspaceID())
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
