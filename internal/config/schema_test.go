package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"

	"github.com/akynte/local-engineer/internal/config"
	"github.com/akynte/local-engineer/internal/eval"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/policy"
)

// §1.2 lists "schema validation (all JSON Schemas compile; all YAML policies
// validate)" as a CI gate. These are that gate.
//
// The third test is the one that matters. A schema that drifts from the code it
// describes is worse than no schema: it calls a file valid that the program
// then rejects, and the error the user sees comes from somewhere else entirely.
// So the schemas are checked against the loader, not just against themselves.

func schemaDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		p := filepath.Join(dir, "schemas")
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("schemas/ not found from the test's working directory")
	return ""
}

func compile(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join(schemaDir(t), name)
	s, err := jsonschema.Compile(path)
	if err != nil {
		t.Fatalf("%s does not compile: %v", name, err)
	}
	return s
}

// yamlAsJSON converts a YAML document into the any-shape a JSON Schema
// validator expects. Going through JSON is deliberate: it is what catches a
// YAML value that has no JSON equivalent, which a user's editor would also
// refuse.
func yamlAsJSON(t *testing.T, body []byte) any {
	t.Helper()
	var doc any
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("not valid YAML: %v", err)
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("not representable as JSON: %v", err)
	}
	var out any
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// 1. Every schema compiles.
func TestSchemasCompile(t *testing.T) {
	entries, err := os.ReadDir(schemaDir(t))
	if err != nil {
		t.Fatal(err)
	}
	var found int
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		found++
		compile(t, e.Name())
	}
	if found == 0 {
		t.Fatal("no schemas found; the CI gate would pass over an empty directory")
	}
}

// 2. Every shipped example validates.
func TestDefaultConfigValidates(t *testing.T) {
	s := compile(t, "le.schema.json")
	dir := t.TempDir()
	if err := config.Save(dir, config.Default()); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(config.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(yamlAsJSON(t, body)); err != nil {
		t.Fatalf("the shipped default configuration does not match its own schema:\n%v", err)
	}
}

func TestShippedProfilesValidateAgainstSchema(t *testing.T) {
	s := compile(t, "profile.schema.json")
	dir := findProfilesDir(t)
	names, err := config.ListProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(dir, name+".yaml"))
			if err != nil {
				t.Skip("profile is embedded, not on disk")
			}
			if err := s.Validate(yamlAsJSON(t, body)); err != nil {
				t.Errorf("%s does not match the profile schema:\n%v", name, err)
			}
		})
	}
}

func TestDefaultProvidersValidate(t *testing.T) {
	s := compile(t, "providers.schema.json")
	dir := t.TempDir()
	if err := llm.SaveProvidersFile(dir, llm.DefaultProvidersFile("http://127.0.0.1:8080", "local")); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "providers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(yamlAsJSON(t, body)); err != nil {
		t.Fatalf("the shipped default providers file does not match its schema:\n%v", err)
	}
}

func TestShippedTasksValidate(t *testing.T) {
	s := compile(t, "task.schema.json")
	dir := findTasksDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".task.yaml") {
			continue
		}
		checked++
		t.Run(e.Name(), func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Validate(yamlAsJSON(t, body)); err != nil {
				t.Errorf("%s does not match the task schema:\n%v", e.Name(), err)
			}
		})
	}
	if checked == 0 {
		t.Fatal("no task files found")
	}
}

