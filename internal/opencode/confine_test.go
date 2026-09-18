package opencode_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/opencode"
	"github.com/akynte/local-engineer/internal/sandbox"
)

func session(t *testing.T) (opencode.Session, string) {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"repo", "state", "tmp", "install/bin"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(dir, "install", "bin", "opencode")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return opencode.Session{
		Binary:   binary,
		Repo:     filepath.Join(dir, "repo"),
		StateDir: filepath.Join(dir, "state"),
		TmpDir:   filepath.Join(dir, "tmp"),
	}, dir
}

// The worktree is the only repository path a session may write. A spec that
// granted the state directory but not the worktree, or the other way round,
// would fail in a way that reads as an OpenCode bug rather than a policy one.
func TestConfineGrantsTheWorktreeStateAndTmpAndNothingElse(t *testing.T) {
	s, dir := session(t)
	spec, err := s.Confine(sandbox.Spec{ReadOnly: []string{"/usr/lib/go"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{s.Repo, s.StateDir, s.TmpDir} {
		if !slices.Contains(spec.ReadWrite, want) {
			t.Fatalf("writable set %v is missing %s", spec.ReadWrite, want)
		}
	}
	// Beyond those three, only device nodes. Anything else in the writable set
	// is a directory the session was never meant to be able to change.
	for _, granted := range spec.ReadWrite {
		switch granted {
		case s.Repo, s.StateDir, s.TmpDir:
		default:
			if !strings.HasPrefix(granted, "/dev/") {
				t.Fatalf("writable set grants %s, which the session does not need", granted)
			}
		}
	}
	if spec.Dir != s.Repo {
		t.Fatalf("session starts in %s, not the worktree", spec.Dir)
	}
	if !slices.Contains(spec.ReadOnly, "/usr/lib/go") {
		t.Fatalf("operator read-only paths were dropped: %v", spec.ReadOnly)
	}
	// An interpreted launcher loads the tree beside its bin/, so granting only
	// the directory holding the file is the exec failure recipe.Runner
	// explains as exit 126.
	if !slices.Contains(spec.ReadOnly, filepath.Join(dir, "install")) {
		t.Fatalf("install root is not readable, so the exec would fail: %v", spec.ReadOnly)
	}
}

func TestConfineRejectsAnIncompleteSession(t *testing.T) {
	if _, err := (opencode.Session{Repo: "/tmp/repo"}).Confine(sandbox.Spec{}); err == nil {
		t.Fatal("a session without a binary or state directory was accepted")
	}
}

// The environment is the hole a path sandbox cannot close: a token exported
// into the developer's shell is not a file, so no mount rule hides it.
func TestEnvIsBuiltFromNothingAndRedirectsTheHome(t *testing.T) {
	s, _ := session(t)
	t.Setenv("AWS_SESSION_TOKEN", "a-real-token")
	t.Setenv("SSH_AUTH_SOCK", "/run/user/1000/keyring/ssh")
	t.Setenv("TERM", "xterm-256color")

	env := s.Env()
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if name == "AWS_SESSION_TOKEN" || name == "SSH_AUTH_SOCK" {
			t.Fatalf("%s reached the session", name)
		}
		if strings.Contains(value, "a-real-token") {
			t.Fatalf("a credential reached the session through %s", name)
		}
	}
	want := map[string]string{
		"HOME":            s.StateDir,
		"XDG_CONFIG_HOME": filepath.Join(s.StateDir, "config"),
		"XDG_DATA_HOME":   filepath.Join(s.StateDir, "data"),
		"TMPDIR":          s.TmpDir,
		"TERM":            "xterm-256color",
	}
	got := map[string]string{}
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		got[name] = value
	}
	for name, value := range want {
		if got[name] != value {
			t.Fatalf("%s is %q, want %q", name, got[name], value)
		}
	}
}

// Two workspaces sharing a session history would leak one repository's work
// into another's prompts, which is the isolation §13 exists to give.
func TestEnvSeparatesWorkspaces(t *testing.T) {
	a, _ := session(t)
	b, _ := session(t)
	if a.Env()[0] == b.Env()[0] {
		t.Fatal("two workspaces share a home directory")
	}
}

func TestRegisterAgentDeniesTheShellAndKeepsTheDeveloperConfig(t *testing.T) {
	repo := t.TempDir()
	existing := `{"model":"local/qwen","agent":{"mine":{"mode":"primary"}}}`
	path := filepath.Join(repo, "opencode.json")
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := opencode.RegisterAgent(repo); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}

	var doc struct {
		Model string `json:"model"`
		Agent map[string]struct {
			Mode       string            `json:"mode"`
			Permission map[string]string `json:"permission"`
		} `json:"agent"`
	}
	body, err := os.ReadFile(path) //nolint:gosec // a path this test wrote
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Model != "local/qwen" {
		t.Fatalf("the developer's model choice was lost: %q", doc.Model)
	}
	if _, ok := doc.Agent["mine"]; !ok {
		t.Fatal("the developer's own agent was dropped")
	}
	agent, ok := doc.Agent[opencode.AgentName]
	if !ok {
		t.Fatalf("%s was not registered", opencode.AgentName)
	}
	if agent.Mode != "primary" {
		t.Fatalf("agent mode is %q; it has to be selectable as the session's agent", agent.Mode)
	}
	// §9 allows no free-form shell in the loop, and §18 no subagents on one
	// GPU. A future schema change that renames these is a thing to notice.
	for _, tool := range []string{"bash", "webfetch", "websearch", "task", "external_directory"} {
		if agent.Permission[tool] != "deny" {
			t.Fatalf("%s is %q, want deny", tool, agent.Permission[tool])
		}
	}
	// The built-in file tools are denied so the firewall-proxied ones are the
	// only route: the sandbox bounds the session to the worktree, and a
	// committed .env or a generated file is inside the worktree too.
	for _, tool := range []string{"read", "edit"} {
		if agent.Permission[tool] != "deny" {
			t.Fatalf("%s is %q; it would bypass the path policy", tool, agent.Permission[tool])
		}
	}
	// Finding files is not reading them, and the sandbox already bounds where
	// a glob can look.
	for _, tool := range []string{"glob", "grep", "list"} {
		if agent.Permission[tool] != "allow" {
			t.Fatalf("%s is %q; the session cannot navigate without it", tool, agent.Permission[tool])
		}
	}

	// Re-registering an unchanged agent must not rewrite a committed file.
	if _, changed, err := opencode.RegisterAgent(repo); err != nil || changed {
		t.Fatalf("second registration rewrote the file: changed=%v err=%v", changed, err)
	}
}

