// Package proxy is the allowlisting egress proxy of design v3 §6.1.
//
// §6.1 gives the `deps` and `docs` lanes "an allowlisting proxy [that] runs
// inside the container and is the only route out for the provisioning lane,
// never for a task sandbox". This is that proxy, and the second half of the
// sentence is the part that shapes the code.
//
// # What it is for
//
// A task sandbox has no egress at all: verification runs with GOPROXY=off, and
// Landlock grants TCP connect to the inference endpoint and assigned test
// ports and nothing else. That is the right posture for running a model's
// edits, but it leaves a real gap — a change that legitimately needs a new
// dependency has no way to acquire one. Provisioning is therefore a separate
// lane with its own, narrower door: a forward proxy that will connect to an
// allowlisted host and refuse everything else.
//
// # What keeps a task out of it
//
// Not authentication. The proxy binds loopback inside the container, and a
// task's Landlock ruleset does not include its port, so a task process cannot
// dial it in the first place. The allowlist bounds where the *provisioning*
// lane can reach; the sandbox bounds who can ask. Confusing the two would be a
// mistake: an allowlist is not an access control.
//
// # Refusals are recorded
//
// Every decision is journalled — host, lane, allowed or denied, and the rule
// that matched. A proxy that silently denies is indistinguishable from a
// network fault, and the operator ends up debugging DNS.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Lane names a provisioning lane. §6.1 names two.
type Lane string

const (
	// LaneDeps fetches dependencies: module proxies, package registries.
	LaneDeps Lane = "deps"
	// LaneDocs fetches documentation for a library the task uses.
	LaneDocs Lane = "docs"
)

// ParseLane validates operator input.
func ParseLane(s string) (Lane, bool) {
	switch Lane(s) {
	case LaneDeps, LaneDocs:
		return Lane(s), true
	}
	return "", false
}

// Rule allows one host pattern for one lane.
//
// A pattern is either an exact host ("proxy.golang.org") or a single leading
// wildcard label ("*.golang.org"). Deliberately no regular expressions: an
// allowlist whose entries need testing is one nobody can read, and the failure
// mode of a too-permissive pattern here is egress from a machine whose whole
// premise is that it has none.
type Rule struct {
	Host string `yaml:"host"`
	// Lanes restricts the rule. Empty means every lane, which is how the
	// shipped defaults express "this host is fine for anything".
	Lanes []Lane `yaml:"lanes,omitempty"`
	// Why is required by Validate. A rule that cannot say why it exists is one
	// nobody can judge later, and the same requirement is on policy rules.
	Why string `yaml:"why"`
}

// Decision is one allow-or-deny, journalled.
type Decision struct {
	At      time.Time `json:"at"`
	Lane    Lane      `json:"lane"`
	Host    string    `json:"host"`
	Port    string    `json:"port"`
	Allowed bool      `json:"allowed"`
	// Rule is the pattern that matched, when one did.
	Rule string `json:"rule,omitempty"`
	// Reason explains a denial in the terms the operator needs to fix it.
	Reason string `json:"reason,omitempty"`
}

// Allowlist decides which hosts a lane may reach.
type Allowlist struct {
	Rules []Rule `yaml:"rules"`
}

// ErrInvalid marks a malformed allowlist.
var ErrInvalid = errors.New("proxy: invalid allowlist")

// Validate rejects a list that would be unsafe or unreadable.
func (a Allowlist) Validate() error {
	seen := map[string]bool{}
	for i, r := range a.Rules {
		h := strings.ToLower(strings.TrimSpace(r.Host))
		switch {
		case h == "":
			return fmt.Errorf("%w: rule %d has no host", ErrInvalid, i+1)
		case h == "*":
			// The one pattern that would turn the allowlist off while looking
			// like configuration.
			return fmt.Errorf("%w: rule %d allows every host; remove the proxy instead of allowing everything", ErrInvalid, i+1)
		case strings.Contains(h, "/"):
			return fmt.Errorf("%w: rule %d (%q) looks like a URL; a host is expected", ErrInvalid, i+1, r.Host)
		case strings.Count(h, "*") > 1:
			return fmt.Errorf("%w: rule %d (%q) has more than one wildcard", ErrInvalid, i+1, r.Host)
		case strings.Contains(h, "*") && !strings.HasPrefix(h, "*."):
			return fmt.Errorf("%w: rule %d (%q): a wildcard must be a leading label, as in *.example.com", ErrInvalid, i+1, r.Host)
		case strings.TrimSpace(r.Why) == "":
			return fmt.Errorf("%w: rule %d (%q) has no `why`", ErrInvalid, i+1, r.Host)
		case seen[h]:
			return fmt.Errorf("%w: rule %d duplicates host %q", ErrInvalid, i+1, r.Host)
		}
		for _, l := range r.Lanes {
			if _, ok := ParseLane(string(l)); !ok {
				return fmt.Errorf("%w: rule %d names unknown lane %q", ErrInvalid, i+1, l)
			}
		}
		seen[h] = true
	}
	return nil
}

