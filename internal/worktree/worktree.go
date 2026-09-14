// Package worktree manages the per-task checkouts a task edits in.
//
// A task never edits the operator's working copy. It gets its own git
// worktree, so an interrupted or abandoned task leaves nothing behind in the
// directory the human is working in, and "what did this task change" is a diff
// rather than a reconstruction.
//
// The worktree is also what makes the journal's candidate hashes meaningful:
// a candidate identifies the exact content of one task's checkout (design v3
// §7.1), which only works if nothing else is writing to it.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/ledger"
)

// gitTimeout bounds every git invocation here.
const gitTimeout = 2 * time.Minute

// Worktree is one task's checkout.
type Worktree struct {
	// ID is the worktree identifier used for leases and in the ledger.
	ID string
	// Path is the absolute path of the checkout.
	Path string
	// Branch is the branch created for the task.
	Branch string
	// Base is the commit the task started from.
	Base string
	// Repo is the repository this worktree belongs to.
	Repo string
}

// Manager creates and removes worktrees under a root directory.
type Manager struct {
	// Root is where checkouts are created, normally the workspace's own
	// directory so they are removed with it.
	Root string
}

// NewManager binds a manager to a root directory.
func NewManager(root string) (*Manager, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("worktree: create %s: %w", root, err)
	}
	return &Manager{Root: root}, nil
}

// ErrNotARepository is returned when the source is not a git repository.
var ErrNotARepository = errors.New("worktree: not a git repository")

// Create makes a worktree for a task from the repository at repoPath.
//
// The branch name embeds the task id so an abandoned checkout is traceable to
// the task that made it, and `git worktree list` is readable by a human
// looking for what went wrong.
func (m *Manager) Create(ctx context.Context, repoPath, taskID string) (*Worktree, error) {
	if !IsRepository(ctx, repoPath) {
		return nil, fmt.Errorf("%w: %s", ErrNotARepository, repoPath)
	}
	base, err := git(ctx, repoPath, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("worktree: resolve HEAD: %w", err)
	}

	id := "wt-" + taskID
	branch := "le/task/" + taskID
	path := filepath.Join(m.Root, id)

	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("worktree: %s already exists; the previous task did not clean up", path)
	}
	// Remove a stale registration left by a crash, so a reused task id does
	// not fail on git's bookkeeping rather than on anything real.
	_, _ = git(ctx, repoPath, "worktree", "prune")

	if _, err := git(ctx, repoPath, "worktree", "add", "-b", branch, path, base); err != nil {
		return nil, fmt.Errorf("worktree: add: %w", err)
	}
	return &Worktree{ID: id, Path: path, Branch: branch, Base: base, Repo: repoPath}, nil
}

// Open returns a handle for an existing worktree, used after a restart when
// recovery needs to reconcile a checkout it did not create.
func (m *Manager) Open(ctx context.Context, repoPath, taskID string) (*Worktree, error) {
	id := "wt-" + taskID
	path := filepath.Join(m.Root, id)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("worktree: %s does not exist: %w", path, err)
	}
	base, _ := git(ctx, path, "rev-parse", "HEAD")
	return &Worktree{ID: id, Path: path, Branch: "le/task/" + taskID, Base: base, Repo: repoPath}, nil
}

// Remove deletes a worktree and its branch.
//
// keepBranch preserves the branch so a rejected task's work can still be
// inspected. Deleting the evidence of a failure by default would be the wrong
// trade: disk is cheap and a lost debugging trail is not.
func (m *Manager) Remove(ctx context.Context, wt *Worktree, keepBranch bool) error {
	if wt == nil {
		return nil
	}
	if _, err := git(ctx, wt.Repo, "worktree", "remove", "--force", wt.Path); err != nil {
		// Fall back to removing the directory and pruning the registration:
		// an unremovable worktree must not strand the task.
		if rmErr := os.RemoveAll(wt.Path); rmErr != nil {
			return errors.Join(err, rmErr)
		}
		_, _ = git(ctx, wt.Repo, "worktree", "prune")
	}
	if !keepBranch {
		_, _ = git(ctx, wt.Repo, "branch", "-D", wt.Branch)
	}
	return nil
}

// Candidate returns the content manifest of the worktree: the identifier the
// journal records as candidate_before and candidate_after (§7.1).
func (wt *Worktree) Candidate() (string, error) {
	return ledger.ContentManifest(wt.Path)
}

// Diff returns the unified diff of the worktree against its base commit,
// including untracked files. Untracked files matter: a task that adds a file
// and does not stage it has still changed the candidate.
func (wt *Worktree) Diff(ctx context.Context) (string, error) {
	// Staging everything into the index makes `git diff --cached` see new
	// files too, without committing anything.
	if _, err := git(ctx, wt.Path, "add", "-A"); err != nil {
		return "", err
	}
	out, err := git(ctx, wt.Path, "diff", "--cached", wt.Base)
	if err != nil {
		return "", err
	}
	return out, nil
}

// ChangedFiles lists the paths the task has changed relative to its base.
// This is what the out-of-scope write check compares against: §6.2 records
// that such writes are detected by diff rather than prevented.
func (wt *Worktree) ChangedFiles(ctx context.Context) ([]string, error) {
	if _, err := git(ctx, wt.Path, "add", "-A"); err != nil {
		return nil, err
	}
	out, err := git(ctx, wt.Path, "diff", "--cached", "--name-only", wt.Base)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			files = append(files, p)
		}
	}
	return files, nil
}

