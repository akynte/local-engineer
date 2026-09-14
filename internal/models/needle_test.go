package models

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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
		body := buildHaystack(500, 0.5, "", tokenModel{CharsPerToken: 3.5})
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
	body := buildHaystack(2000, 0.5, "abc123", tokenModel{CharsPerToken: 3.5})
	if !strings.Contains(body, "func handler") || !strings.Contains(body, "context.Context") {
		t.Error("the filler is not code-shaped")
	}
	if !strings.Contains(body, "abc123") {
		t.Error("the needle is not in the haystack")
	}
}

// Placement actually moves the needle, or every probe measures the same thing.
func TestPlacementMovesTheNeedle(t *testing.T) {
	early := strings.Index(buildHaystack(4000, 0.0, "needle", tokenModel{CharsPerToken: 3.5}), "needle")
	late := strings.Index(buildHaystack(4000, 1.0, "needle", tokenModel{CharsPerToken: 3.5}), "needle")
	if early >= late {
		t.Errorf("placement did not move the needle: 0%% at %d, 100%% at %d", early, late)
	}
}

// counting is a provider that reports usage, like a real llama-server does.
// Its tokenizer is denser than the builder's default estimate, which is the
// condition that produced the original defect: the sweep asked for 32,000
// tokens and sent 36,526.
type counting struct {
	recaller
	// charsPerToken is this fake tokenizer's real ratio.
	charsPerToken float64
	// contextTokens refuses anything larger, the way llama-server does.
	contextTokens int
	// overhead is the fixed cost a chat template adds, which a single-point
	// ratio cannot tell apart from the per-character cost.
	overhead  int
	sizesSent []int
}

func (c *counting) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	var n int
	for _, m := range req.Messages {
		n += len(m.Content)
	}
	tokens := int(float64(n)/c.charsPerToken) + c.overhead
	if c.contextTokens > 0 && tokens > c.contextTokens {
		return nil, fmt.Errorf("HTTP 400: request (%d tokens) exceeds the available "+
			"context size (%d)", tokens, c.contextTokens)
	}
	c.sizesSent = append(c.sizesSent, tokens)
	resp, err := c.recaller.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	resp.PromptTokens = tokens
	return resp, nil
}

