package deploy_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/analyzers/deploy"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

type result struct {
	nodes map[string]graph.Node
	edges []index.PendingEdge
}

func (r result) has(kind graph.EdgeKind, src, dst string) *index.PendingEdge {
	for i, e := range r.edges {
		if e.Kind == kind && e.SrcFQN == src && e.DstFQN == dst {
			return &r.edges[i]
		}
	}
	return nil
}

func (r result) mustHave(t *testing.T, kind graph.EdgeKind, src, dst string) index.PendingEdge {
	t.Helper()
	e := r.has(kind, src, dst)
	if e == nil {
		t.Fatalf("missing %s edge %s -> %s\nedges: %s", kind, src, dst, r.describe())
	}
	return *e
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
		lang := ""
		if strings.HasSuffix(rel, ".yml") || strings.HasSuffix(rel, ".yaml") {
			lang = "yaml"
		}
		list = append(list, index.File{Path: rel, AbsPath: p, Lang: lang, Size: int64(len(body))})
	}

	a := deploy.New()
	a.Warnf = func(f string, args ...any) { t.Logf("deploy: "+f, args...) }
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

// §3.2: "deployment component to service | compose … | declared".
func TestComposeServicesAreDeclaredNotResolved(t *testing.T) {
	r := analyze(t, map[string]string{
		"docker-compose.yml": `
name: payments
services:
  api:
    build: .
    environment:
      DATABASE_URL: postgres://db/payments
      LOG_LEVEL: info
    depends_on:
      db:
        condition: service_healthy
  db:
    image: postgres:17
`,
	})

	stack := "deployment:payments@docker-compose.yml"
	if _, ok := r.nodes[stack]; !ok {
		t.Fatalf("no stack node; got %v", keys(r.nodes))
	}
	e := r.mustHave(t, graph.EdgeDeploys, stack, "service:api")
	// A manifest states an intent. Whether the deployment matches it is a
	// question about a running system, not about the file.
	if e.Evidence != graph.Declared {
		t.Errorf("a compose service must be declared, got %s", e.Evidence)
	}

	// The build context ties the service to a Dockerfile, which is what makes
	// "what deploys this code" answerable.
	r.mustHave(t, graph.EdgeBuilds, "service:api", "dockerfile:Dockerfile")
	r.mustHave(t, graph.EdgeDependsOn, "service:api", "service:db")

	// The other half of the configuration row: the Go analyzer finds who reads
	// a key, this finds who sets it.
	for _, key := range []string{"env:DATABASE_URL", "env:LOG_LEVEL"} {
		if _, ok := r.nodes[key]; !ok {
			t.Errorf("missing config key node %s", key)
		}
		r.mustHave(t, graph.EdgeReadsConfig, "service:api", key)
	}
}

// Compose accepts environment as a mapping or as KEY=VALUE strings.
func TestComposeEnvironmentListForm(t *testing.T) {
	r := analyze(t, map[string]string{
		"compose.yaml": `
services:
  worker:
    image: worker:latest
    environment:
      - QUEUE_URL=amqp://mq
      - DEBUG
`,
	})
	for _, key := range []string{"env:QUEUE_URL", "env:DEBUG"} {
		if _, ok := r.nodes[key]; !ok {
			t.Errorf("missing %s; the list form was not parsed", key)
		}
	}
}

// §3.2: "build target to dependency | Docker COPY/RUN parsing".
func TestDockerfileStagesAndCopies(t *testing.T) {
	r := analyze(t, map[string]string{
		"Dockerfile": `# a multi-stage build
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/ internal/ ./
RUN go build -o /out/app ./cmd/app

FROM debian:bookworm-slim
ENV APP_ENV=production LOG_LEVEL=info
COPY --from=build /out/app /usr/local/bin/app
`,
		"go.mod": "module example.com/app\n",
	})

	target := "dockerfile:Dockerfile"
	buildStage := target + "#build"
	if _, ok := r.nodes[buildStage]; !ok {
		t.Fatalf("named stage missing; got %v", keys(r.nodes))
	}
	// The base image is a dependency of the stage.
	r.mustHave(t, graph.EdgeDependsOn, buildStage, "image:golang:1.26")
	// What a stage COPYs in is what it depends on.
	r.mustHave(t, graph.EdgeBuilds, buildStage, "go.mod")

	// A COPY --from another stage is a stage dependency, not a file.
	runtime := target + "#debian:bookworm-slim"
	if _, ok := r.nodes[runtime]; !ok {
		t.Fatalf("unnamed stage missing; got %v", keys(r.nodes))
	}
	r.mustHave(t, graph.EdgeDependsOn, runtime, buildStage)

	// ENV keys set in the image are configuration.
	for _, key := range []string{"env:APP_ENV", "env:LOG_LEVEL"} {
		if _, ok := r.nodes[key]; !ok {
			t.Errorf("missing %s from an ENV instruction", key)
		}
	}
}

// A glob cannot name one file, so it produces no edge rather than a wrong one.
func TestDockerfileGlobCopyProducesNoFileEdge(t *testing.T) {
	r := analyze(t, map[string]string{
		"Dockerfile": "FROM alpine\nCOPY *.go /src/\nCOPY . /app\n",
		"main.go":    "package main\n",
	})
	if e := r.has(graph.EdgeBuilds, "dockerfile:Dockerfile#alpine", "main.go"); e != nil {
		t.Error("a glob must not be resolved to a specific file")
	}
}

