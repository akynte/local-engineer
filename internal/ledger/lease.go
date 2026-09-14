package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrLeaseHeld is returned when another live holder owns the worktree.
var ErrLeaseHeld = errors.New("ledger: worktree lease is held")

// DefaultLeaseTTL is how long a lease survives without renewal. §7.2 step 4:
// a crashed holder's lease expires and a new instance takes it after
// reconciliation. Two instances never write the same worktree.
const DefaultLeaseTTL = 60 * time.Second

// Lease is an exclusive claim on a worktree.
type Lease struct {
	WorktreeID string    `json:"worktree_id"`
	TaskID     string    `json:"task_id"`
	Holder     string    `json:"holder"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Expired reports whether the lease is past its expiry.
func (ls Lease) Expired() bool { return time.Now().After(ls.ExpiresAt) }

// AcquireLease claims a worktree for a task. It succeeds when the worktree is
// unheld, when the existing lease has expired, or when this holder already
// owns it (renewal). It never steals a live lease.
func (l *Ledger) AcquireLease(ctx context.Context, worktreeID, taskID, holder string, ttl time.Duration) (Lease, error) {
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	var out Lease
	err := l.db.Tx(ctx, func(tx *sql.Tx) error {
		now := time.Now()
		expires := now.Add(ttl)

		var curTask, curHolder sql.NullString
		var curExpires sql.NullInt64
		err := tx.QueryRowContext(ctx,
			`SELECT task_id, holder, expires_at FROM leases WHERE worktree_id = ?`, worktreeID).
			Scan(&curTask, &curHolder, &curExpires)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			live := curExpires.Valid && time.UnixMilli(curExpires.Int64).After(now)
			if live && curHolder.String != holder {
				return fmt.Errorf("%w: %s holds %s until %s", ErrLeaseHeld,
					curHolder.String, worktreeID, time.UnixMilli(curExpires.Int64).Format(time.RFC3339))
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO leases (worktree_id, task_id, holder, expires_at) VALUES (?,?,?,?)
			ON CONFLICT (worktree_id) DO UPDATE SET
			  task_id = excluded.task_id, holder = excluded.holder, expires_at = excluded.expires_at`,
			worktreeID, taskID, holder, expires.UnixMilli()); err != nil {
			return err
		}
		out = Lease{WorktreeID: worktreeID, TaskID: taskID, Holder: holder, ExpiresAt: expires}
		return nil
	})
	return out, err
}

// RenewLease extends a lease this holder already owns.
func (l *Ledger) RenewLease(ctx context.Context, worktreeID, holder string, ttl time.Duration) (Lease, error) {
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	var out Lease
	err := l.db.Tx(ctx, func(tx *sql.Tx) error {
		expires := time.Now().Add(ttl)
		res, err := tx.ExecContext(ctx,
			`UPDATE leases SET expires_at = ? WHERE worktree_id = ? AND holder = ?`,
			expires.UnixMilli(), worktreeID, holder)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("ledger: %s does not hold a lease on %s", holder, worktreeID)
		}
		out = Lease{WorktreeID: worktreeID, Holder: holder, ExpiresAt: expires}
		return nil
	})
	return out, err
}

// ReleaseLease drops a lease held by this holder.
func (l *Ledger) ReleaseLease(ctx context.Context, worktreeID, holder string) error {
	return l.db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM leases WHERE worktree_id = ? AND holder = ?`, worktreeID, holder)
		return err
	})
}

// Leases lists every lease, live or expired, for `le doctor`.
func (l *Ledger) Leases(ctx context.Context) ([]Lease, error) {
	rows, err := l.db.SQL().QueryContext(ctx,
		`SELECT worktree_id, COALESCE(task_id,''), COALESCE(holder,''), COALESCE(expires_at,0)
		 FROM leases ORDER BY worktree_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Lease
	for rows.Next() {
		var ls Lease
		var exp int64
		if err := rows.Scan(&ls.WorktreeID, &ls.TaskID, &ls.Holder, &exp); err != nil {
			return nil, err
		}
		ls.ExpiresAt = time.UnixMilli(exp)
		out = append(out, ls)
	}
	return out, rows.Err()
}