// The sweep sizes its haystacks from the model's own tokenizer. Assuming a
// ratio instead is what made the first run ask for 32,000 and send 36,526 —
// a 14% error in the axis the answer is read off.
func TestSweepCalibratesAgainstTheModelsTokenizer(t *testing.T) {
	c := &counting{charsPerToken: 3.0}
	res, err := Needle(context.Background(), c, NeedleOptions{
		Sizes: []int{4000}, Placements: []NeedlePlacement{0.5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TokensMeasured {
		t.Fatal("the provider reported counts and the result does not say so")
	}
	if got := res.CharsPerToken; got < 2.9 || got > 3.1 {
		t.Errorf("calibrated %.2f chars per token; the model's own ratio is 3.0", got)
	}
	// The sweep probe (the last one sent) must land near what was asked for.
	sent := c.sizesSent[len(c.sizesSent)-1]
	if off := math.Abs(float64(sent-4000)) / 4000; off > 0.05 {
		t.Errorf("asked for 4000 tokens and sent %d (%.0f%% off); the haystack was "+
			"sized from an estimate rather than from this tokenizer", sent, off*100)
	}
}

// Every verdict is read off the provider's count, not off the number asked
// for. A cap in estimated tokens is an estimate wearing a measurement's
// clothes.
func TestCapIsReportedInMeasuredTokens(t *testing.T) {
	c := &counting{charsPerToken: 3.0}
	res, err := Needle(context.Background(), c, NeedleOptions{
		Sizes: []int{4000}, Placements: []NeedlePlacement{0.5},
	})
	if err != nil {
		t.Fatal(err)
	}
	last := res.Probes[len(res.Probes)-1]
	if last.PromptTokens == 0 {
		t.Fatal("no token count was carried through to the probe")
	}
	if res.RecommendedCap != last.PromptTokens {
		t.Errorf("cap = %d but the measured size was %d; the cap came from the "+
			"requested figure", res.RecommendedCap, last.PromptTokens)
	}
	if !strings.Contains(res.Format(), "measured") {
		t.Errorf("the report does not name its axis:\n%s", res.Format())
	}
}

// A provider that reports no usage still produces a usable answer, and the
// report says the number is the size asked for rather than the size sent.
func TestNoUsageCountsStillProducesACapAndSaysItIsEstimated(t *testing.T) {
	p := &recaller{maxChars: 9000}
	res, err := Needle(context.Background(), p, NeedleOptions{Sizes: smallSizes()})
	if err != nil {
		t.Fatal(err)
	}
	if res.RecommendedCap == 0 {
		t.Fatal("a provider without usage counts produced no cap at all")
	}
	if res.TokensMeasured {
		t.Error("the result claims measured tokens from a provider that reported none")
	}
	if !strings.Contains(res.Format(), "no token counts") {
		t.Errorf("the report presents estimated sizes as measured ones:\n%s", res.Format())
	}
}

// Running out of context window is not a recall ceiling: the model was never
// asked. Reporting it as one would put a false limit in a profile.
func TestContextRefusalIsNotARecallCeiling(t *testing.T) {
	c := &counting{charsPerToken: 3.5, contextTokens: 8000}
	res, err := Needle(context.Background(), c, NeedleOptions{
		Sizes: []int{4000, 16000}, Placements: []NeedlePlacement{0.5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.StoppedAtContextLimit {
		t.Fatal("the sweep ran out of window and did not record it")
	}
	for _, probe := range res.Probes {
		if probe.Requested == 16000 && !probe.OverContext {
			t.Error("a refused probe was not marked as over-context")
		}
		if probe.Requested == 16000 && probe.Found {
			t.Error("a probe that was never answered was counted as a hit")
		}
	}
	out := res.Format()
	if !strings.Contains(out, "context window") {
		t.Errorf("the report reads as a retrieval ceiling:\n%s", out)
	}
	if strings.Contains(out, "Recommended max_packet_tokens") {
		t.Errorf("a cap was recommended from a window limit rather than a measurement:\n%s", out)
	}
}

// Placement must move the needle and change nothing else. When the filler
// either side of the needle was generated separately, depth 0% put the whole
// haystack in the six-digit half and the packet came out denser: the first
// calibrated run asked for 8000 tokens and sent 8465. The size axis and the
// depth axis have to be independent, or neither reading means what it says.
func TestPlacementDoesNotChangeTheHaystack(t *testing.T) {
	const secret = "deadbeef"
	strip := func(body string) string {
		i := strings.Index(body, "// Operations note:")
		if i < 0 {
			t.Fatalf("the needle is not in the haystack")
		}
		j := strings.Index(body[i:], "\n\n")
		return body[:i] + body[i+j+2:]
	}
	want := strip(buildHaystack(4000, 0.0, secret, tokenModel{CharsPerToken: 3.5}))
	for _, place := range DefaultPlacements() {
		got := strip(buildHaystack(4000, place, secret, tokenModel{CharsPerToken: 3.5}))
		if got != want {
			t.Errorf("depth %.0f%%: the filler differs from depth 0%% "+
				"(%d chars vs %d); placement is changing the packet, not just "+
				"where the needle sits in it", float64(place)*100, len(got), len(want))
		}
	}
}

// A single measurement cannot separate the per-character cost from the fixed
// cost of the template and the system prompt. Folding them together makes the
// ratio correct only at the size it was taken at — which is how the sweep came
// to ask for 8000 tokens and send 8245.
func TestCalibrationSeparatesFixedOverheadFromTheRatio(t *testing.T) {
	c := &counting{charsPerToken: 3.0, overhead: 250}
	m, err := calibrate(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Measured {
		t.Fatal("the provider reported counts and the model does not say so")
	}
	if math.Abs(m.CharsPerToken-3.0) > 0.05 {
		t.Errorf("ratio = %.3f; the fixed overhead was folded into it (it should be 3.0)",
			m.CharsPerToken)
	}
	// The fixed cost is the fake's own 250 plus the system prompt and the
	// question, which the sweep pays on every probe and calibration therefore
	// has to include. What matters is that it landed in the intercept and not
	// in the slope: a single-point calibration against this provider reports
	// 2.64 characters per token and undersizes a 30,000-token packet by 11%.
	if m.Overhead < 250 {
		t.Errorf("overhead = %d; the fixed cost was folded into the ratio", m.Overhead)
	}

	// And the model predicts, which is the only property anything downstream
	// depends on.
	for _, size := range []int{2000, 30000} {
		body := buildHaystack(size, 0.5, "x", m)
		var chars int
		for _, msg := range needleMessages(body) {
			chars += len(msg.Content)
		}
		got := int(float64(chars)/c.charsPerToken) + c.overhead
		if off := math.Abs(float64(got-size)) / float64(size); off > 0.01 {
			t.Errorf("a haystack built for %d tokens makes a prompt of %d (%.1f%% off)",
				size, got, off*100)
		}
	}
}

// The point of the linear model: a size asked for is the size sent, at every
// size rather than only at the one calibration used.
func TestRequestedSizeMatchesWhatIsSent(t *testing.T) {
	c := &counting{charsPerToken: 3.0, overhead: 250}
	sizes := []int{4000, 12000, 30000}
	if _, err := Needle(context.Background(), c, NeedleOptions{
		Sizes: sizes, Placements: []NeedlePlacement{0.5},
	}); err != nil {
		t.Fatal(err)
	}
	// The sweep's own probes are the tail of what the provider saw; the first
	// two requests were calibration.
	sent := c.sizesSent[len(c.sizesSent)-len(sizes):]
	for i, want := range sizes {
		if off := math.Abs(float64(sent[i]-want)) / float64(want); off > 0.01 {
			t.Errorf("asked for %d tokens and sent %d (%.1f%% off)", want, sent[i], off*100)
		}
	}
}

// Identifier width must not grow with the haystack, or a unit costs more
// tokens at the end of a long packet than at the start and the density a
// calibration measured is wrong at every other size.
func TestFillerDensityDoesNotDriftWithSize(t *testing.T) {
	m := tokenModel{CharsPerToken: 3.5}
	small := buildHaystack(2000, 1.0, "x", m)
	large := buildHaystack(30000, 1.0, "x", m)
	if !strings.Contains(small, "func handler000001") {
		t.Fatal("the filler does not use fixed-width identifiers")
	}
	for _, body := range []string{small, large} {
		for _, line := range strings.Split(body, "\n") {
			if !strings.HasPrefix(line, "func handler") {
				continue
			}
			if len(line) != len("func handler000000(ctx context.Context, req *Request) (*Response, error) {") {
				t.Fatalf("a declaration is a different width: %q", line)
			}
		}
	}
}
