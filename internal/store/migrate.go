package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/akynte/local-engineer/internal/version"
	"github.com/akynte/local-engineer/internal/workspace"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const (
	metaSchemaVersion = "schema_version"
	metaWorkspaceID   = "workspace_id"
	metaCreatedBy     = "created_by"
)

// migrationsFor returns the ordered migration steps for a database, keyed by
// the numeric suffix of the file name (index_001.sql -> 1).
func migrationsFor(name string) ([]migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	prefix := name + "_"
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		numStr := strings.TrimSuffix(strings.TrimPrefix(e.Name(), prefix), ".sql")
		n, err := strconv.Atoi(numStr)
		if err != nil {
			return nil, fmt.Errorf("store: migration %s has a non-numeric version", e.Name())
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: n, name: e.Name(), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("store: migrations for %s are not contiguous at %s", name, m.name)
		}
	}
	return out, nil
}

type migration struct {
	version int
	name    string
	sql     string
}

// migrate brings db forward to the version this build targets. Migrations are
// forward-only; opening a newer schema is refused with a message pointing at
// the documented upgrade path (§4.4, DR-1).
func (d *DB) migrate(ctx context.Context, ws workspace.ID) error {
	target, ok := version.SchemaVersions[d.name]
	if !ok {
		return fmt.Errorf("store: no schema target registered for %q", d.name)
	}
	steps, err := migrationsFor(d.name)
	if err != nil {
		return err
	}
	if len(steps) != target {
		return fmt.Errorf("store: %s targets schema %d but %d migrations are embedded", d.name, target, len(steps))
	}

	current, err := d.currentVersion(ctx)
	if err != nil {
		return err
	}
	if current > target {
		return fmt.Errorf("store: %s is at schema %d, this build understands %d; "+
			"downgrades across schema versions are not supported, restore a backup or upgrade local-engineer",
			d.path, current, target)
	}
	if current == target {
		return d.verifyWorkspaceStamp(ctx, ws)
	}

	for _, step := range steps {
		if step.version <= current {
			continue
		}
		err := d.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, step.sql); err != nil {
				return fmt.Errorf("store: apply %s: %w", step.name, err)
			}
			if err := metaSet(ctx, tx, metaSchemaVersion, strconv.Itoa(step.version)); err != nil {
				return err
			}
			if err := metaSet(ctx, tx, metaWorkspaceID, ws.String()); err != nil {
				return err
			}
			return metaSet(ctx, tx, metaCreatedBy, version.Version)
		})
		if err != nil {
			return err
		}
	}
	return d.verifyWorkspaceStamp(ctx, ws)
}

func (d *DB) currentVersion(ctx context.Context) (int, error) {
	var count int
	err := d.sql.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='meta'`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: probe %s: %w", d.path, err)
	}
	if count == 0 {
		return 0, nil
	}
	v, ok, err := d.metaGet(ctx, metaSchemaVersion)
	if err != nil || !ok {
		return 0, err
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("store: %s has an unreadable schema version %q", d.path, v)
	}
	return n, nil
}

// verifyWorkspaceStamp is the storage-layer half of the isolation contract: a
// database file may only ever be opened as the workspace that created it. If
// the directory were tampered with or a backup restored into the wrong id,
// this fails loudly instead of serving another project's code (§2.2).
func (d *DB) verifyWorkspaceStamp(ctx context.Context, ws workspace.ID) error {
	got, ok, err := d.metaGet(ctx, metaWorkspaceID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("store: %s carries no workspace stamp", d.path)
	}
	if got != ws.String() {
		return fmt.Errorf("store: %s belongs to workspace %s but was opened as %s", d.path, got, ws)
	}
	return nil
}