// OutOfScope returns the changed files that are not covered by the allowed
// prefixes. An empty allow list means every change is in scope.
func (wt *Worktree) OutOfScope(ctx context.Context, allowed []string) ([]string, error) {
	changed, err := wt.ChangedFiles(ctx)
	if err != nil {
		return nil, err
	}
	if len(allowed) == 0 {
		return nil, nil
	}
	var out []string
	for _, f := range changed {
		ok := false
		for _, prefix := range allowed {
			if f == prefix || strings.HasPrefix(f, strings.TrimSuffix(prefix, "/")+"/") {
				ok = true
				break
			}
		}
		if !ok {
			out = append(out, f)
		}
	}
	return out, nil
}

// Reset discards every change, returning the worktree to its base commit. It
// is the escape hatch when recovery finds a partially applied edit and the
// operator chooses to start the step again.
func (wt *Worktree) Reset(ctx context.Context) error {
	if _, err := git(ctx, wt.Path, "reset", "--hard", wt.Base); err != nil {
		return err
	}
	_, err := git(ctx, wt.Path, "clean", "-fd")
	return err
}

// IsRepository reports whether a path is inside a git working tree.
func IsRepository(ctx context.Context, path string) bool {
	out, err := git(ctx, path, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	//nolint:gosec // the subcommand arguments are constants from this file; dir is passed as -C
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "GIT_OPTIONAL_LOCKS=0")

	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// SyncFrom copies the source repository's uncommitted state into the worktree.
//
// A worktree is created from a commit, so without this it holds the last
// committed code — and verifying that while the operator is looking at
// uncommitted changes produces a pass that describes code nobody is running.
// A silent false pass is the worst failure this system can have, so which
// state was verified is never left implicit: callers ask for this explicitly,
// and Report says what happened.
//
// Nothing in the source repository is modified: the tracked diff is read with
// `git diff HEAD`, which touches neither the index nor the working tree, and
// untracked files are copied.
func (wt *Worktree) SyncFrom(ctx context.Context, srcRepo string) (SyncReport, error) {
	var rep SyncReport

	// Tracked modifications, staged and unstaged, against the same base the
	// worktree was created from.
	diff, err := gitRaw(ctx, srcRepo, "diff", "HEAD", "--binary")
	if err != nil {
		return rep, fmt.Errorf("worktree: read uncommitted changes: %w", err)
	}
	if strings.TrimSpace(diff) != "" {
		if err := gitApply(ctx, wt.Path, diff); err != nil {
			return rep, fmt.Errorf("worktree: apply uncommitted changes: %w", err)
		}
		rep.Applied = true
	}

	// Untracked files are not in any diff, but they are part of what the
	// operator is looking at.
	others, err := git(ctx, srcRepo, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return rep, fmt.Errorf("worktree: list untracked files: %w", err)
	}
	for _, rel := range strings.Split(others, "\x00") {
		if rel == "" {
			continue
		}
		src := filepath.Join(srcRepo, filepath.FromSlash(rel))
		info, err := os.Stat(src)
		if err != nil || info.IsDir() {
			continue
		}
		body, err := os.ReadFile(src) //nolint:gosec // a path git itself listed inside the source repository
		if err != nil {
			return rep, err
		}
		dst := filepath.Join(wt.Path, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return rep, err
		}
		// dst is the worktree root joined with a relative path git itself
		// listed inside the source repository; nothing from outside reaches it.
		if err := os.WriteFile(dst, body, info.Mode().Perm()); err != nil { //nolint:gosec // see above
			return rep, err
		}
		rep.Untracked = append(rep.Untracked, rel)
	}
	return rep, nil
}

// SyncReport says what SyncFrom brought across, so a caller can report which
// state it actually verified.
type SyncReport struct {
	// Applied reports whether tracked modifications were carried over.
	Applied bool `json:"applied"`
	// Untracked lists the untracked files copied in.
	Untracked []string `json:"untracked,omitempty"`
}

// Clean reports whether the source had nothing uncommitted, meaning the
// worktree already matched it.
func (s SyncReport) Clean() bool { return !s.Applied && len(s.Untracked) == 0 }

// Describe renders the report for a human.
func (s SyncReport) Describe() string {
	switch {
	case s.Clean():
		return "the working tree was clean; this verifies the committed state"
	case s.Applied && len(s.Untracked) > 0:
		return fmt.Sprintf("verifying your uncommitted changes, including %d untracked file(s)", len(s.Untracked))
	case s.Applied:
		return "verifying your uncommitted changes"
	default:
		return fmt.Sprintf("verifying with %d untracked file(s) included", len(s.Untracked))
	}
}

// gitRaw runs git and returns stdout without trimming, for diffs where
// trailing newlines are significant.
func gitRaw(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	//nolint:gosec // the subcommand arguments are constants from this file; dir is passed as -C
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "GIT_OPTIONAL_LOCKS=0")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// gitApply feeds a patch to `git apply` on stdin.
func gitApply(ctx context.Context, dir, patch string) error {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", dir, "apply", "--whitespace=nowarn", "-")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	cmd.Stdin = strings.NewReader(patch)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr.String()))
	}
	return nil
}
