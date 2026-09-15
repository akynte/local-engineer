package recipe

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Repository-declared verification: the two §10.1 rows that no built-in recipe
// can express.
//
// §10.1 adopts "runtime feedback (browser, disposable DB, integration)" for
// HIGH and UI, and "deterministic generation for boilerplate (sqlc, buf,
// OpenAPI clients)" with "contract checks still apply". Neither can be a
// built-in: how to bring up a database, and which generator produces which
// files, are facts about a repository and not about Go.
//
// So the repository declares them, in `.le/verify.yaml`, and the declaration
// is opt-in for the same reason `.golangci.yml` is: a verdict a repository
// never asked to be measured by is not one its maintainers agreed to.
//
// # Why generation is a check and not a step
//
// Running a generator inside verification would mutate the worktree, and every
// result recorded before it would be evidence about a candidate that no longer
// exists (§7.2). So the generate check runs the generator, compares, and
// restores — the worktree it leaves behind is the one it found. What it
// reports is whether the committed generated code matches what the generator
// produces now, which is the property that actually matters: it catches a
// model hand-editing generated output, and a schema change nobody regenerated.

// DeclaredFile is `.le/verify.yaml` in the repository root.
const DeclaredFile = ".le/verify.yaml"

// Declared is the parsed file.
type Declared struct {
	// Version is 1. It exists so a future shape can be rejected clearly
	// rather than parsed into something half-right.
	Version int `yaml:"version"`
	// Integration are runtime checks: bring something up, exercise it, tear
	// it down. §10.1's "runtime feedback".
	Integration []IntegrationStep `yaml:"integration,omitempty"`
	// Generate are generators whose output must already be up to date.
	Generate []GenerateStep `yaml:"generate,omitempty"`
	// Check are project-invariant analyzers §10.1 adopts but cannot name: the
	// set of useful ones is open (gosec, gitleaks, squawk, buf, oasdiff, a
	// script this team wrote) and which apply is a fact about a repository.
	//
	// They are KindAnalyzer, like semgrep, so a failure blocks acceptance and
	// a missing tool skips rather than fails.
	Check []CheckStep `yaml:"check,omitempty"`
}

// CheckStep is one repository-declared analyzer.
type CheckStep struct {
	Name string `yaml:"name"`
	// Argv runs the analyzer. A non-zero exit is a finding in the code.
	Argv []string `yaml:"argv"`
	// Requires names the binary the check needs. When it is not installed the
	// check SKIPS rather than fails — a missing tool says nothing about the
	// code, and the same rule governs the built-in semgrep recipe. Empty means
	// Argv[0].
	Requires string `yaml:"requires,omitempty"`
	// TimeoutMinutes bounds the analyzer.
	TimeoutMinutes int `yaml:"timeout_minutes,omitempty"`
}

// IntegrationStep is one runtime check.
type IntegrationStep struct {
	Name string `yaml:"name"`
	// Up provisions: a disposable database, a service, a fixture server.
	// Optional — a step that needs nothing brought up omits it.
	Up []string `yaml:"up,omitempty"`
	// Test exercises the running system. Required: a step that brings
	// something up and checks nothing is a slow no-op.
	Test []string `yaml:"test"`
	// Down tears it back down. Run even when Test failed, because leaving a
	// container or a database behind turns one failure into every later run
	// failing for a different reason.
	Down []string `yaml:"down,omitempty"`
	// Ports the step's processes may bind and dial on loopback. The sandbox
	// grants exactly these and nothing else, which is why they have to be
	// declared rather than discovered.
	Ports []int `yaml:"ports,omitempty"`
	// TimeoutMinutes bounds the whole step. Runtime checks are the slowest
	// thing in verification, so this defaults generously and is capped.
	TimeoutMinutes int `yaml:"timeout_minutes,omitempty"`
}

// GenerateStep is one generator whose output is checked, never committed.
type GenerateStep struct {
	Name string `yaml:"name"`
	// Argv runs the generator.
	Argv []string `yaml:"argv"`
	// Outputs are the paths the generator writes, relative to the worktree
	// root. They are declared rather than inferred because the check restores
	// them afterwards, and restoring a path a generator did not write would be
	// a way to lose work.
	Outputs []string `yaml:"outputs"`
	// TimeoutMinutes bounds the generator.
	TimeoutMinutes int `yaml:"timeout_minutes,omitempty"`
}

