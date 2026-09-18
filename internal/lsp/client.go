// Package lsp is a small Language Server Protocol client, written rather than
// taken as a dependency (architecture review §16).
//
// Its job is narrow and worth stating, because an LSP client can grow to fill
// any amount of space. The persistent cross-reference layer is SCIP: compiler
// output, stored in SQLite, queryable without a server running. What SCIP
// cannot do is answer for a file that has been edited since the index was
// built, and during an EDIT phase that is exactly the file under discussion.
// So this exists to answer for dirty files, and for the case §24 names, where
// the SCIP indexer lags a toolchain release and the graph goes stale.
//
// It therefore implements what those questions need — initialize, the text
// synchronisation that tells a server about an unsaved buffer, definition,
// references, implementation and document symbols — and nothing else. A
// completion or a code action would be a feature of an editor, which this is
// not.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultTimeout bounds one request.
//
// A language server that is still indexing answers slowly or not at all, and a
// phase that blocks on one has turned an optional layer into a required one.
// The caller's fallback is the index, which is always there.
const DefaultTimeout = 10 * time.Second

// Client speaks the protocol to one server process.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Reader
	root   string
	closed chan struct{}
	// stop ends the server process. It is not derived from the context that
	// started the client: a language server outlives the call that launched it
	// and is shut down by Close, so binding it to a request context would kill
	// the server the moment that request returned.
	stop context.CancelFunc

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan response

	diagMu      sync.Mutex
	diagnostics map[string][]Diagnostic
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type response struct {
	ID     *int64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *responseError  `json:"error"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type responseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *responseError) Error() string {
	return fmt.Sprintf("lsp: server error %d: %s", e.Code, e.Message)
}

// Position is a zero-based line and UTF-16 character offset, which is what the
// protocol means by a position regardless of what the file is encoded in.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a half-open span.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location is a range in a file, with the path made relative to the root so it
// can be compared with everything else in this system.
type Location struct {
	Path  string `json:"path"`
	Range Range  `json:"range"`
}

// Symbol is one entry from a document's symbol tree.
type Symbol struct {
	Name     string   `json:"name"`
	Kind     int      `json:"kind"`
	Range    Range    `json:"range"`
	Detail   string   `json:"detail,omitempty"`
	Children []Symbol `json:"children,omitempty"`
}

// Diagnostic is a problem a server reported for a file.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"`
	Code     string `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// Start launches a server and completes the initialize handshake.
//
// argv is operator configuration — the command for gopls, rust-analyzer or a
// TypeScript server — and is never model-supplied. A model that could name the
// language server binary would have a free-form process launcher.
func Start(ctx context.Context, root string, argv ...string) (*Client, error) {
	if len(argv) == 0 {
		return nil, errors.New("lsp: no server command configured")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	life, stop := context.WithCancel(context.WithoutCancel(ctx))
	//nolint:gosec // argv is operator configuration, never a model argument
	cmd := exec.CommandContext(life, argv[0], argv[1:]...)
	cmd.Dir = abs
	stdin, err := cmd.StdinPipe()
	if err != nil {
		stop()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stop()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stop()
		return nil, fmt.Errorf("lsp: starting %s: %w", argv[0], err)
	}
	c := &Client{
		cmd: cmd, stdin: stdin, out: bufio.NewReader(stdout), root: abs,
		stop:        stop,
		closed:      make(chan struct{}),
		pending:     map[int64]chan response{},
		diagnostics: map[string][]Diagnostic{},
	}
	go c.read()

	var result json.RawMessage
	if err := c.call(ctx, "initialize", map[string]any{
		"processId": nil,
		"rootUri":   pathToURI(abs),
		"workspaceFolders": []map[string]string{
			{"uri": pathToURI(abs), "name": filepath.Base(abs)},
		},
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"synchronization": map[string]any{"didSave": false, "dynamicRegistration": false},
				"definition":      map[string]any{"dynamicRegistration": false},
				"references":      map[string]any{"dynamicRegistration": false},
				"implementation":  map[string]any{"dynamicRegistration": false},
				"documentSymbol":  map[string]any{"hierarchicalDocumentSymbolSupport": true},
				"publishDiagnostics": map[string]any{
					"relatedInformation": false,
				},
			},
			"workspace": map[string]any{"workspaceFolders": true},
		},
	}, &result); err != nil {
		_ = c.Close() //nolint:contextcheck // see Close: shutdown must not inherit a context that may already be done
		return nil, err
	}
	if err := c.notify("initialized", map[string]any{}); err != nil {
		_ = c.Close() //nolint:contextcheck // as above
		return nil, err
	}
	return c, nil
}

// DidOpen tells the server about a buffer's current contents.
//
// This is the whole reason the client exists: the file on disk and the file the
// index knows about can both be out of date, and the server answers about what
// it was told, so a reference query after an edit means telling it first.
func (c *Client) DidOpen(rel, languageID, text string) error {
	return c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": pathToURI(filepath.Join(c.root, rel)), "languageId": languageID,
			"version": 1, "text": text,
		},
	})
}

// DidClose releases a buffer.
func (c *Client) DidClose(rel string) error {
	return c.notify("textDocument/didClose", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(filepath.Join(c.root, rel))},
	})
}

// Definition resolves where a symbol is defined.
func (c *Client) Definition(ctx context.Context, rel string, pos Position) ([]Location, error) {
	return c.locations(ctx, "textDocument/definition", rel, pos, false)
}

// Implementation resolves the implementations of an interface or method.
func (c *Client) Implementation(ctx context.Context, rel string, pos Position) ([]Location, error) {
	return c.locations(ctx, "textDocument/implementation", rel, pos, false)
}

// References lists every use, including the declaration, which is what an
// impact question wants: a caller list missing the definition reads as if the
// symbol came from nowhere.
func (c *Client) References(ctx context.Context, rel string, pos Position) ([]Location, error) {
	return c.locations(ctx, "textDocument/references", rel, pos, true)
}

func (c *Client) locations(ctx context.Context, method, rel string, pos Position, includeDecl bool) ([]Location, error) {
	params := map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(filepath.Join(c.root, rel))},
		"position":     pos,
	}
	if includeDecl {
		params["context"] = map[string]any{"includeDeclaration": true}
	}
	var raw json.RawMessage
	if err := c.call(ctx, method, params, &raw); err != nil {
		return nil, err
	}
	return c.parseLocations(raw)
}

// parseLocations accepts the three shapes the protocol allows for a location
// result: null, one Location, or an array of Location or LocationLink. A client
// that handled only the array shape would silently return nothing for the
// servers that answer with a single object.
func (c *Client) parseLocations(raw json.RawMessage) ([]Location, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	type wire struct {
		URI string `json:"uri"`
		// LocationLink spells them differently.
		TargetURI   string `json:"targetUri"`
		Range       Range  `json:"range"`
		TargetRange Range  `json:"targetSelectionRange"`
	}
	var many []wire
	if err := json.Unmarshal(raw, &many); err != nil {
		var one wire
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, fmt.Errorf("lsp: unrecognised location result: %w", err)
		}
		many = []wire{one}
	}
	var out []Location
	for _, w := range many {
		uri, r := w.URI, w.Range
		if uri == "" {
			uri, r = w.TargetURI, w.TargetRange
		}
		path, ok := c.relative(uri)
		if !ok {
			// A location outside the workspace is a dependency's source. It is
			// not this repository's code and never enters a packet.
			continue
		}
		out = append(out, Location{Path: path, Range: r})
	}
	return out, nil
}

// DocumentSymbols returns a file's symbol tree.
func (c *Client) DocumentSymbols(ctx context.Context, rel string) ([]Symbol, error) {
	var raw json.RawMessage
	if err := c.call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(filepath.Join(c.root, rel))},
	}, &raw); err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(raw)) == "null" || len(raw) == 0 {
		return nil, nil
	}
	var symbols []Symbol
	if err := json.Unmarshal(raw, &symbols); err == nil && len(symbols) > 0 && symbols[0].Name != "" {
		return symbols, nil
	}
	// The flat SymbolInformation shape, for servers that do not support the
	// hierarchical one.
	var flat []struct {
		Name     string `json:"name"`
		Kind     int    `json:"kind"`
		Location struct {
			Range Range `json:"range"`
		} `json:"location"`
	}
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, fmt.Errorf("lsp: unrecognised documentSymbol result: %w", err)
	}
	out := make([]Symbol, 0, len(flat))
	for _, f := range flat {
		out = append(out, Symbol{Name: f.Name, Kind: f.Kind, Range: f.Location.Range})
	}
	return out, nil
}

// Diagnostics returns what the server has reported for a file so far.
//
// Diagnostics arrive unsolicited, so this reads what has been received rather
// than asking. An empty result means "nothing reported yet", which is not the
// same as "no problems" — the caller's authority on whether code compiles is
// the build, not this.
func (c *Client) Diagnostics(rel string) []Diagnostic {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	return append([]Diagnostic(nil), c.diagnostics[rel]...)
}

// Close shuts the server down, politely first.
//
// It builds its own context rather than taking one. Shutdown runs on the way
// out of a failure as often as a success, and a context that is already done —
// which is the usual reason a caller is unwinding — would skip the polite
// request and leave the kill below as the only path.
func (c *Client) Close() error {
	select {
	case <-c.closed:
		return nil
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var ignored json.RawMessage
	_ = c.call(ctx, "shutdown", nil, &ignored)
	_ = c.notify("exit", nil)
	close(c.closed)
	_ = c.stdin.Close()
	if c.stop != nil {
		defer c.stop()
	}

	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// A language server that ignores shutdown is a known shape of bug and
		// is not worth waiting on: the process holds an index in memory and
		// would otherwise outlive every task in the session.
		_ = c.cmd.Process.Kill()
		<-done
	}
	return nil
}

func (c *Client) call(ctx context.Context, method string, params any, out *json.RawMessage) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan response, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.write(request{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return fmt.Errorf("lsp: %s: %w", method, ctx.Err())
	case <-c.closed:
		return fmt.Errorf("lsp: %s: server exited", method)
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		*out = resp.Result
		return nil
	}
}

func (c *Client) notify(method string, params any) error {
	return c.write(request{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *Client) write(req request) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}

// read is the receive loop: responses go to whoever is waiting, notifications
// are handled here, and anything else is ignored rather than fatal.
func (c *Client) read() {
	for {
		body, err := readMessage(c.out)
		if err != nil {
			c.mu.Lock()
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		var msg response
		if json.Unmarshal(body, &msg) != nil {
			continue
		}
		if msg.ID == nil {
			c.handleNotification(msg)
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[*msg.ID]
		c.mu.Unlock()
		if ok {
			ch <- msg
		}
	}
}

func (c *Client) handleNotification(msg response) {
	if msg.Method != "textDocument/publishDiagnostics" {
		// Progress, log messages and registration requests are not errors and
		// are not this client's business.
		return
	}
	var params struct {
		URI         string       `json:"uri"`
		Diagnostics []Diagnostic `json:"diagnostics"`
	}
	if json.Unmarshal(msg.Params, &params) != nil {
		return
	}
	path, ok := c.relative(params.URI)
	if !ok {
		return
	}
	c.diagMu.Lock()
	c.diagnostics[path] = params.Diagnostics
	c.diagMu.Unlock()
}

// readMessage reads one base-protocol frame.
func readMessage(r *bufio.Reader) ([]byte, error) {
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
		name, value, found := strings.Cut(line, ":")
		if !found || !strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("lsp: bad Content-Length %q", value)
		}
		length = n
	}
	if length < 0 {
		return nil, errors.New("lsp: message without Content-Length")
	}
	// A header claiming a gigabyte is a broken or hostile peer, and allocating
	// for it is the bug rather than the message that follows.
	if length > 64<<20 {
		return nil, fmt.Errorf("lsp: message of %d bytes exceeds the limit", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// relative converts a file URI to a workspace-relative path, reporting false
// for anything outside the root.
func (c *Client) relative(uri string) (string, bool) {
	path, err := uriToPath(uri)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(c.root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func pathToURI(path string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	return u.String()
}

func uriToPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("lsp: not a file URI: %q", uri)
	}
	return filepath.FromSlash(u.Path), nil
}
