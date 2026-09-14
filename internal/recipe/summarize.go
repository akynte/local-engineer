package recipe

import (
	"fmt"
	"regexp"
	"strings"
)

// Summarizers turn tool output into the compressed form a next step actually
// needs (design v3 §8.2: "Compression of tool output at source").
//
// Every one of them follows the same rule: a non-zero exit with parseable
// findings is a Fail; a non-zero exit with nothing parseable is still a Fail,
// but the headline says so rather than claiming the code is fine.

// goDiagnostic matches the compiler's own diagnostic format, which vet, build
// and most Go analysis tools share: file:line:col: message.
var goDiagnostic = regexp.MustCompile(`^(\S+?\.go):(\d+)(?::(\d+))?:\s+(.*)$`)

// Generic is the fallback summarizer: exit status plus the last lines of
// output. It never claims more than it knows.
func Generic(exitCode int, stdout, stderr string) (Status, Summary) {
	if exitCode == 0 {
		return Pass, Summary{Headline: "ok"}
	}
	combined := strings.TrimSpace(stderr)
	if combined == "" {
		combined = strings.TrimSpace(stdout)
	}
	lines := lastLines(combined, 5)
	head := fmt.Sprintf("exited %d", exitCode)
	if len(lines) > 0 {
		head += ": " + lines[0]
	}
	findings := make([]Finding, 0, len(lines))
	for _, l := range lines {
		findings = append(findings, Finding{Message: l})
	}
	return Fail, Summary{Headline: head, Findings: findings}
}

// GoBuild summarizes `go build` and `go vet`.
func GoBuild(exitCode int, stdout, stderr string) (Status, Summary) {
	findings := parseGoDiagnostics(stdout + "\n" + stderr)
	if exitCode == 0 {
		return Pass, Summary{Headline: "compiles"}
	}
	if len(findings) == 0 {
		return Generic(exitCode, stdout, stderr)
	}
	sortFindings(findings)
	shown, truncated := trimFindings(findings)
	return Fail, Summary{
		Headline:  fmt.Sprintf("%d compile error(s), first in %s", len(findings), shown[0].File),
		Findings:  shown,
		Counts:    countsOf(map[string]int{"errors": len(findings)}),
		Truncated: truncated,
	}
}

// GoVet summarizes `go vet`, which reports the same diagnostic shape but
// whose findings are warnings about correctness rather than compile errors.
func GoVet(exitCode int, stdout, stderr string) (Status, Summary) {
	findings := parseGoDiagnostics(stdout + "\n" + stderr)
	if exitCode == 0 {
		return Pass, Summary{Headline: "no vet findings"}
	}
	if len(findings) == 0 {
		return Generic(exitCode, stdout, stderr)
	}
	sortFindings(findings)
	shown, truncated := trimFindings(findings)
	return Fail, Summary{
		Headline:  fmt.Sprintf("%d vet finding(s)", len(findings)),
		Findings:  shown,
		Counts:    countsOf(map[string]int{"findings": len(findings)}),
		Truncated: truncated,
	}
}

func parseGoDiagnostics(out string) []Finding {
	seen := map[string]bool{}
	var findings []Finding
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		// Continuation lines of a multi-line error start with a tab; the
		// leading marker lines are what carry the location.
		m := goDiagnostic.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		f := Finding{File: m[1], Line: atoi(m[2]), Column: atoi(m[3]), Message: m[4]}
		key := fmt.Sprintf("%s:%d:%d:%s", f.File, f.Line, f.Column, f.Message)
		if seen[key] {
			continue
		}
		seen[key] = true
		findings = append(findings, f)
	}
	return findings
}

var (
	testFailLine = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	testPanic    = regexp.MustCompile(`^panic: (.*)`)
	pkgFailLine  = regexp.MustCompile(`^FAIL\s+(\S+)`)
	pkgOKLine    = regexp.MustCompile(`^ok\s+(\S+)`)
	testLocation = regexp.MustCompile(`^\s+(\S+?\.go):(\d+):\s*(.*)$`)
	raceHeader   = regexp.MustCompile(`^WARNING: DATA RACE`)
)

