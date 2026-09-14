package models

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/akynte/local-engineer/internal/llm"
)

// The needle test (design v3 §8.3: "The needle test sets the hard packet cap
// per model profile").
//
// Everything else in this system assumes a packet the model can actually use.
// The cap was derived arithmetically — half the context window — which is a
// statement about arithmetic, not about the model. A window a model accepts and
// a window it retrieves from are different sizes, and the gap between them is
// where §8.3's "context-retrieval misses" come from: the needed slice was in
// the packet and the model did not use it.
//
// The measurement is the usual one, with one choice that matters. The cap is the
// largest size where **every** tested depth is recalled, not where the average
// is good. A packet builder cannot control where in a packet the needed slice
// lands, so a size that works at the edges and fails in the middle is a size
// that fails.

// NeedlePlacement is where in the packet the fact was hidden, as a fraction.
type NeedlePlacement float64

// DefaultPlacements probe the edges and the middle. Models commonly retrieve
// the start and end of a long context and lose what is between them, so an
// average over placements would hide exactly the failure worth finding.
func DefaultPlacements() []NeedlePlacement {
	return []NeedlePlacement{0.0, 0.25, 0.5, 0.75, 1.0}
}

// NeedleProbe is one measurement at one size and depth.
type NeedleProbe struct {
	// Requested is the size asked for. Kept only so a sweep can be reproduced;
	// nothing is decided from it.
	Requested int `json:"requested_tokens"`
	// PromptTokens is what the provider actually counted. Every verdict uses
	// this, because a cap derived from an estimate is an estimate wearing a
	// measurement's clothes — the first run of this test asked for 32,000 and
	// sent 36,526, a 14% error in the axis the answer is read off.
	PromptTokens int             `json:"prompt_tokens"`
	Placement    NeedlePlacement `json:"placement"`
	Found        bool            `json:"found"`
	// Answer is what the model said, kept so a failure is diagnosable rather
	// than a bare false.
	Answer string `json:"answer,omitempty"`
	// Err records a probe that could not be run. It is not a miss: a provider
	// that timed out has said nothing about recall.
	Err string `json:"error,omitempty"`
	// Model is what the provider reported, carried so the result names what
	// was actually measured.
	Model string `json:"model,omitempty"`
	// OverContext marks a probe the provider refused because the request was
	// larger than the window. It is not a recall failure and must never be
	// read as one: the model was never asked.
	OverContext bool `json:"over_context,omitempty"`
}

// tokens is the size this probe should be read off. A provider that reports no
// usage leaves only the requested figure, which is an estimate; the result
// records which of the two it is rather than presenting them alike.
func (p NeedleProbe) tokens() int {
	if p.PromptTokens > 0 {
		return p.PromptTokens
	}
	return p.Requested
}

// NeedleResult is the whole sweep.
type NeedleResult struct {
	Model  string        `json:"model"`
	Probes []NeedleProbe `json:"probes"`
	// RecommendedCap is the largest size at which every placement was found.
	// Zero means even the smallest size tested failed, which is a finding about
	// the model rather than a missing measurement.
	RecommendedCap int `json:"recommended_cap"`
	// LargestTested is the biggest packet actually measured, so a cap equal to
	// it reads as "no ceiling found below this" rather than as a measured limit.
	LargestTested int `json:"largest_tested"`
	// CharsPerToken is the ratio measured for this model, kept so a reader can
	// see the sweep sized its own haystacks rather than guessing.
	CharsPerToken float64 `json:"chars_per_token,omitempty"`
	// PromptOverhead is the fixed cost of the template, the system prompt and
	// the question — the part that does not scale with the haystack, and the
	// part a bare characters-per-token ratio silently gets wrong.
	PromptOverhead int `json:"prompt_overhead,omitempty"`
	// TokensMeasured records that the sizes above came from the provider's own
	// count rather than from the estimate used to build the haystacks. A cap
	// reported without it is a cap in estimated tokens, and the difference has
	// already been 14% once.
	TokensMeasured bool `json:"tokens_measured"`
	// StoppedAtContextLimit records that the sweep ran out of window before it
	// ran out of recall. The cap is then bounded by the context size, and
	// saying so is the difference between "the model stops retrieving here" and
	// "we could not ask it anything larger".
	StoppedAtContextLimit bool `json:"stopped_at_context_limit,omitempty"`
}

// NeedleOptions configures the sweep.
type NeedleOptions struct {
	// Sizes are the packet sizes to try, in tokens. They are tried in
	// increasing order and the sweep stops at the first size that fails every
	// placement, because larger will not be better.
	Sizes []int
	// Placements to probe at each size.
	Placements []NeedlePlacement
	Progress   func(string)
}