// Allows reports whether lane may reach host, and which rule said so.
func (a Allowlist) Allows(lane Lane, host string) (bool, string) {
	h := strings.ToLower(strings.TrimSpace(host))
	// A trailing dot is a legal fully-qualified host and must not be a way
	// past an exact-match rule.
	h = strings.TrimSuffix(h, ".")
	for _, r := range a.Rules {
		if !ruleCoversLane(r, lane) {
			continue
		}
		p := strings.ToLower(strings.TrimSpace(r.Host))
		if strings.HasPrefix(p, "*.") {
			// "*.example.com" covers sub.example.com but NOT example.com
			// itself: a wildcard that silently included the apex would make
			// "*.co.uk"-shaped mistakes worse than they already are.
			if strings.HasSuffix(h, p[1:]) && len(h) > len(p)-1 {
				return true, r.Host
			}
			continue
		}
		if h == p {
			return true, r.Host
		}
	}
	return false, ""
}

func ruleCoversLane(r Rule, lane Lane) bool {
	if len(r.Lanes) == 0 {
		return true
	}
	for _, l := range r.Lanes {
		if l == lane {
			return true
		}
	}
	return false
}

// DefaultAllowlist is what `le config init` writes.
//
// It is short on purpose. Every entry is a host this project's own documented
// workflows reach, and an operator adding to it is making a deliberate choice
// about what their machine talks to.
func DefaultAllowlist() Allowlist {
	return Allowlist{Rules: []Rule{
		{Host: "proxy.golang.org", Lanes: []Lane{LaneDeps}, Why: "the Go module proxy"},
		{Host: "sum.golang.org", Lanes: []Lane{LaneDeps}, Why: "the Go checksum database; without it GONOSUMDB would have to be set"},
		{Host: "registry.npmjs.org", Lanes: []Lane{LaneDeps}, Why: "npm packages for the TypeScript sidecar and Node projects"},
		{Host: "pkg.go.dev", Lanes: []Lane{LaneDocs}, Why: "Go package documentation"},
	}}
}

// Server is the forward proxy.
type Server struct {
	// Allow decides. Required.
	Allow Allowlist
	// Lane is the lane every connection to this listener belongs to. One
	// listener per lane keeps the lane out of the request, where a client
	// could choose it.
	Lane Lane
	// Journal records decisions. Nil discards them, which is only appropriate
	// in tests.
	Journal func(Decision)
	// DialTimeout bounds the upstream connect.
	DialTimeout time.Duration
	// IdleTimeout closes a tunnel that has gone quiet. It is idle time, not
	// total duration: the deadline is extended on every transfer, so a slow
	// but progressing fetch is not cut off.
	IdleTimeout time.Duration
	// AllowPorts are the upstream ports a lane may reach. Empty means the
	// default pair, 80 and 443, which is every port a package manager or a
	// documentation fetch actually needs.
	//
	// This is a field rather than configuration on purpose: `le.yaml` exposes
	// the host allowlist and not this, because widening the port set turns a
	// host allowlist into a general tunnel, and that should not be one line of
	// YAML away. Tests set it to reach a local server on an ephemeral port.
	AllowPorts []string

	mu    sync.Mutex
	conns int
}

// MaxConcurrent bounds tunnels so a runaway client cannot exhaust the process.
const MaxConcurrent = 32

// Handler returns the proxy's HTTP handler.
//
// Both proxy shapes are served: CONNECT for TLS, which is what every package
// manager actually uses, and absolute-URI GET for plain HTTP, which some
// tooling still emits. Plain HTTP is allowlisted identically and is not
// upgraded or rewritten — a proxy that changed a request would be a second
// place for a bug to live.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			s.handleConnect(w, r)
			return
		}
		s.handleHTTP(w, r)
	})
}

func (s *Server) record(d Decision) {
	d.At = time.Now().UTC()
	d.Lane = s.Lane
	if s.Journal != nil {
		s.Journal(d)
	}
}

// check resolves the host and applies the allowlist.
func (s *Server) check(hostport string, defaultPort string) (host, port string, ok bool, rule string, reason string) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, defaultPort
	}
	if host == "" {
		return "", "", false, "", "no host in the request"
	}
	// Only the ports a package manager or documentation fetch actually needs.
	// Allowing arbitrary ports would make the allowlist a host list and the
	// proxy a general tunnel.
	if !s.portAllowed(port) {
		return host, port, false, "", fmt.Sprintf("port %s is not %s", port, strings.Join(s.ports(), " or "))
	}
	allowed, rule := s.Allow.Allows(s.Lane, host)
	if !allowed {
		return host, port, false, "", fmt.Sprintf(
			"%s is not in the %s lane's allowlist; add it to egress.allowlist in le.yaml with a reason", host, s.Lane)
	}
	return host, port, true, rule, ""
}

func (s *Server) ports() []string {
	if len(s.AllowPorts) == 0 {
		return []string{"80", "443"}
	}
	return s.AllowPorts
}

