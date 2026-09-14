package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/config"
)

// TestShippedProfiles validates every profile in profiles/. CI runs this as a
// gate: a shipped profile that does not parse or whose budgets do not add up
// would fail at startup on a user's machine instead.
func TestShippedProfiles(t *testing.T) {
	// The embedded set is what users actually get, in every install shape.
	embedded := config.Embedded()
	if len(embedded) == 0 {
		t.Fatal("no profiles are embedded in the binary")
	}
	for _, name := range embedded {
		if _, err := config.LoadEmbeddedProfile(name); err != nil {
			t.Errorf("embedded profile %s does not load: %v", name, err)
		}
	}
	// An empty directory must still list the embedded profiles.
	listed, err := config.ListProfiles(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(embedded) {
		t.Errorf("listing an empty directory returned %d profiles, expected the %d embedded ones",
			len(listed), len(embedded))
	}

	dir := findProfilesDir(t)
	names, err := config.ListProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatalf("no profiles found under %s", dir)
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			p, err := config.LoadProfile(dir, name)
			if err != nil {
				t.Fatalf("profile %s does not load: %v", name, err)
			}
			if p.Name != name {
				t.Errorf("profile file %s.yaml declares name %q", name, p.Name)
			}
			if p.Description == "" {
				t.Error("a profile must describe what hardware it targets")
			}
			// Every shipped profile is an unmeasured starting point, and must
			// say so: §9.2 wants real numbers to come from `le models bench`.
			if p.Measured == nil && !strings.Contains(strings.ToLower(p.Description), "unmeasured") &&
				!strings.Contains(strings.ToLower(p.Description), "external") &&
				!strings.Contains(strings.ToLower(p.Description), "hosted") {
				t.Error("a profile with no measurement must say so in its description")
			}
		})
	}

	// The default configuration must name a profile that actually ships, and
	// it must resolve from an empty data directory too.
	def := config.Default()
	if _, err := config.LoadProfile(dir, def.Profile); err != nil {
		t.Errorf("the default profile %q does not exist: %v", def.Profile, err)
	}
	if _, err := config.LoadProfile(t.TempDir(), def.Profile); err != nil {
		t.Errorf("the default profile %q does not resolve without an on-disk copy: %v", def.Profile, err)
	}
}

// A profile on disk must win over an embedded one of the same name, so a
// measurement always overrides a shipped starting point.
func TestOnDiskProfileOverridesEmbedded(t *testing.T) {
	dir := t.TempDir()
	name := config.Default().Profile

	override := config.Profile{
		Name: name, Description: "measured on this machine",
		ContextTokens: 4096, MaxPacketTokens: 1000, ReservedOutput: 500,
		PeakVRAMMB: 1234,
	}
	if err := config.SaveProfile(dir, override); err != nil {
		t.Fatal(err)
	}
	got, err := config.LoadProfile(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if got.PeakVRAMMB != 1234 {
		t.Fatalf("the embedded profile won over the on-disk one: %+v", got)
	}
	if !config.OnDisk(dir, name) {
		t.Error("OnDisk must report a profile written to the directory")
	}
}

func findProfilesDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		candidate := filepath.Join(dir, "profiles")
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("profiles/ not found from the test working directory")
	return ""
}

func TestProfileBudgetsMustAddUp(t *testing.T) {
	p := config.Profile{Name: "bad", ContextTokens: 1000, MaxPacketTokens: 900, ReservedOutput: 500}
	if err := p.Validate(); err == nil {
		t.Fatal("a packet cap plus reserved output exceeding the window must be rejected")
	}
}

func TestFallbackProfileIsValidAndSaysItIsAFallback(t *testing.T) {
	p := config.FallbackProfile()
	if err := p.Validate(); err != nil {
		t.Fatalf("the fallback profile must itself be valid: %v", err)
	}
	if !strings.Contains(p.Description, "bench") {
		t.Error("the fallback profile must point at `le models bench`")
	}
}

func TestOfflineRefusesARemoteInferenceURL(t *testing.T) {
	c := config.Default()
	c.Offline = true
	c.Inference.Mode = config.ModeExternal
	c.Inference.BaseURL = "https://api.example.com"
	if err := c.Validate(); err == nil {
		t.Fatal("offline mode must reject a non-local inference URL")
	}
	c.Inference.BaseURL = "http://127.0.0.1:8080"
	if err := c.Validate(); err != nil {
		t.Fatalf("a loopback URL must be accepted in offline mode: %v", err)
	}
}

func TestExposureWarning(t *testing.T) {
	if w := config.ExposureWarning("127.0.0.1:7777", false); w != "" {
		t.Errorf("a loopback bind needs no warning, got %q", w)
	}
	if w := config.ExposureWarning("0.0.0.0:7777", true); !strings.Contains(w, "-p 127.0.0.1:7777:7777") {
		t.Errorf("in a container the warning must name the safe publish flag, got %q", w)
	}
	if w := config.ExposureWarning("0.0.0.0:7777", false); !strings.Contains(w, "not to loopback") {
		t.Errorf("on a host the warning must say the bind is not loopback, got %q", w)
	}
}

func TestEnvOverridesTheBindAddress(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvAPIAddr, "0.0.0.0:9999")
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.API.Addr != "0.0.0.0:9999" {
		t.Fatalf("LE_API_ADDR was not honoured: %q", cfg.API.Addr)
	}
}

// A generated le.yaml must carry the excludes. Leaving them nil in Default
// wrote `excludes: []`, which on the next load is an empty-but-present list —
// and the indexer then walked .git and node_modules.
func TestGeneratedConfigCarriesExcludes(t *testing.T) {
	dir := t.TempDir()
	if err := config.Save(dir, config.Default()); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Index.Excludes) == 0 {
		t.Fatal("a generated configuration must carry the default excludes, or indexing walks .git")
	}
	want := map[string]bool{".git": true, "node_modules": true, "vendor": true, ".le": true}
	got := map[string]bool{}
	for _, e := range loaded.Index.Excludes {
		got[e] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("the default excludes are missing %q", name)
		}
	}
}
