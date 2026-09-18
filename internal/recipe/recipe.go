// Package recipe runs the deterministic verification loops of design v3 §10.1:
// "Compiler, vet, lint, test, race feedback loops | most first-attempt errors
// | seconds per loop | adopted; the core."
//
// Two properties matter beyond running a command.
//
// First, output is compressed at source (§8.2). A failing `go test ./...` can
// be megabytes; what a next step needs is which tests failed and where. The
// full output is kept as a content-addressed artifact, and only the structured
// summary enters a packet.
//
// Second, a result is evidence tied to a candidate. "The tests passed" is a
// fact about one exact state of the code, so every result records the
// candidate hash it was produced against, and recovery marks it stale when the
// worktree moves on (§7.2).
package recipe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/sandbox"
)

// Status is the outcome of a run.
type Status string

const (
	// Pass: the command succeeded.
	Pass Status = "pass"
	// Fail: the command ran and reported a problem in the code.
	Fail Status = "fail"
	// Error: the command could not run. This is NOT a fail — a missing
	// toolchain says nothing about the code, and conflating the two would let
	// a broken environment read as a broken change.
	Error Status = "error"
	// Skipped: the recipe did not apply here.
	Skipped Status = "skipped"
)

// Kind groups recipes by what they check, which is what verification levels
// select on.
type Kind string

const (
	KindBuild    Kind = "build"
	KindVet      Kind = "vet"
	KindTest     Kind = "test"
	KindRace     Kind = "race"
	KindLint     Kind = "lint"
	KindAnalyzer Kind = "analyzer"
	KindFormat   Kind = "format"
	// KindGenerate checks that committed generated code is what the generator
	// produces now (§10.1's "deterministic generation for boilerplate"). It
	// never leaves the generator's output behind: see declared_run.go.
	KindGenerate Kind = "generate"
	// KindIntegration is §10.1's "runtime feedback": a check that exercises
	// the change against something running, which is the only way to catch
	// "compiles but wrong".
	KindIntegration Kind = "integration"
	KindCustom      Kind = "custom"
)

// Recipe is one deterministic check.
type Recipe struct {
	Name string
	Kind Kind
	// Argv is the command. It is never a shell string: a shell would make the
	// sandbox's argument boundary meaningless.
	Argv []string
	// Dir is relative to the worktree root.
	Dir string
	// Timeout bounds the run. A hung test must fail the step, not the system.
	Timeout time.Duration
	// Env adds to the sandboxed environment.
	Env []string
	// Summarize compresses the output. Nil uses a generic summarizer.
	Summarize Summarizer
	// AppliesTo reports whether the recipe is relevant to a worktree. Nil
	// means always.
	AppliesTo func(worktreePath string) bool
}

// Finding is one located problem extracted from tool output.
type Finding struct {
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Message string `json:"message"`
	// Test names the failing test, when the tool reports one.
	Test string `json:"test,omitempty"`
	// Rule names the check that fired, when the tool has named rules. It is
	// kept beside the message rather than folded into it so a finding can be
	// traced back to the rule that produced it — and a rule that keeps firing
	// on correct code can be found and removed.
	Rule string `json:"rule,omitempty"`
}

// Summary is the compressed form of a run: what a next step needs, without
// the full output.
type Summary struct {
	Tests    map[string]Status `json:"tests,omitempty"`
	Headline string            `json:"headline"`
	Findings []Finding         `json:"findings,omitempty"`
	// Counts are tool-specific tallies (tests run, packages failed).
	Counts map[string]int `json:"counts,omitempty"`
	// Truncated is set when findings were dropped, so the summary is never
	// mistaken for the complete list.
	Truncated bool `json:"truncated,omitempty"`
}

// MaxFindings bounds a summary. A change that breaks a hundred call sites
// needs the first handful and the count, not all hundred.
const MaxFindings = 20

// Summarizer turns raw output into a summary.
type Summarizer func(exitCode int, stdout, stderr string) (Status, Summary)

