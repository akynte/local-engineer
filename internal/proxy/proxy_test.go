package proxy_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akynte/local-engineer/internal/proxy"
)

// The allowlist is the whole security boundary of §6.1's provisioning lane, so
// these are written as attempts to get past it rather than as examples of it
// working.

func list(rules ...proxy.Rule) proxy.Allowlist { return proxy.Allowlist{Rules: rules} }

func TestAllowlistExactMatch(t *testing.T) {
	a := list(proxy.Rule{Host: "proxy.golang.org", Why: "test"})
	if ok, _ := a.Allows(proxy.LaneDeps, "proxy.golang.org"); !ok {
		t.Error("the exact host was refused")
	}
	for _, host := range []string{
		"evil.com",
		"proxy.golang.org.evil.com",   // suffix attack
		"notproxy.golang.org",         // prefix attack
		"golang.org",                  // parent
		"sub.proxy.golang.org",        // child of an exact rule
		"proxy.golang.org:443@evil.c", // junk
	} {
		if ok, rule := a.Allows(proxy.LaneDeps, host); ok {
			t.Errorf("%q was allowed by rule %q", host, rule)
		}
	}
}

func TestAllowlistCaseAndTrailingDotCannotEvade(t *testing.T) {
	a := list(proxy.Rule{Host: "proxy.golang.org", Why: "test"})
	// A trailing dot is a legal FQDN and resolves to the same host, so it must
	// not be a way past an exact-match rule.
	for _, host := range []string{"PROXY.GOLANG.ORG", "Proxy.Golang.Org.", "proxy.golang.org."} {
		if ok, _ := a.Allows(proxy.LaneDeps, host); !ok {
			t.Errorf("%q should match the rule; a spelling difference is not a different host", host)
		}
	}
}

func TestWildcardDoesNotCoverTheApexOrEscapeTheSuffix(t *testing.T) {
	a := list(proxy.Rule{Host: "*.golang.org", Why: "test"})
	if ok, _ := a.Allows(proxy.LaneDeps, "proxy.golang.org"); !ok {
		t.Error("a subdomain was refused by its wildcard")
	}
	// The apex is deliberately NOT covered: a wildcard that silently included
	// it makes every "*.co.uk"-shaped mistake worse.
	if ok, _ := a.Allows(proxy.LaneDeps, "golang.org"); ok {
		t.Error("*.golang.org allowed the apex golang.org")
	}
	for _, host := range []string{"evilgolang.org", "golang.org.evil.com", "xgolang.org"} {
		if ok, _ := a.Allows(proxy.LaneDeps, host); ok {
			t.Errorf("%q escaped the wildcard suffix", host)
		}
	}
}

func TestLaneScoping(t *testing.T) {
	a := list(
		proxy.Rule{Host: "proxy.golang.org", Lanes: []proxy.Lane{proxy.LaneDeps}, Why: "test"},
		proxy.Rule{Host: "pkg.go.dev", Lanes: []proxy.Lane{proxy.LaneDocs}, Why: "test"},
		proxy.Rule{Host: "either.example", Why: "no lanes means every lane"},
	)
	cases := []struct {
		lane proxy.Lane
		host string
		want bool
	}{
		{proxy.LaneDeps, "proxy.golang.org", true},
		{proxy.LaneDocs, "proxy.golang.org", false},
		{proxy.LaneDocs, "pkg.go.dev", true},
		{proxy.LaneDeps, "pkg.go.dev", false},
		{proxy.LaneDeps, "either.example", true},
		{proxy.LaneDocs, "either.example", true},
	}
	for _, c := range cases {
		if ok, _ := a.Allows(c.lane, c.host); ok != c.want {
			t.Errorf("lane %s host %s: allowed=%v want %v", c.lane, c.host, ok, c.want)
		}
	}
}

func TestAllowlistValidationRefusesTheDangerousShapes(t *testing.T) {
	bad := []struct {
		name string
		list proxy.Allowlist
	}{
		{"allow everything", list(proxy.Rule{Host: "*", Why: "no"})},
		{"no host", list(proxy.Rule{Host: "  ", Why: "no"})},
		{"no reason", list(proxy.Rule{Host: "example.com"})},
		{"a URL", list(proxy.Rule{Host: "https://example.com/x", Why: "no"})},
		{"interior wildcard", list(proxy.Rule{Host: "foo.*.com", Why: "no"})},
		{"two wildcards", list(proxy.Rule{Host: "*.*.com", Why: "no"})},
		{"duplicate", list(
			proxy.Rule{Host: "example.com", Why: "a"},
			proxy.Rule{Host: "example.com", Why: "b"})},
		{"unknown lane", list(proxy.Rule{Host: "example.com", Lanes: []proxy.Lane{"tasks"}, Why: "no"})},
	}
	for _, c := range bad {
		if err := c.list.Validate(); err == nil {
			t.Errorf("%s: accepted", c.name)
		} else if !errors.Is(err, proxy.ErrInvalid) {
			t.Errorf("%s: error is not ErrInvalid: %v", c.name, err)
		}
	}
	if err := proxy.DefaultAllowlist().Validate(); err != nil {
		t.Errorf("the shipped default allowlist does not validate: %v", err)
	}
}

