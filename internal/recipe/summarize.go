package recipe

import (
	"encoding/json"
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
		head += ": " + truncateLine(lines[0], 200)
	}
	// Every line is bounded, not just the headline. A tool that emits its
	// whole report as ONE line — golangci-lint's JSON is a single 3 KB line —
	// would otherwise put all of it in a packet through the fallback path,
	// which is exactly the token waste §8.2 compresses output to avoid.
	findings := make([]Finding, 0, len(lines))
	for _, l := range lines {
		findings = append(findings, Finding{Message: truncateLine(l, 200)})
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

// semgrepOutput is the subset of semgrep's JSON this reads.
type semgrepOutput struct {
	Results []struct {
		CheckID string `json:"check_id"`
		Path    string `json:"path"`
		Start   struct {
			Line int `json:"line"`
			Col  int `json:"col"`
		} `json:"start"`
		Extra struct {
			Message  string `json:"message"`
			Severity string `json:"severity"`
		} `json:"extra"`
	} `json:"results"`
	Errors []struct {
		Message string `json:"message"`
		Level   string `json:"level"`
	} `json:"errors"`
}

// Semgrep summarises a semgrep run.
//
// The message text is carried through verbatim because it is what reaches the
// model as a finding: a rule's message is written as an instruction, and
// replacing it with a rule id would throw away the only part that says what to
// do instead.
//
// A semgrep *error* — an unparseable rule, a file it could not read — is not a
// finding about the code and must not be reported as one. It is a fault in the
// checking, and saying so is the difference between "your code is fine" and "we
// did not manage to check it".
func Semgrep(exitCode int, stdout, stderr string) (Status, Summary) {
	var out semgrepOutput
	if err := decodeFirstJSON(stdout, &out); err != nil {
		// No parseable JSON: fall back rather than claim a clean run.
		return Generic(exitCode, stdout, stderr)
	}

	if len(out.Results) == 0 && len(out.Errors) == 0 {
		return Pass, Summary{Headline: "no semgrep findings"}
	}

	findings := make([]Finding, 0, len(out.Results))
	bySeverity := map[string]int{}
	for _, r := range out.Results {
		sev := strings.ToUpper(r.Extra.Severity)
		bySeverity[strings.ToLower(sev)]++
		msg := strings.TrimSpace(r.Extra.Message)
		if msg == "" {
			msg = r.CheckID
		}
		findings = append(findings, Finding{
			File: r.Path, Line: r.Start.Line, Column: r.Start.Col,
			Message: msg, Rule: r.CheckID,
		})
	}

	if len(out.Errors) > 0 {
		// Report the fault in the checking, not as a verdict on the code.
		var first string
		if len(out.Errors) > 0 {
			first = strings.TrimSpace(out.Errors[0].Message)
		}
		return Error, Summary{
			Headline: fmt.Sprintf("semgrep could not complete: %s", truncateLine(first, 160)),
			Findings: findings,
			Counts:   countsOf(map[string]int{"errors": len(out.Errors), "findings": len(out.Results)}),
		}
	}

	sortFindings(findings)
	shown, truncated := trimFindings(findings)
	if exitCode == 0 {
		// Findings below the failing severity: real, reported, not fatal.
		return Pass, Summary{
			Headline:  fmt.Sprintf("%d semgrep finding(s), none blocking", len(findings)),
			Findings:  shown,
			Counts:    countsOf(bySeverity),
			Truncated: truncated,
		}
	}
	return Fail, Summary{
		Headline:  fmt.Sprintf("%d semgrep finding(s)", len(findings)),
		Findings:  shown,
		Counts:    countsOf(bySeverity),
		Truncated: truncated,
	}
}

// golangciOutput is the subset of golangci-lint's JSON this reads. Verified
// against golangci-lint 2.13.2: `--output.json.path stdout` emits an object
// with `Issues` and a `Report`, and exits 1 when there are issues.
type golangciOutput struct {
	Issues []struct {
		FromLinter string `json:"FromLinter"`
		Text       string `json:"Text"`
		Severity   string `json:"Severity"`
		Pos        struct {
			Filename string `json:"Filename"`
			Line     int    `json:"Line"`
			Column   int    `json:"Column"`
		} `json:"Pos"`
	} `json:"Issues"`
	Report struct {
		Error string `json:"Error"`
	} `json:"Report"`
}

// GolangciLint summarises a golangci-lint run.
//
// The linter name travels as the finding's Rule for the same reason semgrep's
// check id does: a lint that keeps firing on correct code has to be findable
// so it can be turned off in `.golangci.yml`, and a message alone does not
// say which linter to disable.
//
// The distinction that matters is the one §10.1 depends on: issues in the code
// are a Fail, but a run that could not complete — a bad config, a package that
// would not load — is an Error. golangci-lint signals the second with a
// non-zero exit and no issues, and conflating the two would let a broken
// .golangci.yml read as broken code.
func GolangciLint(exitCode int, stdout, stderr string) (Status, Summary) {
	var out golangciOutput
	if err := decodeFirstJSON(stdout, &out); err != nil {
		// No parseable JSON: fall back rather than claim a clean run.
		return Generic(exitCode, stdout, stderr)
	}

	if e := strings.TrimSpace(out.Report.Error); e != "" {
		return Error, Summary{Headline: "golangci-lint could not complete: " + truncateLine(e, 160)}
	}

	if len(out.Issues) == 0 {
		if exitCode != 0 {
			// Non-zero with nothing to report is the run failing, not the code.
			return Error, Summary{
				Headline: fmt.Sprintf("golangci-lint exited %d with no issues reported", exitCode),
				Findings: genericFindings(stderr),
			}
		}
		return Pass, Summary{Headline: "no golangci-lint issues"}
	}

	findings := make([]Finding, 0, len(out.Issues))
	byLinter := map[string]int{}
	for _, iss := range out.Issues {
		byLinter[iss.FromLinter]++
		msg := strings.TrimSpace(iss.Text)
		if msg == "" {
			msg = iss.FromLinter
		}
		findings = append(findings, Finding{
			File: iss.Pos.Filename, Line: iss.Pos.Line, Column: iss.Pos.Column,
			Message: msg, Rule: iss.FromLinter,
		})
	}

	sortFindings(findings)
	shown, truncated := trimFindings(findings)
	return Fail, Summary{
		Headline:  fmt.Sprintf("%d golangci-lint issue(s)", len(findings)),
		Findings:  shown,
		Counts:    countsOf(byLinter),
		Truncated: truncated,
	}
}

// genericFindings lifts the last few stderr lines into findings so an Error
// carries some explanation rather than only a count.
func genericFindings(stderr string) []Finding {
	var out []Finding
	for _, l := range lastLines(stderr, 3) {
		out = append(out, Finding{Message: l})
	}
	return out
}

// decodeFirstJSON reads the first JSON value from a tool's stdout and ignores
// whatever follows it.
//
// json.Unmarshal on the whole buffer will not do, because a tool is entitled
// to print something after its report and several do: golangci-lint follows
// its JSON with a plain-text tally ("1 issues:\n* errcheck: 1"), which makes
// Unmarshal fail with "extra data". The summarizer then falls back to Generic,
// and a structured report the model could have acted on arrives as an
// undifferentiated blob. That failure is silent — a Fail is still a Fail — so
// it is worth being explicit about which part of the stream is the document.
func decodeFirstJSON(stdout string, v any) error {
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(stdout)))
	return dec.Decode(v)
}

func truncateLine(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
