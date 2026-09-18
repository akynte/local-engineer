package recipe_test

import (
	"github.com/akynte/local-engineer/internal/recipe"
	"os"
	"path/filepath"
	"testing"
)

func TestMonorepoPresetsCoverEveryModule(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"a/go.mod", "b/go.mod", "rust/Cargo.toml", "ui/package.json", "ui/apps/web/package.json"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		body := ""
		if filepath.Ext(path) == ".json" {
			body = `{"packageManager":"pnpm@9.12.3","scripts":{"build":"build","test":"test","typecheck":"tsc"}}`
		}
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	presets, err := recipe.DiscoverPresets(root, recipe.Standard)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, p := range presets {
		counts[p.Dir]++
	}
	if counts["a"] != 4 || counts["b"] != 4 || counts["rust"] != 4 || counts["ui"] != 3 || counts["ui/apps/web"] != 0 {
		t.Fatalf("wrong discovery: %+v", counts)
	}
	results := make([]recipe.Result, 0, len(presets))
	for _, p := range presets {
		results = append(results, recipe.Result{Recipe: p.Name, Kind: p.Kind, Status: recipe.Pass, Candidate: "code"})
	}
	if ok, _ := recipe.CheckPresets(presets, results, "code"); !ok {
		t.Fatal("complete evidence refused")
	}
	if ok, _ := recipe.CheckPresets(presets, results[1:], "code"); ok {
		t.Fatal("another module's pass hid missing evidence")
	}
}

func TestPresetConfigCannotEscapeAndRejectsUnknownFields(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".agent"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		"version=1\n[[presets]]\nname='bad'\nkind='test'\nargv=['go','test']\ndir='../outside'\ntimeout_seconds=10\n",
		"version=1\nunknown=true\n",
	} {
		if err := os.WriteFile(filepath.Join(root, ".agent/verify.toml"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := recipe.DiscoverPresets(root, recipe.Standard); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}

func TestGoJSONKeepsIndividualBaselineTests(t *testing.T) {
	status, summary := recipe.GoTestJSON(1, "{\"Action\":\"pass\",\"Package\":\"p\",\"Test\":\"TestGood\"}\n{\"Action\":\"fail\",\"Package\":\"p\",\"Test\":\"TestBad\"}\n", "")
	if status != recipe.Fail || summary.Tests["p/TestGood"] != recipe.Pass || summary.Tests["p/TestBad"] != recipe.Fail {
		t.Fatalf("lost tests: %+v", summary)
	}
}