func (s *Server) portAllowed(port string) bool {
	for _, p := range s.ports() {
		if p == port {
			return true
		}
	}
	return false
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	host, port, ok, rule, reason := s.check(r.Host, "443")
	if !ok {
		s.record(Decision{Host: host, Port: port, Allowed: false, Reason: reason})
		http.Error(w, "egress refused: "+reason, http.StatusForbidden)
		return
	}

	if !s.acquire() {
		s.record(Decision{Host: host, Port: port, Allowed: false, Rule: rule,
			Reason: "too many concurrent tunnels"})
		http.Error(w, "egress refused: too many concurrent tunnels", http.StatusServiceUnavailable)
		return
	}
	defer s.release()

	upstream, err := s.dial(r.Context(), net.JoinHostPort(host, port))
	if err != nil {
		s.record(Decision{Host: host, Port: port, Allowed: true, Rule: rule,
			Reason: "allowed, but the upstream connect failed: " + err.Error()})
		http.Error(w, "egress: upstream connect failed", http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	s.record(Decision{Host: host, Port: port, Allowed: true, Rule: rule})

	// Hijack rather than use http.ResponseController: a CONNECT tunnel is
	// bytes in both directions with no framing the proxy understands, and
	// that opacity is deliberate — see the package comment.
	hj, canHijack := w.(http.Hijacker)
	if !canHijack {
		http.Error(w, "egress: the server cannot tunnel", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	// Anything the client pipelined after CONNECT is already in the reader.
	if buf != nil && buf.Reader.Buffered() > 0 {
		if _, err := io.CopyN(upstream, buf, int64(buf.Reader.Buffered())); err != nil {
			return
		}
	}
	s.pipe(client, upstream)
}

func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if !r.URL.IsAbs() {
		http.Error(w, "egress: this is a forward proxy; send an absolute URI or CONNECT", http.StatusBadRequest)
		return
	}
	host, port, ok, rule, reason := s.check(r.URL.Host, "80")
	if !ok {
		s.record(Decision{Host: host, Port: port, Allowed: false, Reason: reason})
		http.Error(w, "egress refused: "+reason, http.StatusForbidden)
		return
	}
	s.record(Decision{Host: host, Port: port, Allowed: true, Rule: rule})

	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	// Hop-by-hop headers must not be forwarded.
	for _, h := range []string{"Proxy-Connection", "Proxy-Authenticate", "Proxy-Authorization",
		"Connection", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		outReq.Header.Del(h)
	}

	client := &http.Client{
		Timeout: 2 * time.Minute,
		// The proxy does not follow redirects on the client's behalf: a
		// redirect to a host outside the allowlist must be the client's
		// request to make, so it comes back through the allowlist check.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// gosec flags this as SSRF, and it is: a forward proxy exists to make a
	// request to a host the client named. What bounds it is the allowlist
	// check above — the host is approved before this line is reached, the
	// port is 80 or 443, and redirects are handed back rather than followed.
	// A proxy that refused to proxy would not be one.
	resp, err := client.Do(outReq) //nolint:gosec // allowlisted above; see the comment
	if err != nil {
		http.Error(w, "egress: upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (s *Server) dial(ctx context.Context, addr string) (net.Conn, error) {
	timeout := s.DialTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	d := net.Dialer{Timeout: timeout}
	return d.DialContext(ctx, "tcp", addr)
}

func (s *Server) acquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns >= MaxConcurrent {
		return false
	}
	s.conns++
	return true
}

func (s *Server) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns--
}

// pipe copies in both directions until either side closes.
//
// The deadline is extended on every transfer rather than set once. Setting it
// once would make IdleTimeout an absolute cap on the whole tunnel, so a large
// `go mod download` over a slow link would be cut off mid-transfer — a
// truncated fetch that looks like a corrupt module rather than a timeout. What
// should end a tunnel is silence, not duration.
func (s *Server) pipe(a, b net.Conn) {
	idle := s.IdleTimeout
	if idle <= 0 {
		idle = 5 * time.Minute
	}

	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			_ = src.SetReadDeadline(time.Now().Add(idle))
			n, rerr := src.Read(buf)
			if n > 0 {
				_ = dst.SetWriteDeadline(time.Now().Add(idle))
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if rerr != nil {
				break
			}
		}
		// Half-close so the peer sees EOF rather than waiting for the idle
		// deadline, which would make every finished fetch cost the timeout.
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}

// Serve runs the proxy until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 20 * time.Second,
	}
	// Shutdown gets a fresh context on purpose: it runs *because* ctx was
	// cancelled, so inheriting it would cancel the graceful drain at the one
	// moment it is needed. This is the same reasoning as Store.Close.
	//nolint:gosec,contextcheck // a graceful drain must outlive the cancelled context that triggered it
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return ctx.Err()
	}
	return err
}

// Listen opens a loopback listener for a lane.
//
// Loopback is not configurable. §6.1 puts the proxy inside the container as
// the provisioning lane's only route out; a proxy bound to an interface the
// host can reach would be an open relay for whatever else is on that network.
func Listen(ctx context.Context, port int) (net.Listener, error) {
	var lc net.ListenConfig
	return lc.Listen(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
}
