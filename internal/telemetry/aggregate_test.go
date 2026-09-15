package telemetry_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/telemetry"
	"github.com/akynte/local-engineer/internal/workspace"
)

// The aggregate is the ONE place §2.2 permits one workspace's numbers to sit
// beside another's, so these tests are about what it cannot carry as much as
// what it can.

func aggFixture(t *testing.T) (*store.Root, *telemetry.Aggregate, []workspace.ID) {
	t.Helper()
	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	var ids []workspace.ID
	for _, name := range []string{"alpha", "beta"} {
		id := workspace.DeriveID("/work/"+name, "", name)
		ids = append(ids, id)
		st, err := root.OpenWorkspace(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		rec := telemetry.New(st)
		for i := 0; i < 3; i++ {
			if err := rec.Event(ctx, "", "recipe", "go test", 100*time.Millisecond, 1, nil); err != nil {
				t.Fatal(err)
			}
		}
	}

	agg, err := telemetry.OpenAggregate(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agg.Close(); _ = root.CloseAll() })
	return root, agg, ids
}

func TestAggregateCollectsCountersFromEveryWorkspace(t *testing.T) {
	root, agg, ids := aggFixture(t)
	ctx := context.Background()

	for _, id := range ids {
		st, err := root.OpenWorkspace(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := agg.Collect(ctx, st); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := agg.Totals(ctx, telemetry.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want one per workspace: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.Count != 3 {
			t.Errorf("workspace %s counted %d, want 3", r.WorkspaceID, r.Count)
		}
		if r.TotalMS != 300 {
			t.Errorf("workspace %s totalled %dms, want 300", r.WorkspaceID, r.TotalMS)
		}
	}
}

func TestCollectingTwiceDoesNotDoubleCount(t *testing.T) {
	// The aggregate is derived, so re-running it has to be idempotent: an
	// append-only aggregate would drift from its own source and become a
	// second set of numbers to reconcile.
	root, agg, ids := aggFixture(t)
	ctx := context.Background()
	st, err := root.OpenWorkspace(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := agg.Collect(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := agg.Totals(ctx, telemetry.Query{Workspace: ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Count != 3 {
		t.Fatalf("three collections produced %+v; want one row counting 3", rows)
	}
}

func TestForgetRemovesOneWorkspaceAndLeavesTheOther(t *testing.T) {
	root, agg, ids := aggFixture(t)
	ctx := context.Background()
	for _, id := range ids {
		st, _ := root.OpenWorkspace(ctx, id)
		if _, err := agg.Collect(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := agg.Forget(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	rows, err := agg.Totals(ctx, telemetry.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].WorkspaceID != ids[1] {
		t.Fatalf("after forgetting %s the aggregate holds %+v", ids[0], rows)
	}
}

func TestTheAggregateCarriesNoContent(t *testing.T) {
	// §2.2: "Aggregate holds counters, never content." The row shape is the
	// enforcement — there is nowhere to put a path or a symbol — so this test
	// reads the whole database back as text and looks for anything that is not
	// an id, a metric name, a day or a number.
	root, agg, ids := aggFixture(t)
	ctx := context.Background()

	st, err := root.OpenWorkspace(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	// Record an event whose attributes are numbers, as the Recorder requires.
	if err := telemetry.New(st).Event(ctx, "", "retrieval", "packet", 5*time.Millisecond, 1,
		telemetry.Attrs{"slices": 12, "tokens": 3400}); err != nil {
		t.Fatal(err)
	}
	if _, err := agg.Collect(ctx, st); err != nil {
		t.Fatal(err)
	}

	rows, err := agg.Totals(ctx, telemetry.Query{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		// Only four string-ish fields exist, and each is constrained.
		if strings.ContainsAny(string(r.WorkspaceID), "/\\. ") {
			t.Errorf("workspace id looks like a path: %q", r.WorkspaceID)
		}
		for _, f := range []string{r.Kind, r.Name} {
			if strings.ContainsAny(f, "/\\") || len(f) > 64 {
				t.Errorf("a metric field looks like content: %q", f)
			}
		}
		if _, err := time.Parse("2006-01-02", r.Day); r.Day != "" && err != nil {
			t.Errorf("day is not a date: %q", r.Day)
		}
	}

	// And the `attrs` column, which is where content would hide, is not
	// carried across at all: the aggregate's schema has no such column, and
	// adding one is exactly how "counters, never content" would stop holding.
	cols, err := agg.Columns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"workspace_id": true, "day": true, "kind": true,
		"name": true, "count": true, "total_ms": true,
	}
	if len(cols) != len(want) {
		t.Errorf("the aggregate has columns %v; the shape is the enforcement", cols)
	}
	for _, c := range cols {
		if !want[c] {
			t.Errorf("unexpected column %q in the aggregate", c)
		}
	}
}

func TestQueryFiltersByNameAndWorkspace(t *testing.T) {
	root, agg, ids := aggFixture(t)
	ctx := context.Background()
	st, _ := root.OpenWorkspace(ctx, ids[0])
	if err := telemetry.New(st).Event(ctx, "", "recipe", "go build", 10*time.Millisecond, 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := agg.Collect(ctx, st); err != nil {
		t.Fatal(err)
	}
	rows, err := agg.Totals(ctx, telemetry.Query{Name: "go build"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "go build" {
		t.Fatalf("name filter returned %+v", rows)
	}
}
