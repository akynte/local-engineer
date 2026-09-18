package store

// Migration tests. These run inside the package because they inspect the
// embedded migration set directly.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/version"
	"github.com/akynte/local-engineer/internal/workspace"
)

// TestMigrations checks that every embedded migration applies cleanly, that
// the set is contiguous, and that it matches the schema version this build
// targets. CI runs this as its own gate.
func TestMigrations(t *testing.T) {
	ctx := context.Background()

	for name, target := range version.SchemaVersions {
		t.Run(name, func(t *testing.T) {
			steps, err := migrationsFor(name)
			if err != nil {
				t.Fatalf("migrations for %s: %v", name, err)
			}
			if len(steps) != target {
				t.Fatalf("%s targets schema %d but %d migrations are embedded", name, target, len(steps))
			}
			for i, s := range steps {
				if s.version != i+1 {
					t.Fatalf("migration %s is out of sequence: expected version %d, got %d", s.name, i+1, s.version)
				}
				if strings.TrimSpace(s.sql) == "" {
					t.Fatalf("migration %s is empty", s.name)
				}
			}

			// Apply them for real against a fresh file.
			id := workspace.DeriveID("/migration/test", "", name)
			path := filepath.Join(t.TempDir(), name+".db")
			db, err := openDB(ctx, id, name, path, DurabilityNormal)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			if err := db.migrate(ctx, id); err != nil {
				t.Fatalf("applying migrations: %v", err)
			}
			got, err := db.currentVersion(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got != target {
				t.Fatalf("after migration the schema is at %d, want %d", got, target)
			}

			// Re-running must be a no-op, not an error: every start migrates.
			if err := db.migrate(ctx, id); err != nil {
				t.Fatalf("re-running migrations must be idempotent: %v", err)
			}
		})
	}
}

// A database from a newer build must be refused with an actionable message
// rather than silently used (DR-1: downgrades are not supported).
func TestRefusesANewerSchema(t *testing.T) {
	ctx := context.Background()
	id := workspace.DeriveID("/migration/test", "", "newer")
	path := filepath.Join(t.TempDir(), "index.db")

	db, err := openDB(ctx, id, "index", path, DurabilityNormal)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.migrate(ctx, id); err != nil {
		t.Fatal(err)
	}
	// Pretend a future build wrote this file.
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE meta SET value = '999' WHERE key = ?`, metaSchemaVersion); err != nil {
		t.Fatal(err)
	}
	err = db.migrate(ctx, id)
	if err == nil {
		t.Fatal("opening a newer schema must fail")
	}
	for _, want := range []string{"downgrades", "backup"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must tell the operator what to do; missing %q in: %v", want, err)
		}
	}
	db.Close()
}

// Every table the design names must exist after migration, so a schema
// omission is caught here rather than at the first query.
func TestSchemaHasTheTablesTheDesignNames(t *testing.T) {
	ctx := context.Background()
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.CloseAll()

	id := workspace.DeriveID("/schema/test", "", "tables")
	st, err := root.OpenWorkspace(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	expect := map[string][]string{
		// §5.2 and §10.1
		"index": {"repositories", "worktrees", "files", "nodes", "edges", "chunks", "chunks_fts",
			"embeddings", "index_keys"},
		// §5.2 and §7.1
		"ledger": {"requirements", "tasks", "task_deps", "operations", "checkpoints", "leases",
			"evidence", "handoffs"},
		// §5.2
		"telemetry": {"events", "gpu_samples"},
	}
	for _, db := range st.DBs() {
		for _, table := range expect[db.Name()] {
			var n int
			err := db.sql.QueryRowContext(ctx,
				`SELECT count(*) FROM sqlite_master WHERE name = ?`, table).Scan(&n)
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				t.Errorf("%s.db is missing the %q table", db.Name(), table)
			}
		}
	}
}

// FTS5 must be compiled into the driver: it is the lexical-anchor stage of
// retrieval, and a build without it would fail only at query time.
func TestFTS5IsAvailable(t *testing.T) {
	ctx := context.Background()
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.CloseAll()

	st, err := root.OpenWorkspace(ctx, workspace.DeriveID("/fts/test", "", "fts"))
	if err != nil {
		t.Fatal(err)
	}
	db := st.Index().sql
	if _, err := db.ExecContext(ctx,
		`INSERT INTO chunks_fts (rowid, body, path, symbol) VALUES (1, 'func GetUser returns a user', 'a.go', 'a.go')`); err != nil {
		t.Fatalf("FTS5 insert failed; is the driver built with FTS5? %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM chunks_fts WHERE chunks_fts MATCH '"GetUser"'`).Scan(&n); err != nil {
		t.Fatalf("FTS5 match failed: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 FTS hit, got %d", n)
	}
}