// DefaultCharsPerToken is the starting estimate, used only until the real ratio
// is measured. Code tokenises denser than prose, and denser than the 4.0 an
// English rule of thumb suggests.
const DefaultCharsPerToken = 3.5

// tokenModel is how this provider turns a haystack into a prompt:
//
//	tokens = chars/CharsPerToken + Overhead
//
// The overhead term is the part a bare ratio cannot express — the chat
// template, the system prompt and the question are counted by the provider and
// do not scale with the haystack. Folding them into a per-character ratio makes
// the ratio wrong at every size except the one it was measured at.
type tokenModel struct {
	CharsPerToken float64
	Overhead      int
	// Measured records that both terms came from the provider rather than from
	// the estimate below.
	Measured bool
}

func defaultTokenModel() tokenModel {
	return tokenModel{CharsPerToken: DefaultCharsPerToken}
}

// targetChars is how long a haystack must be to make a prompt of sizeTokens.
func (m tokenModel) targetChars(sizeTokens int) int {
	r := m.CharsPerToken
	if r <= 0 {
		r = DefaultCharsPerToken
	}
	return int(float64(sizeTokens-m.Overhead) * r)
}

// calibrate measures this provider rather than assuming it.
//
// Two probe-shaped requests at different sizes, solved for slope and
// intercept. Two, because one request cannot separate the per-character cost
// from the fixed cost of the template and the system prompt: a single
// measurement folds the fixed part into the ratio, and the ratio is then
// correct only at the size it was taken at. The requests carry the same system
// prompt and question the sweep uses, so the overhead measured is the overhead
// the sweep will pay.
//
// The first version of this test assumed four characters per token, asked for
// 32,000 and sent 36,526 — a 14% error in the very axis the answer is read off.
func calibrate(ctx context.Context, p llm.Provider) (tokenModel, error) {
	sample := func(sizeTokens int) (chars, tokens int, err error) {
		body := buildHaystack(sizeTokens, 0.5, "calibration", defaultTokenModel())
		temp := 0.0
		resp, err := p.Chat(ctx, llm.ChatRequest{
			Messages:  needleMessages(body),
			MaxTokens: 1, Temperature: &temp, Thinking: "off",
		})
		if err != nil {
			return 0, 0, err
		}
		return len(body), resp.PromptTokens, nil
	}

	loChars, loTokens, err := sample(1000)
	if err != nil {
		return defaultTokenModel(), err
	}
	hiChars, hiTokens, err := sample(4000)
	if err != nil {
		return defaultTokenModel(), err
	}
	if loTokens <= 0 || hiTokens <= loTokens {
		// No usable counts to solve against. Say so by returning the estimate:
		// a model nobody measured must not be presented as one that was.
		return defaultTokenModel(), nil
	}

	ratio := float64(hiChars-loChars) / float64(hiTokens-loTokens)
	overhead := hiTokens - int(float64(hiChars)/ratio)
	if ratio <= 0 {
		return defaultTokenModel(), nil
	}
	if overhead < 0 {
		overhead = 0
	}
	return tokenModel{CharsPerToken: ratio, Overhead: overhead, Measured: true}, nil
}

// isOverContext reports whether a provider refused because the request was
// larger than the window. That is not a recall failure — the model was never
// asked — and counting it as one would report a false ceiling.
func isOverContext(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "exceeds the available context") ||
		strings.Contains(msg, "exceed_context_size") ||
		strings.Contains(msg, "context length") ||
		strings.Contains(msg, "too many tokens")
}

