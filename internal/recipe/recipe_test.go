package recipe_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/sandbox"
)

// The summarizers are the part that turns megabytes of output into something
// a next step can act on, so they are tested against real tool output shapes.

func TestGoBuildSummary(t *testing.T) {
	out := `# example.com/app/internal/service
internal/service/user.go:12:9: undefined: missingHelper
internal/service/user.go:20:2: declared and not used: x
`
	status, s := recipe.GoBuild(2, "", out)
	if status != recipe.Fail {
		t.Fatalf("status = %s, want fail", status)
	}
	if len(s.Findings) != 2 {
		t.Fatalf("expected 2 findings, got %d: %+v", len(s.Findings), s.Findings)
	}
	f := s.Findings[0]
	if f.File != "internal/service/user.go" || f.Line != 12 || f.Column != 9 {
		t.Errorf("location not parsed: %+v", f)
	}
	if !strings.Contains(f.Message, "undefined") {
		t.Errorf("message not parsed: %q", f.Message)
	}
	if s.Counts["errors"] != 2 {
		t.Errorf("counts = %v", s.Counts)
	}
}

func TestGoBuildPass(t *testing.T) {
	if status, s := recipe.GoBuild(0, "", ""); status != recipe.Pass || s.Headline != "compiles" {
		t.Fatalf("status=%s headline=%q", status, s.Headline)
	}
}

func TestGoTestSummaryExtractsFailuresAndLocations(t *testing.T) {
	out := `ok  	example.com/app/internal/a	0.02s
--- FAIL: TestRefund (0.00s)
    payment_test.go:41: expected 500, got 400
FAIL
FAIL	example.com/app/internal/service	0.31s
`
	status, s := recipe.GoTest(1, out, "")
	if status != recipe.Fail {
		t.Fatalf("status = %s, want fail", status)
	}
	if s.Counts["failed_tests"] != 1 || s.Counts["passed_packages"] != 1 || s.Counts["failed_packages"] != 1 {
		t.Fatalf("counts = %v", s.Counts)
	}
	if len(s.Findings) != 1 {
		t.Fatalf("findings = %+v", s.Findings)
	}
	f := s.Findings[0]
	if f.Test != "TestRefund" || f.File != "payment_test.go" || f.Line != 41 {
		t.Errorf("finding not fully parsed: %+v", f)
	}
	if !strings.Contains(f.Message, "expected 500") {
		t.Errorf("assertion message lost: %q", f.Message)
	}
}

// "No tests ran" must never read as "the tests passed": a package with no
// tests is not evidence that anything works.
func TestNoTestsIsNotTheSameAsPassing(t *testing.T) {
	_, s := recipe.GoTest(0, "?   	example.com/app	[no test files]\n", "")
	if !strings.Contains(s.Headline, "no test packages ran") {
		t.Fatalf("headline = %q; an empty run must say so", s.Headline)
	}
}

// A race is the finding even when the test itself passed.
func TestRaceIsReportedEvenOnAPassingTest(t *testing.T) {
	out := `WARNING: DATA RACE
Write at 0x00c000123456 by goroutine 8:
ok  	example.com/app	1.20s
`
	status, s := recipe.GoRace(0, out, "")
	if status != recipe.Fail {
		t.Fatalf("a detected race must fail the recipe, got %s", status)
	}
	if s.Counts["races"] != 1 {
		t.Errorf("counts = %v", s.Counts)
	}
}

// gofmt exits zero whether or not files need formatting, so exit status alone
// would always read as a pass.
func TestGofmtUsesOutputNotExitStatus(t *testing.T) {
	status, s := recipe.Gofmt(0, "internal/a/a.go\ninternal/b/b.go\n", "")
	if status != recipe.Fail {
		t.Fatalf("status = %s, want fail", status)
	}
	if len(s.Findings) != 2 {
		t.Fatalf("findings = %+v", s.Findings)
	}
	if status, _ := recipe.Gofmt(0, "", ""); status != recipe.Pass {
		t.Error("clean formatting must pass")
	}
}

func TestSummariesAreTruncatedAndSaySo(t *testing.T) {
	var b strings.Builder
	for i := 0; i < recipe.MaxFindings*3; i++ {
		b.WriteString("a.go:1:1: problem\n")
		b.WriteString("b" + string(rune('a'+i%26)) + ".go:2:1: problem\n")
	}
	_, s := recipe.GoBuild(2, "", b.String())
	if len(s.Findings) > recipe.MaxFindings {
		t.Fatalf("summary carries %d findings, cap is %d", len(s.Findings), recipe.MaxFindings)
	}
	if !s.Truncated {
		t.Error("a truncated summary must say so, or it reads as the complete list")
	}
}

