package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so the binary cross-compiles for the multi-arch images

	"github.com/akynte/local-engineer/internal/workspace"
)

// Durability selects the synchronous pragma. §5.4: NORMAL for index and
// telemetry, FULL for the ledger, which is the crash-recovery record.
type Durability string

const (
	DurabilityNormal Durability = "NORMAL"
	DurabilityFull   Durability = "FULL"
)

// DB is a database handle that knows which workspace it belongs to. Query
// helpers stamp and verify the workspace id so that scope is enforced at the
// storage layer, not by convention (§2.2).
type DB struct {
	sql  *sql.DB
	ws   workspace.ID
	name string
	path string
}

// WorkspaceID reports the workspace this handle is bound to.
func (d *DB) WorkspaceID() workspace.ID { return d.ws }

// Name is "index", "ledger" or "telemetry".
func (d *DB) Name() string { return d.name }

// Path is the file backing this handle.
func (d *DB) Path() string { return d.path }

// SQL exposes the underlying handle for query construction inside internal
// packages. It stays unexported to callers outside internal/* by the module
// boundary, and the storescope analyzer keeps `sql.Open` out of them.
func (d *DB) SQL() *sql.DB { return d.sql }

// dsn builds the connection string. §5.4: WAL mode, busy timeout, foreign keys
// on, and BEGIN IMMEDIATE for write transactions so that two writers fail fast
// instead of deadlocking.
func dsn(path string, dur Durability) string {
	q := url.Values{}
	q.Set("_journal", "WAL")
	q.Set("_sync", string(dur))
	q.Set("_timeout", "5000")
	q.Set("_fk", "true")
	q.Set("_txlock", "immediate")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "wal_autocheckpoint(512)")
	return "file:" + path + "?" + q.Encode()
}

func openDB(ctx context.Context, ws workspace.ID, name, path string, dur Durability) (*DB, error) {
	sdb, err := sql.Open("sqlite", dsn(path, dur))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// SQLite tolerates concurrent readers but one writer. A small bounded pool
	// keeps WAL readers cheap without inviting SQLITE_BUSY storms.
	sdb.SetMaxOpenConns(8)
	sdb.SetMaxIdleConns(4)

	if err := sdb.PingContext(ctx); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}
	db := &DB{sql: sdb, ws: ws, name: name, path: path}

	// §5.4: integrity check on startup.
	if err := db.quickCheck(ctx); err != nil {
		sdb.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) quickCheck(ctx context.Context) error {
	var result string
	if err := d.sql.QueryRowContext(ctx, `PRAGMA quick_check(1)`).Scan(&result); err != nil {
		return fmt.Errorf("store: quick_check %s: %w", d.path, err)
	}
	if !strings.EqualFold(result, "ok") {
		return fmt.Errorf("store: %s failed quick_check: %s", d.path, result)
	}
	return nil
}

// IntegrityCheck runs the full `PRAGMA integrity_check`, used by `le doctor`
// on demand (§5.4).
func (d *DB) IntegrityCheck(ctx context.Context) ([]string, error) {
	rows, err := d.sql.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return nil, fmt.Errorf("store: integrity_check %s: %w", d.path, err)
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if !strings.EqualFold(s, "ok") {
			problems = append(problems, s)
		}
	}
	return problems, rows.Err()
}

// Tx runs fn inside an immediate write transaction. Journal writes use BEGIN
// IMMEDIATE (§5.4); `_txlock=immediate` in the DSN makes every transaction
// started here take the write lock up front.
func (d *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin on %s: %w", d.name, err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ReadTx runs fn inside a deferred read transaction.
func (d *DB) ReadTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := d.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("store: begin read on %s: %w", d.name, err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		err = errors.Join(err, tx.Rollback())
	}()
	return fn(tx)
}

// Checkpoint forces a WAL checkpoint, used before backup and on shutdown
// (§4.4: flush SQLite WAL, then exit).
func (d *DB) Checkpoint(ctx context.Context) error {
	_, err := d.sql.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	if err != nil {
		return fmt.Errorf("store: checkpoint %s: %w", d.name, err)
	}
	return nil
}

// Backup copies this database to dst using SQLite's VACUUM INTO, which is the
// online, transactionally consistent snapshot path available to a pure-Go
// driver (§4.4).
func (d *DB) Backup(ctx context.Context, dst string) error {
	if _, err := d.sql.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("store: backup %s to %s: %w", d.name, dst, err)
	}
	return nil
}

func (d *DB) Close() error {
	if d == nil || d.sql == nil {
		return nil
	}
	return d.sql.Close()
}

// meta get/set back the schema version and the workspace stamp.
func (d *DB) metaGet(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := d.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func metaSet(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec(`INSERT INTO meta(key, value) VALUES(?, ?)
	                   ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