// Needle measures where retrieval starts failing.
func Needle(ctx context.Context, p llm.Provider, opts NeedleOptions) (NeedleResult, error) {
	if len(opts.Sizes) == 0 {
		opts.Sizes = []int{2000, 4000, 8000, 12000, 16000, 24000}
	}
	if len(opts.Placements) == 0 {
		opts.Placements = DefaultPlacements()
	}
	sort.Ints(opts.Sizes)
	progress := opts.Progress
	if progress == nil {
		progress = func(string) {}
	}

	model, err := calibrate(ctx, p)
	switch {
	case err != nil:
		progress(fmt.Sprintf("could not calibrate this provider (%v); using the default estimate", err))
		model = defaultTokenModel()
	case !model.Measured:
		progress("this provider reports no token counts; sizes below are requested, not measured")
	default:
		progress(fmt.Sprintf("measured %.2f characters per token plus %d tokens of fixed "+
			"prompt overhead", model.CharsPerToken, model.Overhead))
	}

	var res NeedleResult
	if model.Measured {
		res.CharsPerToken = model.CharsPerToken
		res.PromptOverhead = model.Overhead
	}
	for _, size := range opts.Sizes {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		allFound := true
		anyRan := false

		overContext := false
		measured := 0
		for _, place := range opts.Placements {
			probe := runNeedleProbe(ctx, p, size, place, model)
			res.Probes = append(res.Probes, probe)
			if probe.OverContext {
				overContext = true
			}
			if probe.PromptTokens > 0 {
				res.TokensMeasured = true
			}
			if probe.Err == "" {
				anyRan = true
				if n := probe.tokens(); n > measured {
					measured = n
				}
				if !probe.Found {
					allFound = false
				}
			}
			progress(fmt.Sprintf("%6d asked / %6d actual, depth %3.0f%%: %s",
				size, probe.PromptTokens, float64(place)*100, verdictOf(probe)))
			if probe.Model != "" {
				res.Model = probe.Model
			}
		}
		if !anyRan {
			if overContext {
				// The window ran out before recall did. That is a fact about
				// the context size, not about retrieval, and the report must
				// not present it as a measured ceiling.
				res.StoppedAtContextLimit = true
				progress("the provider refused: the request is larger than its context window")
				break
			}
			continue
		}
		// Every verdict is in the provider's own count where there is one. The
		// requested figure is what was asked for, and the two have already
		// differed by more than a rounding error.
		res.LargestTested = measured
		if allFound {
			res.RecommendedCap = measured
			continue
		}
		// A size that loses the needle will not start finding it again when
		// the haystack grows.
		progress(fmt.Sprintf("recall failed at %d tokens; larger sizes not tried", size))
		break
	}
	return res, nil
}

func verdictOf(p NeedleProbe) string {
	switch {
	case p.Err != "":
		return "could not run: " + p.Err
	case p.Found:
		return "found"
	default:
		return "MISSED — " + firstLine(p.Answer)
	}
}

func runNeedleProbe(ctx context.Context, p llm.Provider, size int, place NeedlePlacement,
	m tokenModel) NeedleProbe {
	probe := NeedleProbe{Requested: size, Placement: place}

	// A random value, so the answer cannot be produced from anything but the
	// text. A memorable one — "the magic number is 42" — could be guessed, and
	// a guessed hit is a false pass in the direction that flatters.
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		probe.Err = err.Error()
		return probe
	}
	secret := hex.EncodeToString(raw[:])

	haystack := buildHaystack(size, place, secret, m)
	temp := 0.0
	resp, err := p.Chat(ctx, llm.ChatRequest{
		Messages:  needleMessages(haystack),
		MaxTokens: 2048, Temperature: &temp, Thinking: "off",
	})
	if err != nil {
		probe.Err = err.Error()
		probe.OverContext = isOverContext(err)
		return probe
	}
	probe.Model = resp.Model
	probe.PromptTokens = resp.PromptTokens
	probe.Answer = strings.TrimSpace(resp.Content)
	probe.Found = strings.Contains(strings.ToLower(probe.Answer), strings.ToLower(secret))
	return probe
}

const needleSystem = `You are answering a question about a document. The answer is
stated somewhere in it. Quote it exactly and say nothing else.`

// needleMessages is the one place a probe's prompt is assembled, so that
// calibration measures the overhead the sweep will actually pay rather than
// the overhead of a differently-shaped request.
func needleMessages(haystack string) []llm.Message {
	return []llm.Message{
		{Role: "system", Content: needleSystem},
		{Role: "user", Content: haystack + "\n\nWhat is the access code recorded above? " +
			"Answer with the code and nothing else."},
	}
}