func TestVerificationLevels(t *testing.T) {
	if recipe.Low.Includes(recipe.KindTest) {
		t.Error("the low level must not require tests")
	}
	if !recipe.Standard.Includes(recipe.KindTest) || recipe.Standard.Includes(recipe.KindRace) {
		t.Error("standard is build, vet and test")
	}
	if !recipe.High.Includes(recipe.KindRace) {
		t.Error("high must include the race detector")
	}
	if got := len(recipe.GoRecipes(recipe.Standard)); got != 4 {
		t.Errorf("standard should select 4 recipes, got %d", got)
	}
	// High selects every kind, so it must be a superset of standard rather
	// than a fixed count: a hardcoded number here fails whenever a recipe is
	// added, which says nothing about whether the levels are right.
	high := recipe.GoRecipes(recipe.High)
	if len(high) <= len(recipe.GoRecipes(recipe.Standard)) {
		t.Errorf("high selected %d recipes, standard %d; high must be a superset",
			len(high), len(recipe.GoRecipes(recipe.Standard)))
	}
	kinds := map[recipe.Kind]bool{}
	for _, r := range high {
		kinds[r.Kind] = true
	}
	for _, want := range []recipe.Kind{
		recipe.KindBuild, recipe.KindVet, recipe.KindTest,
		recipe.KindRace, recipe.KindFormat, recipe.KindAnalyzer,
		// §10.1's loop is "compiler, vet, lint, test, race". KindLint existed
		// with nothing producing one; this is the assertion that noticed.
		recipe.KindLint,
	} {
		if !kinds[want] {
			t.Errorf("high does not select the %s kind", want)
		}
	}
	// Build must come first so a compile failure short-circuits the rest.
	if recipe.GoRecipes(recipe.High)[0].Kind != recipe.KindBuild {
		t.Error("build must run first")
	}
}

// End-to-end through a real sandbox, because a recipe that is not confined is
// not the thing the design specifies.
func TestRunnerExecutesInsideASandbox(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &recipe.Runner{
		Sandbox: sandbox.ContainerRunner{},
		Spec: sandbox.Spec{
			ReadWrite: []string{dir}, Dir: dir,
			Env: []string{"PATH=/usr/bin:/bin", "NO_COLOR=1"},
		},
	}
	res := r.Run(context.Background(), recipe.Recipe{
		Name: "cat", Kind: recipe.KindCustom,
		Argv: []string{"cat", "marker"}, Timeout: 10 * time.Second,
	}, dir, "candidate-1")

	if res.Status != recipe.Pass {
		t.Fatalf("status=%s err=%s", res.Status, res.Err)
	}
	if res.Candidate != "candidate-1" {
		t.Error("a result must record the candidate it describes, or it is not evidence")
	}
}

// A command that cannot run is an Error, never a Fail: a missing toolchain
// says nothing about the code, and conflating them lets a broken environment
// read as a broken change.
func TestMissingCommandIsAnErrorNotAFailure(t *testing.T) {
	dir := t.TempDir()
	r := &recipe.Runner{Sandbox: sandbox.ContainerRunner{}, Spec: sandbox.Spec{ReadWrite: []string{dir}, Dir: dir}}
	res := r.Run(context.Background(), recipe.Recipe{
		Name: "absent", Argv: []string{"le-definitely-not-a-real-binary"}, Timeout: 5 * time.Second,
	}, dir, "c")

	if res.Status != recipe.Error {
		t.Fatalf("status = %s, want error", res.Status)
	}
	if res.Err == "" {
		t.Error("an error result must say why")
	}
}

func TestAToolTheSandboxDeniedIsAnErrorNotAFailure(t *testing.T) {
	// The sandbox runner re-executes `le` as a helper, so a tool it could not
	// exec surfaces as the helper exiting 126 — not as a Go exec error. A
	// summarizer handed that output sees garbage and calls it a Fail, which
	// reports broken code when nothing checked the code. This is what
	// happened to semgrep when its virtualenv was outside read_only_paths.
	dir := t.TempDir()
	r := &recipe.Runner{Sandbox: sandbox.ContainerRunner{}, Spec: sandbox.Spec{ReadWrite: []string{dir}, Dir: dir}}
	res := r.Run(context.Background(), recipe.Recipe{
		Name: "denied-tool", Kind: recipe.KindAnalyzer,
		// 126 is what a shell reports for "found but not executable".
		Argv: []string{"sh", "-c", "exit 126"}, Timeout: time.Minute,
		Summarize: recipe.Semgrep,
	}, dir, "candidate")

	if res.Status != recipe.Error {
		t.Fatalf("status = %q, want error: a tool that could not run says nothing about the code", res.Status)
	}
	if !strings.Contains(res.Err, "read_only_paths") {
		t.Errorf("the error does not name the likely cause: %q", res.Err)
	}
}

