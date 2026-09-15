package proxy_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/proxy"
	"github.com/akynte/local-engineer/internal/sandbox"
)

// The lane's confinement is the other half of §6.1's guarantee. The proxy
// bounds where provisioning can reach; the sandbox spec bounds what can ask.

func TestALaneGrantsTheProxyPortAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	spec := proxy.Spec{
		Lane:      proxy.LaneDeps,
		ProxyPort: 7780,
		Dir:       dir,
		TmpDir:    filepath.Join(dir, "tmp"),
	}
	sb, err := spec.SandboxSpec()
	if err != nil {
		t.Fatal(err)
	}
	if len(sb.TCPConnect) != 1 || sb.TCPConnect[0] != 7780 {
		t.Fatalf("TCPConnect = %v; a lane may dial the proxy and nothing else", sb.TCPConnect)
	}
	// Not the inference port: a dependency fetch has no business talking to
	// the model, and granting it "because the task spec does" is how a lane
	// quietly becomes a task.
	if len(sb.TCPBind) != 0 {
		t.Errorf("TCPBind = %v; a provisioning lane listens for nothing", sb.TCPBind)
	}

	// /dev/null and friends. Without them a fetch fails in a way that points
	// at the tool rather than at the sandbox — curl reports "Failure writing
	// output to destination", which says nothing about a missing grant.
	var hasNull bool
	for _, p := range sb.ReadWrite {
		if p == "/dev/null" {
			hasNull = true
		}
	}
	if !hasNull {
		t.Errorf("the lane cannot write /dev/null; granted: %v", sb.ReadWrite)
	}
}

func TestALaneRefusesASpecThatWouldConfineNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		spec proxy.Spec
	}{
		{"no lane", proxy.Spec{ProxyPort: 7780, Dir: "/tmp"}},
		{"a task is not a lane", proxy.Spec{Lane: "task", ProxyPort: 7780, Dir: "/tmp"}},
		{"no proxy port", proxy.Spec{Lane: proxy.LaneDeps, Dir: "/tmp"}},
		{"port out of range", proxy.Spec{Lane: proxy.LaneDeps, ProxyPort: 70000, Dir: "/tmp"}},
		{"no directory", proxy.Spec{Lane: proxy.LaneDeps, ProxyPort: 7780}},
	} {
		if _, err := c.spec.SandboxSpec(); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

func TestLaneEnvPointsToolsAtTheProxy(t *testing.T) {
	spec := proxy.Spec{Lane: proxy.LaneDeps, ProxyPort: 7780, Dir: "/tmp"}
	env := strings.Join(spec.ProxyEnv(), "\n")
	for _, want := range []string{
		"HTTPS_PROXY=http://127.0.0.1:7780",
		"https_proxy=http://127.0.0.1:7780",
		"NO_PROXY=127.0.0.1,localhost,::1",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("the lane environment lacks %q:\n%s", want, env)
		}
	}
	// The task sandbox's GOPROXY=off must not leak in and make the lane
	// useless in a way that looks like a network fault.
	if strings.Contains(env, "GOPROXY=off") {
		t.Error("the lane inherited GOPROXY=off, which is the task sandbox's rule, not the lane's")
	}
}

func TestARunnerWithoutASandboxIsRefused(t *testing.T) {
	// A provisioning lane that ran unconfined would be a shell with network
	// access, which is the one thing §6.1 is arranged to prevent.
	r := &proxy.LaneRunner{}
	_, err := r.Run(context.Background(), proxy.Spec{
		Lane: proxy.LaneDeps, ProxyPort: 7780, Dir: t.TempDir(),
	}, "true")
	if err == nil {
		t.Fatal("an unconfined provisioning lane was allowed to run")
	}
	if !strings.Contains(err.Error(), "confined") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

func TestALaneRunsAndReportsItsExit(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &proxy.LaneRunner{Sandbox: sandbox.ContainerRunner{}}
	res, err := r.Run(context.Background(), proxy.Spec{
		Lane: proxy.LaneDeps, ProxyPort: 7780, Dir: dir, TmpDir: tmp,
	}, "sh", "-c", "echo fetched; exit 3")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 {
		t.Errorf("exit = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "fetched") {
		t.Errorf("stdout not captured: %q", res.Stdout)
	}
	if res.Lane != proxy.LaneDeps {
		t.Errorf("lane = %q", res.Lane)
	}
}
