package llm_test

import (
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/llm"
)

// §9.3 lists the limits that must never be hardcoded because they are facts
// about a model and a machine rather than about this code. How long one
// request takes is such a fact: it is prefill of context_tokens plus decode of
// reserved_output_tokens at the rates `le models bench` measured. A fixed
// client timeout smaller than that makes a documented, tunable budget unusable
// against a constant nobody can tune, and the failure arrives as a network
// error rather than as arithmetic.
func TestAProviderCanDeclareItsTimeout(t *testing.T) {
	f := llm.ProvidersFile{
		Default: "local",
		Providers: []llm.ProviderSpec{{
			Name: "local", Kind: llm.KindLlamaCPP,
			BaseURL: "http://127.0.0.1:8080", Model: "m",
			TimeoutSeconds: 1800,
		}},
	}
	if _, err := llm.NewRouter(f, false); err != nil {
		t.Fatalf("a declared timeout must be accepted: %v", err)
	}
}

// Zero means "use the default", which is what every existing providers.yaml
// says by omission. Backward compatibility is the point: adding this field
// must not change what an unmodified configuration does.
func TestAnOmittedTimeoutKeepsTheDefault(t *testing.T) {
	f := llm.DefaultProvidersFile("http://127.0.0.1:8080", "m")
	if f.Providers[0].TimeoutSeconds != 0 {
		t.Fatalf("the shipped configuration should declare no timeout, got %d",
			f.Providers[0].TimeoutSeconds)
	}
	if _, err := llm.NewRouter(f, false); err != nil {
		t.Fatalf("the shipped configuration must still build: %v", err)
	}
	if llm.DefaultTimeout <= 0 {
		t.Error("the fallback timeout must be positive, or every request fails immediately")
	}
}

// A negative timeout is a typo, and http.Client treats a negative duration as
// no deadline at all — the opposite of what the operator wrote. Refusing at
// construction turns it into a startup error instead of a request that hangs.
func TestANegativeTimeoutIsRefused(t *testing.T) {
	f := llm.ProvidersFile{
		Default: "local",
		Providers: []llm.ProviderSpec{{
			Name: "local", Kind: llm.KindLlamaCPP,
			BaseURL: "http://127.0.0.1:8080", Model: "m",
			TimeoutSeconds: -30,
		}},
	}
	_, err := llm.NewRouter(f, false)
	if err == nil {
		t.Fatal("a negative timeout_seconds was accepted; http.Client reads it as no deadline")
	}
	if !strings.Contains(err.Error(), "timeout_seconds") {
		t.Errorf("the error must name the field, got %v", err)
	}
}
