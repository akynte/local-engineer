package gitlog_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/analyzers/gitlog"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// repo builds a real git repository with a known history, because the whole
// point of this analyzer is reading what git actually reports.
func repo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-q", "-b", "main")
	write("a.go", "package a\n\nfunc A() {}\n")
	run("add", "-A")
	run("commit", "-q", "-m", "feat: add A")

	// A second commit touching two files: this is the co-occurrence signal.
	write("a.go", "package a\n\nfunc A() {}\nfunc A2() {}\n")
	write("b.go", "package a\n\nfunc B() {}\n")
	run("add", "-A")
	run("commit", "-q", "-m", "feat: add A2 and B together")

	// A sweeping commit that must be skipped: it says nothing about coupling.
	for i := 0; i < 60; i++ {
		write(filepath.Join("vendored", "f"+string(rune('a'+i%26))+string(rune('a'+i/26))+".go"),
			"package vendored\n")
	}
	run("add", "-A")
	run("commit", "-q", "-m", "chore: vendor drop")

	return dir
}

func analyze(t *testing.T, dir string, files []index.File) index.Result {
	t.Helper()
	a := gitlog.New()
	a.Warnf = func(f string, args ...any) { t.Logf("gitlog: "+f, args...) }
	res, err := a.Analyze(context.Background(), dir, files)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestCommitsTouchIndexedFilesWithObservedEvidence(t *testing.T) {
	dir := repo(t)
	res := analyze(t, dir, []index.File{{Path: "a.go"}, {Path: "b.go"}})

	if len(res.Nodes) == 0 {
		t.Fatal("expected commit nodes")
	}
	for _, n := range res.Nodes {
		if n.Kind != graph.KindCommit {
			t.Errorf("unexpected node kind %s", n.Kind)
		}
		if !strings.HasPrefix(n.FQN, "commit:") {
			t.Errorf("commit fqn should be namespaced, got %s", n.FQN)
		}
		if n.Signature == "" {
			t.Error("a commit node should carry its subject")
		}
	}
	var touches int
	for _, e := range res.Edges {
		if e.Kind != graph.EdgeTouches {
			t.Errorf("unexpected edge kind %s", e.Kind)
		}
		// History is evidence about a relationship, not proof of one.
		if e.Evidence != graph.Observed {
			t.Errorf("a commit edge must be observed, got %s", e.Evidence)
		}
		touches++
	}
	if touches < 3 {
		t.Errorf("expected at least 3 touches edges across two commits, got %d", touches)
	}
}

// A commit touching a large number of files is a reformat or a vendor drop.
// Including it would add a quadratic number of meaningless edges.
func TestSweepingCommitsAreSkipped(t *testing.T) {
	dir := repo(t)
	var files []index.File
	files = append(files, index.File{Path: "a.go"}, index.File{Path: "b.go"})
	for i := 0; i < 60; i++ {
		files = append(files, index.File{
			Path: "vendored/f" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".go",
		})
	}
	res := analyze(t, dir, files)

	for _, n := range res.Nodes {
		if strings.Contains(n.Signature, "vendor drop") {
			t.Error("the sweeping commit should have been skipped")
		}
	}
}

// Only files that are actually indexed get edges: an edge to an unindexed path
// would dangle.
func TestUnindexedFilesGetNoEdges(t *testing.T) {
	dir := repo(t)
	res := analyze(t, dir, []index.File{{Path: "a.go"}})

	for _, e := range res.Edges {
		if e.DstFQN != "a.go" {
			t.Errorf("edge to an unindexed file: %s", e.DstFQN)
		}
	}
}

// A directory that is not a repository is not an error; the rest of the index
// is unaffected.
func TestNonRepositoryIsReportedNotFatal(t *testing.T) {
	dir := t.TempDir()
	a := gitlog.New()
	var warned bool
	a.Warnf = func(string, ...any) { warned = true }

	res, err := a.Analyze(context.Background(), dir, []index.File{{Path: "x.go"}})
	if err != nil {
		t.Fatalf("a non-repository must not fail the index: %v", err)
	}
	if len(res.Nodes) != 0 {
		t.Error("expected no commit nodes outside a repository")
	}
	if !warned {
		t.Error("skipping history must be reported")
	}
}

func TestLogParsesSubjectsContainingSeparators(t *testing.T) {
	dir := repo(t)
	commits, err := gitlog.Log(context.Background(), dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) < 2 {
		t.Fatalf("expected at least 2 commits, got %d", len(commits))
	}
	// Newest first.
	if !strings.Contains(commits[0].Subject, "vendor drop") {
		t.Errorf("expected the newest commit first, got %q", commits[0].Subject)
	}
	for _, c := range commits {
		if len(c.Hash) != 40 {
			t.Errorf("bad hash %q", c.Hash)
		}
		if c.When.IsZero() {
			t.Error("commit time was not parsed")
		}
	}
}

func TestBlameReportsTheCommitsBehindALineRange(t *testing.T) {
	dir := repo(t)
	hashes, err := gitlog.Blame(context.Background(), dir, "a.go", 1, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes) == 0 {
		t.Fatal("expected at least one commit from blame")
	}
	for _, h := range hashes {
		if len(h) != 40 {
			t.Errorf("bad blame hash %q", h)
		}
	}
}
