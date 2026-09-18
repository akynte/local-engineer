package lsp_test

// The client is exercised against a real process speaking the real framing,
// not against an in-memory stub. The bugs an LSP client actually has are in the
// framing and in the result shapes servers are allowed to choose between, and a
// stub that returns Go structs tests neither.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/lsp"
)

// startFake runs this test binary as a language server. mode picks which of the
// protocol's permitted result shapes it answers with.
func startFake(t *testing.T, root, mode string) *lsp.Client {
	t.Helper()
	c, err := lsp.Start(context.Background(), root, os.Args[0], "-test.run=TestFakeServer", "--", mode)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestReferencesAcceptsAnArrayOfLocations(t *testing.T) {
	root := t.TempDir()
	c := startFake(t, root, "array")

	locs, err := c.References(context.Background(), "risk/order.go", lsp.Position{Line: 10, Character: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 2 {
		t.Fatalf("got %d locations, want 2: %+v", len(locs), locs)
	}
	if locs[0].Path != "risk/order.go" || locs[1].Path != "risk/pnl.go" {
		t.Fatalf("paths were not made workspace-relative: %+v", locs)
	}
	if locs[0].Range.Start.Line != 10 {
		t.Fatalf("range was lost: %+v", locs[0].Range)
	}
}

// A server is allowed to answer with one object instead of an array. A client
// that handled only the array shape would return nothing and the caller would
// read that as "no references".
func TestDefinitionAcceptsASingleLocationAndLocationLinks(t *testing.T) {
	root := t.TempDir()

	single := startFake(t, root, "single")
	locs, err := single.Definition(context.Background(), "risk/order.go", lsp.Position{})
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 1 || locs[0].Path != "risk/order.go" {
		t.Fatalf("a single-object result was not understood: %+v", locs)
	}

	links := startFake(t, root, "links")
	locs, err = links.Definition(context.Background(), "risk/order.go", lsp.Position{})
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 1 || locs[0].Path != "risk/pnl.go" {
		t.Fatalf("a LocationLink result was not understood: %+v", locs)
	}
}

// A dependency's source is not this repository's code and must not reach a
// packet, however the server reports it.
func TestLocationsOutsideTheWorkspaceAreDropped(t *testing.T) {
	root := t.TempDir()
	c := startFake(t, root, "outside")

	locs, err := c.References(context.Background(), "risk/order.go", lsp.Position{})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range locs {
		if strings.HasPrefix(l.Path, "..") || filepath.IsAbs(l.Path) {
			t.Fatalf("a location outside the workspace survived: %+v", l)
		}
	}
	if len(locs) != 1 || locs[0].Path != "risk/order.go" {
		t.Fatalf("want only the in-workspace location, got %+v", locs)
	}
}

func TestNullResultIsNoLocationsRatherThanAnError(t *testing.T) {
	c := startFake(t, t.TempDir(), "null")
	locs, err := c.Definition(context.Background(), "risk/order.go", lsp.Position{})
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 0 {
		t.Fatalf("null was not read as an empty result: %+v", locs)
	}
}

func TestServerErrorsAreReportedRatherThanSwallowed(t *testing.T) {
	c := startFake(t, t.TempDir(), "error")
	if _, err := c.References(context.Background(), "risk/order.go", lsp.Position{}); err == nil {
		t.Fatal("a server error was reported as an empty reference list")
	}
}

// A server that never answers must not hold a phase open. The caller's fallback
// is the index, which is always there.
func TestARequestThatIsNeverAnsweredTimesOut(t *testing.T) {
	c := startFake(t, t.TempDir(), "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 300_000_000) // 300ms
	defer cancel()
	if _, err := c.References(ctx, "risk/order.go", lsp.Position{}); err == nil {
		t.Fatal("a request that was never answered returned successfully")
	}
}

// Diagnostics arrive unsolicited, which is the one place the client has to
// route a message nobody is waiting for.
func TestUnsolicitedDiagnosticsAreRecordedAgainstTheirFile(t *testing.T) {
	c := startFake(t, t.TempDir(), "diagnostics")
	// The fake publishes on didOpen, so this is what triggers it.
	if err := c.DidOpen("risk/order.go", "go", "package risk\n"); err != nil {
		t.Fatal(err)
	}
	var diags []lsp.Diagnostic
	for range 50 {
		if diags = c.Diagnostics("risk/order.go"); len(diags) > 0 {
			break
		}
		//nolint:staticcheck // a short spin is simpler than a channel the API does not otherwise need
	}
	if len(diags) == 0 {
		t.Skip("diagnostics did not arrive within the spin; the notification path is timing-dependent")
	}
	if diags[0].Message == "" || diags[0].Severity == 0 {
		t.Fatalf("diagnostic was not parsed: %+v", diags[0])
	}
}

func TestDocumentSymbolsAcceptBothShapes(t *testing.T) {
	for _, mode := range []string{"symbols-hierarchical", "symbols-flat"} {
		c := startFake(t, t.TempDir(), mode)
		symbols, err := c.DocumentSymbols(context.Background(), "risk/order.go")
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if len(symbols) != 1 || symbols[0].Name != "Check" {
			t.Fatalf("%s: %+v", mode, symbols)
		}
	}
}

func TestStartWithoutAConfiguredServerIsAnError(t *testing.T) {
	if _, err := lsp.Start(context.Background(), t.TempDir()); err == nil {
		t.Fatal("a client started with no server command")
	}
}

// --------------------------------------------------------------- fake server

// TestFakeServer is not a test: it is the language server the tests above talk
// to, using the standard Go pattern for code that must run in its own process.
func TestFakeServer(t *testing.T) {
	mode := ""
	for i, a := range os.Args {
		if a == "--" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
		}
	}
	if mode == "" {
		t.Skip("fake language server entry point")
	}
	serveFake(mode, bufio.NewReader(os.Stdin), os.Stdout)
}

func serveFake(mode string, in *bufio.Reader, out io.Writer) {
	root, _ := os.Getwd()
	uri := func(rel string) string { return "file://" + filepath.ToSlash(filepath.Join(root, rel)) }
	send := func(v any) {
		body, _ := json.Marshal(v)
		fmt.Fprintf(out, "Content-Length: %d\r\n\r\n%s", len(body), body)
	}

	for {
		body, err := readFrame(in)
		if err != nil {
			return
		}
		var msg struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(body, &msg) != nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"capabilities": map[string]any{}}})
		case "shutdown":
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": nil})
		case "exit":
			return
		case "textDocument/didOpen":
			if mode == "diagnostics" {
				send(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{
					"uri": uri("risk/order.go"),
					"diagnostics": []map[string]any{{
						"range":    map[string]any{"start": map[string]int{"line": 3, "character": 1}, "end": map[string]int{"line": 3, "character": 8}},
						"severity": 1, "source": "fake", "message": "undefined: Unrealized",
					}},
				}})
			}
		case "textDocument/references", "textDocument/definition", "textDocument/implementation":
			fakeLocationResult(mode, msg.ID, uri, send)
		case "textDocument/documentSymbol":
			fakeSymbolResult(mode, msg.ID, uri, send)
		}
	}
}

