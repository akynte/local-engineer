// Package eval is the evaluation harness of design v3 §10.2.
//
// The design commits to publishing only what this measures: "for each task
// category, the accepted-task rate within a fixed budget for (a) the same
// local model … (b) the supervised system, (c) a frontier agent", with the
// task set, hardware, model manifest and scripts published so anyone can
// reproduce or dispute the numbers.
//
// Three properties make the difference between a measurement and a story.
//
// # Hidden acceptance
//
// A task's acceptance tests are never in the worktree while the task runs.
// They are applied afterwards, to a copy. A model that can read the test can
// satisfy it without solving the problem — and that failure looks exactly like
// success in the results.
//
// # Ground truth separate from the system's own verdict
//
// The supervised system decides acceptance from the evidence it gathered. The
// harness decides it from the hidden tests. When those disagree in the
// system's favour, that is a *false acceptance*: the system claimed success
// and was wrong. It is the single most damaging failure mode a tool like this
// has, and it is reported as its own number rather than averaged away.
//
// # Leak disclosure
//
// A task the model has seen in training gives an inflated number. Every task
// declares its leak risk, and a report that mixes risks says so.
package eval

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Category groups tasks so results can be reported per kind of work. Averaging
// a bug fix with a refactor hides which one the system is bad at.
type Category string

const (
	CategoryBugFix    Category = "bug_fix"
	CategoryFeature   Category = "feature"
	CategoryRefactor  Category = "refactor"
	CategorySchema    Category = "schema_change"
	CategoryAPIChange Category = "api_change"
	CategoryTestGap   Category = "test_gap"
)

// Categories lists every category, for reporting.
func Categories() []Category {
	return []Category{CategoryBugFix, CategoryFeature, CategoryRefactor,
		CategorySchema, CategoryAPIChange, CategoryTestGap}
}

// LeakRisk records how likely it is that a model has seen this task before.
//
// This is disclosed rather than assumed away. A result computed over tasks
// drawn from a public dataset means something different from one computed over
// tasks written for this repository, and a report that does not say which is
// not a result anyone should act on.
type LeakRisk string

const (
	// LeakNone: the task was written for this repository and has never been
	// published. Not a guarantee — nothing is — but the strongest claim
	// available.
	LeakNone LeakRisk = "none"
	// LeakSynthetic: the fixture is generated, so the specific code cannot
	// have been trained on, though the pattern may have been.
	LeakSynthetic LeakRisk = "synthetic"
	// LeakPublic: drawn from a public dataset or a public repository. Results
	// over these are an upper bound, not an estimate.
	LeakPublic LeakRisk = "public"
	// LeakUnknown: provenance not established. Reported separately; never
	// silently folded into a headline number.
	LeakUnknown LeakRisk = "unknown"
)

// Task is one evaluation case.
type Task struct {
	ID       string   `yaml:"id"`
	Category Category `yaml:"category"`
	// Objective is what the system is asked to do, in the words a user would
	// use. It must not name the fix: an objective that says "change x - y to
	// x + y" measures nothing.
	Objective string `yaml:"objective"`
	// Fixture is the directory, relative to the task file, copied in as the
	// starting state.
	Fixture string `yaml:"fixture"`
	// Scope restricts which paths a solution may change, as a real task would.
	Scope []string `yaml:"scope,omitempty"`
	// Acceptance is the ground truth, applied only after the run.
	Acceptance Acceptance `yaml:"acceptance"`
	// Budget bounds the attempt.
	Budget Budget `yaml:"budget"`
	// Verification is the level the supervised arm runs at.
	Verification string `yaml:"verification"`
	// LeakRisk is disclosed per task, never assumed.
	LeakRisk LeakRisk `yaml:"leak_risk"`
	// Set decides whether this task may be tuned against. Ranking weights,
	// thresholds and retrieval constants may be fitted on the dev set; a task
	// in the held-out set is judged on and never fitted to, because a
	// threshold chosen because it scored well on a task is no longer measured
	// by that task. Tasks written before the split default to dev, which is
	// the conservative reading: it keeps them out of held-out headline numbers.
	Set Set `yaml:"set,omitempty"`
	// Expected records the ground truth for localization scoring where it is
	// known — the files and symbols the real fixing commit touched. It is
	// applied only when scoring, never shown to the system.
	Expected Expected `yaml:"expected,omitempty"`
	// Notes record anything a reader of the results would need to interpret
	// them: why the task is hard, what a wrong-but-passing solution looks like.
	Notes string `yaml:"notes,omitempty"`

	// path is where the task was loaded from, for resolving the fixture.
	path string
}

// Expected is localization ground truth, used to score retrieval rather than
// to decide the task. It is optional: a task with no known fixing commit still
// measures success, it just cannot contribute to recall.
type Expected struct {
	// Files the real fix touched, repository-relative.
	Files []string `yaml:"files,omitempty"`
	// Symbols the real fix changed.
	Symbols []string `yaml:"symbols,omitempty"`
	// Callers that had to change because of the fix, where known.
	Callers []string `yaml:"callers,omitempty"`
	// Tests that cover the change, where known.
	Tests []string `yaml:"tests,omitempty"`
}

// Known reports whether this task can contribute to localization scoring.
func (e Expected) Known() bool { return len(e.Files) > 0 || len(e.Symbols) > 0 }

