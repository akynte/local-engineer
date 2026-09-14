package broker

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/akynte/local-engineer/internal/workspace"
)

// save writes a gate, creating or updating it.
func (b *Broker) save(ctx context.Context, g Gate) error {
	return b.db.Tx(ctx, func(tx *sql.Tx) error {
		var decidedAt, expiresAt any
		if g.DecidedAt != nil {
			decidedAt = g.DecidedAt.UnixMilli()
		}
		if g.ExpiresAt != nil {
			expiresAt = g.ExpiresAt.UnixMilli()
		}
		evidence := string(g.Evidence)
		if evidence == "" {
			evidence = "{}"
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO gates (id, workspace_id, task_id, kind, question, evidence,
			                   decision, decided_by, note, created_at, decided_at, expires_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT (id) DO UPDATE SET
			  decision = excluded.decision, decided_by = excluded.decided_by,
			  note = excluded.note, decided_at = excluded.decided_at`,
			g.ID, b.ws.String(), g.TaskID, string(g.Kind), g.Question, evidence,
			string(g.Decision), g.DecidedBy, g.Note, g.CreatedAt.UnixMilli(), decidedAt, expiresAt)
		return err
	})
}

// list reads gates matching a where clause.
func (b *Broker) list(ctx context.Context, where string, args ...any) ([]Gate, error) {
	//nolint:gosec // where is a constant from this package, never from a caller
	q := `SELECT id, workspace_id, task_id, kind, question, evidence, decision,
	             decided_by, note, created_at, decided_at, expires_at
	      FROM gates ` + where + ` ORDER BY created_at`

	rows, err := b.db.SQL().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Gate
	for rows.Next() {
		var g Gate
		var ws, evidence string
		var created int64
		var decided, expires sql.NullInt64

		if err := rows.Scan(&g.ID, &ws, &g.TaskID, &g.Kind, &g.Question, &evidence,
			&g.Decision, &g.DecidedBy, &g.Note, &created, &decided, &expires); err != nil {
			return nil, err
		}
		g.WorkspaceID = workspace.ID(ws)
		g.Evidence = json.RawMessage(evidence)
		g.CreatedAt = time.UnixMilli(created)
		if decided.Valid {
			t := time.UnixMilli(decided.Int64)
			g.DecidedAt = &t
		}
		if expires.Valid {
			t := time.UnixMilli(expires.Int64)
			g.ExpiresAt = &t
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Decoded returns the gate's evidence.
func (g Gate) Decoded() (Evidence, error) {
	var ev Evidence
	if len(g.Evidence) == 0 {
		return ev, nil
	}
	return ev, json.Unmarshal(g.Evidence, &ev)
}
