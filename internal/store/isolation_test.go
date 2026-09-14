package store_test

// The isolation tests required by design v3 §2.3:
//
//	"Isolation tests in CI: index two synthetic repositories with overlapping
//	 symbol names, run retrieval and impact analysis in each, and assert zero
//	 cross-workspace rows, zero cache hits across workspaces, and zero
//	 cross-workspace slices in any packet. A 'workspace switch' test asserts
//	 slot files are removed and the OpenCode data directory differs."

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/cache"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
	"github.com/akynte/local-engineer/internal/retrieval"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// syntheticRepo writes a small repository whose symbol names deliberately
// overlap with the other one: same file names, same function names, different
// bodies. Any leak between workspaces therefore shows up as a wrong body
// rather than as an obviously foreign path.
func syntheticRepo(t *testing.T, root, marker string) {
	t.Helper()
	files := map[string]string{
		"go.mod": "module example.com/" + marker + "\n\ngo 1.26\n",
		"internal/service/user.go": `package service

// UserService loads users. Marker: ` + marker + `
type UserService struct{}

func (s *UserService) GetUser(id string) string { return "` + marker + `:" + id }

func ValidateUser(id string) bool { return id != "" }
`,
		"internal/service/user_test.go": `package service

import "testing"

func TestGetUser(t *testing.T) { _ = (&UserService{}).GetUser("x") }
`,
		"cmd/api/main.go": `package main

// Entry point. Marker: ` + marker + `
func main() {}
`,
		"README.md": "# " + marker + "\n\nSecret string: " + marker + "-only-here\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type fixture struct {
	ws     *workspace.Workspace
	st     *store.Store
	cache  *cache.Cache
	ret    *retrieval.Retriever
	graph  graph.Graph
	repoID string
	root   string
	marker string
}

func setupWorkspace(t *testing.T, root *store.Root, dir, marker string) fixture {
	t.Helper()
	ctx := context.Background()

	syntheticRepo(t, dir, marker)
	ws, err := workspace.Init(dir, workspace.InitOptions{Name: marker})
	if err != nil {
		t.Fatalf("init workspace %s: %v", marker, err)
	}
	st, err := root.OpenWorkspace(ctx, ws.ID())
	if err != nil {
		t.Fatalf("open workspace %s: %v", marker, err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.RecordWorkspace(ws); err != nil {
		t.Fatal(err)
	}

	repo := ws.Manifest.Repositories[0]
	ix := index.New(st, index.Options{})
	if err := ix.RegisterRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Repository(ctx, repo.ID, ws.Root); err != nil {
		t.Fatalf("index %s: %v", marker, err)
	}
	return fixture{
		ws: ws, st: st, cache: cache.New(st), ret: retrieval.New(st),
		graph: graph.New(st), repoID: repo.ID, root: ws.Root, marker: marker,
	}
}

func setupPair(t *testing.T) (*store.Root, fixture, fixture) {
	t.Helper()
	data := t.TempDir()
	root, err := store.OpenRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.CloseAll() })

	a := setupWorkspace(t, root, filepath.Join(t.TempDir(), "alpha"), "alpha")
	b := setupWorkspace(t, root, filepath.Join(t.TempDir(), "bravo"), "bravo")
	if a.ws.ID() == b.ws.ID() {
		t.Fatal("two distinct repositories derived the same workspace id")
	}
	return root, a, b
}

func TestTwoWorkspacesHaveSeparateDatabaseFiles(t *testing.T) {
	root, a, b := setupPair(t)
	l := root.Layout()
	for _, pair := range [][2]string{
		{l.IndexDB(a.ws.ID()), l.IndexDB(b.ws.ID())},
		{l.LedgerDB(a.ws.ID()), l.LedgerDB(b.ws.ID())},
		{l.TelemetryDB(a.ws.ID()), l.TelemetryDB(b.ws.ID())},
	} {
		if pair[0] == pair[1] {
			t.Fatalf("workspaces share a database file: %s", pair[0])
		}
		for _, p := range pair {
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("expected %s to exist: %v", p, err)
			}
		}
	}
}

// §2.3: zero cross-workspace rows.
func TestZeroCrossWorkspaceRows(t *testing.T) {
	ctx := context.Background()
	_, a, b := setupPair(t)
	for _, f := range []fixture{a, b} {
		n, err := f.ret.CountForeignRows(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("workspace %s holds %d rows stamped with another workspace", f.marker, n)
		}
	}
}