// Acceptance is the hidden ground truth for a task.
type Acceptance struct {
	// Files are written into the worktree copy *after* the run, before the
	// command executes. They are never present while the task is being solved.
	Files map[string]string `yaml:"files"`
	// Argv decides the task. A non-zero exit means the task was not solved.
	Argv []string `yaml:"argv"`
	// Timeout bounds the check.
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// MustNotChange lists paths a solution must leave alone. A task that
	// "passes" by deleting the failing test has not been solved, and without
	// this the harness would score it as a success.
	MustNotChange []string `yaml:"must_not_change,omitempty"`
}

// Budget bounds one attempt.
type Budget struct {
	MaxAttempts    int `yaml:"max_attempts"`
	MaxWallSeconds int `yaml:"max_wall_seconds"`
	MaxTokens      int `yaml:"max_tokens,omitempty"`
}

// FixturePath resolves the fixture directory.
func (t Task) FixturePath() string {
	if filepath.IsAbs(t.Fixture) {
		return t.Fixture
	}
	return filepath.Join(filepath.Dir(t.path), t.Fixture)
}

// Validate rejects a task that cannot produce a meaningful measurement.
//
// Each rule here corresponds to a way a task set can quietly stop measuring
// what it claims to.
func (t Task) Validate() error {
	var problems []string

	if t.ID == "" {
		problems = append(problems, "no id")
	}
	if t.Objective == "" {
		problems = append(problems, "no objective")
	}
	if t.Category == "" {
		problems = append(problems, "no category; results are reported per category")
	}
	if t.Set != "" && t.Set != SetDev && t.Set != SetHeldout {
		problems = append(problems, fmt.Sprintf("set %q is not dev or heldout", t.Set))
	}
	if t.LeakRisk == "" {
		problems = append(problems, "no leak_risk; provenance is disclosed, never assumed")
	}
	if t.Fixture == "" {
		problems = append(problems, "no fixture")
	}
	if len(t.Acceptance.Argv) == 0 {
		problems = append(problems,
			"no acceptance command; a task the harness cannot decide is not an evaluation task")
	}
	if len(t.Acceptance.Files) == 0 {
		problems = append(problems,
			"no hidden acceptance files; if the test is already in the fixture the model can read it, "+
				"and satisfying a test you can see is not solving the problem")
	}
	// A hidden file that is also in the fixture is not hidden.
	for path := range t.Acceptance.Files {
		full := filepath.Join(t.FixturePath(), filepath.FromSlash(path))
		if _, err := os.Stat(full); err == nil {
			problems = append(problems, fmt.Sprintf(
				"acceptance file %s is also present in the fixture, so it is visible to the model", path))
		}
	}
	if t.Budget.MaxAttempts <= 0 {
		problems = append(problems, "no attempt budget")
	}
	if t.Budget.MaxWallSeconds <= 0 {
		problems = append(problems, "no wall-clock budget; a result without a fixed budget is not comparable")
	}

	if len(problems) > 0 {
		return fmt.Errorf("task %q: %s", t.ID, strings.Join(problems, "; "))
	}
	return nil
}

// Timeout returns the acceptance command's bound.
func (a Acceptance) Timeout() time.Duration {
	if a.TimeoutSeconds > 0 {
		return time.Duration(a.TimeoutSeconds) * time.Second
	}
	return 5 * time.Minute
}

// WallClock returns the attempt's bound.
func (b Budget) WallClock() time.Duration {
	if b.MaxWallSeconds > 0 {
		return time.Duration(b.MaxWallSeconds) * time.Second
	}
	return 10 * time.Minute
}

// LoadTask reads one task file.
// Membership reports the task's set, defaulting an unlabelled task to dev.
//
// Defaulting to dev is the conservative direction: an unlabelled task cannot
// accidentally end up in a held-out headline number, which is the error that
// would matter.
func (t Task) Membership() Set {
	if t.Set == "" {
		return SetDev
	}
	return t.Set
}

// Synthetic reports whether the fixture is generated rather than drawn from
// real history. Synthetic tasks are reported separately and never folded into
// a headline rate over real tasks.
func (t Task) Synthetic() bool { return t.LeakRisk == LeakSynthetic }

func LoadTask(path string) (Task, error) {
	body, err := os.ReadFile(path) //nolint:gosec // an operator-supplied task file
	if err != nil {
		return Task{}, err
	}
	var t Task
	dec := yaml.NewDecoder(strings.NewReader(string(body)))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return Task{}, fmt.Errorf("eval: parse %s: %w", path, err)
	}
	t.path = path
	return t, t.Validate()
}

// LoadSet reads every task under a directory, in a stable order.
//
// A malformed task is an error rather than a skip: silently running a smaller
// set than the one named in the results is how a number stops meaning what it
// says.
func LoadSet(dir string) ([]Task, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".task.yaml") {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("eval: no .task.yaml files under %s", dir)
	}
	sort.Strings(paths)

	var tasks []Task
	var problems []error
	seen := map[string]string{}
	for _, p := range paths {
		t, err := LoadTask(p)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if prev, dup := seen[t.ID]; dup {
			problems = append(problems, fmt.Errorf("eval: duplicate task id %q in %s and %s", t.ID, prev, p))
			continue
		}
		seen[t.ID] = p
		tasks = append(tasks, t)
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return tasks, nil
}
