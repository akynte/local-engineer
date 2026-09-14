package golang_test

// These tests run the analyzer against a real, type-checked fixture module.
// A fixture rather than a mock: the whole claim of this analyzer is that its
// edges come from the compiler, and a mocked type checker would test nothing.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/analyzers/golang"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// fixture writes a small module exercising every relationship the analyzer
// claims to produce.
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.test/app\n\ngo 1.26\n",

		"internal/store/store.go": `package store

import "context"

// Loader is an interface, so calls through it are dynamic dispatch.
type Loader interface {
	Load(ctx context.Context, id string) (string, error)
}

// Postgres satisfies Loader with a pointer receiver.
type Postgres struct{ DSN string }

func (p *Postgres) Load(ctx context.Context, id string) (string, error) { return id, nil }

// Memory also satisfies Loader, so dispatch has two candidates.
type Memory struct{ items map[string]string }

func (m *Memory) Load(ctx context.Context, id string) (string, error) { return m.items[id], nil }

// NotALoader does not satisfy it: the signature differs.
type NotALoader struct{}

func (n *NotALoader) Load(id int) string { return "" }
`,

		"internal/service/service.go": `package service

import (
	"context"
	"os"

	"example.test/app/internal/store"
)

type Service struct {
	loader store.Loader
}

func New(l store.Loader) *Service { return &Service{loader: l} }

// Get calls through the interface: dynamic dispatch.
func (s *Service) Get(ctx context.Context, id string) (string, error) {
	return s.loader.Load(ctx, id)
}

// Direct calls a concrete method: static dispatch.
func Direct(p *store.Postgres) (string, error) {
	return p.Load(context.Background(), "x")
}

// Config reads a literal environment key.
func Config() string { return os.Getenv("DATABASE_URL") }

// Computed reads a key that is not a literal.
func Computed(key string) string { return os.Getenv(key) }
`,

		"internal/handler/handler.go": `package handler

import (
	"net/http"

	"example.test/app/internal/service"
)

type Handler struct{ svc *service.Service }

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /items/{id}", h.get)
	mux.HandleFunc("POST /items", h.create)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request)    {}
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {}
`,

		"internal/service/service_test.go": `package service

import (
	"context"
	"testing"

	"example.test/app/internal/store"
)

func TestDirect(t *testing.T) {
	if _, err := Direct(&store.Postgres{}); err != nil {
		t.Fatal(err)
	}
}

func TestGet(t *testing.T) {
	s := New(&store.Memory{})
	if _, err := s.Get(context.Background(), "id"); err != nil {
		t.Fatal(err)
	}
}
`,
	}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type analysis struct {
	nodes map[string]graph.Node          // keyed by fqn
	edges map[string][]index.PendingEdge // keyed by "kind:src->dst"
	raw   index.Result
}

func analyze(t *testing.T) analysis {
	t.Helper()
	dir := fixture(t)

	var files []index.File
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		lang := ""
		switch {
		case strings.HasSuffix(p, ".go"):
			lang = "go"
		case strings.HasSuffix(p, "go.mod"):
			lang = "gomod"
		}
		files = append(files, index.File{Path: filepath.ToSlash(rel), AbsPath: p, Lang: lang})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	a := golang.New()
	a.Warnf = func(format string, args ...any) { t.Logf("analyzer: "+format, args...) }

	res, err := a.Analyze(context.Background(), dir, files)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if len(res.Nodes) == 0 {
		t.Fatal("the analyzer produced no nodes; the fixture did not type-check")
	}

	out := analysis{nodes: map[string]graph.Node{}, edges: map[string][]index.PendingEdge{}, raw: res}
	for _, n := range res.Nodes {
		out.nodes[n.FQN] = n
	}
	for _, e := range res.Edges {
		key := string(e.Kind) + ":" + e.SrcFQN + "->" + e.DstFQN
		out.edges[key] = append(out.edges[key], e)
	}
	return out
}

func (a analysis) has(kind graph.EdgeKind, src, dst string) bool {
	return len(a.edges[string(kind)+":"+src+"->"+dst]) > 0
}

func (a analysis) edge(t *testing.T, kind graph.EdgeKind, src, dst string) index.PendingEdge {
	t.Helper()
	got := a.edges[string(kind)+":"+src+"->"+dst]
	if len(got) == 0 {
		t.Fatalf("missing %s edge %s -> %s", kind, src, dst)
	}
	return got[0]
}

