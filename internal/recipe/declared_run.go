package recipe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Executing a declared step.
//
// This runs inside the sandbox, as a subcommand of `le`, because neither kind
// is a single command: an integration step is up-then-test-then-down with the
// teardown guaranteed, and a generate check is snapshot-run-compare-restore.
// Expressing either as argv would need a shell, and a shell inside the sandbox
// makes the argument boundary meaningless.

// StepResult is what the subcommand prints, and what DeclaredSummary reads.
//
// It is JSON on stdout rather than text because the summarizer has to
// distinguish "the check failed" from "the check could not run", and parsing
// that back out of prose is how a summarizer starts lying.
type StepResult struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Status is pass, fail or error, with the meanings the package uses
	// everywhere: fail is a verdict on the code, error is the check not
	// happening.
	Status string `json:"status"`
	// Headline is one line.
	Headline string `json:"headline"`
	// Changed lists the generated files that differ, for a generate check.
	Changed []string `json:"changed,omitempty"`
	// Phase names which part failed: up, test, down, generate, compare.
	Phase string `json:"phase,omitempty"`
	// ExitCode of the phase that failed.
	ExitCode int `json:"exit_code,omitempty"`
	// Output is the tail of the failing phase's output, already bounded.
	Output string `json:"output,omitempty"`
	// Teardown reports whether Down ran and whether it succeeded. A step whose
	// teardown failed has left something behind, and the next run failing for
	// an unrelated reason is the consequence.
	Teardown string `json:"teardown,omitempty"`
}

// RunDeclared executes one declared step in worktree and returns its result.
func RunDeclared(ctx context.Context, worktree, kind, name string) (StepResult, error) {
	d, err := LoadDeclared(worktree)
	if err != nil {
		return StepResult{}, err
	}
	switch kind {
	case string(KindGenerate):
		for _, g := range d.Generate {
			if g.Name == name {
				return runGenerate(ctx, worktree, g), nil
			}
		}
	case string(KindIntegration):
		for _, i := range d.Integration {
			if i.Name == name {
				return runIntegration(ctx, worktree, i), nil
			}
		}
	case "check":
		for _, c := range d.Check {
			if c.Name == name {
				return runCheck(ctx, worktree, c), nil
			}
		}
	default:
		return StepResult{}, fmt.Errorf("recipe: %q is not a declared step kind", kind)
	}
	return StepResult{}, fmt.Errorf("recipe: no %s step named %q in %s", kind, name, DeclaredFile)
}

const maxStepOutput = 4000

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// runPhase runs one command inside the worktree.
func runPhase(ctx context.Context, worktree string, argv []string) (int, string, error) {
	if len(argv) == 0 {
		return 0, "", nil
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // declared by the repository being verified, inside its own sandbox
	cmd.Dir = worktree
	var buf strings.Builder
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, buf.String(), nil
	case asExecExit(err, &ee):
		return ee.ExitCode(), buf.String(), nil
	default:
		return -1, buf.String(), err
	}
}

func asExecExit(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok { //nolint:errorlint // the concrete type is what exec returns
		*target = ee
		return true
	}
	return false
}

// runCheck runs one repository-declared analyzer.
//
// The distinction that matters is the one every recipe makes: a non-zero exit
// is a finding in the code, and a tool that could not run is not. The
// AppliesTo predicate already skips a check whose binary is absent, so
// reaching here and failing to execute means something broke rather than
// something is missing.
func runCheck(ctx context.Context, worktree string, c CheckStep) StepResult {
	res := StepResult{Kind: string(KindAnalyzer), Name: c.Name}
	code, out, err := runPhase(ctx, worktree, c.Argv)
	switch {
	case err != nil:
		res.Status, res.Phase = string(Error), "check"
		res.Headline = fmt.Sprintf("check %q could not run: %v", c.Name, err)
	case code != 0:
		res.Status, res.Phase, res.ExitCode = string(Fail), "check", code
		res.Headline = fmt.Sprintf("check %q reported findings", c.Name)
	default:
		res.Status = string(Pass)
		res.Headline = fmt.Sprintf("check %q found nothing", c.Name)
	}
	if res.Status != string(Pass) {
		res.Output = tail(out, maxStepOutput)
	}
	return res
}

