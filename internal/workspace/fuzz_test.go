package workspace

// Identity derivation must be total, stable and collision-resistant across the
// three components. A workspace id that varied run to run, or that collided
// between distinct inputs, would break isolation directly.

import (
	"strings"
	"testing"
)

func FuzzDeriveID(f *testing.F) {
	f.Add("/home/a/project", "git@github.com:a/b.git", "project")
	f.Add("", "", "")
	f.Add("/a\x00b", "x", "y")
	f.Add(strings.Repeat("/deep", 200), "", "name")

	f.Fuzz(func(t *testing.T, root, remote, name string) {
		id := DeriveID(root, remote, name)

		if len(id) != IDLength {
			t.Fatalf("id %q has length %d, want %d", id, len(id), IDLength)
		}
		if !id.Valid() {
			t.Fatalf("DeriveID produced an id that fails its own validator: %q", id)
		}
		// Stable: the same inputs always give the same id.
		if again := DeriveID(root, remote, name); again != id {
			t.Fatalf("derivation is not deterministic: %q then %q", id, again)
		}
		// The components must not be able to slide into one another. Moving a
		// character across the boundary must change the id, or two different
		// projects could share a workspace.
		if root != "" {
			shifted := DeriveID(root[:len(root)-1], root[len(root)-1:]+remote, name)
			if shifted == id {
				t.Fatalf("component boundary is ambiguous: (%q,%q,%q) collides", root, remote, name)
			}
		}
	})
}

func FuzzRepositoryID(f *testing.F) {
	f.Add("aaaaaaaaaaaaaaaaaaaaaaaaaa", "services/api", "git@x:y.git")
	f.Add("", "", "")

	f.Fuzz(func(t *testing.T, ws, rel, remote string) {
		got := DeriveRepositoryID(ID(ws), rel, remote)
		if len(got) != IDLength {
			t.Fatalf("repository id %q has length %d", got, len(got))
		}
		// A repository id must differ between workspaces even for the same
		// path: repository ids appear in index rows and must not collide.
		other := DeriveRepositoryID(ID(ws+"x"), rel, remote)
		if got == other {
			t.Fatalf("repository id collides across workspaces for path %q", rel)
		}
	})
}
