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
	if got := len(recipe.GoRecipes(recipe.Standard)); got != 3 {
		t.Errorf("standard should select 3 recipes, got %d", got)
	}
	if got := len(recipe.GoRecipes(recipe.High)); got != 5 {
		t.Errorf("high should select every recipe, got %d", got)
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
