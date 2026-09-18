package retrieval_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/retrieval"
)

func grepRepo(t *testing.T) string {
	t.Helper()
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
	write("risk/order.go", "package risk\n\nfunc Check() error { return nil }\nfunc Checkpoint() {}\n")
	write("risk/pnl.go", "package risk\n\n// Check is called here\nvar _ = Check\n")
	write(".env", "AWS_SECRET=Check\n")
	write("node_modules/dep/index.js", "function Check() {}\n")
	return root
}

func TestGrepFindsMatchesAndSkipsWhatIsNotSource(t *testing.T) {
	root := grepRepo(t)
	res, err := retrieval.Grep(context.Background(), root, "Check", false)
	if errors.Is(err, retrieval.ErrRipgrepMissing) {
		t.Skip("ripgrep is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, h := range res.Hits {
		paths[h.Path] = true
		if h.Line <= 0 || h.Text == "" {
			t.Fatalf("incomplete hit: %+v", h)
		}
	}
	if !paths["risk/order.go"] || !paths["risk/pnl.go"] {
		t.Fatalf("source matches missing: %v", paths)
	}
	// A secret file is not evidence, and a vendored tree answers questions
	// about someone else's code.
	if paths[".env"] {
		t.Fatal("a secret path reached a search result")
	}
	if paths["node_modules/dep/index.js"] {
		t.Fatal("a vendored file reached a search result")
	}
}

// The symbol-shaped route wants Check and not Checkpoint.
func TestGrepWordBoundariesExcludeLongerIdentifiers(t *testing.T) {
	root := grepRepo(t)
	res, err := retrieval.Grep(context.Background(), root, "Check", true)
	if errors.Is(err, retrieval.ErrRipgrepMissing) {
		t.Skip("ripgrep is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range res.Hits {
		if strings.Contains(h.Text, "Checkpoint") && !strings.Contains(h.Text, " Check(") {
			t.Fatalf("a word-boundary search matched a longer identifier: %q", h.Text)
		}
	}
	if len(res.Hits) == 0 {
		t.Fatal("word search found nothing")
	}
}

// Task text reaching ripgrep as a regular expression would make a task about
// `a.*b` search for something else entirely.
func TestGrepTreatsThePatternAsLiteralText(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("literal a.*b here\nand axxb elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := retrieval.Grep(context.Background(), root, "a.*b", false)
	if errors.Is(err, retrieval.ErrRipgrepMissing) {
		t.Skip("ripgrep is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || !strings.Contains(res.Hits[0].Text, "literal") {
		t.Fatalf("the pattern was interpreted rather than matched: %+v", res.Hits)
	}
}

// "No other callers" and "no other callers in the first 200 ms" are different
// answers, and only one is safe to plan against.
func TestGrepReportsWhenACapStoppedIt(t *testing.T) {
	root := t.TempDir()
	for i := range retrieval.GrepFileCap + 10 {
		name := filepath.Join(root, "f"+string(rune('a'+i%26))+string(rune('a'+i/26))+".go")
		if err := os.WriteFile(name, []byte("var Check = 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, err := retrieval.Grep(context.Background(), root, "Check", false)
	if errors.Is(err, retrieval.ErrRipgrepMissing) {
		t.Skip("ripgrep is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || res.Reason == "" {
		t.Fatalf("a capped search did not say so: %+v", res)
	}
}

func TestExpandTermsProducesTheSpellingsSourceActuallyUses(t *testing.T) {
	got := retrieval.ExpandTerms("account limit business logic")
	// The camel, snake and pascal joins of the adjacent words, plus the bare
	// word: those are the four spellings source actually uses.
	want := map[string]bool{"accountLimit": false, "account_limit": false, "AccountLimit": false, "limit": false}
	for _, term := range got {
		if _, ok := want[term]; ok {
			want[term] = true
		}
	}
	for term, found := range want {
		if !found {
			t.Fatalf("%s is how source spells it, and it was not produced: %v", term, got)
		}
	}
	if len(got) > 12 {
		t.Fatalf("expansion is unbounded: %d terms", len(got))
	}
}
