// Package analyzers holds the cross-analyzer invariant tests.
//
// Individual analyzers are tested in their own packages. What is tested here
// is the property that only holds across all of them: every edge points from
// the consumer to the thing consumed, so impact analysis — which is a reverse
// traversal — finds everything a change affects.
//
// This has a test because it was twice wrong. Adding a method to an interface
// reported zero implementations, and a configuration key reported the
// manifests that set it but not the code that read it. Neither failure is
// visible in the output: the report is simply shorter than the truth, which is
// the worst way for an analysis tool to be wrong.
package analyzers_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/analyzers/deploy"
	"github.com/akynte/local-engineer/internal/analyzers/golang"
	sqlan "github.com/akynte/local-engineer/internal/analyzers/sql"
	"github.com/akynte/local-engineer/internal/analyzers/terraform"
	"github.com/akynte/local-engineer/internal/analyzers/typescript"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// fullStack is a repository exercising every analyzer, wired together the way
// a real service is: Go code reading a table and an environment variable, the
// migration defining that table, and the manifests setting that variable.
var fullStack = map[string]string{
	"go.mod": "module example.test/full\n\ngo 1.26\n",

	// A TypeScript front end over the same stack, so the direction invariant is
	// checked across analyzers rather than only within the Go one.
	"web/models/user.ts": "export interface UserStore { load(id: string): string }\n",
	"web/sql_store.ts": "import { UserStore } from './models/user';\n" +
		"export class SqlUserStore implements UserStore {\n" +
		"  load(id: string) { return id; }\n}\n",

	"store.go": `package full

import (
	"context"
	"database/sql"
	"os"
)

// Loader is an interface, so a change to it must reach its implementations.
type Loader interface {
	Load(ctx context.Context, id string) error
}

type Postgres struct{ db *sql.DB }

func (p *Postgres) Load(ctx context.Context, id string) error {
	_, err := p.db.QueryContext(ctx, "SELECT id FROM payments WHERE id = $1", id)
	return err
}

type Memory struct{}

func (m *Memory) Load(ctx context.Context, id string) error { return nil }

func DSN() string { return os.Getenv("DATABASE_URL") }
`,

	"migrations/001.sql": `
CREATE TABLE payments (id TEXT PRIMARY KEY, amount NUMERIC NOT NULL);
`,

	"docker-compose.yml": `
services:
  api:
    image: api
    environment:
      DATABASE_URL: postgres://db/app
`,

	"infra/main.tf": `
resource "aws_ecs_task_definition" "api" {
  family = "api"
  environment {
    name  = "DATABASE_URL"
    value = "postgres://db/app"
  }
}
`,
}

// impactOf indexes the fixture and returns the FQNs an impact report names.
func impactOf(t *testing.T, symbol string, change graph.ChangeKind) map[string]graph.Consumer {
	t.Helper()
	ctx := context.Background()

	// The fixture is written to disk and the indexer walks it, so this
	// exercises the same path `le index` takes — including the language
	// detection and the exclude rules, not just the analyzers.
	dir := t.TempDir()
	for rel, body := range fullStack {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.CloseAll() })
	st, err := root.OpenWorkspace(ctx, workspace.DeriveID("/direction/test", "", t.Name()))
	if err != nil {
		t.Fatal(err)
	}

	quiet := func(string, ...any) {}
	goa, sq, dep, tf := golang.New(), sqlan.New(), deploy.New(), terraform.New()
	ts := typescript.New()
	goa.Warnf, sq.Warnf, dep.Warnf, tf.Warnf, ts.Warnf = quiet, quiet, quiet, quiet, quiet

	ix := index.New(st, index.Options{Analyzers: []index.Analyzer{goa, ts, sq, dep, tf}})
	repo := workspace.Repository{ID: "r1", Name: "full", Path: ".", DefaultBranch: "main"}
	if err := ix.RegisterRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Repository(ctx, repo.ID, dir); err != nil {
		t.Fatal(err)
	}

	g := graph.New(st)
	nodes, err := g.NodesByName(ctx, symbol, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) == 0 {
		t.Fatalf("no indexed symbol named %q", symbol)
	}
	ids := make([]int64, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	imp, err := g.ImpactOf(ctx, ids, change)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]graph.Consumer{}
	for _, c := range imp.Consumers {
		out[c.Node.FQN] = c
	}
	return out
}

