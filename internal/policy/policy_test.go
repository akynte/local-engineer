package policy_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/akynte/local-engineer/internal/policy"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const good = `name: test
protected:
  - path: .github/workflows/**
    reason: these decide whether a change is accepted
  - path: policies/**
    reason: a task that can edit the policy can remove the rule stopping it
`

// The gap this closes: a task that declares no scope is unrestricted by scope,
// so protection has to be independent of it.
func TestProtectionIsIndependentOfScope(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "p.yaml", good)
	set, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	v := set.Check([]string{".github/workflows/ci.yml", "internal/a/a.go"})
	if len(v) != 1 {
		t.Fatalf("got %d violations, want 1: %+v", len(v), v)
	}
	if v[0].Path != ".github/workflows/ci.yml" {
		t.Errorf("wrong path flagged: %s", v[0].Path)
	}
	// The reason is what an operator reads at the gate.
	if v[0].Reason == "" {
		t.Error("the violation carries no reason")
	}
}

// `a/**` must protect the whole subtree. path.Match's * never crosses a
// separator, so a pattern written the obvious way would protect one level and
// silently miss everything deeper — the failure nobody notices until it matters.
func TestSubtreePatternsReachAllTheWayDown(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "p.yaml", good)
	set, _ := policy.Load(dir)

	for _, p := range []string{
		".github/workflows/ci.yml",
		".github/workflows/nested/deep/thing.yml",
	} {
		if len(set.Check([]string{p})) == 0 {
			t.Errorf("%s was not protected by the subtree pattern", p)
		}
	}
}

// A bare directory name means its contents, which is what an operator writing
// `policies` rather than `policies/**` intends.
func TestBareDirectoryNameProtectsContents(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "p.yaml", "name: t\nprotected:\n  - path: secrets\n    reason: obvious\n")
	set, _ := policy.Load(dir)
	if len(set.Check([]string{"secrets/key.pem"})) == 0 {
		t.Error("a bare directory name did not protect its contents")
	}
}

// A rule that cannot say why it exists is one nobody can judge, and the reason
// is the only part a person reads when deciding whether to allow the change.
func TestRuleWithoutReasonIsRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "p.yaml", "name: t\nprotected:\n  - path: a/**\n")
	_, err := policy.Load(dir)
	if !errors.Is(err, policy.ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
}

// A policy that protects nothing is one nobody will notice is broken.
func TestEmptyPolicyIsRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "p.yaml", "name: t\nprotected: []\n")
	if _, err := policy.Load(dir); !errors.Is(err, policy.ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
}

// No policies is the ordinary case for a fresh checkout, not an error.
func TestMissingDirectoryIsNotAnError(t *testing.T) {
	set, err := policy.Load(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("a repository with no policies reported an error: %v", err)
	}
	if len(set.Check([]string{"anything"})) != 0 {
		t.Error("an empty set protected something")
	}
}

// The shipped policy must load and must protect the things it claims to.
func TestShippedPolicyProtectsWhatItSays(t *testing.T) {
	dir := shippedPolicyDir(t)
	set, err := policy.Load(dir)
	if err != nil {
		t.Fatalf("the shipped policy does not load: %v", err)
	}
	for _, p := range []string{
		".github/workflows/ci.yml",
		"policies/protected-paths.yaml",
		".le/workspace.yaml",
		"semgrep/project-invariants.yaml",
		"schemas/le.schema.json",
	} {
		if len(set.Check([]string{p})) == 0 {
			t.Errorf("%s is not protected by the shipped policy", p)
		}
	}
	// And ordinary source is not protected, or every task would be gated.
	if v := set.Check([]string{"internal/task/runner.go"}); len(v) != 0 {
		t.Errorf("ordinary source was flagged: %+v", v)
	}
}

func shippedPolicyDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		p := filepath.Join(dir, "policies")
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("policies/ not found")
	return ""
}