// newProxy starts a proxy on a real loopback listener.
func newProxy(t *testing.T, lane proxy.Lane, a proxy.Allowlist, allowPorts ...string) (*url.URL, *[]proxy.Decision) {
	t.Helper()
	var mu sync.Mutex
	var log []proxy.Decision
	s := &proxy.Server{
		Lane:  lane,
		Allow: a,
		Journal: func(d proxy.Decision) {
			mu.Lock()
			defer mu.Unlock()
			log = append(log, d)
		},
		DialTimeout: 5 * time.Second,
		AllowPorts:  allowPorts,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = s.Serve(ctx, ln) }()
	t.Cleanup(func() { cancel(); _ = ln.Close() })

	u, err := url.Parse("http://" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return u, &log
}

func clientVia(u *url.URL) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(u),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // a test server's self-signed cert
		},
	}
}

func TestPlainHTTPThroughTheProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "upstream ok")
	}))
	defer upstream.Close()
	host, _, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))

	pu, log := newProxy(t, proxy.LaneDeps, list(proxy.Rule{Host: host, Why: "test upstream"}))

	// The test upstream is on an ephemeral port, and the proxy only permits
	// 80 and 443 — so this asserts the port rule, which is the point.
	resp, err := clientVia(pu).Get(upstream.URL)
	if err != nil {
		t.Fatalf("request failed outright: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: the proxy must refuse a port that is not 80 or 443", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "not 80 or 443") {
		t.Errorf("the refusal does not say why: %q", body)
	}
	if len(*log) == 0 || (*log)[0].Allowed {
		t.Error("the refusal was not journalled as a denial")
	}
}

func TestDeniedHostIsRefusedAndSaysHowToFixIt(t *testing.T) {
	pu, log := newProxy(t, proxy.LaneDeps, list(proxy.Rule{Host: "allowed.example", Why: "test"}))

	resp, err := clientVia(pu).Get("http://denied.example/x")
	if err != nil {
		t.Fatalf("request failed outright: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// A refusal that does not name the allowlist leaves the operator
	// debugging DNS, which is the failure this proxy most easily causes.
	if !strings.Contains(string(body), "allowlist") {
		t.Errorf("the refusal does not name the allowlist: %q", body)
	}
	if len(*log) != 1 || (*log)[0].Allowed || (*log)[0].Host != "denied.example" {
		t.Errorf("the denial was not journalled with its host: %+v", *log)
	}
}

func TestConnectToADeniedHostIsRefusedBeforeDialing(t *testing.T) {
	// The upstream here does not exist. If the proxy dialled before checking,
	// the failure would be a timeout rather than a refusal — so a prompt 403
	// is the evidence that the allowlist runs first.
	pu, log := newProxy(t, proxy.LaneDeps, list(proxy.Rule{Host: "allowed.example", Why: "test"}))

	_, err := clientVia(pu).Get("https://denied.example/x")
	if err == nil {
		t.Fatal("a CONNECT to a denied host succeeded")
	}
	if len(*log) != 1 || (*log)[0].Allowed {
		t.Fatalf("the CONNECT denial was not journalled: %+v", *log)
	}
	if !strings.Contains((*log)[0].Reason, "allowlist") {
		t.Errorf("reason = %q", (*log)[0].Reason)
	}
}

func TestADocsLaneCannotReachADepsHost(t *testing.T) {
	// The lane comes from the listener, not the request, so a client cannot
	// ask for a lane it was not given.
	a := list(proxy.Rule{Host: "deps.example", Lanes: []proxy.Lane{proxy.LaneDeps}, Why: "test"})
	pu, _ := newProxy(t, proxy.LaneDocs, a)

	resp, err := clientVia(pu).Get("http://deps.example/x")
	if err != nil {
		t.Fatalf("request failed outright: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("the docs lane reached a deps-only host: status %d", resp.StatusCode)
	}
}

func TestANonProxyRequestIsRejected(t *testing.T) {
	pu, _ := newProxy(t, proxy.LaneDeps, proxy.DefaultAllowlist())
	// A direct request with a relative URI: not a proxy request at all.
	resp, err := http.Get(pu.String() + "/somewhere") //nolint:noctx // a test against a local listener
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// The happy paths. A proxy whose refusals are all tested and whose successes
// are not is one that might refuse everything.

func TestAnAllowedPlainHTTPRequestReachesUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "upstream ok")
	}))
	defer upstream.Close()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))

	pu, log := newProxy(t, proxy.LaneDeps, list(proxy.Rule{Host: host, Why: "test upstream"}), port)

	resp, err := clientVia(pu).Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "upstream ok" {
		t.Fatalf("status %d body %q; the allowed request did not reach upstream", resp.StatusCode, body)
	}
	if len(*log) != 1 || !(*log)[0].Allowed || (*log)[0].Rule != host {
		t.Errorf("the allow was not journalled with its rule: %+v", *log)
	}
}

