package recipe

import (
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Level is a verification level. A task declares how much evidence its
// completion requires, so a comment fix does not pay for a race run and a
// concurrency change cannot skip one.
type Level string

const (
	// Low: it still compiles. For documentation and comment-only changes.
	Low Level = "low"
	// Standard: compiles, vets, and the tests pass. The default.
	Standard Level = "standard"
	// High: adds the race detector, formatting, lint and the project's own
	// analyzers. For concurrency, public API and anything touching shared
	// state. The lint and analyzer recipes run only where the repository
	// committed a configuration for them, so High is not a different verdict
	// on a repository that adopted neither.
	High Level = "high"
)

// ParseLevel validates operator input.
func ParseLevel(s string) (Level, bool) {
	switch Level(s) {
	case Low, Standard, High:
		return Level(s), true
	}
	return "", false
}

// Includes reports whether a level requires a recipe kind.
func (l Level) Includes(k Kind) bool {
	switch l {
	case Low:
		return k == KindBuild
	case Standard:
		return k == KindBuild || k == KindVet || k == KindTest
	case High:
		return true
	}
	return false
}

// IncludesDeclared reports whether a level runs repository-declared checks.
// Both are HIGH-only: §10.1 adopts runtime feedback "for HIGH and UI", and a
// generator run is the slowest cheap check there is.
func (l Level) IncludesDeclared() bool { return l == High }

// GoRecipes returns the built-in Go verification recipes for a level.
//
// Ordering matters: build first, so a compile failure short-circuits the rest
// rather than producing a page of test noise about code that never built.
func GoRecipes(level Level) []Recipe {
	isGo := func(worktree string) bool {
		_, err := os.Stat(filepath.Join(worktree, "go.mod"))
		return err == nil
	}

	all := []Recipe{
		{
			Name: "go build", Kind: KindBuild, AppliesTo: isGo,
			Argv: []string{"go", "build", "./..."}, Timeout: 5 * time.Minute,
			Summarize: GoBuild,
		},
		{
			Name: "go vet", Kind: KindVet, AppliesTo: isGo,
			Argv: []string{"go", "vet", "./..."}, Timeout: 5 * time.Minute,
			Summarize: GoVet,
		},
		{
			Name: "go test", Kind: KindTest, AppliesTo: isGo,
			Argv: []string{"go", "test", "./..."}, Timeout: 15 * time.Minute,
			Summarize: GoTest,
		},
		{
			Name: "gofmt", Kind: KindFormat, AppliesTo: isGo,
			Argv: []string{"gofmt", "-l", "."}, Timeout: time.Minute,
			Summarize: Gofmt,
		},
		{
			Name: "go test -race", Kind: KindRace, AppliesTo: isGo,
			Argv: []string{"go", "test", "-race", "./..."}, Timeout: 25 * time.Minute,
			Summarize: GoRace,
		},
		{
			// §10.1 names lint in the core feedback loop: "compiler, vet,
			// lint, test, race". KindLint existed for it and nothing produced
			// one, so the loop shipped with four of its five legs.
			//
			// It runs only where the repository committed a `.golangci.yml`.
			// golangci-lint's default set is opinionated, and a verdict a
			// repository never opted into is a verdict its maintainers did not
			// agree to be measured by — the same reason semgrep runs only
			// where there are rules.
			Name: "golangci-lint", Kind: KindLint, AppliesTo: HasGolangciConfig,
			Argv: []string{
				"golangci-lint", "run", "--output.json.path", "stdout", "./...",
			},
			Timeout: 10 * time.Minute, Summarize: GolangciLint,
		},
		{
			// §10.1: project-invariant analyzers catch "domain rules the model
			// does not know" — patterns that compile, pass vet, and are still
			// wrong for this repository.
			Name: "semgrep", Kind: KindAnalyzer, AppliesTo: HasSemgrepRules,
			Argv: []string{
				"semgrep", "--config", SemgrepRuleDir, "--json", "--quiet",
				"--error", "--disable-version-check", "--metrics", "off", ".",
			},
			Timeout: 5 * time.Minute, Summarize: Semgrep,
		},
	}

	out := make([]Recipe, 0, len(all))
	for _, r := range all {
		if level.Includes(r.Kind) {
			out = append(out, r)
		}
	}
	return out
}

// GoEnv is the environment a Go recipe needs inside the sandbox. The caches
// are per-workspace, so one project's build cache never serves another's.
//
// Pair it with GoSandboxPaths: an environment naming directories the sandbox
// has not granted produces a confusing permission error from deep inside the
// toolchain rather than a clear one from the sandbox.
func GoEnv(goCacheDir, goModCacheDir, tmpDir string) []string {
	return []string{
		"HOME=" + tmpDir,
		"GOCACHE=" + goCacheDir,
		"GOMODCACHE=" + goModCacheDir,
		"GOTMPDIR=" + tmpDir,
		"TMPDIR=" + tmpDir,
		// The toolchain's telemetry uploader forks a child and opens files
		// outside the granted set. It is off by default in a sandbox because
		// a build must not depend on paths the task has no business touching.
		"GOTELEMETRY=off",
		"GOTELEMETRYDIR=" + tmpDir + "/telemetry",
		// Deterministic output: colour and progress noise would defeat the
		// summarizers and make artifacts differ run to run.
		"NO_COLOR=1",
		"TERM=dumb",
		"GOFLAGS=-mod=mod",
		// A verification run must not reach the network. A task that needs a
		// new dependency has to say so, rather than silently acquiring one
		// mid-verification.
		"GOPROXY=off",
		"GOTOOLCHAIN=local",
		"PATH=/usr/local/go/bin:/opt/le/gotools/bin:/usr/local/bin:/usr/bin:/bin",
	}
}

// GoSandboxPaths returns the paths a Go recipe must be able to write and
// read, for the same directories GoEnv names.
//
// The writable set is derived here rather than left to each caller: a caller
// that forgets one gets "permission denied" from inside the compiler, which
// looks like a broken sandbox instead of a missing grant.
func GoSandboxPaths(goCacheDir, goModCacheDir, tmpDir string) (readWrite, readOnly []string) {
	readWrite = []string{goCacheDir, tmpDir}
	if goModCacheDir != "" {
		// The module cache is written when a build populates it, so it cannot
		// be read-only even though most runs only read from it.
		readWrite = append(readWrite, goModCacheDir)
	}
	readOnly = []string{
		"/usr/local/go", "/usr/lib/go", "/usr/share/go",
		"/etc/ssl", "/etc/ca-certificates", "/etc/passwd", "/etc/group", "/etc/nsswitch.conf",
	}
	return readWrite, readOnly
}

// DeviceFiles are the device nodes any ordinary program expects to exist.
// Without them a toolchain fails in ways that look like a bug in the sandbox
// rather than a missing grant: the Go compiler's own error for a denied
// /dev/null names a telemetry child process, which is not a useful clue.
func DeviceFiles() []string {
	return []string{"/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom", "/dev/tty"}
}

// Required reports the recipe kinds a level demands actually pass. A skipped
// recipe satisfies nothing: if `go test` did not run, the task has no evidence
// its tests pass.
//
// KindLint and KindAnalyzer are deliberately NOT here, and the reason is
// worth stating because their absence looks like the oversight it partly was.
// Both are conditional: they run only where the repository committed a
// configuration for them. Demanding a result unconditionally would mean every
// repository without a `.golangci.yml` failed `high` for a check that could
// never have run.
//
// That is not the same as letting them be ignored. A conditional check that
// runs and finds something still blocks acceptance — see Accept, which treats
// a Fail from any kind as disqualifying. The distinction is between "this
// kind must have produced evidence" and "no check may have found a problem",
// and only the first depends on the level.
func Required(level Level) []Kind {
	switch level {
	case Low:
		return []Kind{KindBuild}
	case Standard:
		return []Kind{KindBuild, KindVet, KindTest}
	case High:
		return []Kind{KindBuild, KindVet, KindTest, KindRace, KindFormat}
	}
	return nil
}

// ConditionalKinds are the kinds that run only where a repository asked for
// them, and so cannot be on Required's list at any level.
//
// They are named here rather than left implicit because "not required" reads
// as "optional", and they are not: Accept disqualifies on a Fail from any kind.
// What is conditional is whether the check exists, not whether its verdict
// counts.
func ConditionalKinds() []Kind {
	return []Kind{KindLint, KindAnalyzer, KindGenerate, KindIntegration}
}

// GolangciConfigNames are the config filenames golangci-lint itself looks for.
var GolangciConfigNames = []string{
	".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json",
}

// HasGolangciConfig reports whether the lint recipe is worth running: the
// repository committed a golangci-lint configuration and the tool is
// installed.
//
// Both halves matter, for the same reasons as HasSemgrepRules. A repository
// with no config has not chosen a linter set, and running the default one
// would hold a change to rules its maintainers never adopted. And a machine
// without golangci-lint should skip the recipe, not fail verification over a
// missing tool — a missing toolchain says nothing about the code.
func HasGolangciConfig(worktree string) bool {
	var found bool
	for _, name := range GolangciConfigNames {
		if st, err := os.Stat(filepath.Join(worktree, name)); err == nil && !st.IsDir() {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	_, err := exec.LookPath("golangci-lint")
	return err == nil
}

// SemgrepRuleDir is where a repository keeps its own invariant rules.
const SemgrepRuleDir = "semgrep"

// HasSemgrepRules reports whether the recipe is worth running: the repository
// carries rules and semgrep is installed.
//
// Both halves matter. A repository with no rules has nothing to check, and
// running semgrep anyway would report a pass that means nothing. And semgrep is
// an addition rather than a dependency of the build — a machine without it
// should skip the recipe, not fail verification over a missing tool.
func HasSemgrepRules(worktree string) bool {
	entries, err := os.ReadDir(filepath.Join(worktree, SemgrepRuleDir))
	if err != nil {
		return false
	}
	var hasRules bool
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch filepath.Ext(e.Name()) {
		case ".yaml", ".yml":
			hasRules = true
		}
	}
	if !hasRules {
		return false
	}
	_, err = exec.LookPath("semgrep")
	return err == nil
}