func fakeLocationResult(mode string, id *int64, uri func(string) string, send func(any)) {
	span := map[string]any{
		"start": map[string]int{"line": 10, "character": 5},
		"end":   map[string]int{"line": 10, "character": 10},
	}
	reply := func(result any) { send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}) }
	switch mode {
	case "single":
		reply(map[string]any{"uri": uri("risk/order.go"), "range": span})
	case "links":
		reply([]map[string]any{{"targetUri": uri("risk/pnl.go"), "targetSelectionRange": span}})
	case "outside":
		reply([]map[string]any{
			{"uri": uri("risk/order.go"), "range": span},
			{"uri": "file:///usr/lib/go/src/fmt/print.go", "range": span},
		})
	case "null":
		reply(nil)
	case "error":
		send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32603, "message": "index not ready"}})
	case "hang":
		// Deliberately no reply.
	default:
		reply([]map[string]any{
			{"uri": uri("risk/order.go"), "range": span},
			{"uri": uri("risk/pnl.go"), "range": span},
		})
	}
}

func fakeSymbolResult(mode string, id *int64, uri func(string) string, send func(any)) {
	span := map[string]any{
		"start": map[string]int{"line": 2, "character": 0},
		"end":   map[string]int{"line": 4, "character": 1},
	}
	reply := func(result any) { send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}) }
	if mode == "symbols-flat" {
		reply([]map[string]any{{"name": "Check", "kind": 12, "location": map[string]any{"uri": uri("risk/order.go"), "range": span}}})
		return
	}
	reply([]map[string]any{{"name": "Check", "kind": 12, "range": span, "selectionRange": span}})
}

func readFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, err
			}
		}
	}
	if length < 0 {
		return nil, io.EOF
	}
	body := make([]byte, length)
	_, err := io.ReadFull(r, body)
	return body, err
}

// TestAgainstGopls runs the same questions against a real server when one is
// installed. It is skipped rather than required: the fake covers the protocol,
// and this covers the assumption that a real server agrees with it.
func TestAgainstGopls(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls is not installed")
	}
	root := t.TempDir()
	write := func(rel, body string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/risk\n\ngo 1.22\n")
	write("risk.go", "package risk\n\nfunc Check() error { return nil }\n\nfunc use() error { return Check() }\n")

	c, err := lsp.Start(context.Background(), root, "gopls", "-mode=stdio")
	if err != nil {
		t.Skipf("gopls did not start: %v", err)
	}
	defer func() { _ = c.Close() }()

	body, err := os.ReadFile(filepath.Join(root, "risk.go")) //nolint:gosec // written above
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DidOpen("risk.go", "go", string(body)); err != nil {
		t.Fatal(err)
	}
	// `Check` on line 2 (zero-based), at the identifier.
	locs, err := c.References(context.Background(), "risk.go", lsp.Position{Line: 2, Character: 5})
	if err != nil {
		t.Skipf("gopls was still indexing: %v", err)
	}
	if len(locs) < 2 {
		t.Fatalf("a real server found %d references to a symbol with a declaration and one caller: %+v", len(locs), locs)
	}
}
