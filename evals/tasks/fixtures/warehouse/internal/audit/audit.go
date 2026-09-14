// Package audit records what the service did, in order. The log is append-only
// and every state change goes through it: a reconciliation that cannot explain
// itself from the log is a bug in the log, not in the reconciliation.
package audit

import (
	"context"
	"sync"
	"time"
)

// Event is one recorded action.
type Event struct {
	At      time.Time
	Actor   string
	Action  string
	Subject string
	Detail  map[string]string
}

// Log records events.
type Log interface {
	Record(ctx context.Context, ev Event) error
	Since(ctx context.Context, t time.Time) ([]Event, error)
}

// MemoryLog is an in-process log.
type MemoryLog struct {
	mu     sync.RWMutex
	events []Event
	now    func() time.Time
}

// NewMemoryLog builds a log.
func NewMemoryLog() *MemoryLog { return &MemoryLog{now: time.Now} }

// Record appends an event, stamping it if the caller did not.
func (l *MemoryLog) Record(_ context.Context, ev Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ev.At.IsZero() {
		ev.At = l.now()
	}
	l.events = append(l.events, ev)
	return nil
}

// Since returns events at or after a time.
func (l *MemoryLog) Since(_ context.Context, t time.Time) ([]Event, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []Event
	for _, ev := range l.events {
		if !ev.At.Before(t) {
			out = append(out, ev)
		}
	}
	return out, nil
}

// Count reports how many events are held, for tests and for the health page.
func (l *MemoryLog) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.events)
}