// Adding a method to an interface is the most common breaking interface
// change. Before the direction was corrected this reported zero
// implementations.
func TestChangingAnInterfaceFindsItsImplementations(t *testing.T) {
	consumers := impactOf(t, "Loader", graph.ChangeAddField)

	for _, impl := range []string{"example.test/full.Postgres", "example.test/full.Memory"} {
		c, ok := consumers[impl]
		if !ok {
			t.Errorf("adding a method to the interface did not reach %s\n%s", impl, list(consumers))
			continue
		}
		if c.Verdict != graph.Breaking {
			t.Errorf("%s must be breaking when a method is added, got %s", impl, c.Verdict)
		}
		if c.Migration == "" {
			t.Errorf("%s: a breaking verdict must name the migration step", impl)
		}
	}
}

// A configuration key is read by code and set by manifests. A change to it
// must find both, in one traversal, from three different analyzers.
func TestChangingAConfigKeyFindsCodeAndManifests(t *testing.T) {
	consumers := impactOf(t, "DATABASE_URL", graph.ChangeConfig)

	want := map[string]string{
		"example.test/full.DSN":                   "the Go code that reads it",
		"service:api":                             "the compose service that sets it",
		"tf:resource.aws_ecs_task_definition.api": "the infrastructure that provisions it",
	}
	for fqn, why := range want {
		if _, ok := consumers[fqn]; !ok {
			t.Errorf("changing the key did not reach %s (%s)\n%s", fqn, why, list(consumers))
		}
	}
}

// A schema change must find the code that queries the table, across the SQL
// and Go analyzers.
func TestChangingATableFindsTheCodeThatQueriesIt(t *testing.T) {
	consumers := impactOf(t, "payments", graph.ChangeSchema)

	reader := "example.test/full.Postgres.Load"
	c, ok := consumers[reader]
	if !ok {
		t.Fatalf("changing the table did not reach the query that reads it\n%s", list(consumers))
	}
	if c.Verdict != graph.Breaking {
		t.Errorf("a schema change is breaking for a query that reads it, got %s", c.Verdict)
	}
}

// The invariant itself: no analyzer may emit an edge whose direction makes its
// consumers invisible to a reverse traversal.
func TestEdgeDirectionInvariant(t *testing.T) {
	// Each case is (changed symbol, a consumer that must be reachable).
	cases := []struct {
		symbol   string
		change   graph.ChangeKind
		consumer string
		why      string
	}{
		{"Loader", graph.ChangeAddField, "example.test/full.Postgres",
			"implements must point from the implementation to the interface"},
		{"DATABASE_URL", graph.ChangeConfig, "example.test/full.DSN",
			"reads_config must point from the reader to the key"},
		{"payments", graph.ChangeSchema, "example.test/full.Postgres.Load",
			"reads_schema must point from the query to the table"},
		{"Load", graph.ChangeSignature, "example.test/full.Postgres",
			"contains must point from the container to the member"},
	}
	for _, c := range cases {
		t.Run(c.consumer, func(t *testing.T) {
			consumers := impactOf(t, c.symbol, c.change)
			if _, ok := consumers[c.consumer]; !ok {
				t.Errorf("%s\nchanging %s did not reach %s\n%s",
					c.why, c.symbol, c.consumer, list(consumers))
			}
		})
	}
}

func list(consumers map[string]graph.Consumer) string {
	var b strings.Builder
	b.WriteString("reached:")
	for fqn, c := range consumers {
		b.WriteString("\n  " + string(c.Via) + " " + fqn)
	}
	return b.String()
}

// TestTypeScriptImplementsPointsAtTheInterface holds the TypeScript analyzer to
// the same invariant as the Go one. The direction is load-bearing: impact
// analysis is a reverse traversal, so an `implements` edge that pointed
// interface to class would report zero implementations when a method is added
// to an interface — the exact question the edge exists to answer.
func TestTypeScriptImplementsPointsAtTheInterface(t *testing.T) {
	consumers := impactOf(t, "UserStore", graph.ChangeSignature)
	if _, ok := consumers["ts:web/sql_store.ts#SqlUserStore"]; !ok {
		t.Fatalf("changing the UserStore interface did not report SqlUserStore as a "+
			"consumer; the implements edge points the wrong way.\n%s", list(consumers))
	}
}

// And an importer must be a consumer of what it imports, for the same reason:
// changing a module has to find the modules that pull it in.
func TestTypeScriptImporterIsAConsumer(t *testing.T) {
	consumers := impactOf(t, "user.ts", graph.ChangeSignature)
	if _, ok := consumers["ts:web/sql_store.ts"]; !ok {
		t.Fatalf("changing a module did not report its importer as a consumer; "+
			"the imports edge points the wrong way.\n%s", list(consumers))
	}
}
