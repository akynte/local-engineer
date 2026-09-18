package firewall

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akynte/local-engineer/internal/policy"
)

func TestAccessChecksBothNamesBeforeWrites(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"allowed", "other"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for alias, target := range map[string]string{"allowed/alias": "../other", "public.txt": ".env", "escape": t.TempDir()} {
		if err := os.Symlink(target, filepath.Join(root, alias)); err != nil {
			t.Fatal(err)
		}
	}
	a := Access{WriteScope: []string{"allowed"}, Protected: policy.Set{Policies: []policy.Policy{
		{Name: "operator", Protected: []policy.Rule{{Path: "allowed/frozen.go", Reason: "API freeze"}}},
	}}}
	for _, tc := range []struct {
		path         string
		write, allow bool
	}{
		{"allowed/new/sub/file.go", true, true},
		{"other/file.go", true, false},
		{"allowed/alias/new/sub/file.go", true, false},
		{"allowed/frozen.go", true, false},
		{"allowed/types.pb.go", true, false},
		{"allowed/zz_generated.go", true, false},
		{"allowed/dist/app.js", true, false},
		{"other/file.go", false, true},
		{".env", false, false},
		{"public.txt", false, false},
		{"allowed/credentials.json", false, false},
		{"allowed/key.pem", false, false},
		{".git/config", false, false},
		{"escape/new/sub/file.go", true, false},
		{"../outside", true, false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if err := a.Check(root, tc.path, tc.write); (err == nil) != tc.allow {
				t.Fatalf("allow=%v, got %v", tc.allow, err)
			}
		})
	}
}

func TestEmptyScopeAndMetadataFailClosed(t *testing.T) {
	root := t.TempDir()
	if err := (Access{}).Check(root, "new.go", true); err == nil {
		t.Fatal("empty scope permitted write")
	}
	for _, name := range []string{".le/workspace.yaml", ".agent/policy.toml", ".env", "key.pem"} {
		if err := (Access{WriteScope: []string{"."}}).Check(root, name, true); err == nil {
			t.Fatalf("permitted %s", name)
		}
	}
}