func TestTimeoutIsAnErrorNotAVerdict(t *testing.T) {
	dir := t.TempDir()
	r := &recipe.Runner{Sandbox: sandbox.ContainerRunner{}, Spec: sandbox.Spec{ReadWrite: []string{dir}, Dir: dir}}
	res := r.Run(context.Background(), recipe.Recipe{
		Name: "slow", Argv: []string{"sleep", "30"}, Timeout: 300 * time.Millisecond,
	}, dir, "c")

	if res.Status != recipe.Error {
		t.Fatalf("a timeout is a failure of the run, not a verdict on the code; got %s", res.Status)
	}
	if !strings.Contains(res.Err, "timed out") {
		t.Errorf("err = %q", res.Err)
	}
}

func TestRunningUnconfinedIsRefused(t *testing.T) {
	r := &recipe.Runner{} // no sandbox
	res := r.Run(context.Background(), recipe.Recipe{Name: "x", Argv: []string{"true"}}, t.TempDir(), "c")
	if res.Status != recipe.Error || !strings.Contains(res.Err, "sandbox") {
		t.Fatalf("verification must refuse to run unconfined; got %s %q", res.Status, res.Err)
	}
}

// A compile failure makes everything after it noise rather than information.
func TestRunAllStopsAfterABuildFailure(t *testing.T) {
	dir := t.TempDir()
	r := &recipe.Runner{Sandbox: sandbox.ContainerRunner{}, Spec: sandbox.Spec{ReadWrite: []string{dir}, Dir: dir}}

	results := r.RunAll(context.Background(), []recipe.Recipe{
		{Name: "build", Kind: recipe.KindBuild, Argv: []string{"false"}, Timeout: 5 * time.Second},
		{Name: "test", Kind: recipe.KindTest, Argv: []string{"true"}, Timeout: 5 * time.Second},
	}, dir, "c")

	if len(results) != 2 {
		t.Fatalf("every recipe must be accounted for, got %d", len(results))
	}
	if results[0].Status != recipe.Fail {
		t.Errorf("build status = %s", results[0].Status)
	}
	if results[1].Status != recipe.Skipped {
		t.Errorf("the test recipe should be skipped after a build failure, got %s", results[1].Status)
	}
	if !strings.Contains(results[1].Summary.Headline, "does not compile") {
		t.Errorf("the skip should say why: %q", results[1].Summary.Headline)
	}
}

// Full output goes to the artifact store; only the summary travels.
func TestFullOutputIsStoredSeparatelyFromTheSummary(t *testing.T) {
	dir := t.TempDir()
	store := &fakeStore{}
	r := &recipe.Runner{
		Sandbox: sandbox.ContainerRunner{}, Store: store,
		Spec: sandbox.Spec{ReadWrite: []string{dir}, Dir: dir, Env: []string{"PATH=/usr/bin:/bin"}},
	}
	res := r.Run(context.Background(), recipe.Recipe{
		Name: "noisy", Argv: []string{"sh", "-c", "echo line1; echo line2 >&2"}, Timeout: 10 * time.Second,
	}, dir, "c")

	if res.ArtifactHash == "" {
		t.Fatal("the full output must be addressable")
	}
	body := string(store.last)
	if !strings.Contains(body, "line1") || !strings.Contains(body, "line2") {
		t.Errorf("both streams must be preserved in the artifact: %q", body)
	}
}

type fakeStore struct{ last []byte }

func (f *fakeStore) Put(body []byte) (string, error) {
	f.last = append([]byte(nil), body...)
	return "hash-of-output", nil
}

// A semgrep error is a fault in the checking, not a verdict on the code.
// Reporting it as "no findings" is the difference between "your code is fine"
// and "we did not manage to check it".
func TestSemgrepErrorsAreNotAPass(t *testing.T) {
	out := `{"results":[],"errors":[{"message":"invalid rule: missing pattern","level":"error"}]}`
	status, summary := recipe.Semgrep(2, out, "")
	if status == recipe.Pass {
		t.Fatal("a semgrep run that could not complete was reported as a pass")
	}
	if !strings.Contains(summary.Headline, "could not complete") {
		t.Errorf("headline does not say the check failed to run: %q", summary.Headline)
	}
}

