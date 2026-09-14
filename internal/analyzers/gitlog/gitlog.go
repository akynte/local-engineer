// Package gitlog contributes the commit-history row of design v3 §3.2:
// "commit to file and symbol | git log -L, blame | observed".
//
// These edges are `observed`, not `resolved`: history records what happened,
// which is evidence about a relationship rather than proof of one. Two files
// that always change together may be coupled, or may just have been touched by
// the same refactor — so the graph records the co-occurrence and lets the
// consumer decide what it means.
package gitlog

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// Analyzer reads commit history.
type Analyzer struct {
	// MaxCommits bounds how far back history is read. A deep history adds
	// little signal and a lot of edges, and the most recent work is what a
	// task is usually about.
	MaxCommits int
	// Timeout bounds the git invocation.
	Timeout time.Duration
	// MaxFilesPerCommit skips sweeping commits. A commit touching hundreds of
	// files is a reformat or a vendor drop; it says nothing about coupling and
	// would add a quadratic number of edges.
	MaxFilesPerCommit int
	Warnf             func(format string, args ...any)
}

// New returns an analyzer with the shipped defaults.
func New() *Analyzer {
	return &Analyzer{MaxCommits: 500, Timeout: 60 * time.Second, MaxFilesPerCommit: 40}
}

func (a *Analyzer) Name() string { return "gitlog" }

// Handles accepts everything: the analyzer works on the repository, not on
// individual files, and needs the file set only to know what is indexed.
func (a *Analyzer) Handles(index.File) bool { return true }

func (a *Analyzer) warn(format string, args ...any) {
	if a.Warnf != nil {
		a.Warnf(format, args...)
	}
}

// Commit is one parsed history entry.
type Commit struct {
	Hash    string
	Short   string
	Author  string
	When    time.Time
	Subject string
	Files   []string
}

// Analyze reads recent history and emits commit nodes with touches edges.
func (a *Analyzer) Analyze(ctx context.Context, repoRoot string, files []index.File) (index.Result, error) {
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}

	indexed := make(map[string]bool, len(files))
	for _, f := range files {
		indexed[f.Path] = true
	}

	commits, err := Log(ctx, repoRoot, a.MaxCommits)
	if err != nil {
		// Not a git repository, or git is absent. Neither is an error: the
		// rest of the index is unaffected.
		a.warn("gitlog: %s: %v", repoRoot, err)
		return index.Result{}, nil
	}

	var res index.Result
	for _, c := range commits {
		if len(c.Files) == 0 || len(c.Files) > a.MaxFilesPerCommit {
			continue
		}
		var touched []string
		for _, f := range c.Files {
			if indexed[f] {
				touched = append(touched, f)
			}
		}
		if len(touched) == 0 {
			continue
		}

		res.Nodes = append(res.Nodes, graph.Node{
			Kind: graph.KindCommit, Name: c.Short, FQN: "commit:" + c.Hash,
			Signature: c.Subject,
			Attrs: fmt.Sprintf(`{"author":%q,"when":%q,"files":%d}`,
				c.Author, c.When.UTC().Format(time.RFC3339), len(c.Files)),
		})
		for _, f := range touched {
			res.Edges = append(res.Edges, index.PendingEdge{
				SrcKind: graph.KindCommit, SrcFQN: "commit:" + c.Hash,
				DstKind: graph.KindFile, DstFQN: f,
				Kind: graph.EdgeTouches, Evidence: graph.Observed,
			})
		}
	}
	return res, nil
}

// Log reads the last n commits with the files each touched.
//
// The format uses NUL separators and -z for the name list, because commit
// subjects and file paths can both contain newlines and a line-oriented parse
// would silently mis-associate files with commits.
func Log(ctx context.Context, repoRoot string, n int) ([]Commit, error) {
	if n <= 0 {
		n = 100
	}
	const sep = "\x1e" // record separator: cannot appear in a path
	args := []string{
		"-C", repoRoot, "log",
		fmt.Sprintf("--max-count=%d", n),
		"--no-merges",
		"--name-only",
		"--date=iso-strict",
		"--pretty=format:" + sep + "%H%x1f%h%x1f%an%x1f%ad%x1f%s",
	}
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // fixed arguments; only repoRoot varies and it is passed via -C
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat")

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}

	var commits []Commit
	for _, record := range strings.Split(string(out), sep) {
		record = strings.TrimLeft(record, "\n")
		if record == "" {
			continue
		}
		header, body, _ := strings.Cut(record, "\n")
		fields := strings.Split(header, "\x1f")
		if len(fields) < 5 {
			continue
		}
		c := Commit{Hash: fields[0], Short: fields[1], Author: fields[2], Subject: fields[4]}
		if t, err := time.Parse(time.RFC3339, fields[3]); err == nil {
			c.When = t
		}

		sc := bufio.NewScanner(strings.NewReader(body))
		for sc.Scan() {
			if path := strings.TrimSpace(sc.Text()); path != "" {
				c.Files = append(c.Files, path)
			}
		}
		sort.Strings(c.Files)
		commits = append(commits, c)
	}
	return commits, nil
}

// Blame reports the commits that last touched each line of a range. It backs
// the `git_touch` style tool the design mentions for repository memory.
func Blame(ctx context.Context, repoRoot, path string, startLine, endLine int) ([]string, error) {
	args := []string{"-C", repoRoot, "blame", "--porcelain"}
	if startLine > 0 && endLine >= startLine {
		args = append(args, fmt.Sprintf("-L%d,%d", startLine, endLine))
	}
	args = append(args, "--", path)

	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // fixed arguments plus a caller-supplied path after --
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git blame %s: %w", path, err)
	}

	seen := map[string]bool{}
	var hashes []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		// A porcelain header line starts with the 40-character hash.
		if len(line) < 40 || line[0] == '\t' || line[0] == ' ' {
			continue
		}
		hash := line[:40]
		if !isHex(hash) || seen[hash] {
			continue
		}
		seen[hash] = true
		hashes = append(hashes, hash)
	}
	return hashes, sc.Err()
}

func isHex(s string) bool {
	for _, r := range s {
		isDigit := r >= '0' && r <= '9'
		isLower := r >= 'a' && r <= 'f'
		if !isDigit && !isLower {
			return false
		}
	}
	return true
}