func TestAnAllowedCONNECTTunnelCarriesBytes(t *testing.T) {
	// A real TLS server behind a real CONNECT: this is the shape every package
	// manager actually uses, and the one place the proxy handles raw bytes.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "tunnelled ok")
	}))
	defer upstream.Close()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "https://"))

	pu, log := newProxy(t, proxy.LaneDeps, list(proxy.Rule{Host: host, Why: "test upstream"}), port)

	resp, err := clientVia(pu).Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "tunnelled ok" {
		t.Fatalf("status %d body %q; the tunnel did not carry the exchange", resp.StatusCode, body)
	}
	if len(*log) != 1 || !(*log)[0].Allowed {
		t.Errorf("the tunnel was not journalled as allowed: %+v", *log)
	}
}

func TestTheProxyDoesNotFollowRedirectsOnTheClientsBehalf(t *testing.T) {
	// A redirect to a host outside the allowlist must come back through the
	// allowlist as the client's own request, not be followed inside the proxy
	// where nothing would check it.
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "should not be reachable")
	}))
	defer elsewhere.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/x", http.StatusFound)
	}))
	defer redirector.Close()

	rHost, rPort, _ := net.SplitHostPort(strings.TrimPrefix(redirector.URL, "http://"))
	_, ePort, _ := net.SplitHostPort(strings.TrimPrefix(elsewhere.URL, "http://"))

	// Only the redirector is allowed, and both ports are permitted so the only
	// thing that can stop the second hop is the allowlist.
	pu, log := newProxy(t, proxy.LaneDeps, list(proxy.Rule{Host: rHost, Why: "test"}), rPort, ePort)

	c := clientVia(pu)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Get(redirector.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302 handed back to the client", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "should not be reachable") {
		t.Fatal("the proxy followed the redirect itself, past the allowlist")
	}
	for _, d := range *log {
		if d.Host != rHost {
			t.Errorf("the proxy contacted %s, which the client never asked it to", d.Host)
		}
	}
}

func TestATunnelIsNotCutOffWhileItIsStillProgressing(t *testing.T) {
	// The deadline must be idle time, not total duration. Set once, it would
	// cap the whole tunnel — and a large `go mod download` over a slow link
	// would arrive truncated, which reads as a corrupt module rather than a
	// timeout.
	const idle = 150 * time.Millisecond

	// TLS, so the request goes through CONNECT and therefore through pipe —
	// the plain-HTTP path uses an http.Client and would not exercise this.
	//
	// The upstream trickles: each chunk arrives well inside the idle window,
	// but the whole response takes several times longer than it.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, _ := w.(http.Flusher)
		for i := 0; i < 8; i++ {
			fmt.Fprint(w, "chunk")
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(idle / 3)
		}
	}))
	defer upstream.Close()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "https://"))

	var mu sync.Mutex
	var log []proxy.Decision
	srv := &proxy.Server{
		Lane:        proxy.LaneDeps,
		Allow:       list(proxy.Rule{Host: host, Why: "test upstream"}),
		AllowPorts:  []string{port},
		IdleTimeout: idle,
		Journal: func(d proxy.Decision) {
			mu.Lock()
			defer mu.Unlock()
			log = append(log, d)
		},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln) }()
	defer ln.Close()

	pu, err := url.Parse("http://" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := clientVia(pu).Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("the transfer was interrupted: %v", err)
	}
	if got := string(body); got != strings.Repeat("chunk", 8) {
		t.Fatalf("body = %q; a slow but progressing transfer was truncated", got)
	}
}
