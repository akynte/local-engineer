package index_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// index_keys.updated_at is written by two paths, and they disagreed: a full
// index wrote seconds and MarkDirty wrote milliseconds into the same column.
// The reader treats the value as seconds, so a freshly dirtied scope became
// the year 58,700 and `le doctor` reported "last indexed -2562047h47m0s ago" —
// a number that reads as a display quirk and is really a corrupt timestamp.
func TestBothWritersAgreeOnTheTimestampUnit(t *testing.T) {
	repo, ix, _ := indexedRepo(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(repo, "a.go"),
		[]byte("package demo\n\nfunc A() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Repository(ctx, "r1", repo); err != nil {
		t.Fatal(err)
	}
	if err := ix.MarkDirty(ctx, "repository", "r1"); err != nil {
		t.Fatalf("mark dirty: %v", err)
	}

	// Both writers have now touched the row. Whatever is there must read as a
	// time close to now when interpreted the way the reader interprets it.
	n, err := ix.Dirty(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("MarkDirty recorded nothing, so this proves nothing")
	}
	updatedAt := ix.LastIndexedAt(ctx)
	age := time.Since(time.Unix(updatedAt, 0))
	if age < 0 {
		t.Errorf("the stored timestamp is %s in the future: the two writers disagree on the unit",
			-age)
	}
	if age > time.Hour {
		t.Errorf("the stored timestamp reads as %s old immediately after writing it", age)
	}
}
