//go:build docs

// Package docs holds the executable documentation test required by design v3
// §4.5:
//
//	"Prerequisites, installation, configuration, model configuration,
//	 persistent storage, start, stop, update, troubleshooting, example
//	 workflows: each is a how-to page with a copy-paste command block and an
//	 expected-output block that CI executes against the CPU image."
//
// A command block opts in with an HTML comment immediately above the fence:
//
//	<!-- test:run -->
//	```console
//	$ le version
//	local-engineer …
//	```
//
// Lines starting with "$ " are commands; the lines after them are the expected
// output. "…" matches any run of characters, so a version string or a path can
// be asserted without pinning it. A block marked `<!-- test:skip reason -->` is
// reported as skipped with its reason rather than silently ignored: an
// unrunnable command block is a documentation debt, and it should be visible.
package docs

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type block struct {
	file     string
	line     int
	commands []command
	skip     string
}

type command struct {
	line     int
	cmd      string
	expected []string
}

var (
	runMarker  = regexp.MustCompile(`^<!--\s*test:run\s*-->$`)
	skipMarker = regexp.MustCompile(`^<!--\s*test:skip\s+(.*?)\s*-->$`)
	fenceOpen  = regexp.MustCompile("^```(console|shell|bash)$")
)

func TestDocumentedCommands(t *testing.T) {
	root := repoRoot(t)
	le := buildLE(t, root)

	// Every documented command runs against a scratch data directory, so the
	// test never touches a real one and never depends on state from a previous
	// run.
	dataDir := t.TempDir()
	workDir := t.TempDir()

	blocks := collect(t, filepath.Join(root, "docs"))
	if len(blocks) == 0 {
		t.Fatal("no executable command blocks found; §4.5 requires the how-to pages to carry them")
	}
	t.Logf("found %d executable command blocks", len(blocks))

	// A reader works through a page from the top, so the runner does too: the
	// working directory carries across the blocks of one file, and each file
	// starts from a clean directory.
	cwd := map[string]string{}
	for _, b := range blocks {
		name := filepath.Base(b.file) + ":" + itoa(b.line)
		t.Run(name, func(t *testing.T) {
			if b.skip != "" {
				t.Skipf("%s:%d marked test:skip — %s", b.file, b.line, b.skip)
			}
			dir, ok := cwd[b.file]
			if !ok {
				dir = filepath.Join(workDir, sanitize(b.file))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, c := range b.commands {
				dir = runOne(t, c, b.file, le, dataDir, dir)
			}
			cwd[b.file] = dir
		})
	}
}

// sanitize turns a documentation path into a directory name.
func sanitize(path string) string {
	r := strings.NewReplacer("/", "_", ".", "_")
	return r.Replace(strings.TrimPrefix(path, "/"))
}

// cwdMarker is appended to every command so the runner can carry the working
// directory to the next one.
const cwdMarker = "__LE_DOC_CWD__:"

// runOne executes one documented command and returns the working directory the
// next command should start in.
func runOne(t *testing.T, c command, file, le, dataDir, workDir string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	script := c.cmd + "\nstatus=$?\nprintf '%s%s\\n' '" + cwdMarker + "' \"$PWD\"\nexit $status"
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"LE_DATA="+dataDir,
		"PATH="+filepath.Dir(le)+":"+os.Getenv("PATH"),
		// Keep the documented commands deterministic.
		"NO_COLOR=1", "TERM=dumb",
	)
	out, err := cmd.CombinedOutput()
	got, next := splitCWD(string(out), workDir)

	// `le doctor` exits 1 on a warning, which is its documented contract and
	// not a failure of the command block.
	if err != nil && !isExpectedExit(c.cmd, err) {
		t.Fatalf("%s:%d\n$ %s\nfailed: %v\n%s", file, c.line, c.cmd, err, got)
	}
	if len(c.expected) == 0 {
		return next
	}
	if !matches(got, c.expected) {
		t.Fatalf("%s:%d\n$ %s\n--- expected (… matches anything) ---\n%s\n--- got ---\n%s",
			file, c.line, c.cmd, strings.Join(c.expected, "\n"), got)
	}
	return next
}

// splitCWD strips the working-directory marker from the output and returns the
// directory the shell ended in.
func splitCWD(out, fallback string) (string, string) {
	lines := strings.Split(out, "\n")
	dir := fallback
	kept := lines[:0]
	for _, l := range lines {
		if strings.HasPrefix(l, cwdMarker) {
			dir = strings.TrimPrefix(l, cwdMarker)
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n"), dir
}

// isExpectedExit allows the non-zero exits that are part of a command's
// documented contract.
func isExpectedExit(cmd string, err error) bool {
	var ee *exec.ExitError
	if !asExit(err, &ee) {
		return false
	}
	// `le doctor`: 0 clean, 1 warnings, 2 failures. On a CI host without a
	// container boundary, warnings are expected.
	if strings.Contains(cmd, "le doctor") && ee.ExitCode() == 1 {
		return true
	}
	return false
}

func asExit(err error, dst **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*dst = ee
	}
	return ok
}

// matches checks that every expected line appears in order in the output.
// "…" is a wildcard for any run of characters within a line.
func matches(got string, expected []string) bool {
	lines := strings.Split(got, "\n")
	i := 0
	for _, want := range expected {
		pattern := linePattern(want)
		found := false
		for ; i < len(lines); i++ {
			if pattern.MatchString(lines[i]) {
				found = true
				i++
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func linePattern(want string) *regexp.Regexp {
	parts := strings.Split(strings.TrimSpace(want), "…")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile(strings.Join(parts, ".*"))
}

func collect(t *testing.T, dir string) []block {
	t.Helper()
	var out []block
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		blocks, perr := parse(path)
		if perr != nil {
			return perr
		}
		out = append(out, blocks...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func parse(path string) ([]block, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []block
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNo := 0
	pendingRun, pendingSkip := false, ""
	for sc.Scan() {
		lineNo++
		line := strings.TrimRight(sc.Text(), " \t")

		if runMarker.MatchString(strings.TrimSpace(line)) {
			pendingRun, pendingSkip = true, ""
			continue
		}
		if m := skipMarker.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			pendingRun, pendingSkip = true, m[1]
			continue
		}
		if !fenceOpen.MatchString(strings.TrimSpace(line)) {
			if strings.TrimSpace(line) != "" {
				pendingRun, pendingSkip = false, ""
			}
			continue
		}
		if !pendingRun {
			// A fence with no marker is documentation, not a test.
			for sc.Scan() {
				lineNo++
				if strings.TrimSpace(sc.Text()) == "```" {
					break
				}
			}
			continue
		}

		b := block{file: path, line: lineNo, skip: pendingSkip}
		var cur *command
		for sc.Scan() {
			lineNo++
			body := sc.Text()
			if strings.TrimSpace(body) == "```" {
				break
			}
			if strings.HasPrefix(body, "$ ") {
				b.commands = append(b.commands, command{line: lineNo, cmd: strings.TrimPrefix(body, "$ ")})
				cur = &b.commands[len(b.commands)-1]
				continue
			}
			if cur != nil && strings.TrimSpace(body) != "" {
				cur.expected = append(cur.expected, body)
			}
		}
		if len(b.commands) > 0 {
			out = append(out, b)
		}
		pendingRun, pendingSkip = false, ""
	}
	return out, sc.Err()
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find the repository root")
	return ""
}

func buildLE(t *testing.T, root string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "le")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/le")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building le: %v\n%s", err, out)
	}
	return bin
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