// runIntegration is up, then test, then down — with down guaranteed.
func runIntegration(ctx context.Context, worktree string, s IntegrationStep) StepResult {
	res := StepResult{Kind: string(KindIntegration), Name: s.Name}

	if len(s.Up) > 0 {
		code, out, err := runPhase(ctx, worktree, s.Up)
		if err != nil || code != 0 {
			// Bringing the runtime up is not a verdict on the code: a database
			// that would not start says nothing about the change.
			res.Status, res.Phase, res.ExitCode = string(Error), "up", code
			res.Headline = fmt.Sprintf("integration %q: `up` failed", s.Name)
			if err != nil {
				res.Headline += ": " + err.Error()
			}
			res.Output = tail(out, maxStepOutput)
			// Tear down anyway: a partial `up` can still have left something.
			// teardown builds its own context deliberately — see its comment.
			res.Teardown = teardown(worktree, s) //nolint:contextcheck // teardown must outlive a cancelled step
			return res
		}
	}

	code, out, err := runPhase(ctx, worktree, s.Test)
	res.Teardown = teardown(worktree, s) //nolint:contextcheck // teardown must outlive a cancelled step

	switch {
	case err != nil:
		res.Status, res.Phase = string(Error), "test"
		res.Headline = fmt.Sprintf("integration %q: the test command could not run: %v", s.Name, err)
	case code != 0:
		res.Status, res.Phase, res.ExitCode = string(Fail), "test", code
		res.Headline = fmt.Sprintf("integration %q failed against a live runtime", s.Name)
	default:
		res.Status = string(Pass)
		res.Headline = fmt.Sprintf("integration %q passed against a live runtime", s.Name)
	}
	if res.Status != string(Pass) {
		res.Output = tail(out, maxStepOutput)
	}
	return res
}

func teardown(worktree string, s IntegrationStep) string {
	if len(s.Down) == 0 {
		return ""
	}
	// Teardown gets its own context: the step's context may already be
	// cancelled by a timeout, and that is exactly when something is left
	// running. Inheriting it would mean the teardown that matters most is the
	// one that never runs.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute) //nolint:contextcheck // see above
	defer cancel()
	code, _, err := runPhase(ctx, worktree, s.Down) //nolint:contextcheck // see above
	if err != nil {
		return "down failed to run: " + err.Error()
	}
	if code != 0 {
		return fmt.Sprintf("down exited %d; something may still be running", code)
	}
	return "down ok"
}

// runGenerate snapshots the declared outputs, runs the generator, compares,
// and restores.
//
// Restoring is what makes this safe to run inside verification. A generator
// that left the worktree changed would make every result recorded before it
// evidence about a candidate that no longer exists (§7.2), and would attribute
// generator output to the model's diff.
func runGenerate(ctx context.Context, worktree string, g GenerateStep) StepResult {
	res := StepResult{Kind: string(KindGenerate), Name: g.Name}

	before, err := snapshot(worktree, g.Outputs)
	if err != nil {
		res.Status, res.Phase = string(Error), "snapshot"
		res.Headline = fmt.Sprintf("generate %q: could not read the declared outputs: %v", g.Name, err)
		return res
	}

	code, out, runErr := runPhase(ctx, worktree, g.Argv)
	if runErr != nil || code != 0 {
		// A generator that will not run says nothing about whether the
		// committed output is current.
		res.Status, res.Phase, res.ExitCode = string(Error), "generate", code
		res.Headline = fmt.Sprintf("generate %q: the generator failed to run", g.Name)
		if runErr != nil {
			res.Headline += ": " + runErr.Error()
		}
		res.Output = tail(out, maxStepOutput)
		// A generator that failed partway can still have written files, so the
		// current state is re-read rather than assumed unchanged.
		partial, _ := snapshot(worktree, g.Outputs)
		_ = restore(worktree, before, partial)
		return res
	}

	after, err := snapshot(worktree, g.Outputs)
	if err != nil {
		res.Status, res.Phase = string(Error), "compare"
		res.Headline = fmt.Sprintf("generate %q: could not re-read the outputs: %v", g.Name, err)
		partial, _ := snapshot(worktree, g.Outputs)
		_ = restore(worktree, before, partial)
		return res
	}

	changed := diffSnapshots(before, after)
	if rerr := restore(worktree, before, after); rerr != nil {
		// Fail, not Error, and the distinction is deliberate even though this
		// is not a verdict on the code.
		//
		// An Error does not block acceptance — correctly, because a tool that
		// could not run says nothing. But a failed restore means the worktree
		// is no longer what any other result measured, and the recipes that
		// already ran cannot be trusted about it. Reporting that as "could not
		// run" would accept a task against a tree nobody has verified. Between
		// blocking and not blocking, an unknown worktree blocks.
		res.Status, res.Phase = string(Fail), "restore"
		res.Headline = fmt.Sprintf(
			"generate %q: the worktree could NOT be restored and is now in an unknown state: %v",
			g.Name, rerr)
		return res
	}

	if len(changed) == 0 {
		res.Status = string(Pass)
		res.Headline = fmt.Sprintf("generate %q: the committed output is current", g.Name)
		return res
	}
	res.Status, res.Phase = string(Fail), "compare"
	res.Changed = changed
	res.Headline = fmt.Sprintf("generate %q: %d generated file(s) are not what the generator produces",
		g.Name, len(changed))
	return res
}

