package firewall

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/akynte/local-engineer/internal/policy"
	"github.com/akynte/local-engineer/internal/worktree"
)

// Access is the native editor's per-attempt authority. The supervisor supplies
// it from the task's declared scope; model arguments cannot expand it.
// See local-coding-system-review.md §§9 and 14.
type Access struct {
	WriteScope []string
	Protected  policy.Set
}

// Check applies policy to both the supplied path and its canonical target so
// an in-repository symlink cannot disguise a secret or an out-of-scope file.
func (a Access) Check(root, rel string, write bool) error {
	full, err := worktree.Resolve(root, rel)
	if err != nil {
		return err
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	target, err := filepath.Rel(canonicalRoot, full)
	if err != nil {
		return err
	}
	for _, name := range []string{filepath.ToSlash(filepath.Clean(rel)), filepath.ToSlash(target)} {
		if policy.Sensitive(name) || name == ".git" || strings.HasPrefix(name, ".git/") {
			return fmt.Errorf("access denied: protected content %q", name)
		}
		if !write {
			continue
		}
		if name == ".le" || strings.HasPrefix(name, ".le/") || name == ".agent" || strings.HasPrefix(name, ".agent/") {
			return fmt.Errorf("write denied: supervisor metadata %q", name)
		}
		if Generated(name) {
			return fmt.Errorf("write denied: %q is generated; declare its generator under the plan's regenerate list instead of editing it", name)
		}
		if violations := a.Protected.Check([]string{name}); len(violations) > 0 {
			return fmt.Errorf("write denied: %s: %s", name, violations[0].Reason)
		}
		if !policy.Covers(a.WriteScope, name) {
			return fmt.Errorf("write denied: %q is outside the declared scope; re-plan before editing", name)
		}
	}
	return nil
}

// Generated reports paths a generator owns. They are never edited by hand:
// the next run of the generator would discard the edit, and the discrepancy
// surfaces as a build failure about the generated file rather than about the
// schema that actually moved. A plan that needs one changed declares its
// generator instead (review §9.3).
func Generated(rel string) bool {
	base := path.Base(rel)
	return strings.HasSuffix(base, ".pb.go") || strings.HasSuffix(base, "_gen.go") ||
		strings.HasPrefix(base, "zz_generated") || strings.HasPrefix(rel, "dist/") ||
		strings.Contains(rel, "/dist/")
}