// ErrNoDeclaredFile means the repository declared nothing, which is ordinary.
var ErrNoDeclaredFile = errors.New("recipe: no .le/verify.yaml")

// MaxStepMinutes caps a declared timeout. A repository cannot ask verification
// to hang: the point of a bounded check is that it is bounded.
const MaxStepMinutes = 60

// LoadDeclared reads and validates `.le/verify.yaml`.
func LoadDeclared(worktree string) (Declared, error) {
	path := filepath.Join(worktree, DeclaredFile)
	body, err := os.ReadFile(path) //nolint:gosec // a path inside the worktree being verified
	if err != nil {
		if os.IsNotExist(err) {
			return Declared{}, ErrNoDeclaredFile
		}
		return Declared{}, err
	}
	var d Declared
	if err := yaml.Unmarshal(body, &d); err != nil {
		return Declared{}, fmt.Errorf("recipe: %s: %w", DeclaredFile, err)
	}
	if err := d.Validate(); err != nil {
		return Declared{}, fmt.Errorf("recipe: %s: %w", DeclaredFile, err)
	}
	return d, nil
}

// Validate rejects a declaration that would verify nothing or hang.
func (d Declared) Validate() error {
	if d.Version != 1 {
		return fmt.Errorf("version is %d; this build understands version 1", d.Version)
	}
	seen := map[string]bool{}
	for i, s := range d.Integration {
		switch {
		case strings.TrimSpace(s.Name) == "":
			return fmt.Errorf("integration step %d has no name", i+1)
		case seen["i:"+s.Name]:
			return fmt.Errorf("two integration steps are named %q", s.Name)
		case len(s.Test) == 0:
			return fmt.Errorf("integration step %q has no `test`; a step that checks nothing is a slow no-op", s.Name)
		case s.TimeoutMinutes > MaxStepMinutes:
			return fmt.Errorf("integration step %q asks for %d minutes; the cap is %d",
				s.Name, s.TimeoutMinutes, MaxStepMinutes)
		}
		for _, p := range s.Ports {
			if p <= 0 || p > 65535 {
				return fmt.Errorf("integration step %q declares port %d", s.Name, p)
			}
		}
		seen["i:"+s.Name] = true
	}
	for i, s := range d.Check {
		switch {
		case strings.TrimSpace(s.Name) == "":
			return fmt.Errorf("check step %d has no name", i+1)
		case seen["c:"+s.Name]:
			return fmt.Errorf("two check steps are named %q", s.Name)
		case len(s.Argv) == 0:
			return fmt.Errorf("check step %q has no `argv`", s.Name)
		case s.TimeoutMinutes > MaxStepMinutes:
			return fmt.Errorf("check step %q asks for %d minutes; the cap is %d",
				s.Name, s.TimeoutMinutes, MaxStepMinutes)
		}
		seen["c:"+s.Name] = true
	}
	for i, s := range d.Generate {
		switch {
		case strings.TrimSpace(s.Name) == "":
			return fmt.Errorf("generate step %d has no name", i+1)
		case seen["g:"+s.Name]:
			return fmt.Errorf("two generate steps are named %q", s.Name)
		case len(s.Argv) == 0:
			return fmt.Errorf("generate step %q has no `argv`", s.Name)
		case len(s.Outputs) == 0:
			// Without declared outputs the check cannot restore the worktree,
			// and a generate check that leaves the tree mutated invalidates
			// every other result (§7.2).
			return fmt.Errorf("generate step %q declares no `outputs`; "+
				"the check restores them afterwards and cannot guess which they are", s.Name)
		case s.TimeoutMinutes > MaxStepMinutes:
			return fmt.Errorf("generate step %q asks for %d minutes; the cap is %d",
				s.Name, s.TimeoutMinutes, MaxStepMinutes)
		}
		for _, o := range s.Outputs {
			if err := safeRelPath(o); err != nil {
				return fmt.Errorf("generate step %q output %q: %w", s.Name, o, err)
			}
		}
		seen["g:"+s.Name] = true
	}
	return nil
}

