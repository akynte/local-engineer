package protoavro_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/analyzers/protoavro"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

func analyze(t *testing.T, files map[string]string) index.Result {
	t.Helper()
	root := t.TempDir()
	var list []index.File
	a := protoavro.New()
	a.Warnf = func(f string, args ...any) { t.Logf("warn: "+f, args...) }
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		f := index.File{Path: rel}
		if a.Handles(f) {
			list = append(list, f)
		}
	}
	res, err := a.Analyze(context.Background(), root, list)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func hasNode(res index.Result, fqn string) bool {
	for _, n := range res.Nodes {
		if n.FQN == fqn {
			return true
		}
	}
	return false
}

func hasEdge(res index.Result, src, dst string, kind graph.EdgeKind) bool {
	for _, e := range res.Edges {
		if e.SrcFQN == src && e.DstFQN == dst && e.Kind == kind {
			return true
		}
	}
	return false
}

func TestProtoMessagesAndServices(t *testing.T) {
	res := analyze(t, map[string]string{
		"api/billing.proto": `syntax = "proto3";
package billing.v1;

import "common/money.proto";

message Invoice {
  string id = 1;
}

enum Status {
  DRAFT = 0;
}

service Billing {
  rpc GetInvoice(GetInvoiceRequest) returns (Invoice);
}

message GetInvoiceRequest {
  string id = 1;
}
`,
	})
	for _, want := range []string{
		"proto:billing.v1.Invoice", "proto:billing.v1.Status",
		"proto:billing.v1.Billing", "proto:billing.v1.GetInvoiceRequest",
	} {
		if !hasNode(res, want) {
			t.Errorf("missing node %s", want)
		}
	}
	// The service consumes its request and response types, so changing a
	// message finds the RPCs that carry it.
	if !hasEdge(res, "proto:billing.v1.Billing", "proto:billing.v1.Invoice", graph.EdgeUsesType) {
		t.Error("the service does not point at its response message")
	}
	if !hasEdge(res, "proto:billing.v1.Billing", "proto:billing.v1.GetInvoiceRequest", graph.EdgeUsesType) {
		t.Error("the service does not point at its request message")
	}
	if !hasEdge(res, "schema:api/billing.proto", "schema:common/money.proto", graph.EdgeImports) {
		t.Error("the import between contracts was not recorded")
	}
}

// A commented-out message must not become a node. An analyzer that reports a
// contract because the word appeared in a comment is worse than one reporting
// nothing: the graph's value is that an edge in it means something.
func TestCommentedDeclarationsAreIgnored(t *testing.T) {
	res := analyze(t, map[string]string{
		"a.proto": `syntax = "proto3";
package p;

// message Ghost {
//   string id = 1;
// }

/* message AlsoGhost { } */

message Real {
  string id = 1;
}
`,
	})
	if hasNode(res, "proto:p.Ghost") || hasNode(res, "proto:p.AlsoGhost") {
		t.Error("a commented-out message became a node")
	}
	if !hasNode(res, "proto:p.Real") {
		t.Error("the real message was not found")
	}
}

func TestAvroRecordAndFields(t *testing.T) {
	res := analyze(t, map[string]string{
		"events/payment.avsc": `{
  "type": "record",
  "name": "PaymentReceived",
  "namespace": "com.example.events",
  "fields": [
    {"name": "id", "type": "string"},
    {"name": "amount", "type": ["null", "long"]}
  ]
}`,
	})
	record := "avro:com.example.events.PaymentReceived"
	if !hasNode(res, record) {
		t.Fatalf("the Avro record was not found:\n%+v", res.Nodes)
	}
	for _, f := range []string{"id", "amount"} {
		if !hasEdge(res, record, record+"."+f, graph.EdgeContains) {
			t.Errorf("field %s is not contained by the record", f)
		}
	}
	// A union type is recorded as written rather than resolved: chasing every
	// Avro shape would be a schema compiler.
	for _, n := range res.Nodes {
		if n.FQN == record+".amount" && !strings.Contains(n.Attrs, "null") {
			t.Errorf("the union type was not recorded: %s", n.Attrs)
		}
	}
}

// Everything here is `declared`: the file states it, and whether the generated
// code matches is a question about the generator having been run.
func TestSchemaEdgesAreDeclaredOrResolved(t *testing.T) {
	res := analyze(t, map[string]string{
		"a.proto": "syntax = \"proto3\";\npackage p;\nmessage M { string id = 1; }\n",
	})
	for _, e := range res.Edges {
		switch e.Evidence {
		case graph.Declared, graph.Resolved:
		default:
			t.Errorf("edge %s -> %s has evidence %q; a schema file states its contents",
				e.SrcFQN, e.DstFQN, e.Evidence)
		}
	}
}

func TestHandlesOnlySchemaFiles(t *testing.T) {
	a := protoavro.New()
	for _, p := range []string{"a.proto", "b.avsc"} {
		if !a.Handles(index.File{Path: p}) {
			t.Errorf("%s was rejected", p)
		}
	}
	for _, p := range []string{"a.go", "b.json", "c.yaml"} {
		if a.Handles(index.File{Path: p}) {
			t.Errorf("%s was accepted", p)
		}
	}
}