// §4.4 promises "forward-only schema migrations with a pre-migration backup of
// /data databases". The backup is what makes the forward-only rule survivable:
// downgrades are refused by name, so without one a migration that goes wrong
// leaves nothing to go back to.
func TestMigrationTakesABackupFirst(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Open once to create and migrate from nothing.
	root, err := OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := workspace.DeriveID("/backup/test", "", "premigration")
	st, err := root.OpenWorkspace(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	wsDir := st.Dir()

	// A fresh database has nothing to lose, so no backup is taken: copying an
	// empty file would be ceremony.
	first, err := PreMigrationBackups(wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 0 {
		t.Errorf("a database created from nothing was backed up: %v", first)
	}
	if err := root.CloseAll(); err != nil {
		t.Fatal(err)
	}

	// Make it a genuine schema-2 database: undo migrations 3, 4 and 5 and
	// record the older version. Rewinding the number alone would re-run
	// migration 2 against objects it already created, which tests nothing
	// except that CREATE TABLE is not idempotent.
	ledgerPath := filepath.Join(wsDir, "ledger.db")
	db, err := sql.Open("sqlite", ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE operations DROP COLUMN error`); err != nil {
		t.Fatalf("undoing migration 3: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE task_workflow`); err != nil {
		t.Fatalf("undoing migration 4: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP INDEX gates_by_task_kind_candidate`); err != nil {
		t.Fatalf("undoing migration 5 index: %v", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE gates DROP COLUMN candidate`); err != nil {
		t.Fatalf("undoing migration 5: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE meta SET value = '2' WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	root2, err := OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root2.CloseAll() }()
	if _, err := root2.OpenWorkspace(ctx, id); err != nil {
		t.Fatalf("re-opening after a rewind should migrate forward: %v", err)
	}

	after, err := PreMigrationBackups(wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) == 0 {
		t.Fatal("a migration ran with no pre-migration backup; a bad one would be " +
			"unrecoverable, and downgrades are refused")
	}
	// The backup must be a real database, not an empty file.
	var found bool
	for _, b := range after {
		st, err := os.Stat(b)
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() > 0 && strings.Contains(filepath.Base(b), "ledger") {
			found = true
		}
	}
	if !found {
		t.Errorf("no non-empty ledger backup among %v", after)
	}
}

// A backup that cannot be taken must stop the migration. Doing the
// irreversible thing having lost the way back is the opposite of the point.
func TestMigrationRefusesWhenTheBackupFails(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	root, err := OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := workspace.DeriveID("/backup/refuse", "", "premigration")
	st, err := root.OpenWorkspace(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	wsDir := st.Dir()
	if err := root.CloseAll(); err != nil {
		t.Fatal(err)
	}

	ledgerPath := filepath.Join(wsDir, "ledger.db")
	db, err := sql.Open("sqlite", ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE operations DROP COLUMN error`); err != nil {
		t.Fatalf("undoing migration 3: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE meta SET value = '2' WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// Make the backup destination impossible: a file where the directory goes.
	if err := os.WriteFile(filepath.Join(wsDir, "pre-migration"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	root2, err := OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root2.CloseAll() }()
	if _, err := root2.OpenWorkspace(ctx, id); err == nil {
		t.Fatal("the migration proceeded despite being unable to take a backup")
	}
}