const (
	pkgStore   = "pkg:example.test/app/internal/store"
	pkgService = "pkg:example.test/app/internal/service"
	loader     = "example.test/app/internal/store.Loader"
	postgres   = "example.test/app/internal/store.Postgres"
	memory     = "example.test/app/internal/store.Memory"
	notLoader  = "example.test/app/internal/store.NotALoader"
	pgLoad     = "example.test/app/internal/store.Postgres.Load"
	memLoad    = "example.test/app/internal/store.Memory.Load"
	svcGet     = "example.test/app/internal/service.Service.Get"
	svcDirect  = "example.test/app/internal/service.Direct"
	svcConfig  = "example.test/app/internal/service.Config"
)

// §3.2: "import to dependency | compilers | resolved".
func TestImportsAreResolved(t *testing.T) {
	a := analyze(t)
	e := a.edge(t, graph.EdgeImports, pkgService, pkgStore)
	if e.Evidence != graph.Resolved {
		t.Errorf("an import edge must be resolved, got %s", e.Evidence)
	}
	// An external import becomes a dependency node so it is still answerable.
	if _, ok := a.nodes["pkg:net/http"]; !ok {
		t.Error("a standard-library import should appear as a dependency node")
	}
}

// §3.2: "interface to implementation | types.Implements | resolved".
func TestInterfaceSatisfactionUsesTheTypeChecker(t *testing.T) {
	a := analyze(t)

	// The implementation points at the interface: impact analysis walks
	// backwards, so "add a method to this interface" must reach every type
	// that has to grow one.
	for _, impl := range []string{postgres, memory} {
		e := a.edge(t, graph.EdgeImplements, impl, loader)
		if e.Evidence != graph.Resolved {
			t.Errorf("%s implements %s must be resolved, got %s", impl, loader, e.Evidence)
		}
	}
	// The decisive case: a type with a same-named method but a different
	// signature does NOT satisfy the interface. A name-matching heuristic
	// would report it; the type checker does not.
	if a.has(graph.EdgeImplements, notLoader, loader) {
		t.Error("NotALoader has a Load method with a different signature and must not be reported as an implementation")
	}
}

// §3.2: "function to function (caller to callee) | resolved (with VTA
// assumptions recorded)".
func TestStaticCallsAreResolved(t *testing.T) {
	a := analyze(t)
	e := a.edge(t, graph.EdgeCalls, svcDirect, pgLoad)
	if e.Evidence != graph.Resolved {
		t.Errorf("a call on a concrete receiver must be resolved, got %s", e.Evidence)
	}
}

// Dynamic dispatch reaches every candidate, is labelled inferred, and records
// the assumption — the design allows the over-approximation but requires it to
// be visible.
func TestInterfaceDispatchIsInferredAndRecordsItsAssumption(t *testing.T) {
	a := analyze(t)

	for _, impl := range []string{pgLoad, memLoad} {
		e := a.edge(t, graph.EdgeCalls, svcGet, impl)
		if e.Evidence != graph.Inferred {
			t.Errorf("a call through an interface must be inferred, not %s", e.Evidence)
		}
		if !strings.Contains(e.Attrs, "assumption") {
			t.Errorf("the assumption must be recorded on the edge, got attrs %s", e.Attrs)
		}
		if !strings.Contains(e.Attrs, "CHA") {
			t.Errorf("the recorded assumption should name the analysis, got %s", e.Attrs)
		}
	}
	// NotALoader does not satisfy the interface, so it is not a candidate.
	if a.has(graph.EdgeCalls, svcGet, "example.test/app/internal/store.NotALoader.Load") {
		t.Error("dispatch reached a type that does not satisfy the interface")
	}
}

// §3.2: "configuration to consumer | os.Getenv | resolved".
func TestLiteralConfigKeysOnly(t *testing.T) {
	a := analyze(t)

	if _, ok := a.nodes["env:DATABASE_URL"]; !ok {
		t.Fatal("a literal os.Getenv key must produce a config_key node")
	}
	// The reader points at the key, matching the deployment analyzers.
	e := a.edge(t, graph.EdgeReadsConfig, svcConfig, "env:DATABASE_URL")
	if e.Evidence != graph.Resolved {
		t.Errorf("a literal config read must be resolved, got %s", e.Evidence)
	}
	// A computed key produces nothing. Naming it would require guessing, and a
	// wrong config_key makes an impact report confidently incomplete.
	for fqn := range a.nodes {
		if strings.HasPrefix(fqn, "env:") && fqn != "env:DATABASE_URL" {
			t.Errorf("unexpected config key from a non-literal argument: %s", fqn)
		}
	}
}