// safeRelPath refuses an output path that could escape the worktree.
//
// The check restores these paths after running the generator, so a path that
// resolved outside the worktree would be a way to overwrite something the task
// was never allowed to touch.
func safeRelPath(p string) error {
	if p == "" {
		return errors.New("is empty")
	}
	if filepath.IsAbs(p) {
		return errors.New("is absolute; outputs are relative to the worktree root")
	}
	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("escapes the worktree")
	}
	return nil
}

// Ports collects every port any integration step declares, so the caller can
// build one sandbox spec for the whole set.
func (d Declared) Ports() []uint16 {
	seen := map[int]bool{}
	var out []uint16
	for _, s := range d.Integration {
		for _, p := range s.Ports {
			if p > 0 && p <= 65535 && !seen[p] {
				seen[p] = true
				out = append(out, uint16(p)) //nolint:gosec // bounds checked
			}
		}
	}
	return out
}

// HasDeclaredVerification reports whether a worktree declares anything. It is
// the AppliesTo predicate for both declared kinds.
func HasDeclaredVerification(worktree string) bool {
	d, err := LoadDeclared(worktree)
	if err != nil {
		return false
	}
	return len(d.Integration) > 0 || len(d.Generate) > 0 || len(d.Check) > 0
}

// DeclaredRecipes turns a declaration into recipes.
//
// self is the path to the `le` binary, which runs both kinds as a subcommand.
// A recipe is argv and never a shell string, and neither of these is one
// command: an integration step is up-then-test-then-down, and a generate check
// is snapshot-run-compare-restore. Putting that sequencing in a subcommand
// keeps it testable and keeps a shell out of the sandbox.
func DeclaredRecipes(level Level, worktree, self string) []Recipe {
	d, err := LoadDeclared(worktree)
	if err != nil {
		return nil
	}
	var out []Recipe

	// Generation runs FIRST, before build. A repository whose generated code
	// is stale will fail to build in a way that points at the generated file
	// rather than at the schema that moved, and the useful message is this
	// one.
	if level.Includes(KindGenerate) {
		for _, g := range d.Generate {
			name := g.Name
			out = append(out, Recipe{
				Name: "generate: " + name, Kind: KindGenerate,
				Argv:      []string{self, "verify-declared", "--kind", "generate", "--name", name},
				Timeout:   stepTimeout(g.TimeoutMinutes, 10*time.Minute),
				Summarize: DeclaredSummary,
				AppliesTo: HasDeclaredVerification,
			})
		}
	}

	if level.Includes(KindAnalyzer) {
		for _, c := range d.Check {
			name := c.Name
			need := c.Requires
			if need == "" {
				need = c.Argv[0]
			}
			out = append(out, Recipe{
				Name: "check: " + name, Kind: KindAnalyzer,
				Argv:      []string{self, "verify-declared", "--kind", "check", "--name", name},
				Timeout:   stepTimeout(c.TimeoutMinutes, 10*time.Minute),
				Summarize: DeclaredSummary,
				AppliesTo: func(worktree string) bool {
					if !HasDeclaredVerification(worktree) {
						return false
					}
					// A machine without the tool skips, as with semgrep: a
					// missing toolchain says nothing about the code.
					_, err := exec.LookPath(need)
					return err == nil
				},
			})
		}
	}

	if level.Includes(KindIntegration) {
		for _, i := range d.Integration {
			name := i.Name
			out = append(out, Recipe{
				Name: "integration: " + name, Kind: KindIntegration,
				Argv:      []string{self, "verify-declared", "--kind", "integration", "--name", name},
				Timeout:   stepTimeout(i.TimeoutMinutes, 20*time.Minute),
				Summarize: DeclaredSummary,
				AppliesTo: HasDeclaredVerification,
			})
		}
	}
	return out
}

func stepTimeout(minutes int, fallback time.Duration) time.Duration {
	if minutes <= 0 {
		return fallback
	}
	if minutes > MaxStepMinutes {
		minutes = MaxStepMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// SelfPath is the `le` binary, for recipes that run a subcommand of it.
func SelfPath() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	if p, err := exec.LookPath("le"); err == nil {
		return p
	}
	return "le"
}