// §2.3: zero cross-workspace slices in any packet, checked against content
// that exists only in the other workspace.
func TestNoCrossWorkspaceSlicesInPackets(t *testing.T) {
	ctx := context.Background()
	_, a, b := setupPair(t)

	for _, pair := range []struct{ self, other fixture }{{a, b}, {b, a}} {
		// Query for a string that exists only in the *other* workspace.
		pkt, err := pair.self.ret.Build(ctx, retrieval.Request{
			Query:       pair.other.marker + "-only-here",
			ExpandDepth: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(pkt.Rejected) != 0 {
			t.Errorf("%s: guard rejected slices, which means foreign rows reached the builder: %v",
				pair.self.marker, pkt.Rejected)
		}
		for _, s := range pkt.Slices {
			if s.WorkspaceID != pair.self.ws.ID() {
				t.Errorf("%s: packet carries a slice from workspace %s", pair.self.marker, s.WorkspaceID)
			}
			if strings.Contains(s.Body, pair.other.marker+"-only-here") {
				t.Errorf("%s: packet body leaked content unique to %s", pair.self.marker, pair.other.marker)
			}
		}

		// The same query in its own workspace must hit, or the test above
		// would pass vacuously.
		own, err := pair.self.ret.Build(ctx, retrieval.Request{Query: pair.self.marker + "-only-here"})
		if err != nil {
			t.Fatal(err)
		}
		if len(own.Slices) == 0 {
			t.Errorf("%s: expected its own unique string to be retrievable", pair.self.marker)
		}
	}
}

// §2.3: overlapping symbol names must resolve to each workspace's own node,
// and impact analysis must stay inside the workspace.
func TestImpactAnalysisStaysInsideWorkspace(t *testing.T) {
	ctx := context.Background()
	_, a, b := setupPair(t)

	for _, pair := range []struct{ self, other fixture }{{a, b}, {b, a}} {
		nodes, err := pair.self.graph.NodesByName(ctx, "user.go", nil, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) == 0 {
			t.Fatalf("%s: expected the overlapping file name to be indexed", pair.self.marker)
		}
		var ids []int64
		for _, n := range nodes {
			if n.WorkspaceID != pair.self.ws.ID() {
				t.Fatalf("%s: node lookup returned a node from %s", pair.self.marker, n.WorkspaceID)
			}
			ids = append(ids, n.ID)
		}
		imp, err := pair.self.graph.ImpactOf(ctx, ids, graph.ChangeSignature)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range imp.Consumers {
			if c.Node.WorkspaceID != pair.self.ws.ID() {
				t.Errorf("%s: impact report names a consumer in %s", pair.self.marker, c.Node.WorkspaceID)
			}
			if c.Node.RepositoryID != pair.self.repoID {
				t.Errorf("%s: impact report names a consumer in repository %s", pair.self.marker, c.Node.RepositoryID)
			}
		}
		if imp.Caveat == "" {
			t.Error("impact report must always carry the 'missing edge means not discovered' caveat")
		}
	}
}

// §2.3: zero cache hits across workspaces, even for byte-identical content.
func TestZeroCacheHitsAcrossWorkspaces(t *testing.T) {
	_, a, b := setupPair(t)

	const ns = "package-load"
	manifest := cache.ManifestOf(map[string]string{"internal/service/user.go": "identical-hash"})

	if err := a.cache.Put(ns, manifest, []byte("alpha analysis")); err != nil {
		t.Fatal(err)
	}
	if got, err := a.cache.Get(ns, manifest); err != nil || string(got) != "alpha analysis" {
		t.Fatalf("own cache read back %q, %v", got, err)
	}
	// Same namespace, same manifest, different workspace: must miss.
	if _, err := b.cache.Get(ns, manifest); err == nil {
		t.Fatal("workspace bravo read alpha's cache entry for identical content")
	}
	if a.cache.Key(ns, manifest) == b.cache.Key(ns, manifest) {
		t.Fatal("two workspaces derived the same cache key for identical content")
	}
	if s := b.cache.Stats(); s.Hits != 0 {
		t.Fatalf("bravo recorded %d cache hits, expected 0", s.Hits)
	}

	// The entry must physically live under alpha's directory only.
	if a.cache.Dir() == b.cache.Dir() {
		t.Fatal("both workspaces share a cache directory")
	}
	if _, err := os.Stat(filepath.Join(b.cache.Dir(), a.cache.Key(ns, manifest)[:2])); !os.IsNotExist(err) {
		t.Fatal("alpha's cache entry is reachable from bravo's cache directory")
	}
}

// §2.3: the "workspace switch" test.
func TestWorkspaceSwitchClearsSlotsAndSeparatesOpenCodeDirs(t *testing.T) {
	_, a, b := setupPair(t)

	if a.st.OpenCodeDir() == b.st.OpenCodeDir() {
		t.Fatal("both workspaces share an OpenCode data directory")
	}
	if !strings.Contains(a.st.OpenCodeDir(), a.ws.ID().String()) {
		t.Fatalf("OpenCode dir %s is not scoped by workspace id", a.st.OpenCodeDir())
	}

	slot := filepath.Join(a.st.SlotsDir(), "slot0.bin")
	if err := os.WriteFile(slot, []byte("prompt cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Switching away from alpha clears its slots so neither cache contents nor
	// cache timing can leak (§2.2).
	if err := a.st.ClearSlots(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(slot); !os.IsNotExist(err) {
		t.Fatalf("slot file survived the workspace switch: %v", err)
	}
	if _, err := os.Stat(a.st.SlotsDir()); err != nil {
		t.Fatalf("slots directory itself must remain: %v", err)
	}
}

// A database file may only be opened as the workspace that created it.
func TestDatabaseRefusesToOpenUnderAnotherWorkspaceID(t *testing.T) {
	ctx := context.Background()
	root, a, b := setupPair(t)
	l := root.Layout()

	if err := a.st.Close(); err != nil {
		t.Fatal(err)
	}
	// Move alpha's index database into a fresh id's directory: a restore into
	// the wrong workspace must fail loudly rather than serve another project.
	victim := workspace.ID("zzzzzzzzzzzzzzzzzzzzzzzzzz")
	if err := os.MkdirAll(l.WorkspaceDir(victim), 0o750); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(l.IndexDB(a.ws.ID()))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.IndexDB(victim), data, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := root.OpenWorkspace(ctx, victim); err == nil {
		t.Fatal("opening another workspace's database file under a new id must fail")
	} else if !strings.Contains(err.Error(), "belongs to workspace") {
		t.Fatalf("expected a workspace-stamp error, got: %v", err)
	}
	_ = b
}

// Guard is the enforcement point named in §2.3; test it directly too.
func TestGuardRejectsForeignSlices(t *testing.T) {
	active := workspace.ID("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	foreign := workspace.ID("bbbbbbbbbbbbbbbbbbbbbbbbbb")
	in := []retrieval.Slice{
		{WorkspaceID: active, RepositoryID: "r", Path: "a.go", ContentHash: "h", IndexVersion: 1, Origin: retrieval.OriginAnchor},
		{WorkspaceID: foreign, RepositoryID: "r", Path: "b.go", ContentHash: "h", IndexVersion: 1, Origin: retrieval.OriginAnchor},
		{WorkspaceID: active, RepositoryID: "r", Path: "c.go", Origin: retrieval.OriginAnchor}, // missing hash
	}
	kept, rejected := retrieval.Guard(active, in)
	if len(kept) != 1 || kept[0].Path != "a.go" {
		t.Fatalf("expected only the valid in-workspace slice, got %+v", kept)
	}
	if len(rejected) != 2 {
		t.Fatalf("expected 2 rejections, got %d: %v", len(rejected), rejected)
	}
	var fse *retrieval.ForeignSliceError
	if !errors.As(rejected[0], &fse) {
		t.Fatalf("expected a ForeignSliceError first, got %T", rejected[0])
	}
	if fse.Found != foreign || fse.Active != active {
		t.Errorf("the rejection must name both workspaces, got %+v", fse)
	}
}

// A workspace path containing a URI metacharacter must open its own database.
//
// This is not hypothetical tidiness. "#" starts a fragment and "?" starts a
// query in a URI, so a concatenated path was silently truncated at either —
// and two workspaces whose paths differed only after that character resolved
// to the *same file*. Both opened successfully, so nothing downstream could
// detect it.
func TestPathsWithURIMetacharactersGetTheirOwnDatabase(t *testing.T) {
	ctx := context.Background()

	for _, name := range []string{"plain", "with#hash", "with?query", "with space", "with%percent"} {
		t.Run(name, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), name)
			if err := os.MkdirAll(base, 0o750); err != nil {
				t.Fatal(err)
			}
			root, err := store.OpenRoot(base)
			if err != nil {
				t.Fatalf("opening a data directory under %q: %v", name, err)
			}
			defer root.CloseAll()

			id := workspace.DeriveID("/uri/"+name, "", name)
			st, err := root.OpenWorkspace(ctx, id)
			if err != nil {
				t.Fatalf("opening a workspace under %q: %v", name, err)
			}
			// The database must be the file the layout named, not one the URI
			// parser invented.
			if _, err := os.Stat(root.Layout().IndexDB(id)); err != nil {
				t.Fatalf("the index database was not created where the layout says: %v", err)
			}
			if _, err := st.Index().SQL().ExecContext(ctx,
				`INSERT INTO repositories (repository_id, workspace_id, name, rel_path)
				 VALUES ('r', ?, 'r', '.')`, id.String()); err != nil {
				t.Fatalf("writing to the database: %v", err)
			}
		})
	}
}

// Two data directories differing only after a "#" must not share storage.
func TestTwoDirectoriesDifferingAfterAHashDoNotShareADatabase(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()

	open := func(name string) *store.Store {
		t.Helper()
		base := filepath.Join(parent, name)
		if err := os.MkdirAll(base, 0o750); err != nil {
			t.Fatal(err)
		}
		root, err := store.OpenRoot(base)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { root.CloseAll() })
		st, err := root.OpenWorkspace(ctx, workspace.DeriveID("/uri/"+name, "", name))
		if err != nil {
			t.Fatal(err)
		}
		return st
	}

	a := open("data#one")
	b := open("data#two")

	if _, err := a.Index().SQL().ExecContext(ctx,
		`INSERT INTO repositories (repository_id, workspace_id, name, rel_path)
		 VALUES ('only-in-a', ?, 'a', '.')`, a.ID().String()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.Index().SQL().QueryRowContext(ctx,
		`SELECT count(*) FROM repositories WHERE repository_id = 'only-in-a'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("two data directories differing only after a '#' are sharing one database")
	}
}

// §2.2 requires that saved slots are cleared on workspace switch, so that
// "neither cache contents nor cache timing can leak between projects".
//
// Prompt-cache reuse needs an identical token prefix, so cross-workspace
// *content* leakage was never possible. What the switch removes is the rest:
// one project's slot files sitting where the next project's work runs, and
// cache statistics that are an observable even when the contents are not.
func TestWorkspaceSwitchClearsTheSlotsLeftBehind(t *testing.T) {
	ctx := context.Background()
	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.CloseAll() })

	first := workspace.DeriveID("/switch/a", "", "a")
	second := workspace.DeriveID("/switch/b", "", "b")

	a, err := root.OpenWorkspace(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.SwitchTo(ctx, first); err != nil {
		t.Fatal(err)
	}

	// Something the inference server would have left behind.
	slot := filepath.Join(a.SlotsDir(), "slot-0.bin")
	if err := os.WriteFile(slot, []byte("cached prefix"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := root.OpenWorkspace(ctx, second); err != nil {
		t.Fatal(err)
	}
	previous, err := root.SwitchTo(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if previous != first {
		t.Errorf("switch reported previous=%q, want %q", previous, first)
	}
	if _, err := os.Stat(slot); !os.IsNotExist(err) {
		t.Errorf("the previous workspace's slot file survived the switch (%v); "+
			"one project's cache is sitting where the next project's work runs", err)
	}
	if root.Active() != second {
		t.Errorf("active workspace = %q, want %q", root.Active(), second)
	}
}

// Switching to the workspace already active must not clear anything: a command
// run twice in the same repository would otherwise throw away the prompt cache
// it is meant to benefit from.
func TestSwitchingToTheSameWorkspaceKeepsTheCache(t *testing.T) {
	ctx := context.Background()
	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.CloseAll() })

	id := workspace.DeriveID("/switch/same", "", "same")
	st, err := root.OpenWorkspace(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.SwitchTo(ctx, id); err != nil {
		t.Fatal(err)
	}
	slot := filepath.Join(st.SlotsDir(), "slot-0.bin")
	if err := os.WriteFile(slot, []byte("cached prefix"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := root.SwitchTo(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(slot); err != nil {
		t.Errorf("re-selecting the same workspace cleared its cache: %v", err)
	}
}
