package models

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/llm"
)

// recaller answers with the needle only when the haystack is at or below a
// size it will tolerate, and optionally only at certain depths. It stands in
// for a model whose retrieval degrades with length, which is the thing the
// sweep exists to find.
type recaller struct {
	maxChars int
	// loseMiddle makes it fail when the needle is buried rather than at an
	// edge, which is the common real failure and the one an average hides.
	loseMiddle bool
	calls      int
}

func (r *recaller) Name() string { return "recaller" }
func (r *recaller) Capabilities() llm.Capabilities {
	return llm.Capabilities{Kind: llm.KindLlamaCPP, Local: true}
}
func (r *recaller) Health(context.Context) error { return nil }
func (r *recaller) Close() error                 { return nil }

func (r *recaller) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	r.calls++
	body := req.Messages[len(req.Messages)-1].Content
	code := extractCode(body)

	if r.maxChars > 0 && len(body) > r.maxChars {
		return &llm.ChatResponse{Content: "I could not find an access code.", Model: "recaller"}, nil
	}
	if r.loseMiddle && buriedInTheMiddle(body, code) {
		return &llm.ChatResponse{Content: "I could not find an access code.", Model: "recaller"}, nil
	}
	return &llm.ChatResponse{Content: code, Model: "recaller"}, nil
}

func (r *recaller) ChatStructured(context.Context, llm.ChatRequest, json.RawMessage) (*llm.ChatResponse, error) {
	return nil, &llm.UnsupportedError{Provider: "recaller", Capability: "structured output"}
}
func (r *recaller) Embed(context.Context, llm.EmbedRequest) (*llm.EmbedResponse, error) {
	return nil, &llm.UnsupportedError{Provider: "recaller", Capability: "embeddings"}
}
func (r *recaller) Infill(context.Context, llm.InfillRequest) (*llm.ChatResponse, error) {
	return nil, &llm.UnsupportedError{Provider: "recaller", Capability: "infill"}
}

func extractCode(body string) string {
	const marker = "the access code is "
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i+len(marker):]
	if j := strings.IndexByte(rest, '.'); j >= 0 {
		return rest[:j]
	}
	return ""
}

func buriedInTheMiddle(body, code string) bool {
	at := strings.Index(body, code)
	if at < 0 || len(body) == 0 {
		return false
	}
	frac := float64(at) / float64(len(body))
	return frac > 0.15 && frac < 0.85
}

func smallSizes() []int { return []int{1000, 2000, 4000} }

// The cap is the largest size where retrieval still works, and it comes from
// measurement rather than from halving the context window.
func TestNeedleFindsWhereRecallStops(t *testing.T) {
	// Tolerates about 2000 tokens of code (~4 chars per token) and no more.
	p := &recaller{maxChars: 9000}
	res, err := Needle(context.Background(), p, NeedleOptions{Sizes: smallSizes()})
	if err != nil {
		t.Fatal(err)
	}
	if res.RecommendedCap == 0 {
		t.Fatal("no cap was found even though the model recalls at small sizes")
	}
	if res.RecommendedCap >= 4000 {
		t.Errorf("cap = %d; the model stops recalling well before that", res.RecommendedCap)
	}
}

// The measurement that matters: a model that recalls the edges and loses the
// middle must not be given a cap based on its good cases. A packet builder
// cannot choose where the needed slice lands.
func TestAveragingWouldHideAMiddleFailure(t *testing.T) {
	p := &recaller{loseMiddle: true}
	res, err := Needle(context.Background(), p, NeedleOptions{Sizes: smallSizes()})
	if err != nil {
		t.Fatal(err)
	}
	if res.RecommendedCap != 0 {
		t.Errorf("cap = %d for a model that loses the middle at every size; "+
			"every depth must pass, not most of them", res.RecommendedCap)
	}
	// And the failures are visible rather than reduced to a number.
	var missed int
	for _, probe := range res.Probes {
		if !probe.Found && probe.Err == "" {
			missed++
		}
	}
	if missed == 0 {
		t.Error("no missed probes were recorded")
	}
}

// The sweep stops once a size fails: a haystack that lost the needle will not
// start finding it again when it grows.
func TestSweepStopsAfterFailure(t *testing.T) {
	p := &recaller{maxChars: 5000}
	res, err := Needle(context.Background(), p, NeedleOptions{
		Sizes: []int{1000, 2000, 4000, 8000, 16000, 32000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.LargestTested >= 16000 {
		t.Errorf("kept probing to %d after recall had already failed", res.LargestTested)
	}
}

// A model that never finds it gets no cap, and the report says that is a
// finding about the model rather than a missing measurement.
func TestNoRecallAtAllProducesNoCap(t *testing.T) {
	p := &recaller{maxChars: 1}
	res, err := Needle(context.Background(), p, NeedleOptions{Sizes: []int{1000}})
	if err != nil {
		t.Fatal(err)
	}
	if res.RecommendedCap != 0 {
		t.Errorf("cap = %d for a model that found nothing", res.RecommendedCap)
	}
	if !strings.Contains(res.Format(), "not retrieving") {
		t.Errorf("the report does not say what happened:\n%s", res.Format())
	}
}

// The needle is random so it cannot be guessed. A memorable value would let a
// confabulated answer count as a hit, which is a false pass in the direction
// that flatters.
func TestNeedleIsNotGuessable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		p := &recaller{}
		if _, err := Needle(context.Background(), p, NeedleOptions{
			Sizes: []int{500}, Placements: []NeedlePlacement{0.5},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Two sweeps must not use the same secret; the haystack builder is given a
	// fresh one per probe.
	for i := 0; i < 20; i++ {
		body := buildHaystack(500, 0.5, "")
		code := extractCode(body)
		if seen[code] && code != "" {
			t.Fatal("the same needle was used twice")
		}
		seen[code] = true
	}
}

// The haystack is code-shaped, because this measures recall over a packet of
// retrieved source and recall over English is not evidence about that.
func TestHaystackIsCodeShaped(t *testing.T) {
	body := buildHaystack(2000, 0.5, "abc123")
	if !strings.Contains(body, "func handler") || !strings.Contains(body, "context.Context") {
		t.Error("the filler is not code-shaped")
	}
	if !strings.Contains(body, "abc123") {
		t.Error("the needle is not in the haystack")
	}
}

// Placement actually moves the needle, or every probe measures the same thing.
func TestPlacementMovesTheNeedle(t *testing.T) {
	early := strings.Index(buildHaystack(4000, 0.0, "needle"), "needle")
	late := strings.Index(buildHaystack(4000, 1.0, "needle"), "needle")
	if early >= late {
		t.Errorf("placement did not move the needle: 0%% at %d, 100%% at %d", early, late)
	}
}