func TestSemgrepFindingsCarryRuleAndMessage(t *testing.T) {
	out := `{"results":[{"check_id":"evidence-must-be-stated","path":"internal/a/a.go",
	  "start":{"line":42,"col":3},
	  "extra":{"message":"This edge does not state its evidence category.","severity":"ERROR"}}],
	  "errors":[]}`
	status, summary := recipe.Semgrep(1, out, "")
	if status != recipe.Fail {
		t.Fatalf("status = %q, want fail", status)
	}
	if len(summary.Findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(summary.Findings))
	}
	f := summary.Findings[0]
	if f.Rule != "evidence-must-be-stated" {
		t.Errorf("rule = %q; a finding must be traceable to the rule that fired", f.Rule)
	}
	// The message is what reaches the model, and it is written as an
	// instruction. Replacing it with a rule id throws away the useful half.
	if !strings.Contains(f.Message, "evidence category") {
		t.Errorf("message was not carried through: %q", f.Message)
	}
	if f.File != "internal/a/a.go" || f.Line != 42 {
		t.Errorf("location lost: %+v", f)
	}
}

func TestSemgrepCleanRunPasses(t *testing.T) {
	status, summary := recipe.Semgrep(0, `{"results":[],"errors":[]}`, "")
	if status != recipe.Pass {
		t.Fatalf("status = %q, want pass", status)
	}
	if summary.Headline == "" {
		t.Error("a passing run has no headline")
	}
}

// Unparseable output must not be read as a clean run.
func TestSemgrepUnparseableOutputIsNotAPass(t *testing.T) {
	status, _ := recipe.Semgrep(2, "semgrep: command not found", "")
	if status == recipe.Pass {
		t.Fatal("unparseable output was reported as a pass")
	}
}

// The recipe is skipped when there is nothing to check or no tool to check
// with: semgrep is an addition, not a dependency of the build.
func TestSemgrepAppliesOnlyWithRules(t *testing.T) {
	if recipe.HasSemgrepRules(t.TempDir()) {
		t.Error("the recipe applied to a repository with no rules")
	}
}

func TestEveryRecipeKindHasARecipe(t *testing.T) {
	// A Kind with no recipe behind it is a verification level promising
	// something nothing delivers. KindLint was exactly that until the
	// golangci-lint recipe existed, and the levels reported no problem.
	produced := map[recipe.Kind]bool{}
	for _, r := range recipe.GoRecipes(recipe.High) {
		produced[r.Kind] = true
	}
	for _, k := range []recipe.Kind{
		recipe.KindBuild, recipe.KindVet, recipe.KindTest, recipe.KindRace,
		recipe.KindLint, recipe.KindAnalyzer, recipe.KindFormat,
	} {
		if !produced[k] {
			t.Errorf("kind %q is selectable by a level but no recipe produces one", k)
		}
	}

	// The declared kinds come from the repository rather than from GoRecipes,
	// so they are checked against their own constructor.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".le"), 0o755); err != nil {
		t.Fatal(err)
	}
	decl := "version: 1\n" +
		"generate:\n  - name: g\n    argv: [\"true\"]\n    outputs: [\"out\"]\n" +
		"integration:\n  - name: i\n    test: [\"true\"]\n"
	if err := os.WriteFile(filepath.Join(dir, recipe.DeclaredFile), []byte(decl), 0o644); err != nil {
		t.Fatal(err)
	}
	declared := map[recipe.Kind]bool{}
	for _, r := range recipe.DeclaredRecipes(recipe.High, dir, "le") {
		declared[r.Kind] = true
	}
	for _, k := range []recipe.Kind{recipe.KindGenerate, recipe.KindIntegration} {
		if !declared[k] {
			t.Errorf("kind %q is selectable but no declared recipe produces one", k)
		}
	}
}

func TestGolangciLintAppliesOnlyWithCommittedConfig(t *testing.T) {
	// golangci-lint's default set is opinionated. A repository that never
	// committed a config never chose those rules, and holding a change to
	// them would be a verdict its maintainers did not agree to.
	if recipe.HasGolangciConfig(t.TempDir()) {
		t.Error("the lint recipe applied to a repository with no golangci config")
	}
}