// GoTest summarizes `go test ./...`.
//
// It reports which tests failed and where, plus the tallies, rather than the
// full log. The distinction between "no tests" and "tests passed" is kept:
// a package with no tests is not evidence that anything works.
func GoTest(exitCode int, stdout, stderr string) (Status, Summary) {
	out := stdout + "\n" + stderr

	var findings []Finding
	var failedTests, okPkgs, failedPkgs, races int
	var currentTest string

	for _, line := range strings.Split(out, "\n") {
		switch {
		case raceHeader.MatchString(line):
			races++
			findings = append(findings, Finding{Message: "data race detected", Test: currentTest})
		case testFailLine.MatchString(line):
			m := testFailLine.FindStringSubmatch(line)
			currentTest = m[1]
			failedTests++
			findings = append(findings, Finding{Test: currentTest, Message: "test failed"})
		case testPanic.MatchString(line):
			m := testPanic.FindStringSubmatch(line)
			findings = append(findings, Finding{Message: "panic: " + m[1], Test: currentTest})
		case pkgFailLine.MatchString(line):
			failedPkgs++
		case pkgOKLine.MatchString(line):
			okPkgs++
		case testLocation.MatchString(line) && currentTest != "":
			m := testLocation.FindStringSubmatch(line)
			// Attach the location to the finding for the current test.
			for i := len(findings) - 1; i >= 0; i-- {
				if findings[i].Test == currentTest && findings[i].File == "" {
					findings[i].File = m[1]
					findings[i].Line = atoi(m[2])
					if msg := strings.TrimSpace(m[3]); msg != "" {
						findings[i].Message = msg
					}
					break
				}
			}
		}
	}

	counts := countsOf(map[string]int{
		"failed_tests": failedTests, "failed_packages": failedPkgs,
		"passed_packages": okPkgs, "races": races,
	})

	if exitCode == 0 {
		head := fmt.Sprintf("%d package(s) passed", okPkgs)
		if okPkgs == 0 {
			// Do not let "no tests ran" read as "the tests passed".
			head = "no test packages ran"
		}
		return Pass, Summary{Headline: head, Counts: counts}
	}
	if len(findings) == 0 {
		st, s := Generic(exitCode, stdout, stderr)
		s.Counts = counts
		return st, s
	}
	sortFindings(findings)
	shown, truncated := trimFindings(findings)

	head := fmt.Sprintf("%d test(s) failed in %d package(s)", failedTests, failedPkgs)
	if races > 0 {
		head = fmt.Sprintf("%d data race(s) detected; %s", races, head)
	}
	return Fail, Summary{Headline: head, Findings: shown, Counts: counts, Truncated: truncated}
}

// GoRace summarizes `go test -race`. A race is reported even when the test
// otherwise passed, because the race is the finding.
func GoRace(exitCode int, stdout, stderr string) (Status, Summary) {
	status, summary := GoTest(exitCode, stdout, stderr)
	if summary.Counts["races"] > 0 && status == Pass {
		return Fail, Summary{
			Headline: fmt.Sprintf("%d data race(s) detected", summary.Counts["races"]),
			Findings: summary.Findings, Counts: summary.Counts,
		}
	}
	return status, summary
}

// Gofmt summarizes `gofmt -l`, which lists unformatted files on stdout and
// exits zero either way — so exit status alone would always read as a pass.
func Gofmt(exitCode int, stdout, stderr string) (Status, Summary) {
	var findings []Finding
	for _, line := range strings.Split(stdout, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			findings = append(findings, Finding{File: p, Message: "not gofmt'd"})
		}
	}
	if exitCode != 0 && len(findings) == 0 {
		return Generic(exitCode, stdout, stderr)
	}
	if len(findings) == 0 {
		return Pass, Summary{Headline: "formatting is clean"}
	}
	sortFindings(findings)
	shown, truncated := trimFindings(findings)
	return Fail, Summary{
		Headline:  fmt.Sprintf("%d file(s) are not gofmt'd", len(findings)),
		Findings:  shown,
		Counts:    countsOf(map[string]int{"files": len(findings)}),
		Truncated: truncated,
	}
}

func lastLines(s string, n int) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	all := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var kept []string
	for _, l := range all {
		if strings.TrimSpace(l) != "" {
			kept = append(kept, strings.TrimSpace(l))
		}
	}
	if len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	return kept
}