// Result is one completed run.
type Result struct {
	Recipe   string        `json:"recipe"`
	Kind     Kind          `json:"kind"`
	Status   Status        `json:"status"`
	Summary  Summary       `json:"summary"`
	ExitCode int           `json:"exit_code"`
	Duration time.Duration `json:"duration"`
	// Candidate is the content manifest this result describes. Without it a
	// result is not evidence, only an anecdote.
	Candidate string `json:"candidate"`
	// ArtifactHash addresses the full output in the artifact store.
	ArtifactHash string `json:"artifact_hash,omitempty"`
	// Err carries why the recipe could not run, when Status is Error.
	Err string `json:"error,omitempty"`
}

// Passed reports whether the result is a pass.
func (r Result) Passed() bool { return r.Status == Pass }

// Runner executes recipes inside a sandbox.
type Runner struct {
	// Sandbox confines each run. It is required: running verification
	// unconfined would hand a repository's test suite the whole container.
	Sandbox sandbox.Runner
	// Spec is the sandbox specification, with the worktree already writable.
	Spec sandbox.Spec
	// Store persists full output. Nil keeps output in the result only.
	Store ArtifactStore
	// MaxOutputBytes caps what is captured per stream.
	MaxOutputBytes int
}

// ArtifactStore is the subset of the artifact store a runner needs.
type ArtifactStore interface {
	Put(body []byte) (string, error)
}

// DefaultMaxOutput bounds captured output. A test suite printing without limit
// must not exhaust memory.
const DefaultMaxOutput = 8 << 20

// Run executes one recipe against a worktree.
func (r *Runner) Run(ctx context.Context, rec Recipe, worktreePath, candidate string) Result {
	res := Result{Recipe: rec.Name, Kind: rec.Kind, Candidate: candidate}
	start := time.Now()

	if rec.AppliesTo != nil && !rec.AppliesTo(worktreePath) {
		res.Status = Skipped
		res.Summary = Summary{Headline: rec.Name + " does not apply to this worktree"}
		return res
	}
	if len(rec.Argv) == 0 {
		res.Status = Error
		res.Err = "recipe has no command"
		return res
	}
	if r.Sandbox == nil {
		res.Status = Error
		res.Err = "no sandbox runner configured; verification must not run unconfined"
		return res
	}

	timeout := rec.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	spec := r.Spec
	spec.Dir = worktreePath
	if rec.Dir != "" {
		spec.Dir = worktreePath + "/" + strings.TrimPrefix(rec.Dir, "/")
	}
	spec.Env = append(append([]string{}, spec.Env...), rec.Env...)

	cmd, err := r.Sandbox.Command(runCtx, spec, rec.Argv...)
	if err != nil {
		res.Status = Error
		res.Err = err.Error()
		res.Duration = time.Since(start)
		return res
	}

	limit := r.MaxOutputBytes
	if limit <= 0 {
		limit = DefaultMaxOutput
	}
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = limit, limit
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	runErr := cmd.Run()
	res.Duration = time.Since(start)
	res.ExitCode = exitCodeOf(runErr)

	// A timeout is a failure of the run, not a verdict on the code.
	if runCtx.Err() != nil {
		res.Status = Error
		res.Err = fmt.Sprintf("%s timed out after %s", rec.Name, timeout)
		res.Summary = Summary{Headline: res.Err}
		r.persist(&res, stdout.String(), stderr.String())
		return res
	}
	// A command that could not start says nothing about the code either.
	var execErr *exec.Error
	if runErr != nil && asExecError(runErr, &execErr) {
		res.Status = Error
		res.Err = fmt.Sprintf("%s could not run: %v", rec.Name, runErr)
		res.Summary = Summary{Headline: res.Err}
		return res
	}
	// The same thing one level down. The sandbox runner re-executes `le` as a
	// helper, so a tool the helper could not exec is the *helper* exiting
	// 126 or 127, not a Go exec error — and a summarizer handed that output
	// sees garbage and calls it a Fail. That is the one confusion the Error
	// status exists to prevent: it reports broken code when nothing checked
	// the code.
	//
	// 126 and 127 are the POSIX exec-failure conventions ("found but not
	// executable", "not found"). No verification tool here uses them as a
	// verdict: go test exits 1, golangci-lint at most 7, semgrep at most 8.
	if res.ExitCode == 126 || res.ExitCode == 127 {
		res.Status = Error
		hint := "not found on PATH inside the sandbox"
		if res.ExitCode == 126 {
			// By far the most common cause, and the least obvious: the binary
			// exists and PATH finds it, but its real location was never
			// granted read access, so Landlock denies the exec.
			hint = "found but not executable inside the sandbox — if it lives " +
				"outside sandbox.read_only_paths, Landlock denies the exec"
		}
		res.Err = fmt.Sprintf("%s could not run (exit %d): %s: %s",
			rec.Name, res.ExitCode, hint, truncateLine(stderr.String(), 200))
		res.Summary = Summary{Headline: res.Err}
		r.persist(&res, stdout.String(), stderr.String())
		return res
	}

	summarize := rec.Summarize
	if summarize == nil {
		summarize = Generic
	}
	res.Status, res.Summary = summarize(res.ExitCode, stdout.String(), stderr.String())
	r.persist(&res, stdout.String(), stderr.String())
	return res
}

