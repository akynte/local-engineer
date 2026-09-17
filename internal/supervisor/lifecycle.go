package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/ledger"
	"github.com/akynte/local-engineer/internal/policy"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/task"
)

// The lifecycle primitives below are what an executor that is not Local
// Engineer's own model loop needs in order to be supervised.
//
// `le task run` drives a model and owns everything between start and finish, so
// it never needed these as separate calls. An editor's agent does own that
// middle: it edits, it asks the user questions, it decides when it is done. What
// it cannot do is judge itself, and what it does not do is remember. These
// record what happened and judge the result, without taking the keyboard.
//
// The states are the ones the ledger's CHECK constraint already permits. A
// parallel vocabulary — STARTED, EXECUTING, VERIFYING — would read well in a
// diagram and would not be writable to the database, so the diagram's states are
// mapped onto the existing ones and the finer detail lives in the journal, which
// is where a sequence of events belongs anyway.

// Started maps to pending: the task exists and its intent is recorded.
// Executing maps to running. Verified maps to review — the work passed the
// contract and is awaiting the human's reading of it. Finished maps to accepted.

// Review is the final record of a supervised task.
//
// It is not an approval. By the time it exists the agent has already edited the
// working tree, so there is nothing left to withhold; the change is in git and
// `git diff` is the authoritative view of it. What this adds is the part git
// cannot reconstruct: what was asked for, what the user decided along the way,
// what was checked, and what the contract concluded.
type Review struct {
	TaskID            string        `json:"task_id"`
	Intent            string        `json:"intent"`
	Verdict           string        `json:"verdict"`
	Decisions         []Decision    `json:"decisions,omitempty"`
	FilesChangedCount int           `json:"files_changed_count"`
	FilesChanged      []string      `json:"files_changed,omitempty"`
	Checks            []CheckResult `json:"checks,omitempty"`
	Protected         []string      `json:"protected_violations,omitempty"`
	Warnings          []string      `json:"warnings,omitempty"`
	FinishedAt        time.Time     `json:"finished_at"`
}

// Decision is one question the executor had to ask, and what it was told.
type Decision struct {
	Question string    `json:"question"`
	Answer   string    `json:"answer"`
	At       time.Time `json:"at"`
}

// CheckResult is one verification recipe's verdict, flattened for a reader.
type CheckResult struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Headline string `json:"headline"`
}

// StartTask opens a supervised task and journals the intent before any work.
//
// §7.1's ordering is the reason this is a call the executor makes rather than
// something inferred later: the intent is durable before the side effect, so an
// interrupted session leaves a state that can be reconciled instead of guessed.
func StartTask(ctx context.Context, st *store.Store, objective string, level recipe.Level) (task.Task, error) {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return task.Task{}, fmt.Errorf("supervisor: a task needs an objective")
	}
	t := task.Task{
		ID:           task.NewID("oc"),
		Title:        objective,
		Kind:         "supervised",
		Verification: level,
		Budget:       task.Budget{MaxAttempts: 1, MaxWallTime: 30 * time.Minute},
	}
	if err := task.NewStore(st).Create(ctx, t); err != nil {
		return task.Task{}, err
	}
	if err := RecordEvent(ctx, st, t.ID, ledger.KindSessionStart,
		map[string]any{"objective": objective, "executor": "opencode"}); err != nil {
		return t, err
	}
	return t, nil
}

// RecordEvent writes one journal entry for something the executor did.
//
// It completes immediately: the executor is reporting an action it has already
// taken, so there is no window between intent and effect for this journal to
// protect. What it preserves is the sequence, which is what makes a session
// reconstructable afterwards.
func RecordEvent(ctx context.Context, st *store.Store, taskID string, kind ledger.Kind, detail any) error {
	l := ledger.New(st)
	h, err := l.Begin(ctx, taskID, kind, detail, "")
	if err != nil {
		return err
	}
	return h.Complete(ctx, detail, "", "")
}

// RecordAnswer stores a question the executor asked the user and the answer it
// was given.
//
// Local Engineer does not ask the questions — it has no channel to the user, and
// the agent holding the conversation does. But the answer is a decision about
// this project, and a decision that lives only in a chat transcript is lost to
// the next session. This is where it stops being conversational and becomes part
// of the record.
func RecordAnswer(ctx context.Context, st *store.Store, taskID, question, answer string) error {
	question, answer = strings.TrimSpace(question), strings.TrimSpace(answer)
	if question == "" || answer == "" {
		return fmt.Errorf("supervisor: a decision needs both the question and the answer")
	}
	return RecordEvent(ctx, st, taskID, ledger.KindDecision, map[string]any{
		"question": question,
		"answer":   answer,
		"source":   "user",
	})
}