// §3.2: "build target to dependency | Makefile targets | resolved plus inferred".
func TestMakefileTargetsAndPrerequisites(t *testing.T) {
	r := analyze(t, map[string]string{
		"Makefile": `
VERSION := 1.0
.PHONY: build test

build: generate
	go build ./...

test: build
	go test ./...

generate:
	go generate ./...
`,
	})
	file := "makefile:Makefile"
	for _, target := range []string{"build", "test", "generate"} {
		if _, ok := r.nodes[file+"#"+target]; !ok {
			t.Errorf("missing target %s; got %v", target, keys(r.nodes))
		}
	}
	// The structure the file states outright is resolved.
	e := r.mustHave(t, graph.EdgeDependsOn, file+"#test", file+"#build")
	if e.Evidence != graph.Resolved {
		t.Errorf("a stated prerequisite is resolved, got %s", e.Evidence)
	}
	// A variable assignment shares the colon with a rule and must not become
	// a target.
	if _, ok := r.nodes[file+"#VERSION"]; ok {
		t.Error("a variable assignment was parsed as a target")
	}
	// .PHONY is a directive, not a target.
	if _, ok := r.nodes[file+"#.PHONY"]; ok {
		t.Error(".PHONY was parsed as a target")
	}
}

// §3.2: "deployment component to service | … Kubernetes manifests | declared".
func TestKubernetesWorkloadsAndConfig(t *testing.T) {
	r := analyze(t, map[string]string{
		"deploy/app.yaml": `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: payments-api
  namespace: prod
spec:
  template:
    spec:
      containers:
        - name: api
          image: ghcr.io/example/api:1.2.3
          env:
            - name: DATABASE_URL
              valueFrom:
                secretKeyRef: {name: db, key: url}
            - name: LOG_LEVEL
              value: info
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
data:
  FEATURE_FLAG: "true"
---
apiVersion: v1
kind: Service
metadata:
  name: payments-svc
spec:
  selector: {app: payments}
`,
	})

	workload := "k8s:deployment:prod/payments-api"
	if _, ok := r.nodes[workload]; !ok {
		t.Fatalf("workload missing; got %v", keys(r.nodes))
	}
	e := r.mustHave(t, graph.EdgeDeploys, workload, "service:api")
	if e.Evidence != graph.Declared {
		t.Errorf("a manifest declares, it does not resolve; got %s", e.Evidence)
	}
	r.mustHave(t, graph.EdgeReadsConfig, "service:api", "env:DATABASE_URL")

	// A ConfigMap's keys are configuration.
	r.mustHave(t, graph.EdgeProvisions, "k8s:configmap:app-config", "env:FEATURE_FLAG")

	// A Service is infrastructure, not a workload.
	svc, ok := r.nodes["k8s:service:payments-svc"]
	if !ok {
		t.Fatal("the Service object was not recorded")
	}
	if svc.Kind != graph.KindInfraResource {
		t.Errorf("a Kubernetes Service is infrastructure, not a workload; got kind %s", svc.Kind)
	}
}

// A Helm template is not YAML. Rendering needs values this analyzer does not
// have, and a half-rendered template read as YAML produces confident nonsense.
func TestHelmTemplatesAreRecordedNotParsed(t *testing.T) {
	r := analyze(t, map[string]string{
		"chart/templates/deployment.yaml": `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-api
spec:
  replicas: {{ .Values.replicas }}
`,
	})
	n, ok := r.nodes["helm:chart/templates/deployment.yaml"]
	if !ok {
		t.Fatalf("the template was not recorded; got %v", keys(r.nodes))
	}
	if !strings.Contains(n.Attrs, "not parsed") {
		t.Errorf("the node must say it was not parsed: %s", n.Attrs)
	}
	// And it must NOT have produced a bogus workload from the template text.
	for fqn := range r.nodes {
		if strings.HasPrefix(fqn, "k8s:deployment:") {
			t.Errorf("a Helm template was parsed as a manifest: %s", fqn)
		}
	}
}

// A YAML file that is not a manifest must produce nothing.
func TestOrdinaryYAMLIsIgnored(t *testing.T) {
	r := analyze(t, map[string]string{
		".github/workflows/ci.yml": "name: CI\non: [push]\njobs:\n  test:\n    runs-on: ubuntu-latest\n",
		"config/app.yaml":          "server:\n  port: 8080\n",
	})
	if len(r.nodes) != 0 {
		t.Errorf("ordinary YAML produced %d nodes: %v", len(r.nodes), keys(r.nodes))
	}
}

// One malformed document must not lose the others.
func TestAMalformedDocumentDoesNotLoseTheRest(t *testing.T) {
	r := analyze(t, map[string]string{
		"k8s.yaml": `
apiVersion: v1
kind: ConfigMap
metadata:
  name: good
data:
  KEY: value
---
this: is: not: valid: yaml:
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: also-good
spec:
  template:
    spec:
      containers:
        - name: c
          image: img
`,
	})
	for _, want := range []string{"k8s:configmap:good", "k8s:deployment:also-good"} {
		if _, ok := r.nodes[want]; !ok {
			t.Errorf("missing %s; a bad document lost the good ones", want)
		}
	}
}

func keys(m map[string]graph.Node) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