// RunAll executes recipes in order, stopping early when a build failure makes
// the rest meaningless: running tests against code that does not compile
// produces noise, not information.
func (r *Runner) RunAll(ctx context.Context, recipes []Recipe, worktreePath, candidate string) []Result {
	out := make([]Result, 0, len(recipes))
	for _, rec := range recipes {
		if ctx.Err() != nil {
			break
		}
		res := r.Run(ctx, rec, worktreePath, candidate)
		out = append(out, res)
		if rec.Kind == KindBuild && res.Status == Fail {
			for _, skipped := range recipes[len(out):] {
				out = append(out, Result{
					Recipe: skipped.Name, Kind: skipped.Kind, Status: Skipped, Candidate: candidate,
					Summary: Summary{Headline: "skipped: the code does not compile"},
				})
			}
			break
		}
	}
	return out
}

func (r *Runner) persist(res *Result, stdout, stderr string) {
	if r.Store == nil {
		return
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "$ recipe %s (%s)\nexit %d after %s\n\n--- stdout ---\n%s\n--- stderr ---\n%s\n",
		res.Recipe, res.Kind, res.ExitCode, res.Duration.Round(time.Millisecond), stdout, stderr)
	if hash, err := r.Store.Put(b.Bytes()); err == nil {
		res.ArtifactHash = hash
	}
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if asExitError(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func asExitError(err error, dst **exec.ExitError) bool {
	// errors.As, not a type assertion: exec wraps its errors, and a bare
	// assertion would misreport a wrapped exit status as "could not run".
	return errors.As(err, dst)
}

func asExecError(err error, dst **exec.Error) bool {
	return errors.As(err, dst)
}

// cappedBuffer collects output up to a limit and records that it truncated.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if remaining := c.limit - c.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			c.buf.Write(p[:remaining])
			c.truncated = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	// Always report a full write: a command must not fail because its output
	// exceeded our capture budget.
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	s := c.buf.String()
	if c.truncated {
		s += "\n[output truncated]"
	}
	return s
}

// trimFindings caps a finding list and records the truncation.
func trimFindings(findings []Finding) ([]Finding, bool) {
	if len(findings) <= MaxFindings {
		return findings, false
	}
	return findings[:MaxFindings], true
}

func countsOf(kv map[string]int) map[string]int {
	if len(kv) == 0 {
		return nil
	}
	out := make(map[string]int, len(kv))
	for k, v := range kv {
		if v != 0 {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sortFindings gives a stable order so the same failure produces the same
// summary, which is what lets a caller tell "the same problem" from "a new one".
func sortFindings(f []Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].File != f[j].File {
			return f[i].File < f[j].File
		}
		if f[i].Line != f[j].Line {
			return f[i].Line < f[j].Line
		}
		return f[i].Message < f[j].Message
	})
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