func TestDeniedSummaryNamesEveryRefusalWithItsReason(t *testing.T) {
	summary := opencode.DeniedSummary()
	for _, tool := range []string{"bash", "webfetch", "websearch", "task", "external_directory"} {
		if !strings.Contains(summary, tool) {
			t.Fatalf("%s is refused but not reported: %s", tool, summary)
		}
	}
	if got, want := strings.Count(summary, "\n"), 7; got != want {
		t.Fatalf("summary is %d lines, want one per refusal (%d): %q", got, want, summary)
	}
}

// Without a model the editor opens with the tools registered, the agent
// defined, and nothing to talk to — it asks the developer to sign in to a cloud
// provider. The endpoint is already known, so asking them to transcribe it into
// a second file is asking them to get it wrong.
func TestRegisterModelPointsTheEditorAtTheSupervisorsEndpoint(t *testing.T) {
	repo := t.TempDir()
	if _, changed, err := opencode.RegisterModel(repo, "http://127.0.0.1:8080",
		"/home/someone/models/Ternary-Bonsai-2-27B-PTQ1_0.gguf"); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var doc struct {
		Model    string `json:"model"`
		Provider map[string]struct {
			NPM     string            `json:"npm"`
			Options map[string]string `json:"options"`
			Models  map[string]struct {
				Name string `json:"name"`
			} `json:"models"`
		} `json:"provider"`
	}
	body, err := os.ReadFile(filepath.Join(repo, "opencode.json")) //nolint:gosec // written above
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	p, ok := doc.Provider[opencode.ProviderName]
	if !ok {
		t.Fatalf("no provider registered: %s", body)
	}
	if p.Options["baseURL"] != "http://127.0.0.1:8080/v1" {
		t.Fatalf("baseURL is %q", p.Options["baseURL"])
	}
	if p.NPM != "@ai-sdk/openai-compatible" {
		t.Fatalf("adapter is %q", p.NPM)
	}
	// opencode.json is committed. A local GGUF path in it would put one
	// developer's filesystem into a file everyone else clones.
	if strings.Contains(string(body), "/home/someone") {
		t.Fatalf("a local filesystem path reached the committed config: %s", body)
	}
	if doc.Model != opencode.ProviderName+"/Ternary-Bonsai-2-27B-PTQ1_0" {
		t.Fatalf("selected model is %q", doc.Model)
	}
}

// A developer who has chosen a model has made a decision. Replacing it with a
// local endpoint would be the kind of helpfulness that loses configuration.
func TestRegisterModelLeavesAnExistingChoiceAlone(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "opencode.json")
	if err := os.WriteFile(path, []byte(`{"model":"anthropic/claude-opus-5"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := opencode.RegisterModel(repo, "http://127.0.0.1:8080", "local.gguf"); err != nil || changed {
		t.Fatalf("an existing model choice was overwritten: changed=%v err=%v", changed, err)
	}
}

// No endpoint configured is a supported state, not an error: the developer may
// intend to pick a model inside the editor.
func TestRegisterModelWithNoEndpointDoesNothing(t *testing.T) {
	repo := t.TempDir()
	if _, changed, err := opencode.RegisterModel(repo, "", ""); err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "opencode.json")); !os.IsNotExist(err) {
		t.Fatal("a config was written for a workspace with no endpoint")
	}
}
