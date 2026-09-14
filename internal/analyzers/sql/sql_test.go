package sql_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlan "github.com/akynte/local-engineer/internal/analyzers/sql"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// The lexer is tested first, because a semicolon inside a string or a
// dollar-quoted body would otherwise split a statement in half and everything
// downstream would be parsing fragments.

func TestSemicolonsInsideLiteralsDoNotSplitStatements(t *testing.T) {
	stmts := sqlan.Parse(`
CREATE TABLE notes (id TEXT PRIMARY KEY, body TEXT DEFAULT 'a; b; c');
CREATE TABLE other (id TEXT);
`)
	var tables []string
	for _, s := range stmts {
		if s.Kind == sqlan.CreateTable {
			tables = append(tables, s.Table)
		}
	}
	if len(tables) != 2 {
		t.Fatalf("expected 2 tables, got %d (%v); a semicolon in a literal split a statement", len(tables), tables)
	}
}

func TestDollarQuotedBodiesAreOneToken(t *testing.T) {
	stmts := sqlan.Parse(`
CREATE FUNCTION touch() RETURNS trigger AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TABLE after_function (id INT);
`)
	var found bool
	for _, s := range stmts {
		if s.Kind == sqlan.CreateTable && s.Table == "after_function" {
			found = true
		}
	}
	if !found {
		t.Fatal("a dollar-quoted body's semicolons split the file; the statement after it was lost")
	}
}

func TestCommentsAreIgnored(t *testing.T) {
	stmts := sqlan.Parse(`
-- CREATE TABLE commented_out (id INT);
/* CREATE TABLE also_commented (id INT);
   /* nested */ still a comment */
CREATE TABLE real_table (id INT);
`)
	for _, s := range stmts {
		if s.Table == "commented_out" || s.Table == "also_commented" {
			t.Errorf("a commented-out statement was parsed: %s", s.Table)
		}
	}
	if len(stmts) != 1 || stmts[0].Table != "real_table" {
		t.Fatalf("expected only the real table, got %+v", stmts)
	}
}

func TestCreateTableColumnsAndConstraints(t *testing.T) {
	stmts := sqlan.Parse(`
CREATE TABLE IF NOT EXISTS public.payments (
    id          TEXT PRIMARY KEY,
    amount      NUMERIC(12,2) NOT NULL,
    currency    CHAR(3) NOT NULL DEFAULT 'GBP',
    customer_id TEXT REFERENCES customers(id),
    note        TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT positive_amount CHECK (amount > 0),
    FOREIGN KEY (currency) REFERENCES currencies(code)
);
`)
	if len(stmts) != 1 || stmts[0].Kind != sqlan.CreateTable {
		t.Fatalf("expected one CREATE TABLE, got %+v", stmts)
	}
	st := stmts[0]
	if st.Table != "public.payments" {
		t.Errorf("the schema qualifier was dropped: %q", st.Table)
	}
	if len(st.Columns) != 6 {
		t.Fatalf("expected 6 columns, got %d: %+v", len(st.Columns), st.Columns)
	}

	byName := map[string]sqlan.Column{}
	for _, c := range st.Columns {
		byName[c.Name] = c
	}
	// The constraints that change compatibility must be captured: adding a
	// NOT NULL column without a default breaks existing writers.
	if !byName["amount"].NotNull || byName["amount"].HasDefault {
		t.Errorf("amount: %+v", byName["amount"])
	}
	if !byName["currency"].HasDefault {
		t.Errorf("currency should have a default: %+v", byName["currency"])
	}
	if !byName["id"].PrimaryKey {
		t.Errorf("id should be the primary key: %+v", byName["id"])
	}
	if got := byName["amount"].Type; !strings.Contains(got, "NUMERIC") {
		t.Errorf("type not captured: %q", got)
	}
	// Both the column-level and table-level foreign keys.
	refs := strings.Join(st.References, ",")
	for _, want := range []string{"customers", "currencies"} {
		if !strings.Contains(refs, want) {
			t.Errorf("missing foreign key to %s; got %v", want, st.References)
		}
	}
}

func TestAlterTableAddAndDrop(t *testing.T) {
	stmts := sqlan.Parse(`
ALTER TABLE payments ADD COLUMN refunded_at TIMESTAMPTZ;
ALTER TABLE payments DROP COLUMN note;
ALTER TABLE payments ADD CONSTRAINT fk_cust FOREIGN KEY (customer_id) REFERENCES customers(id);
`)
	if len(stmts) != 3 {
		t.Fatalf("expected 3 statements, got %d", len(stmts))
	}
	if len(stmts[0].Columns) != 1 || stmts[0].Columns[0].Name != "refunded_at" {
		t.Errorf("ADD COLUMN: %+v", stmts[0])
	}
	if len(stmts[1].DroppedColumns) != 1 || stmts[1].DroppedColumns[0] != "note" {
		t.Errorf("DROP COLUMN: %+v", stmts[1])
	}
	if len(stmts[2].References) != 1 || stmts[2].References[0] != "customers" {
		t.Errorf("ADD CONSTRAINT: %+v", stmts[2])
	}
}