// §3.2: "route to handler | router-registration call sites".
func TestRoutesAreInferredFromLiteralPatterns(t *testing.T) {
	a := analyze(t)

	const getRoute = "route:GET /items/{id}"
	n, ok := a.nodes[getRoute]
	if !ok {
		t.Fatalf("expected a route node for %q; got %v", getRoute, keys(a.nodes, "route:"))
	}
	if !strings.Contains(n.Attrs, `"method":"GET"`) {
		t.Errorf("the method must be split out of the pattern, got attrs %s", n.Attrs)
	}
	e := a.edge(t, graph.EdgeRoutesTo, getRoute, "example.test/app/internal/handler.Handler.get")
	// A registration is a convention, not a compiler fact.
	if e.Evidence != graph.Inferred {
		t.Errorf("a route registration must be inferred, got %s", e.Evidence)
	}
	if !strings.Contains(e.Attrs, "assumption") {
		t.Error("the route assumption must be recorded on the edge")
	}
}

// §3.2: "test to implementation | call graph from _test.go".
func TestTestsEdgeReachesTheImplementation(t *testing.T) {
	a := analyze(t)
	testFQN := "example.test/app/internal/service.TestDirect"
	if _, ok := a.nodes[testFQN]; !ok {
		t.Fatalf("test functions must be indexed; got %v", keys(a.nodes, "example.test/app/internal/service.Test"))
	}
	if !a.has(graph.EdgeTests, testFQN, svcDirect) {
		t.Error("a test calling an implementation must produce a tests edge")
	}
}

func TestNodeKindsAndSignatures(t *testing.T) {
	a := analyze(t)

	for fqn, wantKind := range map[string]graph.NodeKind{
		loader:    graph.KindInterface,
		postgres:  graph.KindType,
		pgLoad:    graph.KindMethod,
		svcDirect: graph.KindFunction,
		"example.test/app/internal/service.TestDirect": graph.KindTest,
		pkgStore: graph.KindPackage,
	} {
		n, ok := a.nodes[fqn]
		if !ok {
			t.Errorf("missing node %s", fqn)
			continue
		}
		if n.Kind != wantKind {
			t.Errorf("%s has kind %s, want %s", fqn, n.Kind, wantKind)
		}
	}
	if sig := a.nodes[pgLoad].Signature; !strings.Contains(sig, "string") {
		t.Errorf("a method node must carry its signature, got %q", sig)
	}
	if a.nodes[postgres].StartLine == 0 {
		t.Error("a declaration node must carry its line")
	}
}

// Every edge must be valid before it reaches the database, so a bad analyzer
// fails here rather than at an opaque CHECK constraint.
func TestEveryEdgeIsWellFormed(t *testing.T) {
	a := analyze(t)
	for _, e := range a.raw.Edges {
		if e.SrcFQN == "" || e.DstFQN == "" {
			t.Errorf("edge %s has an empty endpoint: %+v", e.Kind, e)
		}
		if !e.Evidence.Valid() {
			t.Errorf("edge %s %s->%s has invalid evidence %q", e.Kind, e.SrcFQN, e.DstFQN, e.Evidence)
		}
		if e.Evidence == graph.Inferred && !strings.Contains(e.Attrs, "assumption") {
			t.Errorf("inferred edge %s %s->%s records no assumption", e.Kind, e.SrcFQN, e.DstFQN)
		}
	}
}

// A module that does not type-check must degrade, not fail: a broken build is
// when the graph is needed most.
func TestBrokenCodeStillProducesAGraph(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.test/broken\n\ngo 1.26\n")
	write(t, dir, "ok.go", "package broken\n\nfunc Fine() int { return 1 }\n")
	write(t, dir, "bad.go", "package broken\n\nfunc Broken() int { return undefinedSymbol() }\n")

	a := golang.New()
	var warned bool
	a.Warnf = func(string, ...any) { warned = true }

	res, err := a.Analyze(context.Background(), dir, []index.File{
		{Path: "go.mod", Lang: "gomod"}, {Path: "ok.go", Lang: "go"}, {Path: "bad.go", Lang: "go"},
	})
	if err != nil {
		t.Fatalf("a type error must not fail the run: %v", err)
	}
	if !warned {
		t.Error("type errors must be reported, not swallowed")
	}
	var foundFine bool
	for _, n := range res.Nodes {
		if strings.HasSuffix(n.FQN, ".Fine") {
			foundFine = true
		}
	}
	if !foundFine {
		t.Error("the part that did type-check should still be indexed")
	}
}

