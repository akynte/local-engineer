// Package policy loads the operator's repository-wide rules (design v3 §1.2,
// §5.2, §6.2).
//
// A task declares the scope it may change, and writes outside that scope are
// detected. But a task that declares no scope was unrestricted: OutOfScope
// returns nothing when the allowed list is empty, so "change whatever you like"
// was the default for an undeclared task. That is the gap this closes.
//
// Protected paths are not scope. Scope is per-task and says where this work
// belongs; protection is repository-wide and says what no task may touch
// whatever it was asked to do — the CI workflows that decide whether a change
// is accepted, the policy files themselves, the workspace identity pin.
// §6.2 lists "cannot modify policy, ledger, hidden tests" as a guarantee, and
// inside a worktree the sandbox cannot provide it: the task legitimately has
// write access to the checkout it is editing.
package policy

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Rule is one protected pattern and the reason for it.
type Rule struct {
	// Path is a glob against a repository-relative path. A trailing "/**"
	// protects a whole subtree.
	Path string `yaml:"path"`
	// Reason reaches the operator when a gate opens, so it has to say why this
	// matters rather than restating the path.
	Reason string `yaml:"reason"`
}

// Policy is one loaded file.
type Policy struct {
	Name string `yaml:"name"`
	// Protected lists paths no task may change.
	Protected []Rule `yaml:"protected"`
}

// Set is every policy the operator has installed.
type Set struct {
	Policies []Policy
}

// ErrInvalid is returned for a policy file that cannot be applied.
var ErrInvalid = errors.New("policy: invalid")

// Validate rejects a policy that would silently protect nothing.
func (p Policy) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("%w: a policy needs a name", ErrInvalid)
	}
	if len(p.Protected) == 0 {
		return fmt.Errorf("%w: %s protects nothing", ErrInvalid, p.Name)
	}
	for i, r := range p.Protected {
		if strings.TrimSpace(r.Path) == "" {
			return fmt.Errorf("%w: %s rule %d has no path", ErrInvalid, p.Name, i+1)
		}
		if strings.TrimSpace(r.Reason) == "" {
			// A rule that cannot say why it exists is one nobody can judge, and
			// the reason is what an operator reads at the gate.
			return fmt.Errorf("%w: %s rule %q has no reason", ErrInvalid, p.Name, r.Path)
		}
		if _, err := path.Match(r.Path, "probe"); err != nil {
			return fmt.Errorf("%w: %s rule %q is not a valid pattern: %w",
				ErrInvalid, p.Name, r.Path, err)
		}
	}
	return nil
}

// Load reads every .yaml under dir. A missing directory is not an error: a
// repository with no policies is the ordinary case.
func Load(dir string) (Set, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Set{}, nil
		}
		return Set{}, err
	}
	var set Set
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch filepath.Ext(e.Name()) {
		case ".yaml", ".yml":
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // dir is operator configuration
		if err != nil {
			return Set{}, err
		}
		var p Policy
		if err := yaml.Unmarshal(body, &p); err != nil {
			return Set{}, fmt.Errorf("%w: %s: %w", ErrInvalid, name, err)
		}
		if err := p.Validate(); err != nil {
			return Set{}, fmt.Errorf("%s: %w", name, err)
		}
		set.Policies = append(set.Policies, p)
	}
	return set, nil
}

// Violation is one changed path that a policy protects.
type Violation struct {
	Path   string `json:"path"`
	Policy string `json:"policy"`
	Rule   string `json:"rule"`
	Reason string `json:"reason"`
}

// Check reports which changed paths are protected.
//
// It is deliberately independent of a task's declared scope. A task that
// declares no scope is unrestricted by scope, and that is exactly when this
// matters most.
func (s Set) Check(changed []string) []Violation {
	var out []Violation
	for _, c := range changed {
		rel := filepath.ToSlash(strings.TrimPrefix(c, "./"))
		for _, p := range s.Policies {
			for _, r := range p.Protected {
				if matches(r.Path, rel) {
					out = append(out, Violation{
						Path: rel, Policy: p.Name, Rule: r.Path, Reason: r.Reason,
					})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// matches applies one pattern. "a/**" protects the whole subtree, which
// path.Match does not do on its own: its * never crosses a separator, so a
// pattern written the obvious way would protect one level and silently miss
// everything deeper.
func matches(pattern, rel string) bool {
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return rel == prefix || strings.HasPrefix(rel, prefix+"/")
	}
	if ok, _ := path.Match(pattern, rel); ok {
		return true
	}
	// A bare directory name protects its contents too, which is what an
	// operator writing `policies` rather than `policies/**` means.
	return strings.HasPrefix(rel, strings.TrimSuffix(pattern, "/")+"/")
}

// Paths lists every protected pattern, for `le policy show`.
func (s Set) Paths() []string {
	var out []string
	for _, p := range s.Policies {
		for _, r := range p.Protected {
			out = append(out, r.Path)
		}
	}
	sort.Strings(out)
	return out
}
