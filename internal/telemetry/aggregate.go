package telemetry

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// The optional cross-workspace aggregate of design v3 §2.2.
//
// §2.2's isolation table ends with: "Telemetry — `workspaces/<id>/telemetry.db`
// plus an optional aggregate with workspace ids only | Aggregate holds
// counters, never content."
//
// Two words in that sentence carry the whole design.
//
// **Optional.** §2.2 also says "There is no global semantic memory", and every
// other subsystem is per-workspace with no cross-workspace query in existence.
// An aggregate is the one deliberate exception, so it is off unless asked for,
// and it is built by an explicit command rather than written continuously —
// nothing accumulates across your projects while you are not looking.
//
// **Counters, never content.** The aggregate stores a workspace id, a metric
// name and a number. Not a path, not a symbol, not a task title. The Recorder
// already refuses content at the point of writing (Attrs.Validate), and this
// narrows further: only the numeric columns cross a workspace boundary, and
// the row shape has nowhere to put a string that is not a metric name.
//
// What it is for: "is the second project slower than the first", "how many
// tasks did I accept this month across everything". Questions about *volume*,
// which are the only questions an aggregate can honestly answer without
// becoming the cross-project channel §2.2 forbids.

// AggregateFile is the aggregate's name inside the data directory root.
const AggregateFile = "telemetry-aggregate.db"

// Row is one workspace's count of one metric over one day.
//
// A day is the finest resolution offered. Per-second counters across
// workspaces would be a timing channel between projects, which §2.2's slot
// clearing is careful to avoid elsewhere; a daily bucket answers the volume
// questions and nothing more.
type Row struct {
	WorkspaceID workspace.ID `json:"workspace_id"`
	Day         string       `json:"day"` // UTC, YYYY-MM-DD
	Kind        string       `json:"kind"`
	Name        string       `json:"name"`
	Count       int64        `json:"count"`
	TotalMS     int64        `json:"total_ms"`
}

// Aggregate is the cross-workspace counter store.
type Aggregate struct {
	db *store.DB
}

// OpenAggregate opens or creates the aggregate in the data directory root.
func OpenAggregate(ctx context.Context, root *store.Root) (*Aggregate, error) {
	db, err := root.OpenAggregate(ctx, AggregateFile)
	if err != nil {
		return nil, err
	}
	if err := ensureAggregateSchema(ctx, db); err != nil {
		return nil, err
	}
	return &Aggregate{db: db}, nil
}

func ensureAggregateSchema(ctx context.Context, db *store.DB) error {
	// The schema is here rather than in migrations/ because the aggregate is
	// optional and disposable: it is derived entirely from per-workspace
	// telemetry, so a shape change deletes and rebuilds rather than migrating.
	const schema = `
CREATE TABLE IF NOT EXISTS rows (
  workspace_id TEXT NOT NULL,
  day          TEXT NOT NULL,
  kind         TEXT NOT NULL,
  name         TEXT NOT NULL,
  count        INTEGER NOT NULL,
  total_ms     INTEGER NOT NULL,
  PRIMARY KEY (workspace_id, day, kind, name)
) STRICT;
CREATE INDEX IF NOT EXISTS rows_by_day ON rows(day);`
	_, err := db.SQL().ExecContext(ctx, schema)
	return err
}

// Close releases the aggregate.
func (a *Aggregate) Close() error { return a.db.Close() }

// Collect replaces one workspace's rows from its telemetry database.
//
// Replace rather than append: the aggregate is derived, so re-running it is
// idempotent and a workspace whose telemetry was pruned shrinks here too. An
// append-only aggregate would drift from its own source and become a second
// set of numbers to reconcile.
func (a *Aggregate) Collect(ctx context.Context, s *store.Store) (int, error) {
	id := s.ID()
	rows, err := s.Telemetry().SQL().QueryContext(ctx, `
		SELECT strftime('%Y-%m-%d', ts/1000, 'unixepoch') AS day,
		       kind, name, SUM(count), SUM(duration_ms)
		FROM events
		GROUP BY day, kind, name`)
	if err != nil {
		return 0, fmt.Errorf("telemetry: reading %s: %w", id, err)
	}
	defer rows.Close()

	var collected []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Day, &r.Kind, &r.Name, &r.Count, &r.TotalMS); err != nil {
			return 0, err
		}
		r.WorkspaceID = id
		collected = append(collected, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	tx, err := a.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM rows WHERE workspace_id = ?`, string(id)); err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO rows (workspace_id, day, kind, name, count, total_ms)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for _, r := range collected {
		if _, err := stmt.ExecContext(ctx, string(r.WorkspaceID), r.Day, r.Kind, r.Name, r.Count, r.TotalMS); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(collected), nil
}

// Forget removes one workspace from the aggregate.
//
// The aggregate is the only place a workspace's numbers sit beside another's,
// so leaving it is something you must be able to do without deleting the file.
func (a *Aggregate) Forget(ctx context.Context, id workspace.ID) (int64, error) {
	res, err := a.db.SQL().ExecContext(ctx, `DELETE FROM rows WHERE workspace_id = ?`, string(id))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Query options for reading the aggregate back.
type Query struct {
	// Since bounds the days returned. Zero means everything.
	Since time.Time
	// Name filters to one metric. Empty means every metric.
	Name string
	// Workspace filters to one workspace. Empty means every workspace.
	Workspace workspace.ID
}

// Totals reports counts per workspace and metric.
func (a *Aggregate) Totals(ctx context.Context, q Query) ([]Row, error) {
	where := "1=1"
	var args []any
	if !q.Since.IsZero() {
		where += " AND day >= ?"
		args = append(args, q.Since.UTC().Format("2006-01-02"))
	}
	if q.Name != "" {
		where += " AND name = ?"
		args = append(args, q.Name)
	}
	if q.Workspace != "" {
		where += " AND workspace_id = ?"
		args = append(args, string(q.Workspace))
	}
	//nolint:gosec // `where` is built from fixed fragments; every value is a placeholder
	rows, err := a.db.SQL().QueryContext(ctx, `
		SELECT workspace_id, kind, name, SUM(count), SUM(total_ms)
		FROM rows WHERE `+where+`
		GROUP BY workspace_id, kind, name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var r Row
		var ws string
		if err := rows.Scan(&ws, &r.Kind, &r.Name, &r.Count, &r.TotalMS); err != nil {
			return nil, err
		}
		r.WorkspaceID = workspace.ID(ws)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].WorkspaceID != out[j].WorkspaceID {
			return out[i].WorkspaceID < out[j].WorkspaceID
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Workspaces lists the workspace ids present in the aggregate.
func (a *Aggregate) Workspaces(ctx context.Context) ([]workspace.ID, error) {
	rows, err := a.db.SQL().QueryContext(ctx, `SELECT DISTINCT workspace_id FROM rows ORDER BY workspace_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []workspace.ID
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, workspace.ID(s))
	}
	return out, rows.Err()
}

// Columns reports the aggregate's stored column names.
//
// It exists so a test can assert the shape rather than trust the comment
// above it: §2.2's "counters, never content" is enforced by there being
// nowhere to put content, and a new column is exactly how that would stop
// being true.
func (a *Aggregate) Columns(ctx context.Context) ([]string, error) {
	rows, err := a.db.SQL().QueryContext(ctx, `SELECT name FROM pragma_table_info('rows') ORDER BY cid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ErrNoAggregate is returned when the aggregate has not been built.
var ErrNoAggregate = sql.ErrNoRows
