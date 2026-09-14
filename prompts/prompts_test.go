package prompts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/prompts"
)

// §8.2 lays a packet out cache-first: the system turn is the stable prefix the
// prompt cache reuses across every step of a task. A value interpolated into it
// changes the prefix and loses the cache on every call — which is invisible
// except as the whole thing being slower.
func TestNoInterpolationInPrompts(t *testing.T) {
	for _, f := range promptFiles(t) {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range []string{"%s", "%d", "%v", "{{", "${"} {
			if strings.Contains(string(body), marker) {
				t.Errorf("%s contains %q. Anything task-specific belongs in the user "+
					"turn: interpolating here breaks the cache prefix for every "+
					"subsequent step.", filepath.Base(f), marker)
			}
		}
	}
}

// Exhortation costs prefill on every call for no measured effect. §10.1 rejects
// "reflection without new evidence" on the same grounds, and a prompt telling a
// small model to be careful is the same trade with worse odds.
func TestNoExhortation(t *testing.T) {
	banned := []string{
		"think step by step", "be careful", "you are an expert",
		"take your time", "very important", "do your best",
	}
	for _, f := range promptFiles(t) {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(body))
		for _, phrase := range banned {
			if strings.Contains(lower, phrase) {
				t.Errorf("%s contains %q. If the behaviour matters, name the action "+
					"that produces it instead.", filepath.Base(f), phrase)
			}
		}
	}
}

// The engine's prompt has to name the loop and the stopping condition, because
// that is the part that changes what a small model does.
func TestEngineSystemNamesTheLoop(t *testing.T) {
	p := prompts.EngineSystem()
	if p == "" {
		t.Fatal("the engine system prompt is empty")
	}
	for _, want := range []string{"run_verification", "done", "impact_of", "edit_file"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt does not mention %s, which is part of the loop it "+
				"is meant to describe", want)
		}
	}
}

// A prompt long enough to notice is prefill spent on every step of every task.
func TestPromptsStaySmall(t *testing.T) {
	const cap = 2000
	for _, f := range promptFiles(t) {
		st, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() > cap {
			t.Errorf("%s is %d bytes, over the %d-byte cap; this is paid on every "+
				"call of every task", filepath.Base(f), st.Size(), cap)
		}
	}
}

func promptFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") && e.Name() != "README.md" {
			out = append(out, e.Name())
		}
	}
	if len(out) == 0 {
		t.Fatal("no prompt files found")
	}
	return out
}