// 3. The schema and the loader must agree. This is what stops them drifting:
// adding a required field to a struct without adding it here fails the test,
// and a schema that accepts something the loader rejects fails it too.
func TestSchemaAndLoaderAgree(t *testing.T) {
	s := compile(t, "le.schema.json")

	cases := []struct {
		name string
		yaml string
		// valid says what BOTH the schema and the loader must conclude.
		valid bool
		why   string
	}{
		{
			name:  "the default",
			yaml:  "profile: reference-8gb-cuda-64gb-ram\n",
			valid: true,
		},
		{
			name:  "an unknown inference mode",
			yaml:  "inference:\n  mode: externl\n  base_url: http://x\n",
			valid: false,
			why:   "a misspelled mode must fail by name rather than falling back to a default",
		},
		{
			name:  "external mode with no base url",
			yaml:  "inference:\n  mode: external\n",
			valid: false,
			why:   "external inference with nowhere to send it is a misconfiguration",
		},
		{
			name:  "an acp address with no agent",
			yaml:  "api:\n  addr: 127.0.0.1:7777\n  acp_addr: 127.0.0.1:7778\n",
			valid: false,
			why:   "the bridge would accept connections and have no agent to hand them to",
		},
		{
			name:  "an acp address with an agent",
			yaml:  "api:\n  acp_addr: 127.0.0.1:7778\n  acp_command: [\"opencode\", \"acp\"]\n",
			valid: true,
		},
		{
			name:  "an unknown top-level key",
			yaml:  "colour: blue\n",
			valid: false,
			why:   "a typo in a key name silently does nothing unless something rejects it",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schemaErr := s.Validate(yamlAsJSON(t, []byte(tc.yaml)))

			dir := t.TempDir()
			if err := os.WriteFile(config.Path(dir), []byte(tc.yaml), 0o640); err != nil {
				t.Fatal(err)
			}
			_, loaderErr := config.Load(dir)

			schemaOK := schemaErr == nil
			loaderOK := loaderErr == nil

			if schemaOK != tc.valid {
				t.Errorf("schema says valid=%v, want %v. %s\n%v", schemaOK, tc.valid, tc.why, schemaErr)
			}
			if loaderOK != tc.valid {
				t.Errorf("loader says valid=%v, want %v. %s\n%v", loaderOK, tc.valid, tc.why, loaderErr)
			}
			if schemaOK != loaderOK {
				t.Errorf("the schema and the loader disagree (schema=%v, loader=%v). "+
					"A user would be told a file is fine and then have it rejected, or "+
					"the other way round.", schemaOK, loaderOK)
			}
		})
	}
}

func findTasksDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		p := filepath.Join(dir, "evals", "tasks")
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("evals/tasks not found")
	return ""
}

var _ = eval.Task{}

// §1.2's gate is "all JSON Schemas compile; all YAML policies validate". This
// is the second half.
func TestShippedPoliciesValidate(t *testing.T) {
	s := compile(t, "policy.schema.json")
	dir := findDirUpwards(t, "policies")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		checked++
		t.Run(e.Name(), func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Validate(yamlAsJSON(t, body)); err != nil {
				t.Errorf("%s does not match the policy schema:\n%v", e.Name(), err)
			}
		})
	}
	if checked == 0 {
		t.Fatal("no policy files found; the gate would pass over an empty directory")
	}
}

// The policy schema and the policy loader must agree, for the same reason the
// config schema and its loader must.
func TestPolicySchemaAndLoaderAgree(t *testing.T) {
	s := compile(t, "policy.schema.json")
	cases := []struct {
		name  string
		yaml  string
		valid bool
	}{
		{"a complete rule", "name: t\nprotected:\n  - path: a/**\n    reason: because\n", true},
		{"a rule with no reason", "name: t\nprotected:\n  - path: a/**\n", false},
		{"a policy protecting nothing", "name: t\nprotected: []\n", false},
		{"a policy with no name", "protected:\n  - path: a/**\n    reason: because\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schemaOK := s.Validate(yamlAsJSON(t, []byte(tc.yaml))) == nil

			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(tc.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			_, loadErr := policy.Load(dir)
			loaderOK := loadErr == nil

			if schemaOK != tc.valid || loaderOK != tc.valid {
				t.Errorf("schema=%v loader=%v, want %v (%v)", schemaOK, loaderOK, tc.valid, loadErr)
			}
			if schemaOK != loaderOK {
				t.Errorf("the schema and the loader disagree: a user would be told a "+
					"policy is fine and then have it rejected (schema=%v, loader=%v)",
					schemaOK, loaderOK)
			}
		})
	}
}

func findDirUpwards(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
		dir = filepath.Dir(dir)
	}
	t.Skipf("%s not found", name)
	return ""
}