// snapshot records the content and mode of every declared output path.
//
// The mode matters: restoring content alone would turn a generated script into
// a non-executable file, which is a mutation the check exists to avoid.
type snapFile struct {
	body []byte
	mode os.FileMode
}

type snap map[string]snapFile

func snapshot(worktree string, outputs []string) (snap, error) {
	s := snap{}
	for _, o := range outputs {
		root := filepath.Join(worktree, filepath.Clean(o))
		info, err := os.Stat(root)
		if err != nil {
			if os.IsNotExist(err) {
				// A declared output that does not exist yet is a legitimate
				// state: the generator is about to create it, and its
				// appearance is a difference.
				continue
			}
			return nil, err
		}
		if !info.IsDir() {
			body, err := os.ReadFile(root) //nolint:gosec // a declared path inside the worktree
			if err != nil {
				return nil, err
			}
			s[filepath.Clean(o)] = snapFile{body: body, mode: info.Mode().Perm()}
			continue
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			body, err := os.ReadFile(p) //nolint:gosec // under a declared path inside the worktree
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(worktree, p)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			s[rel] = snapFile{body: body, mode: info.Mode().Perm()}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

func diffSnapshots(before, after snap) []string {
	seen := map[string]bool{}
	var changed []string
	for p, b := range after {
		seen[p] = true
		a, had := before[p]
		if !had {
			changed = append(changed, p+" (would be created)")
			continue
		}
		if digest(a.body) != digest(b.body) || a.mode != b.mode {
			changed = append(changed, p)
		}
	}
	for p := range before {
		if !seen[p] {
			changed = append(changed, p+" (would be removed)")
		}
	}
	sort.Strings(changed)
	return changed
}

func digest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// restore puts the declared outputs back exactly as they were found.
//
// Both directions matter, and only one of them is obvious. Rewriting what
// changed is the easy half; **removing what the generator created** is the
// half that decides whether this is safe to run inside verification. A
// generator that adds a file — the common case when a schema gains a table —
// would otherwise leave it in the worktree, which is precisely the mutation
// the whole snapshot-and-restore exists to avoid: stale evidence (§7.2) and
// generator output in the model's diff at the gate.
//
// `after` is what the generator produced; anything in it that `before` did not
// have is removed.
func restore(worktree string, before, after snap) error {
	for p := range after {
		if _, had := before[p]; had {
			continue
		}
		if err := os.Remove(filepath.Join(worktree, p)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for p, f := range before {
		full := filepath.Join(worktree, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		mode := f.mode
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(full, f.body, mode); err != nil { //nolint:gosec // restoring content and mode the worktree already had
			return err
		}
		// WriteFile only applies a mode when it CREATES the file, and the
		// common case here is rewriting one that already exists — so a
		// generator that chmod'd its output would leave the new mode behind.
		if err := os.Chmod(full, mode); err != nil {
			return err
		}
	}
	return nil
}

// DeclaredSummary reads the JSON a declared step prints.
func DeclaredSummary(exitCode int, stdout, stderr string) (Status, Summary) {
	var r StepResult
	if err := decodeFirstJSON(stdout, &r); err != nil {
		return Generic(exitCode, stdout, stderr)
	}
	sum := Summary{Headline: r.Headline}
	if r.Teardown != "" && !strings.HasSuffix(r.Teardown, "ok") {
		// A failed teardown is not the verdict, but it is the thing that makes
		// the *next* run fail for an unrelated reason.
		sum.Findings = append(sum.Findings, Finding{Message: "teardown: " + r.Teardown})
	}
	for _, c := range r.Changed {
		sum.Findings = append(sum.Findings, Finding{File: c, Message: "not what the generator produces"})
	}
	if r.Output != "" {
		for _, l := range lastLines(r.Output, 5) {
			sum.Findings = append(sum.Findings, Finding{Message: truncateLine(l, 200)})
		}
	}
	shown, truncated := trimFindings(sum.Findings)
	sum.Findings, sum.Truncated = shown, truncated
	if r.Phase != "" {
		sum.Counts = countsOf(map[string]int{"failed_phase_" + r.Phase: 1})
	}
	switch Status(r.Status) {
	case Pass:
		return Pass, sum
	case Fail:
		return Fail, sum
	default:
		return Error, sum
	}
}

// WriteStepResult prints a result as the JSON the summarizer reads.
func WriteStepResult(w io.Writer, r StepResult) error {
	enc := json.NewEncoder(w)
	return enc.Encode(r)
}
