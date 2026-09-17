package eval

import "testing"

func gradeTask(files map[string]string) Task {
	return Task{Acceptance: Acceptance{Files: files}}
}

const twoTests = `package p

import "testing"

func TestAlpha(t *testing.T) {}
func TestBeta(t *testing.T)  {}
`

// A run that satisfied the acceptance command satisfied every test in it. This
// is the only case where no FAIL lines means success.
func TestAnAcceptedRunScoresEverything(t *testing.T) {
	g := gradeRun(gradeTask(map[string]string{"a_test.go": twoTests}), true, "")
	if g.Total != 2 || g.Passed != 2 || g.Score() != 1 {
		t.Errorf("accepted run graded %d/%d (score %v)", g.Passed, g.Total, g.Score())
	}
}

// The point of grading: a run that failed one of two tests is not the same
// evidence as a run that failed both, and a binary outcome cannot tell them
// apart.
func TestAPartialFailureScoresPartially(t *testing.T) {
	out := "--- FAIL: TestBeta (0.00s)\n    a_test.go:9: nope\nFAIL\n"
	g := gradeRun(gradeTask(map[string]string{"a_test.go": twoTests}), false, out)
	if g.Passed != 1 || g.Total != 2 {
		t.Fatalf("graded %d/%d, want 1/2", g.Passed, g.Total)
	}
	if len(g.Failed) != 1 || g.Failed[0] != "TestBeta" {
		t.Errorf("failed = %v, want [TestBeta]", g.Failed)
	}
}

// A build error runs no tests and prints no FAIL lines. Scoring that as full
// marks would hand the highest grade to the code that compiles least.
func TestAFailingRunWithNoNamedFailuresScoresZero(t *testing.T) {
	out := "# example.com/p\n./p.go:4:2: undefined: Missing\nFAIL\texample.com/p [build failed]\n"
	g := gradeRun(gradeTask(map[string]string{"a_test.go": twoTests}), false, out)
	if g.Passed != 0 || g.Score() != 0 {
		t.Errorf("a build failure graded %d/%d (score %v)", g.Passed, g.Total, g.Score())
	}
}

// Only the task's own hidden tests count. A fixture's visible tests failing is
// a different fact, and letting them in would make the score depend on how
// many tests the fixture happens to ship.
func TestOnlyHiddenTestsAreCounted(t *testing.T) {
	out := "--- FAIL: TestSomethingElse (0.00s)\n--- FAIL: TestBeta (0.00s)\nFAIL\n"
	g := gradeRun(gradeTask(map[string]string{"a_test.go": twoTests}), false, out)
	if g.Total != 2 {
		t.Fatalf("total = %d, want 2", g.Total)
	}
	if g.Passed != 1 {
		t.Errorf("passed = %d, want 1: a test the task does not own was counted", g.Passed)
	}
}

// A task with no hidden tests cannot be graded, and must say so rather than
// report a zero that reads as total failure.
func TestAnUngradableTaskIsNotRecorded(t *testing.T) {
	g := gradeRun(gradeTask(map[string]string{"notes.md": "no tests here"}), false, "")
	if g.Recorded() {
		t.Error("a task with no hidden tests was reported as graded")
	}
}

// Hidden tests may be spread over several files; all of them count.
func TestTestsAcrossFilesAreAllCounted(t *testing.T) {
	files := map[string]string{
		"a_test.go": twoTests,
		"b_test.go": "package q\n\nimport \"testing\"\n\nfunc TestGamma(t *testing.T) {}\n",
	}
	if got := len(hiddenTests(gradeTask(files))); got != 3 {
		t.Errorf("found %d hidden tests across two files, want 3", got)
	}
}
