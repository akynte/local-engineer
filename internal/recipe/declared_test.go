package recipe_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/recipe"
)

func writeDecl(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".le"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, recipe.DeclaredFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNoDeclarationIsOrdinary(t *testing.T) {
	dir := t.TempDir()
	if _, err := recipe.LoadDeclared(dir); err == nil {
		t.Fatal("a missing file was not reported")
	}
	if recipe.HasDeclaredVerification(dir) {
		t.Error("a repository with no declaration was treated as having one")
	}
	if got := recipe.DeclaredRecipes(recipe.High, dir, "le"); len(got) != 0 {
		t.Errorf("got %d recipes from a repository that declared none", len(got))
	}
}

func TestDeclarationValidationRefusesTheUnsafeShapes(t *testing.T) {
	bad := map[string]string{
		"wrong version":              "version: 2\n",
		"integration without a test": "version: 1\nintegration:\n  - name: db\n    up: [\"true\"]\n",
		"integration without a name": "version: 1\nintegration:\n  - test: [\"true\"]\n",
		"generate without outputs":   "version: 1\ngenerate:\n  - name: sqlc\n    argv: [\"true\"]\n",
		"generate without argv":      "version: 1\ngenerate:\n  - name: sqlc\n    outputs: [\"gen\"]\n",
		// An output that escapes the worktree would let the restore step
		// overwrite something the task was never allowed to touch.
		"output escapes":        "version: 1\ngenerate:\n  - name: x\n    argv: [\"true\"]\n    outputs: [\"../outside\"]\n",
		"output absolute":       "version: 1\ngenerate:\n  - name: x\n    argv: [\"true\"]\n    outputs: [\"/etc/passwd\"]\n",
		"timeout above the cap": "version: 1\nintegration:\n  - name: db\n    test: [\"true\"]\n    timeout_minutes: 600\n",
		"duplicate names": "version: 1\ngenerate:\n" +
			"  - name: x\n    argv: [\"true\"]\n    outputs: [\"a\"]\n" +
			"  - name: x\n    argv: [\"true\"]\n    outputs: [\"b\"]\n",
	}
	for name, body := range bad {
		dir := t.TempDir()
		writeDecl(t, dir, body)
		if _, err := recipe.LoadDeclared(dir); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestGenerateCheckPassesWhenTheOutputIsCurrent(t *testing.T) {
	dir := t.TempDir()
	// A "generator" that writes exactly what is already there.
	gen := filepath.Join(dir, "gen.sh")
	if err := os.WriteFile(gen, []byte("#!/bin/sh\nprintf 'generated\\n' > out.txt\n"), 0o755); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "out.txt"), []byte("generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeDecl(t, dir, "version: 1\ngenerate:\n  - name: fixture\n    argv: [\"./gen.sh\"]\n    outputs: [\"out.txt\"]\n")

	res, err := recipe.RunDeclared(context.Background(), dir, "generate", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != string(recipe.Pass) {
		t.Fatalf("status = %q (%s); the committed output matches the generator", res.Status, res.Headline)
	}
}

func TestGenerateCheckFailsOnStaleOutputAndLeavesTheWorktreeAlone(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "gen.sh")
	if err := os.WriteFile(gen, []byte("#!/bin/sh\nprintf 'fresh\\n' > out.txt\n"), 0o755); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	// The committed output is stale: someone hand-edited it, or a schema moved.
	if err := os.WriteFile(filepath.Join(dir, "out.txt"), []byte("hand-edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeDecl(t, dir, "version: 1\ngenerate:\n  - name: fixture\n    argv: [\"./gen.sh\"]\n    outputs: [\"out.txt\"]\n")

	res, err := recipe.RunDeclared(context.Background(), dir, "generate", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != string(recipe.Fail) {
		t.Fatalf("status = %q, want fail: the committed output is not what the generator produces", res.Status)
	}
	if len(res.Changed) != 1 || !strings.Contains(res.Changed[0], "out.txt") {
		t.Errorf("the changed file was not reported: %v", res.Changed)
	}

	// The restore is the property that makes this safe to run inside
	// verification. A generator that left its output behind would make every
	// result recorded before it evidence about a candidate that no longer
	// exists (§7.2), and would put generator output in the model's diff.
	body, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hand-edited\n" {
		t.Fatalf("the worktree was left mutated: out.txt is now %q", body)
	}
}

func TestAGeneratorThatCreatesAFileLeavesNoneBehind(t *testing.T) {
	// The case the first restore missed. Rewriting what changed is the easy
	// half; removing what the generator *created* is the half that decides
	// whether this is safe inside verification — and a schema gaining a table
	// is exactly when a generator adds a file.
	dir := t.TempDir()
	gen := filepath.Join(dir, "gen.sh")
	if err := os.WriteFile(gen, []byte("#!/bin/sh\nmkdir -p gen\nprintf 'new\\n' > gen/added.go\n"), 0o755); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "gen"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDecl(t, dir, "version: 1\ngenerate:\n  - name: fixture\n    argv: [\"./gen.sh\"]\n    outputs: [\"gen\"]\n")

	res, err := recipe.RunDeclared(context.Background(), dir, "generate", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != string(recipe.Fail) {
		t.Fatalf("status = %q, want fail: the generator produces a file the repository does not have", res.Status)
	}
	if len(res.Changed) != 1 || !strings.Contains(res.Changed[0], "would be created") {
		t.Errorf("the new file was not reported as created: %v", res.Changed)
	}
	if _, err := os.Stat(filepath.Join(dir, "gen", "added.go")); !os.IsNotExist(err) {
		t.Fatal("the generated file was left in the worktree; every earlier result is now evidence " +
			"about a candidate that no longer exists, and the file lands in the model's diff")
	}
}

func TestRestorePutsBackTheOriginalMode(t *testing.T) {
	// Restoring content but not permissions turns a generated script into a
	// non-executable file — a mutation, just a quieter one.
	dir := t.TempDir()
	gen := filepath.Join(dir, "gen.sh")
	if err := os.WriteFile(gen, []byte("#!/bin/sh\nchmod 0644 out.sh\n"), 0o755); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "out.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // the fixture under test is executable on purpose
		t.Fatal(err)
	}
	writeDecl(t, dir, "version: 1\ngenerate:\n  - name: fixture\n    argv: [\"./gen.sh\"]\n    outputs: [\"out.sh\"]\n")

	if _, err := recipe.RunDeclared(context.Background(), dir, "generate", "fixture"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "out.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755: the worktree was left changed", info.Mode().Perm())
	}
}

func TestAGeneratorThatCannotRunIsAnErrorNotAFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "out.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeDecl(t, dir, "version: 1\ngenerate:\n  - name: missing\n    argv: [\"definitely-not-installed\"]\n    outputs: [\"out.txt\"]\n")

	res, err := recipe.RunDeclared(context.Background(), dir, "generate", "missing")
	if err != nil {
		t.Fatal(err)
	}
	// A generator that is not installed says nothing about whether the
	// committed output is current.
	if res.Status != string(recipe.Error) {
		t.Fatalf("status = %q, want error", res.Status)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "out.txt"))
	if string(body) != "x\n" {
		t.Errorf("the worktree was disturbed by a generator that never ran: %q", body)
	}
}

func TestIntegrationRunsUpTestDownInOrder(t *testing.T) {
	dir := t.TempDir()
	writeDecl(t, dir, "version: 1\nintegration:\n"+
		"  - name: fixture\n"+
		"    up: [\"sh\", \"-c\", \"echo up >> trace\"]\n"+
		"    test: [\"sh\", \"-c\", \"echo test >> trace\"]\n"+
		"    down: [\"sh\", \"-c\", \"echo down >> trace\"]\n")

	res, err := recipe.RunDeclared(context.Background(), dir, "integration", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != string(recipe.Pass) {
		t.Fatalf("status = %q (%s)", res.Status, res.Headline)
	}
	trace, err := os.ReadFile(filepath.Join(dir, "trace"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(trace)); len(got) != 3 || got[0] != "up" || got[1] != "test" || got[2] != "down" {
		t.Errorf("phases ran as %v, want up test down", got)
	}
}

func TestTeardownRunsEvenWhenTheTestFails(t *testing.T) {
	// Leaving a database or a container behind turns one failure into every
	// later run failing for a different reason.
	dir := t.TempDir()
	writeDecl(t, dir, "version: 1\nintegration:\n"+
		"  - name: fixture\n"+
		"    up: [\"sh\", \"-c\", \"echo up >> trace\"]\n"+
		"    test: [\"sh\", \"-c\", \"echo test >> trace; exit 1\"]\n"+
		"    down: [\"sh\", \"-c\", \"echo down >> trace\"]\n")

	res, err := recipe.RunDeclared(context.Background(), dir, "integration", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != string(recipe.Fail) {
		t.Fatalf("status = %q, want fail", res.Status)
	}
	trace, _ := os.ReadFile(filepath.Join(dir, "trace"))
	if !strings.Contains(string(trace), "down") {
		t.Fatalf("teardown did not run after a failing test: %q", trace)
	}
	if res.Teardown != "down ok" {
		t.Errorf("teardown = %q", res.Teardown)
	}
}

func TestAFailingUpIsAnErrorNotAFailure(t *testing.T) {
	// A database that would not start says nothing about the change.
	dir := t.TempDir()
	writeDecl(t, dir, "version: 1\nintegration:\n"+
		"  - name: fixture\n"+
		"    up: [\"sh\", \"-c\", \"exit 7\"]\n"+
		"    test: [\"sh\", \"-c\", \"echo test >> trace\"]\n"+
		"    down: [\"sh\", \"-c\", \"echo down >> trace\"]\n")

	res, err := recipe.RunDeclared(context.Background(), dir, "integration", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != string(recipe.Error) {
		t.Fatalf("status = %q, want error", res.Status)
	}
	if res.Phase != "up" {
		t.Errorf("phase = %q, want up", res.Phase)
	}
	trace, _ := os.ReadFile(filepath.Join(dir, "trace"))
	if strings.Contains(string(trace), "test") {
		t.Error("the test ran even though `up` failed")
	}
	// Teardown still runs: a partial `up` can have left something behind.
	if !strings.Contains(string(trace), "down") {
		t.Error("teardown did not run after a failing `up`")
	}
}

func TestDeclaredPortsAreCollectedForTheSandbox(t *testing.T) {
	dir := t.TempDir()
	writeDecl(t, dir, "version: 1\nintegration:\n"+
		"  - name: a\n    test: [\"true\"]\n    ports: [5432, 6379]\n"+
		"  - name: b\n    test: [\"true\"]\n    ports: [5432]\n")
	d, err := recipe.LoadDeclared(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := d.Ports()
	if len(got) != 2 {
		t.Fatalf("ports = %v; the same port named twice must produce one rule", got)
	}
}

func TestDeclaredRecipesOnlyAtHigh(t *testing.T) {
	dir := t.TempDir()
	writeDecl(t, dir, "version: 1\nintegration:\n  - name: a\n    test: [\"true\"]\n")
	if got := recipe.DeclaredRecipes(recipe.Standard, dir, "le"); len(got) != 0 {
		t.Errorf("standard selected %d declared recipes; §10.1 adopts runtime feedback for HIGH", len(got))
	}
	if got := recipe.DeclaredRecipes(recipe.High, dir, "le"); len(got) != 1 {
		t.Errorf("high selected %d declared recipes, want 1", len(got))
	}
}

func TestDeclaredSummaryDistinguishesFailureFromNotRunning(t *testing.T) {
	_, sum := recipe.DeclaredSummary(1,
		`{"kind":"generate","name":"sqlc","status":"fail","headline":"2 files stale","changed":["a.go","b.go"]}`, "")
	if len(sum.Findings) != 2 {
		t.Errorf("the changed files did not become findings: %+v", sum.Findings)
	}
	st, _ := recipe.DeclaredSummary(1,
		`{"kind":"integration","name":"db","status":"error","phase":"up","headline":"up failed"}`, "")
	if st != recipe.Error {
		t.Errorf("status = %q, want error: a runtime that would not start is not a verdict on the code", st)
	}
	st, _ = recipe.DeclaredSummary(1, "not json at all", "")
	if st == recipe.Pass {
		t.Error("unparseable output was reported as a pass")
	}
}

func TestAFailedRestoreBlocksRatherThanBeingReportedAsNotRun(t *testing.T) {
	// An Error does not block acceptance, correctly: a tool that could not run
	// says nothing about the code. A failed restore is different — the
	// worktree is no longer what any other result measured — so it must not
	// take the Error path, or a task is accepted against a tree nobody
	// verified.
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(out, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The generator replaces the output with a DIRECTORY, so writing the file
	// back fails.
	gen := filepath.Join(dir, "gen.sh")
	if err := os.WriteFile(gen, []byte("#!/bin/sh\nrm -f out.txt && mkdir out.txt\n"), 0o755); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	writeDecl(t, dir, "version: 1\ngenerate:\n  - name: fixture\n    argv: [\"./gen.sh\"]\n    outputs: [\"out.txt\"]\n")

	res, err := recipe.RunDeclared(context.Background(), dir, "generate", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != string(recipe.Fail) {
		t.Fatalf("status = %q, want fail: an unverifiable worktree must block acceptance", res.Status)
	}
	if !strings.Contains(res.Headline, "unknown state") {
		t.Errorf("the headline does not say the worktree is unknown: %q", res.Headline)
	}
}