func TestNoGoModIsReportedNotGuessed(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stray.go", "package stray\n\nfunc F() {}\n")

	a := golang.New()
	var warned bool
	a.Warnf = func(string, ...any) { warned = true }

	res, err := a.Analyze(context.Background(), dir, []index.File{{Path: "stray.go", Lang: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 0 {
		t.Error("without a go.mod there is nothing to type-check against; emitting guesses would be worse than nothing")
	}
	if !warned {
		t.Error("skipping analysis must be reported")
	}
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string]graph.Node, prefix string) []string {
	var out []string
	for k := range m {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

// §3.2: "schema to application code | … embedded SQL | resolved for parsed
// SQL, inferred for dynamic SQL". This is the half that makes the schema row
// useful: the sql analyzer finds what the schema is, this finds who touches it.
func TestEmbeddedSQLReferencesTables(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.test/db\n\ngo 1.26\n")
	write(t, dir, "store.go", `package db

import (
	"context"
	"database/sql"
)

type Store struct{ db *sql.DB }

// Literal query against database/sql: the call is type-checked and the string
// is a constant, so both halves are known.
func (s *Store) Load(ctx context.Context, id string) error {
	_, err := s.db.QueryContext(ctx, "SELECT id, amount FROM payments WHERE id = $1", id)
	return err
}

// A join names two tables.
func (s *Store) Report(ctx context.Context) error {
	_, err := s.db.QueryContext(ctx,
		"SELECT p.id FROM payments p JOIN customers c ON c.id = p.customer_id")
	return err
}

// A query built at runtime: recognised as a database call, but no table is
// named, because naming one would mean guessing.
func (s *Store) Dynamic(ctx context.Context, table string) error {
	_, err := s.db.QueryContext(ctx, "SELECT * FROM "+table)
	return err
}

// Not a query at all.
func (s *Store) NotSQL(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "VACUUM")
	return err
}
`)

	a := golang.New()
	a.Warnf = func(f string, args ...any) { t.Logf("go: "+f, args...) }
	res, err := a.Analyze(context.Background(), dir, []index.File{
		{Path: "go.mod", Lang: "gomod"}, {Path: "store.go", Lang: "go"},
	})
	if err != nil {
		t.Fatal(err)
	}

	type ref struct{ src, table string }
	got := map[ref]graph.Evidence{}
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeReadsSchema {
			got[ref{e.SrcFQN, e.DstFQN}] = e.Evidence
		}
	}

	load := "example.test/db.Store.Load"
	if ev, ok := got[ref{load, "table:payments"}]; !ok {
		t.Errorf("no schema reference from Load; got %v", got)
	} else if ev != graph.Resolved {
		t.Errorf("a literal query through database/sql is resolved, got %s", ev)
	}

	report := "example.test/db.Store.Report"
	for _, table := range []string{"table:payments", "table:customers"} {
		if _, ok := got[ref{report, table}]; !ok {
			t.Errorf("a join must name both tables; missing %s", table)
		}
	}

	// A runtime-assembled query names nothing: a wrong schema edge makes an
	// impact report confidently incomplete.
	for r := range got {
		if strings.HasSuffix(r.src, ".Dynamic") {
			t.Errorf("a runtime-built query produced a table reference: %v", r)
		}
		if strings.HasSuffix(r.src, ".NotSQL") {
			t.Errorf("a non-query statement produced a table reference: %v", r)
		}
	}
}

// A driver that wraps database/sql is matched by method name, which is a
// heuristic — so those edges are inferred and record the assumption.
func TestWrapperDriversAreInferred(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.test/wrap\n\ngo 1.26\n")
	write(t, dir, "wrap.go", `package wrap

import "context"

// Pool stands in for pgxpool or sqlx: same method shape, different type.
type Pool struct{}

func (p *Pool) QueryContext(ctx context.Context, q string, args ...any) error { return nil }

type Repo struct{ pool *Pool }

func (r *Repo) List(ctx context.Context) error {
	return r.pool.QueryContext(ctx, "SELECT id FROM orders")
}
`)

	a := golang.New()
	res, err := a.Analyze(context.Background(), dir, []index.File{
		{Path: "go.mod", Lang: "gomod"}, {Path: "wrap.go", Lang: "go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range res.Edges {
		if e.Kind != graph.EdgeReadsSchema || e.DstFQN != "table:orders" {
			continue
		}
		found = true
		if e.Evidence != graph.Inferred {
			t.Errorf("a method-name match is a heuristic; evidence = %s", e.Evidence)
		}
		if !strings.Contains(e.Attrs, "assumption") {
			t.Errorf("the assumption must be recorded: %s", e.Attrs)
		}
	}
	if !found {
		t.Error("no schema reference from a wrapper driver")
	}
}

// §3.2's "API to consumer" row. `references` was an edge kind the impact
// analyser traversed and no analyzer ever produced, so "who calls this
// endpoint" always answered nobody — which reads as a safe change rather than
// as an unanswered question.
func TestAPIConsumerEdgesReachTheRoute(t *testing.T) {
	res := analyzeFixture(t, map[string]string{
		"go.mod": "module example.test/api\n\ngo 1.26\n",
		"server/server.go": `package server

import "net/http"

func Payment(w http.ResponseWriter, r *http.Request) {}

func Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /payments/{id}", Payment)
	return mux
}
`,
		"client/client.go": `package client

import (
	"net/http"
)

// FetchPayment is the consumer: a literal path that reaches the route above.
func FetchPayment() (*http.Response, error) {
	return http.Get("http://billing.internal/payments/42")
}
`,
	})

	var found bool
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeReferences &&
			strings.Contains(e.SrcFQN, "FetchPayment") &&
			strings.Contains(e.DstFQN, "payments") {
			found = true
			if e.Evidence != graph.Inferred {
				t.Errorf("the consumer edge is %q; a literal URL is a convention, "+
					"not a compiler fact", e.Evidence)
			}
			if !strings.Contains(e.Attrs, "assumption") {
				t.Errorf("the edge does not record its assumption: %s", e.Attrs)
			}
		}
	}
	if !found {
		t.Fatalf("no references edge from the client call to the route:\n%s", edgeSummary(res))
	}
}

// A path that matches no route must not invent an edge.
func TestUnmatchedClientCallProducesNoEdge(t *testing.T) {
	res := analyzeFixture(t, map[string]string{
		"go.mod": "module example.test/api\n\ngo 1.26\n",
		"server/server.go": `package server

import "net/http"

func Payment(w http.ResponseWriter, r *http.Request) {}

func Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /payments/{id}", Payment)
	return mux
}
`,
		"client/client.go": `package client

import "net/http"

func FetchSomethingElse() (*http.Response, error) {
	return http.Get("http://other.internal/invoices/7")
}
`,
	})
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeReferences && strings.Contains(e.SrcFQN, "FetchSomethingElse") {
			t.Errorf("a client call to an unrelated path produced an edge: %+v", e)
		}
	}
}

// A method mismatch is not a match: POSTing to a GET route reaches nothing.
func TestMethodMismatchProducesNoEdge(t *testing.T) {
	res := analyzeFixture(t, map[string]string{
		"go.mod": "module example.test/api\n\ngo 1.26\n",
		"server/server.go": `package server

import "net/http"

func Payment(w http.ResponseWriter, r *http.Request) {}

func Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /payments/{id}", Payment)
	return mux
}
`,
		"client/client.go": `package client

import (
	"net/http"
	"strings"
)

func CreatePayment() (*http.Response, error) {
	return http.Post("http://billing.internal/payments/42", "application/json",
		strings.NewReader("{}"))
}
`,
	})
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeReferences && strings.Contains(e.SrcFQN, "CreatePayment") {
			t.Errorf("a POST matched a GET route: %+v", e)
		}
	}
}

// analyzeFixture writes a module and runs the analyzer over it, returning the
// raw result. The other tests here use a shared fixture; these need their own
// because the property under test is a match between two packages.
func analyzeFixture(t *testing.T, files map[string]string) index.Result {
	t.Helper()
	dir := t.TempDir()
	var list []index.File
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		lang := ""
		if strings.HasSuffix(rel, ".go") {
			lang = "go"
		}
		list = append(list, index.File{Path: rel, AbsPath: p, Lang: lang})
	}

	a := golang.New()
	a.Warnf = func(format string, args ...any) { t.Logf("analyzer: "+format, args...) }
	res, err := a.Analyze(context.Background(), dir, list)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return res
}

func edgeSummary(res index.Result) string {
	var b strings.Builder
	for _, e := range res.Edges {
		b.WriteString("  " + string(e.Kind) + " " + e.SrcFQN + " -> " + e.DstFQN + "\n")
	}
	return b.String()
}
