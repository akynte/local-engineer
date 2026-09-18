package eval

// Schema versions travel with every result file.
//
// A result read a year from now has to be interpretable by the code reading it,
// and the failure mode without versions is silent: a field changes meaning, an
// old file still parses, and the numbers come out wrong with no error. These
// are compared on load and a mismatch is reported rather than guessed at.
const (
	// BenchmarkSchemaVersion covers the Report and Outcome shapes.
	BenchmarkSchemaVersion = 2
	// TaskSchemaVersion covers the Task file format.
	TaskSchemaVersion = 2
	// ReportSchemaVersion covers the generated summary and its aggregates.
	ReportSchemaVersion = 1
)

// Status classifies how a run ended.
//
// The distinction that matters is between the system failing to do the work and
// the harness failing to ask it properly. A dependency that would not install
// is not evidence about the model, and counting it as a failure makes the
// system look worse than it is; hiding it makes the sample look larger than it
// is. Both are wrong, so every run carries a status and every status appears in
// the report.
type Status string

const (
	// StatusCompleted: the run finished and its verdict is evidence.
	StatusCompleted Status = "completed"
	// StatusTaskFailed: the run finished and did not solve the task. This is
	// evidence, and is distinct from completed only in that Solved is false —
	// it exists so a reader scanning statuses can see the shape of the set.
	StatusTaskFailed Status = "task_failed"
	// StatusEnvironmentFailed: the harness or the machine failed. Not
	// evidence about the system under test.
	StatusEnvironmentFailed Status = "environment_failed"
	// StatusTimeout: the run exceeded its wall-clock budget. Reported
	// separately because a budget that is too small is a harness parameter,
	// not a property of the system.
	StatusTimeout Status = "timeout"
	// StatusInvalid: an integrity rule was broken — hidden tests reachable,
	// the worktree not at the base commit, benchmark contamination. Never
	// counted as evidence in either direction.
	StatusInvalid Status = "invalid"
	// StatusCancelled: the operator stopped it.
	StatusCancelled Status = "cancelled"
)

// Evidence reports whether a run may be counted in a rate.
//
// Only completed and task_failed runs say anything about the system. The rest
// are kept, shown, and excluded from every denominator.
func (s Status) Evidence() bool {
	return s == StatusCompleted || s == StatusTaskFailed
}

// Statuses lists every status, for reporting the shape of a run set.
func Statuses() []Status {
	return []Status{StatusCompleted, StatusTaskFailed, StatusEnvironmentFailed,
		StatusTimeout, StatusInvalid, StatusCancelled}
}

// Set separates the tasks parameters may be tuned on from the tasks they are
// judged on.
//
// A threshold chosen because it scored well on a task is no longer measured by
// that task. Keeping the two sets apart in the schema rather than in a
// convention means a report can state which one it ran on, and a reader does
// not have to take anyone's word for it.
type Set string

const (
	// SetDev is the tuning set: ranking weights, thresholds and retrieval
	// constants may be fitted here.
	SetDev Set = "dev"
	// SetHeldout is the judged set. Nothing may be tuned against it.
	SetHeldout Set = "heldout"
)

// Sets lists both, for validation.
func Sets() []Set { return []Set{SetDev, SetHeldout} }
