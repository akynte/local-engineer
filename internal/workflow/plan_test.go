package workflow_test

import (
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/workflow"
)

func plan() workflow.Plan {
	return workflow.Plan{
		RootCause:      "the account limit check reads committed totals alone",
		Files:          []string{"checks/order_check.go"},
		Symbols:        []string{"accountLimitExceeded"},
		Tests:          []string{"TestOrderCheck_Check"},
		WriteAllowlist: []string{"checks/order_check.go"},
	}
}

// Hand-editing generated output is a change the next generator run discards,
// so the plan has to name the generator instead of the file.
func TestPlanCannotGrantWritesToGeneratedFiles(t *testing.T) {
	for _, file := range []string{"api/events.pb.go", "risk/mock_gen.go", "dist/bundle.js", "zz_generated_deepcopy.go"} {
		p := plan()
		p.Files = append(p.Files, file)
		p.WriteAllowlist = append(p.WriteAllowlist, file)
		err := p.Validate(nil)
		if err == nil {
			t.Fatalf("%s was granted as a plain write", file)
		}
		if !strings.Contains(err.Error(), "regenerate") {
			t.Fatalf("the refusal does not say what to do instead: %v", err)
		}
	}
}

func TestPlanWithoutGeneratedFilesStillValidates(t *testing.T) {
	if err := plan().Validate(nil); err != nil {
		t.Fatal(err)
	}
}

// A model selects a generator; it never supplies one. The presets were frozen
// during INTAKE from the operator's own configuration.
func TestRegenerationResolvesOnlyFrozenGeneratePresets(t *testing.T) {
	presets := []recipe.Preset{
		{Name: "proto", Kind: recipe.KindGenerate, Argv: []string{"buf", "generate"}, TimeoutSeconds: 120},
		{Name: "go test", Kind: recipe.KindTest, Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 600},
	}

	p := plan()
	p.Regenerate = []string{"proto"}
	if err := p.ValidateRegeneration(presets); err != nil {
		t.Fatal(err)
	}
	if !p.RegeneratesGenerated() {
		t.Fatal("a plan that declared a generator does not report one")
	}

	p.Regenerate = []string{"buf generate --template custom.yaml"}
	if err := p.ValidateRegeneration(presets); err == nil {
		t.Fatal("a command the operator never froze was accepted as a generator")
	}

	// Selecting the test preset as a "generator" would run an arbitrary frozen
	// command under the exemption that makes its output in-scope.
	p.Regenerate = []string{"go test"}
	err := p.ValidateRegeneration(presets)
	if err == nil || !strings.Contains(err.Error(), "generate") {
		t.Fatalf("a non-generate preset was accepted: %v", err)
	}
}

func TestPlanWithoutRegenerationReportsNone(t *testing.T) {
	p := plan()
	if p.RegeneratesGenerated() {
		t.Fatal("a plan with no generators reports one")
	}
	if err := p.ValidateRegeneration(nil); err != nil {
		t.Fatal(err)
	}
}