func TestGolangciLintFindingsCarryLinterAndLocation(t *testing.T) {
	// Verified against golangci-lint 2.13.2 output.
	out := `{"Issues":[{"FromLinter":"errcheck",
	  "Text":"Error return value of ` + "`f.Close`" + ` is not checked",
	  "Severity":"","Pos":{"Filename":"p.go","Offset":71,"Line":7,"Column":15}}],
	  "Report":{"Linters":[{"Name":"errcheck","Enabled":true}]}}`
	status, summary := recipe.GolangciLint(1, out, "")
	if status != recipe.Fail {
		t.Fatalf("status = %q, want fail", status)
	}
	if len(summary.Findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(summary.Findings))
	}
	f := summary.Findings[0]
	// The linter name is what a maintainer needs to turn the rule off.
	if f.Rule != "errcheck" {
		t.Errorf("rule = %q, want errcheck", f.Rule)
	}
	if f.File != "p.go" || f.Line != 7 || f.Column != 15 {
		t.Errorf("location lost: %+v", f)
	}
	if summary.Counts["errcheck"] != 1 {
		t.Errorf("counts by linter = %v", summary.Counts)
	}
}

func TestGolangciLintJSONFollowedByTextStillParses(t *testing.T) {
	// golangci-lint prints a plain-text tally after its JSON. Unmarshalling
	// the whole buffer fails with "extra data", the summarizer falls back to
	// Generic, and a structured report arrives as an undifferentiated blob —
	// silently, because a Fail is still a Fail. This is the real output shape.
	out := `{"Issues":[{"FromLinter":"errcheck","Text":"Error return value of ` + "`f.Close`" + ` is not checked",` +
		`"Pos":{"Filename":"p.go","Line":7,"Column":15}}],"Report":{"Linters":[{"Name":"errcheck","Enabled":true}]}}` +
		"\n1 issues:\n* errcheck: 1\n"
	status, summary := recipe.GolangciLint(1, out, "")
	if status != recipe.Fail {
		t.Fatalf("status = %q, want fail", status)
	}
	if len(summary.Findings) != 1 || summary.Findings[0].Rule != "errcheck" {
		t.Fatalf("the trailing text defeated the parser: %+v", summary)
	}
	if strings.Contains(summary.Headline, "{") {
		t.Errorf("headline is raw output rather than a summary: %q", summary.Headline)
	}
}

func TestAFallbackSummaryCannotDumpAWholeReportIntoAPacket(t *testing.T) {
	// §8.2 compresses tool output at source. The fallback path has to honour
	// that too: a tool whose entire report is ONE long line would otherwise
	// carry all of it into a packet.
	oneHugeLine := "{" + strings.Repeat("x", 8000) + "}"
	_, summary := recipe.Generic(1, oneHugeLine, "")
	if len(summary.Headline) > 300 {
		t.Errorf("headline is %d bytes; a fallback must still be a summary", len(summary.Headline))
	}
	for _, f := range summary.Findings {
		if len(f.Message) > 300 {
			t.Errorf("finding is %d bytes; every line must be bounded", len(f.Message))
		}
	}
}

func TestGolangciLintCleanRunPasses(t *testing.T) {
	status, summary := recipe.GolangciLint(0, `{"Issues":[],"Report":{}}`, "")
	if status != recipe.Pass {
		t.Fatalf("status = %q, want pass", status)
	}
	if summary.Headline == "" {
		t.Error("a passing run has no headline")
	}
}

func TestGolangciLintFailingToRunIsNotAFailingVerdict(t *testing.T) {
	// A broken .golangci.yml must not read as broken code. golangci-lint
	// signals it with a non-zero exit and nothing to report.
	status, summary := recipe.GolangciLint(3, `{"Issues":[],"Report":{}}`, "can't load config: unknown linter")
	if status != recipe.Error {
		t.Fatalf("status = %q, want error", status)
	}
	if !strings.Contains(summary.Headline, "no issues reported") {
		t.Errorf("headline does not say the run failed: %q", summary.Headline)
	}

	// Same when the report carries the error explicitly.
	status, summary = recipe.GolangciLint(3, `{"Issues":[],"Report":{"Error":"context loading failed"}}`, "")
	if status != recipe.Error {
		t.Fatalf("status = %q, want error", status)
	}
	if !strings.Contains(summary.Headline, "could not complete") {
		t.Errorf("headline = %q", summary.Headline)
	}
}

func TestGolangciLintUnparseableOutputIsNotAPass(t *testing.T) {
	status, _ := recipe.GolangciLint(1, "panic: runtime error", "")
	if status == recipe.Pass {
		t.Fatal("unparseable output was reported as a pass")
	}
}
