package terraform_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/analyzers/terraform"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

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
		list = append(list, index.File{Path: rel, AbsPath: p, Size: int64(len(body))})
	}
	a := terraform.New()
	a.Warnf = func(f string, args ...any) { t.Logf("terraform: "+f, args...) }
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

func TestResourcesAndReferencesAreDeclared(t *testing.T) {
	r := analyze(t, map[string]string{
		"infra/main.tf": `
variable "environment" {
  type    = string
  default = "production"
}

resource "aws_db_instance" "payments" {
  identifier = "payments-${var.environment}"
  engine     = "postgres"
}

resource "aws_ecs_task_definition" "api" {
  family = "payments-api"

  container_definitions = jsonencode([{
    name = "api"
  }])

  environment {
    name  = "DATABASE_URL"
    value = aws_db_instance.payments.endpoint
  }
}

module "networking" {
  source = "./modules/net"
}
`,
	})

	db := "tf:resource.aws_db_instance.payments"
	task := "tf:resource.aws_ecs_task_definition.api"
	for _, want := range []string{db, task, "tf:variable.environment", "tf:module.networking"} {
		if _, ok := r.nodes[want]; !ok {
			t.Errorf("missing %s; got %v", want, keys(r.nodes))
		}
	}

	// A reference between resources is stated by the file, so it is declared.
	e := r.edge(graph.EdgeDependsOn, task, db)
	if e == nil {
		t.Fatalf("missing dependency from the task to the database%s", r.describe())
	}
	if e.Evidence != graph.Declared {
		t.Errorf("a file states a reference; it does not resolve it. got %s", e.Evidence)
	}

	// A resource interpolating a variable depends on it.
	if r.edge(graph.EdgeDependsOn, db, "tf:variable.environment") == nil {
		t.Errorf("missing dependency on the variable%s", r.describe())
	}
}

// Linking infrastructure to application code rests on two files agreeing on a
// string, which is weaker than either file's own contents.
func TestEnvironmentLinksAreInferredAndSayWhy(t *testing.T) {
	r := analyze(t, map[string]string{
		"infra/ecs.tf": `
resource "aws_ecs_task_definition" "api" {
  family = "api"
  environment {
    name  = "DATABASE_URL"
    value = "postgres://db/app"
  }
}

resource "aws_lambda_function" "worker" {
  function_name = "worker"
  environment_variables = {
    QUEUE_URL   = "https://sqs/queue"
    LOG_LEVEL   = "info"
  }
}
`,
	})

	for _, key := range []string{"env:DATABASE_URL", "env:QUEUE_URL", "env:LOG_LEVEL"} {
		if _, ok := r.nodes[key]; !ok {
			t.Errorf("missing config key %s; got %v", key, keys(r.nodes))
		}
	}
	e := r.edge(graph.EdgeProvisions, "tf:resource.aws_ecs_task_definition.api", "env:DATABASE_URL")
	if e == nil {
		t.Fatalf("missing provisions edge%s", r.describe())
	}
	if e.Evidence != graph.Inferred {
		t.Errorf("a name-matched link is inferred, got %s", e.Evidence)
	}
	if !strings.Contains(e.Attrs, "assumption") {
		t.Errorf("the assumption must be recorded: %s", e.Attrs)
	}
}

// A reference to something declared elsewhere produces no edge: a missing
// edge means not discovered.
func TestReferencesToUndeclaredResourcesAreDropped(t *testing.T) {
	r := analyze(t, map[string]string{
		"infra/main.tf": `
resource "aws_instance" "app" {
  subnet_id = aws_subnet.elsewhere.id
}
`,
	})
	for _, e := range r.edges {
		if e.Kind == graph.EdgeDependsOn && strings.Contains(e.DstFQN, "elsewhere") {
			t.Errorf("an edge was emitted to a resource that is not declared here: %+v", e)
		}
	}
}

// A file mid-edit is normal; parse what is valid and report the rest.
func TestAMalformedFileIsReportedNotFatal(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.tf")
	bad := filepath.Join(dir, "bad.tf")
	if err := os.WriteFile(good, []byte(`resource "aws_s3_bucket" "assets" { bucket = "x" }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte(`resource "broken" { = = =`), 0o644); err != nil {
		t.Fatal(err)
	}

	a := terraform.New()
	var warned bool
	a.Warnf = func(string, ...any) { warned = true }

	res, err := a.Analyze(context.Background(), dir, []index.File{
		{Path: "good.tf", AbsPath: good}, {Path: "bad.tf", AbsPath: bad},
	})
	if err != nil {
		t.Fatalf("a malformed file must not fail the run: %v", err)
	}
	if !warned {
		t.Error("a parse failure must be reported, not swallowed")
	}
	var found bool
	for _, n := range res.Nodes {
		if n.FQN == "tf:resource.aws_s3_bucket.assets" {
			found = true
		}
	}
	if !found {
		t.Error("the valid file should still have been parsed")
	}
}

// terraform {} and backend blocks configure the tool, not infrastructure.
func TestToolConfigurationIsNotInfrastructure(t *testing.T) {
	r := analyze(t, map[string]string{
		"infra/versions.tf": `
terraform {
  required_version = ">= 1.5"
  backend "s3" {
    bucket = "state"
  }
}
`,
	})
	if len(r.nodes) != 0 {
		t.Errorf("tool configuration produced %d nodes: %v", len(r.nodes), keys(r.nodes))
	}
}

func keys(m map[string]graph.Node) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