// Decisions reads back what the user was asked during a task.
func Decisions(ctx context.Context, st *store.Store, taskID string) ([]Decision, error) {
	ops, err := ledger.New(st).Operations(ctx, taskID)
	if err != nil {
		return nil, err
	}
	var out []Decision
	for _, op := range ops {
		if op.Kind != ledger.KindDecision {
			continue
		}
		var d struct {
			Question string `json:"question"`
			Answer   string `json:"answer"`
			Source   string `json:"source"`
		}
		if err := json.Unmarshal(op.Intent, &d); err != nil || d.Source != "user" {
			continue
		}
		// The ledger stamps in milliseconds; the index stamps in seconds. Being
		// explicit here rather than assuming is how the other one was found.
		out = append(out, Decision{Question: d.Question, Answer: d.Answer, At: time.UnixMilli(op.StartedAt).UTC()})
	}
	return out, nil
}

// VerificationRecord is a verification result as the journal keeps it.
type VerificationRecord struct {
	Accepted   bool          `json:"accepted"`
	Candidate  string        `json:"candidate"`
	Reasons    []string      `json:"reasons,omitempty"`
	OutOfScope []string      `json:"out_of_scope,omitempty"`
	Checks     []CheckResult `json:"checks,omitempty"`
}

// RecordVerification journals a verification against the task it judged.
//
// Durable rather than held in memory: the server is long-lived and may be
// restarted between a verification and the finish that reads it, and a review
// that silently forgot what was verified would report UNVERIFIED for work that
// passed. The journal already survives that, so it is where this belongs.
func RecordVerification(ctx context.Context, st *store.Store, taskID string, o *task.Outcome) error {
	if o == nil {
		return nil
	}
	rec := VerificationRecord{
		Accepted: o.Accepted, Candidate: o.Candidate,
		Reasons: o.Reasons, OutOfScope: o.OutOfScope,
	}
	for _, r := range o.Results {
		rec.Checks = append(rec.Checks, CheckResult{
			Name: r.Recipe, Status: string(r.Status), Headline: r.Summary.Headline,
		})
	}
	return RecordEvent(ctx, st, taskID, ledger.KindRecipeRun, rec)
}

// LastVerification returns the most recent verification recorded for a task.
//
// The most recent rather than the first: an executor that fixes what the
// contract reported and verifies again must be judged on the second result, or
// the loop the design asks for could never succeed.
func LastVerification(ctx context.Context, st *store.Store, taskID string) (VerificationRecord, bool, error) {
	ops, err := ledger.New(st).Operations(ctx, taskID)
	if err != nil {
		return VerificationRecord{}, false, err
	}
	var found bool
	var latest VerificationRecord
	for _, op := range ops {
		if op.Kind != ledger.KindRecipeRun {
			continue
		}
		var rec VerificationRecord
		if err := json.Unmarshal(op.Intent, &rec); err != nil {
			continue
		}
		latest, found = rec, true
	}
	return latest, found, nil
}

// FinishTask closes a supervised task and produces its final review.
//
// The verdict comes from the verification that was actually run, not from the
// executor's opinion of its own work: a caller that never verified gets a review
// saying so rather than one saying the work is fine.
func FinishTask(ctx context.Context, st *store.Store, repoRoot, taskID string) (Review, error) {
	ts := task.NewStore(st)
	t, err := ts.Get(ctx, taskID)
	if err != nil {
		return Review{}, err
	}

	rev := Review{TaskID: t.ID, Intent: t.Title, FinishedAt: time.Now().UTC()}
	if rev.Decisions, err = Decisions(ctx, st, taskID); err != nil {
		return Review{}, err
	}

	verified, ok, err := LastVerification(ctx, st, taskID)
	if err != nil {
		return Review{}, err
	}
	switch {
	case !ok:
		rev.Verdict = "UNVERIFIED"
		rev.Warnings = append(rev.Warnings,
			"this task was finished without a verification run, so nothing checked the work")
	case verified.Accepted:
		rev.Verdict = "VERIFIED"
	default:
		rev.Verdict = "NOT VERIFIED"
		rev.Warnings = append(rev.Warnings, verified.Reasons...)
	}
	rev.Checks = verified.Checks
	rev.Protected = verified.OutOfScope

	rev.FilesChanged = changedFiles(ctx, repoRoot)
	rev.FilesChangedCount = len(rev.FilesChanged)
	if rev.FilesChangedCount == 0 {
		rev.Warnings = append(rev.Warnings,
			"the working tree is unchanged: this task recorded no edits")
	}
	if violations := protectedViolations(repoRoot, rev.FilesChanged); len(violations) > 0 {
		rev.Protected = append(rev.Protected, violations...)
		rev.Warnings = append(rev.Warnings,
			"files this repository protects were changed; review them before committing")
	}

	if err := RecordEvent(ctx, st, taskID, ledger.KindReview, rev); err != nil {
		return rev, err
	}
	state := task.StateAccepted
	if rev.Verdict != "VERIFIED" {
		// A task that did not pass is not accepted. Recording it as such would
		// make the journal agree with the executor rather than with the
		// evidence, which is the failure the contract exists to prevent.
		state = task.StateFailed
	}
	if err := ts.SetState(ctx, taskID, state); err != nil {
		return rev, err
	}
	return rev, nil
}

