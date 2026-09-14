// Package telemetry records counters and timings for one workspace (design v3
// §5.2). §2.2 is strict about what may be stored: "Aggregate holds counters,
// never content." Every writer here takes numbers and enum-like names, and
// there is no API that accepts source text.
package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// Recorder writes telemetry for one workspace.
type Recorder struct {
	db *store.DB
	ws workspace.ID
}

// New binds a recorder to a workspace store.
func New(s *store.Store) *Recorder { return &Recorder{db: s.Telemetry(), ws: s.ID()} }

// Attrs are numeric and enum-valued attributes. Only numbers, booleans and
// short enum strings belong here; this is enforced by Validate rather than by
// documentation, because a leak of source content into telemetry would be
// invisible.
type Attrs map[string]any

// MaxEnumLength bounds a string attribute. Anything longer is a sign that
// content is being recorded, which §2.2 forbids.
const MaxEnumLength = 64

// Validate rejects attributes that look like content rather than measurement.
func (a Attrs) Validate() error {
	for k, v := range a {
		switch t := v.(type) {
		case int, int64, float64, bool, nil:
		case string:
			if len(t) > MaxEnumLength {
				return fmt.Errorf("telemetry: attribute %q is %d characters; telemetry holds counters and enums, never content (§2.2)",
					k, len(t))
			}
		default:
			return fmt.Errorf("telemetry: attribute %q has unsupported type %T", k, v)
		}
	}
	return nil
}

// Event records one measurement.
func (r *Recorder) Event(ctx context.Context, taskID, kind, name string, duration time.Duration, count int, attrs Attrs) error {
	if attrs == nil {
		attrs = Attrs{}
	}
	if err := attrs.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(attrs)
	if err != nil {
		return err
	}
	return r.db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO events (workspace_id, task_id, ts, kind, name, duration_ms, count, attrs)
			VALUES (?,?,?,?,?,?,?,?)`,
			r.ws.String(), taskID, time.Now().UnixMilli(), kind, name,
			duration.Milliseconds(), count, string(body))
		return err
	})
}

// GPUSample records a VRAM observation, which is what admission control is
// measured against (§9.2).
func (r *Recorder) GPUSample(ctx context.Context, device, usedMB, totalMB, utilization int, powerW float64) error {
	return r.db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO gpu_samples (ts, device, vram_used_mb, vram_total_mb, utilization, power_w)
			VALUES (?,?,?,?,?,?)`,
			time.Now().UnixMilli(), device, usedMB, totalMB, utilization, powerW)
		return err
	})
}

// Counter is one aggregated metric row.
type Counter struct {
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Count     int64   `json:"count"`
	TotalMS   int64   `json:"total_ms"`
	P50MS     int64   `json:"p50_ms"`
	MaxMS     int64   `json:"max_ms"`
	MeanPerOp float64 `json:"mean_ms"`
}

// Counters aggregates events since a cutoff, which is what the dashboard and
// `le doctor` display.
func (r *Recorder) Counters(ctx context.Context, since time.Time) ([]Counter, error) {
	rows, err := r.db.SQL().QueryContext(ctx, `
		SELECT name, kind, count(*) AS n, COALESCE(sum(duration_ms),0), COALESCE(max(duration_ms),0),
		       COALESCE((SELECT duration_ms FROM events e2
		                 WHERE e2.name = e1.name AND e2.ts >= ?
		                 ORDER BY duration_ms LIMIT 1 OFFSET (SELECT count(*)/2 FROM events e3
		                                                      WHERE e3.name = e1.name AND e3.ts >= ?)), 0)
		FROM events e1 WHERE ts >= ? GROUP BY name, kind ORDER BY n DESC`,
		since.UnixMilli(), since.UnixMilli(), since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Counter
	for rows.Next() {
		var c Counter
		if err := rows.Scan(&c.Name, &c.Kind, &c.Count, &c.TotalMS, &c.MaxMS, &c.P50MS); err != nil {
			return nil, err
		}
		if c.Count > 0 {
			c.MeanPerOp = float64(c.TotalMS) / float64(c.Count)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