func TestCreateIndex(t *testing.T) {
	stmts := sqlan.Parse(`
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS payments_by_status
    ON payments (status, created_at DESC);
`)
	if len(stmts) != 1 || stmts[0].Kind != sqlan.CreateIndex {
		t.Fatalf("got %+v", stmts)
	}
	st := stmts[0]
	if st.Index != "payments_by_status" || st.Table != "payments" {
		t.Errorf("index=%q table=%q", st.Index, st.Table)
	}
	if len(st.IndexColumns) != 2 {
		t.Errorf("columns = %v", st.IndexColumns)
	}
}

// What the parser cannot interpret is reported, not silently dropped. The
// fraction of a schema the graph does not cover must be visible.
func TestUnparsedStatementsAreReported(t *testing.T) {
	stmts := sqlan.Parse(`
CREATE TABLE fine (id INT);
GRANT SELECT ON fine TO readonly;
CREATE TRIGGER t BEFORE UPDATE ON fine FOR EACH ROW EXECUTE FUNCTION touch();
`)
	var unparsed int
	for _, s := range stmts {
		if s.Kind == sqlan.Unparsed {
			unparsed++
			if s.Raw == "" {
				t.Error("an unparsed statement must say what it was")
			}
		}
	}
	if unparsed != 2 {
		t.Fatalf("expected 2 unparsed statements, got %d: %+v", unparsed, stmts)
	}
}

// Migrations are applied in order, so the result is the schema as it ends up.
func TestMigrationsApplyInOrder(t *testing.T) {
	r := analyze(t, map[string]string{
		"migrations/001_init.sql": `
CREATE TABLE users (id TEXT PRIMARY KEY, name TEXT, legacy_field TEXT);
`,
		"migrations/002_add_email.sql": `
ALTER TABLE users ADD COLUMN email TEXT NOT NULL;
ALTER TABLE users DROP COLUMN legacy_field;
`,
		"migrations/003_sessions.sql": `
CREATE TABLE sessions (id TEXT PRIMARY KEY, user_id TEXT REFERENCES users(id));
`,
	})

	if _, ok := r.nodes["table:users"]; !ok {
		t.Fatalf("users table missing; got %v", keys(r.nodes))
	}
	// A column added by a later migration lands on the table an earlier one
	// created.
	if _, ok := r.nodes["table:users.email"]; !ok {
		t.Error("a column added by a later migration is missing")
	}
	// And a dropped column is gone from the final schema.
	if _, ok := r.nodes["table:users.legacy_field"]; ok {
		t.Error("a dropped column is still in the final schema")
	}
	// A foreign key is a dependency between tables.
	if r.edge(graph.EdgeDependsOn, "table:sessions", "table:users") == nil {
		t.Errorf("missing foreign-key edge; got %s", r.describe())
	}
	// The migration that created a table writes it.
	if r.edge(graph.EdgeWritesSchema, "schema:migrations/001_init.sql", "table:users") == nil {
		t.Error("missing writes_schema edge from the migration that created the table")
	}
}

// A table dropped by a later migration is not in the final schema.
func TestADroppedTableIsGone(t *testing.T) {
	r := analyze(t, map[string]string{
		"migrations/001.sql": "CREATE TABLE temp_thing (id INT);",
		"migrations/002.sql": "DROP TABLE temp_thing;",
	})
	if _, ok := r.nodes["table:temp_thing"]; ok {
		t.Error("a dropped table is still in the final schema")
	}
}

type result struct {
	nodes map[string]graph.Node
	edges []index.PendingEdge
}

func (r result) edge(kind graph.EdgeKind, src, dst string) *index.PendingEdge {
	for i, e := range r.edges {
		if e.Kind == kind && e.SrcFQN == src && e.DstFQN == dst {
			return &r.edges[i]
		}
	}
	return nil
}

func (r result) describe() string {
	var b strings.Builder
	for _, e := range r.edges {
		b.WriteString("\n  " + string(e.Kind) + " " + e.SrcFQN + " -> " + e.DstFQN)
	}
	return b.String()
}

func analyze(t *testing.T, files map[string]string) result {
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
		list = append(list, index.File{Path: rel, AbsPath: p, Lang: "sql", Size: int64(len(body))})
	}
	a := sqlan.New()
	a.Warnf = func(f string, args ...any) { t.Logf("sql: "+f, args...) }
	res, err := a.Analyze(context.Background(), dir, list)
	if err != nil {
		t.Fatal(err)
	}
	out := result{nodes: map[string]graph.Node{}, edges: res.Edges}
	for _, n := range res.Nodes {
		out.nodes[n.FQN] = n
	}
	return out
}

func keys(m map[string]graph.Node) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