// buildHaystack pads a document to roughly size tokens with the needle placed
// at the given depth.
//
// The filler is deliberately code-shaped rather than prose: this measures what a
// packet of retrieved source does, and a model's recall over English is not
// evidence about its recall over Go.
func buildHaystack(sizeTokens int, place NeedlePlacement, secret string, m tokenModel) string {
	// Fixed-width identifiers, so a unit costs the same number of tokens
	// wherever it lands. With `handler%d` the index widened as the haystack
	// grew — handler7 against handler4096 — so density drifted with size and a
	// ratio measured at one size was wrong at the next.
	const unit = "func handler%06d(ctx context.Context, req *Request) (*Response, error) {\n" +
		"\treturn service%02d.Process(ctx, req.Payload)\n}\n\n"
	needle := fmt.Sprintf("// Operations note: the access code is %s. Do not share it.\n\n", secret)

	unitLen := len(fmt.Sprintf(unit, 0, 0))
	target := m.targetChars(sizeTokens)

	// The needle is part of the packet, and the filler loop used to run until
	// it passed the target rather than stopping at it. Together those put the
	// haystack a unit and a needle over every time, which the calibration then
	// read back as fixed overhead that is not fixed at all. Units are all the
	// same width now, so the count is arithmetic: round to the nearest.
	fillerTarget := target - len(needle)
	units := (fillerTarget + unitLen/2) / unitLen
	if units < 2 {
		units = 2
	}

	var filler strings.Builder
	filler.Grow(units * unitLen)
	for i := 0; i < units; i++ {
		fmt.Fprintf(&filler, unit, i, i%50)
	}
	body := filler.String()

	at := int(float64(len(body)) * float64(place))
	if at > 0 && at < len(body) {
		// Cut on a declaration boundary, so the split never lands inside an
		// identifier and changes the tokenisation around it.
		at -= at % unitLen
	}
	return body[:at] + needle + body[at:]
}

// Format renders the sweep for a terminal.
func (r NeedleResult) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "needle test — %s\n\n", orUnknown(r.Model))

	if r.CharsPerToken > 0 {
		fmt.Fprintf(&b, "%.2f characters per token plus %d tokens of fixed prompt overhead,\n"+
			"measured against this model's own tokenizer\n\n",
			r.CharsPerToken, r.PromptOverhead)
	}
	if !r.TokensMeasured {
		b.WriteString("This provider reported no token counts, so every size below is the size\n" +
			"asked for, not the size sent. Read the cap as an estimate.\n\n")
	}

	bySize := map[int][]NeedleProbe{}
	var sizes []int
	for _, p := range r.Probes {
		// Group by what was asked for, but label with what was sent.
		if _, seen := bySize[p.Requested]; !seen {
			sizes = append(sizes, p.Requested)
		}
		bySize[p.Requested] = append(bySize[p.Requested], p)
	}
	sort.Ints(sizes)

	if r.TokensMeasured {
		fmt.Fprintf(&b, "%-10s %-10s %s\n", "asked", "measured", "recall by depth (0% … 100%)")
	} else {
		fmt.Fprintf(&b, "%-10s %-10s %s\n", "asked", "sent", "recall by depth (0% … 100%)")
	}
	for _, size := range sizes {
		measured := 0
		for _, p := range bySize[size] {
			if p.Err == "" && p.tokens() > measured {
				measured = p.tokens()
			}
		}
		label := "—"
		if measured > 0 {
			label = fmt.Sprintf("%d", measured)
		}
		fmt.Fprintf(&b, "%-10d %-10s ", size, label)
		for _, p := range bySize[size] {
			switch {
			case p.Err != "":
				b.WriteString("?  ")
			case p.Found:
				b.WriteString(".  ")
			default:
				b.WriteString("X  ")
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	switch {
	case r.RecommendedCap == 0:
		b.WriteString("Recall failed at every size tested. The packet cap cannot be set from\n" +
			"this measurement; the model is not retrieving from a packet at all.\n")
	case r.StoppedAtContextLimit:
		fmt.Fprintf(&b, "Every depth was recalled up to %d %s, and the sweep then\n"+
			"ran out of context window rather than out of recall.\n\n"+
			"That is a fact about the window, not a retrieval ceiling: this model was\n"+
			"never asked anything larger. The packet cap here is bounded by\n"+
			"context_tokens minus reserved output, not by what the model can retrieve\n"+
			"from. Raising the served context is what would move it.\n",
			r.LargestTested, r.tokenUnit())
	case r.RecommendedCap == r.LargestTested:
		fmt.Fprintf(&b, "No ceiling found up to %d %s. That is not a measured\n"+
			"limit: try larger sizes before treating it as one.\n",
			r.LargestTested, r.tokenUnit())
	default:
		fmt.Fprintf(&b, "Recommended max_packet_tokens: %d\n\n", r.RecommendedCap)
		b.WriteString("This is the largest size where every depth was recalled, not where the\n" +
			"average was good. A packet builder cannot choose where the needed slice\n" +
			"lands, so a size that works at the edges and fails in the middle fails.\n")
	}
	return b.String()
}

// tokenUnit names the axis so a sentence cannot claim a measurement that was
// never taken.
func (r NeedleResult) tokenUnit() string {
	if r.TokensMeasured {
		return "measured tokens"
	}
	return "requested tokens"
}

func orUnknown(s string) string {
	if s == "" {
		return "(provider reported no model name)"
	}
	return s
}