// changedFiles lists what the working tree has that HEAD does not.
//
// git is the source of truth for what changed: the executor's account of which
// files it touched is a claim, and this is the fact.
func changedFiles(ctx context.Context, repoRoot string) []string {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain=v1") //nolint:gosec // a fixed argv against a workspace root
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parsePorcelain(string(out))
}

// parsePorcelain reads `git status --porcelain=v1`.
//
// The whole output must not be trimmed before splitting: the first two columns
// are status codes and an unmodified-in-index file begins with a space, so
// trimming the blob shifts that line left by one and eats the first character
// of its path. That produced "imit.go" for a modified limit.go, which is the
// kind of defect that reads as a rendering quirk until someone tries to open
// the file.
func parsePorcelain(out string) []string {
	var files []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 4 {
			continue
		}
		path := line[3:]
		// A rename is reported as "old -> new". The new name is the one that
		// exists now, and the one a reader wants.
		if _, after, ok := strings.Cut(path, " -> "); ok {
			path = after
		}
		if path = strings.TrimSpace(path); path != "" {
			files = append(files, path)
		}
	}
	sort.Strings(files)
	return files
}

// protectedViolations reports changed files that the repository's policy
// protects. It is checked here as well as during verification because a task
// may be finished without one.
func protectedViolations(repoRoot string, changed []string) []string {
	set, err := policy.Load(repoRoot + "/policies")
	if err != nil || len(set.Policies) == 0 {
		return nil
	}
	hits := set.Check(changed)
	var out []string
	for _, h := range hits {
		out = append(out, h.Path)
	}
	return out
}

// Format renders a review for a person to read.
func (r Review) Format() string {
	var b strings.Builder
	b.WriteString("FINAL REVIEW\n\n")
	fmt.Fprintf(&b, "Task:   %s\n", r.Intent)
	fmt.Fprintf(&b, "Status: %s\n\n", r.Verdict)

	fmt.Fprintf(&b, "Changes:\n  %d file(s) in the working tree\n", r.FilesChangedCount)
	for _, f := range r.FilesChanged {
		fmt.Fprintf(&b, "    %s\n", f)
	}
	if len(r.Checks) > 0 {
		b.WriteString("\nVerification:\n")
		for _, c := range r.Checks {
			mark := "x"
			if c.Status == "pass" {
				mark = "✓"
			}
			fmt.Fprintf(&b, "  %s %-16s %s\n", mark, c.Name, c.Headline)
		}
	}
	if len(r.Decisions) > 0 {
		b.WriteString("\nUser decisions:\n")
		for _, d := range r.Decisions {
			fmt.Fprintf(&b, "  - %s\n    %s\n", d.Question, d.Answer)
		}
	}
	if len(r.Protected) > 0 {
		b.WriteString("\nProtected or out-of-scope paths touched:\n")
		for _, p := range r.Protected {
			fmt.Fprintf(&b, "  %s\n", p)
		}
	}
	b.WriteString("\nWarnings:\n")
	if len(r.Warnings) == 0 {
		b.WriteString("  none\n")
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "  %s\n", w)
	}
	b.WriteString("\nThe change itself is in git. `git diff` is the authoritative view of it; " +
		"this record is what git cannot reconstruct.\n")
	return b.String()
}
