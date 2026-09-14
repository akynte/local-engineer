package models

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/akynte/local-engineer/internal/llm"
)

// fakeProvider answers with fixed token counts and, optionally, a per-phase
// timing split, while actually sleeping for the wall clock the two phases add
// up to. That is what lets the test tell a real measurement apart from one
// that charges each phase for the other's time.
type fakeProvider struct {
	prompt, cached, output int
	prefillMS, decodeMS    int64
	report                 bool
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Capabilities() llm.Capabilities {
	return llm.Capabilities{Kind: llm.KindLlamaCPP, Local: true, MaxContext: 32768, ThinkingControl: true}
}

func (f *fakeProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	time.Sleep(time.Duration(f.prefillMS+f.decodeMS) * time.Millisecond)
	resp := &llm.ChatResponse{
		Content: "ok", Model: "fake-model", FinishReason: "stop",
		PromptTokens: f.prompt, CachedTokens: f.cached, OutputTokens: f.output,
	}
	if f.report {
		resp.PrefillMS, resp.DecodeMS = f.prefillMS, f.decodeMS
	}
	return resp, nil
}

func (f *fakeProvider) ChatStructured(ctx context.Context, req llm.ChatRequest, _ json.RawMessage) (*llm.ChatResponse, error) {
	return f.Chat(ctx, req)
}
func (f *fakeProvider) Embed(context.Context, llm.EmbedRequest) (*llm.EmbedResponse, error) {
	return nil, &llm.UnsupportedError{Provider: "fake", Capability: "embeddings"}
}
func (f *fakeProvider) Infill(context.Context, llm.InfillRequest) (*llm.ChatResponse, error) {
	return nil, &llm.UnsupportedError{Provider: "fake", Capability: "infill"}
}
func (f *fakeProvider) Health(context.Context) error { return nil }
func (f *fakeProvider) Close() error                 { return nil }

// TestBenchUsesReportedPhaseTimings pins the bug this test was written for:
// prefill and decode are separate phases of one call, and dividing both by the
// same wall clock understates each by the other's share. With 1000 uncached
// prompt tokens prefilled in 200ms the rate is 5000 tok/s, not 1000/(0.2+0.8).
func TestBenchUsesReportedPhaseTimings(t *testing.T) {
	p := &fakeProvider{prompt: 1000, output: 400, prefillMS: 200, decodeMS: 800, report: true}
	res, err := Bench(context.Background(), p, BenchOptions{Iterations: 1, PromptTokens: 1000, OutputTokens: 400})
	if err != nil {
		t.Fatal(err)
	}
	if res.TimingSource != TimingReported {
		t.Errorf("timing source = %q, want %q", res.TimingSource, TimingReported)
	}
	// 1000 tokens / 0.2s = 5000 tok/s. Charging it the decode time too would
	// give 1000 tok/s, so a generous tolerance still catches the bug.
	if res.PrefillTokensSec < 4000 {
		t.Errorf("prefill = %.0f tok/s, want ~5000; the decode phase is being "+
			"charged to prefill", res.PrefillTokensSec)
	}
	// 400 tokens / 0.8s = 500 tok/s; conflated it would be 400.
	if res.DecodeTokensSec < 450 {
		t.Errorf("decode = %.0f tok/s, want ~500", res.DecodeTokensSec)
	}
}

// TestBenchApportionsWhenUnreported checks the fallback: a provider with no
// token-level timings still gets a split proportional to token counts, and the
// result says the numbers are an estimate rather than a measurement.
func TestBenchApportionsWhenUnreported(t *testing.T) {
	p := &fakeProvider{prompt: 1000, output: 1000, prefillMS: 500, decodeMS: 500}
	res, err := Bench(context.Background(), p, BenchOptions{Iterations: 1, PromptTokens: 1000, OutputTokens: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if res.TimingSource != TimingEstimated {
		t.Errorf("timing source = %q, want %q", res.TimingSource, TimingEstimated)
	}
	// Equal token counts split the second evenly: ~2000 tok/s each. Dividing
	// both by the full second would give ~1000.
	if res.PrefillTokensSec < 1500 || res.DecodeTokensSec < 1500 {
		t.Errorf("prefill %.0f / decode %.0f tok/s, want ~2000 each",
			res.PrefillTokensSec, res.DecodeTokensSec)
	}
}

// TestBenchExcludesCachedTokensFromPrefill guards the other half of the rate:
// tokens served from the prompt cache were never prefilled, so counting them
// inflates the measured throughput.
func TestBenchExcludesCachedTokensFromPrefill(t *testing.T) {
	p := &fakeProvider{prompt: 1000, cached: 900, output: 100, prefillMS: 100, decodeMS: 100, report: true}
	res, err := Bench(context.Background(), p, BenchOptions{Iterations: 1, PromptTokens: 1000, OutputTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	// 100 uncached tokens / 0.1s = 1000 tok/s. Counting the cached 900 too
	// would report 10000.
	if res.PrefillTokensSec > 2000 {
		t.Errorf("prefill = %.0f tok/s; cached tokens are being counted as "+
			"prefilled work", res.PrefillTokensSec)
	}
	if res.CacheReusePct < 89 || res.CacheReusePct > 91 {
		t.Errorf("cache reuse = %.1f%%, want 90%%", res.CacheReusePct)
	}
}

// TestProfileFromStatesTimingProvenance keeps the profile honest: a reader must
// be able to tell whether the rates in it were measured or apportioned.
func TestProfileFromStatesTimingProvenance(t *testing.T) {
	measured := ProfileFrom(Result{TimingSource: TimingReported, DeclaredContext: 32768}, "m", 0)
	if !contains(measured.Description, "per-phase timings") {
		t.Errorf("measured profile description does not say the rates were measured: %q", measured.Description)
	}
	est := ProfileFrom(Result{TimingSource: TimingEstimated, DeclaredContext: 32768}, "e", 0)
	if !contains(est.Description, "apportioned") {
		t.Errorf("estimated profile description does not say the rates were apportioned: %q", est.Description)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// TestBenchIgnoresFullyCachedPrefill pins the second half of the same problem:
// an iteration the prompt cache served almost entirely did no prefill worth
// timing, so its ratio is per-request overhead, not throughput. Believing it
// halved the recorded prefill rate between two runs against the same server.
func TestBenchIgnoresFullyCachedPrefill(t *testing.T) {
	// 8000 prompt tokens, 7990 of them cached: 10 tokens of real prefill.
	p := &fakeProvider{prompt: 8000, cached: 7990, output: 200, prefillMS: 120, decodeMS: 400, report: true}
	res, err := Bench(context.Background(), p, BenchOptions{Iterations: 2, PromptTokens: 8000, OutputTokens: 200})
	if err != nil {
		t.Fatal(err)
	}
	if res.PrefillTokensSec != 0 {
		t.Errorf("prefill = %.1f tok/s from %d uncached tokens; a cache hit is "+
			"not a prefill measurement", res.PrefillTokensSec, 10)
	}
	// The cache reuse itself is still reported — it is a real property.
	if res.CacheReusePct < 99 {
		t.Errorf("cache reuse = %.1f%%, want ~99.9%%", res.CacheReusePct)
	}
	// Decode is unaffected by the cache and must still be measured.
	if res.DecodeTokensSec < 400 {
		t.Errorf("decode = %.0f tok/s, want ~500", res.DecodeTokensSec)
	}
}

// TestStableFillerDiffersBetweenRuns is what makes the first iteration of a run
// a cold prefill: a byte-identical prefix would be served from the cache the
// previous run left behind.
func TestStableFillerDiffersBetweenRuns(t *testing.T) {
	a, b := stableFiller(256, runNonce()), stableFiller(256, runNonce())
	if a == b {
		t.Fatal("two runs produced an identical prefix; the second would measure " +
			"the prompt cache rather than prefill")
	}
	// Within one run the prefix must stay stable, or cache reuse is never
	// exercised and §8.2's layout goes unmeasured.
	n := runNonce()
	if stableFiller(256, n) != stableFiller(256, n) {
		t.Fatal("the prefix is not stable within a run")
	}
}
