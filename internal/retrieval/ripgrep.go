package retrieval

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/policy"
)

// GrepBudget is §6.1's live-search allowance.
//
// ripgrep is not an index and is not treated as one: it runs against the
// working tree on every query, so it is the one retrieval layer that sees
// uncommitted edits and the one that can block a phase. A fixed budget makes it
// safe to reach for — a search that cannot answer in 200 ms returns what it
// found and says it was cut short, rather than holding up a call that has a
// deterministic answer waiting in the index.
const GrepBudget = 200 * time.Millisecond

// GrepFileCap and GrepMatchCap bound what a single query can return. A term
// that matches ten thousand times has told you it is the wrong term.
const (
	GrepFileCap  = 40
	GrepMatchCap = 200
)

// GrepHit is one match: a path, a line and the text.
type GrepHit struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// GrepResult is what a live search found and whether it finished.
type GrepResult struct {
	Hits []GrepHit `json:"hits"`
	// Truncated reports that the budget or a cap stopped the search. It is
	// surfaced rather than swallowed: "no other callers" and "no other callers
	// in the first 200 ms" are different answers, and only one of them is safe
	// to plan against.
	Truncated bool `json:"truncated"`
	// Reason says which limit stopped it, for the trace.
	Reason string `json:"reason,omitempty"`
}

// ErrRipgrepMissing reports that rg is not installed.
//
// It is a named error because the caller's correct response is to fall back to
// the index rather than to fail the phase: ripgrep is the cheapest layer, not a
// required one.
var ErrRipgrepMissing = errors.New("retrieval: ripgrep (rg) is not installed")

// Grep runs a bounded literal search over the working tree.
//
// The pattern is passed with --fixed-strings, so repository text and task text
// can never reach ripgrep as a regular expression: a task mentioning `a.*b`
// searches for those five characters. Word is the §6.2 router's "symbol-shaped
// query" case, which wants `Check` and not `Checkpoint`.
func Grep(ctx context.Context, root, pattern string, word bool) (GrepResult, error) {
	if strings.TrimSpace(pattern) == "" {
		return GrepResult{}, nil
	}
	binary, err := exec.LookPath("rg")
	if err != nil {
		return GrepResult{}, ErrRipgrepMissing
	}

	ctx, cancel := context.WithTimeout(ctx, GrepBudget)
	defer cancel()

	args := []string{
		"--fixed-strings", "--line-number", "--no-heading", "--with-filename",
		"--color", "never",
		// Binary files are skipped by default. Hidden directories and anything
		// .gitignore excludes are not repository source either, and a vendored
		// tree answers questions about somebody else's code.
		"--glob", "!.git", "--glob", "!.le", "--glob", "!node_modules",
		"--glob", "!vendor", "--glob", "!target", "--glob", "!dist",
		"--max-filesize", "2M",
		"--max-count", strconv.Itoa(GrepMatchCap),
	}
	if word {
		args = append(args, "--word-regexp")
	}
	args = append(args, "--", pattern, ".")

	//nolint:gosec // the binary is resolved from PATH and every argument after
	// -- is data: the pattern is fixed-strings and the path is the worktree.
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = root
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return GrepResult{}, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return GrepResult{}, err
	}

	var result GrepResult
	files := map[string]bool{}
	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 4096), 1<<20)
	for scan.Scan() {
		hit, ok := parseGrepLine(scan.Text())
		if !ok || policy.Sensitive(hit.Path) {
			continue
		}
		if !files[hit.Path] && len(files) >= GrepFileCap {
			result.Truncated, result.Reason = true, fmt.Sprintf("more than %d files matched", GrepFileCap)
			break
		}
		files[hit.Path] = true
		result.Hits = append(result.Hits, hit)
		if len(result.Hits) >= GrepMatchCap {
			result.Truncated, result.Reason = true, fmt.Sprintf("more than %d matches", GrepMatchCap)
			break
		}
	}
	_ = stdout.Close()
	waitErr := cmd.Wait()

	switch {
	case ctx.Err() != nil:
		result.Truncated = true
		result.Reason = "search budget of " + GrepBudget.String() + " elapsed"
	case result.Truncated:
		// A cap stopped the read, so the child was killed on purpose and its
		// exit status describes that rather than the search.
	case waitErr != nil:
		// rg exits 1 for "no matches", which is an answer rather than a
		// failure. Every other status is a real error and is reported as one:
		// treating an unrecognised flag or an unreadable directory as "nothing
		// matched" is how a retrieval layer silently stops working, and the
		// caller reads an empty result as evidence of absence.
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return result, fmt.Errorf("retrieval: ripgrep: %w", waitErr)
		}
		if code := exitErr.ExitCode(); code != 1 {
			detail := strings.TrimSpace(stderr.String())
			if detail == "" {
				detail = "no diagnostic"
			}
			return GrepResult{}, fmt.Errorf("retrieval: ripgrep exited %d: %s", code, detail)
		}
	}
	sort.SliceStable(result.Hits, func(i, j int) bool {
		if result.Hits[i].Path != result.Hits[j].Path {
			return result.Hits[i].Path < result.Hits[j].Path
		}
		return result.Hits[i].Line < result.Hits[j].Line
	})
	return result, nil
}

// grepLine splits `path:line:text`, allowing for a Windows-style drive letter
// never appearing here but a colon in the path being possible.
var grepLine = regexp.MustCompile(`^(.*?):(\d+):(.*)$`)

func parseGrepLine(line string) (GrepHit, bool) {
	m := grepLine.FindStringSubmatch(line)
	if m == nil {
		return GrepHit{}, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return GrepHit{}, false
	}
	text := m[3]
	// A matched line in a minified bundle is a screenful of noise that says
	// nothing about where the symbol is defined.
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return GrepHit{Path: filepath.ToSlash(strings.TrimPrefix(m[1], "./")), Line: n, Text: text}, true
}

// ExpandTerms produces the spellings a concept query has in source.
//
// §6.2's concept-shaped route: a task says "account limit" and the code says
// accountLimit, account_limit or ACCOUNT_LIMIT. Searching only what the user
// typed finds the comment that mentions it and misses the function that
// implements it.
func ExpandTerms(query string) []string {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	})
	seen := map[string]bool{}
	var out []string
	// Case-sensitively: the search is case-sensitive, so accountLimit and
	// AccountLimit are two different identifiers and folding them together
	// drops whichever spelling the repository actually uses.
	add := func(s string) {
		if len(s) < 3 || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, f := range fields {
		add(f)
	}
	// Adjacent words are the likeliest identifier: "account limit" is
	// accountLimit far more often than "daily" and "account limit" are separately
	// interesting.
	for i := 0; i+1 < len(fields); i++ {
		a, b := fields[i], fields[i+1]
		if len(a) < 2 || len(b) < 2 {
			continue
		}
		add(strings.ToLower(a) + strings.Title(strings.ToLower(b))) //nolint:staticcheck // ASCII identifiers, not display text
		add(strings.ToLower(a) + "_" + strings.ToLower(b))
		add(strings.Title(strings.ToLower(a)) + strings.Title(strings.ToLower(b))) //nolint:staticcheck // same
	}
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}
