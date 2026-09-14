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
}

// NeedleResult is the whole sweep.
type NeedleResult struct {
	Model  string        `json:"model"`
	Probes []NeedleProbe `json:"probes"`
	// RecommendedCap is the largest size at which every placement was found.
	// Zero means even the smallest size tested failed, which is a finding about
	// the model rather than a missing measurement.
	RecommendedCap int `json:"recommended_cap"`
	// LargestTested is the biggest packet actually attempted, so a cap equal to
	// it reads as "no ceiling found below this" rather than as a measured limit.
	LargestTested int `json:"largest_tested"`
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

	var res NeedleResult
	for _, size := range opts.Sizes {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		allFound := true
		anyRan := false

		for _, place := range opts.Placements {
			probe := runNeedleProbe(ctx, p, size, place)
			res.Probes = append(res.Probes, probe)
			if probe.Err == "" {
				anyRan = true
				if !probe.Found {
					allFound = false
				}
			}
			progress(fmt.Sprintf("%6d tokens, depth %3.0f%%: %s",
				size, float64(place)*100, verdictOf(probe)))
			if probe.Model != "" {
				res.Model = probe.Model
			}
		}
		if !anyRan {
			continue
		}
		res.LargestTested = size
		if allFound {
			res.RecommendedCap = size
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

func runNeedleProbe(ctx context.Context, p llm.Provider, size int, place NeedlePlacement) NeedleProbe {
	probe := NeedleProbe{PromptTokens: size, Placement: place}

	// A random value, so the answer cannot be produced from anything but the
	// text. A memorable one — "the magic number is 42" — could be guessed, and
	// a guessed hit is a false pass in the direction that flatters.
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		probe.Err = err.Error()
		return probe
	}
	secret := hex.EncodeToString(raw[:])

	haystack := buildHaystack(size, place, secret)
	temp := 0.0
	resp, err := p.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: needleSystem},
			{Role: "user", Content: haystack + "\n\nWhat is the access code recorded above? " +
				"Answer with the code and nothing else."},
		},
		MaxTokens: 2048, Temperature: &temp, Thinking: "off",
	})
	if err != nil {
		probe.Err = err.Error()
		return probe
	}
	probe.Model = resp.Model
	probe.Answer = strings.TrimSpace(resp.Content)
	probe.Found = strings.Contains(strings.ToLower(probe.Answer), strings.ToLower(secret))
	return probe
}

const needleSystem = `You are answering a question about a document. The answer is
stated somewhere in it. Quote it exactly and say nothing else.`

// buildHaystack pads a document to roughly size tokens with the needle placed
// at the given depth.
//
// The filler is deliberately code-shaped rather than prose: this measures what a
// packet of retrieved source does, and a model's recall over English is not
// evidence about its recall over Go.
func buildHaystack(sizeTokens int, place NeedlePlacement, secret string) string {
	const unit = "func handler%d(ctx context.Context, req *Request) (*Response, error) {\n" +
		"\treturn service%d.Process(ctx, req.Payload)\n}\n\n"
	needle := fmt.Sprintf("// Operations note: the access code is %s. Do not share it.\n\n", secret)

	// Roughly four characters per token for code.
	target := sizeTokens * 4
	if target < len(needle)*4 {
		target = len(needle) * 4
	}
	var before, after strings.Builder
	beforeTarget := int(float64(target) * float64(place))

	for i := 0; before.Len() < beforeTarget; i++ {
		fmt.Fprintf(&before, unit, i, i%50)
	}
	for i := 0; before.Len()+after.Len() < target; i++ {
		fmt.Fprintf(&after, unit, 100000+i, i%50)
	}
	return before.String() + needle + after.String()
}

// Format renders the sweep for a terminal.
func (r NeedleResult) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "needle test — %s\n\n", orUnknown(r.Model))

	bySize := map[int][]NeedleProbe{}
	var sizes []int
	for _, p := range r.Probes {
		if _, seen := bySize[p.PromptTokens]; !seen {
			sizes = append(sizes, p.PromptTokens)
		}
		bySize[p.PromptTokens] = append(bySize[p.PromptTokens], p)
	}
	sort.Ints(sizes)

	fmt.Fprintf(&b, "%-10s %s\n", "tokens", "recall by depth (0% … 100%)")
	for _, size := range sizes {
		fmt.Fprintf(&b, "%-10d ", size)
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
	case r.RecommendedCap == r.LargestTested:
		fmt.Fprintf(&b, "No ceiling found up to %d tokens. That is not a measured limit:\n"+
			"try larger sizes before treating it as one.\n", r.LargestTested)
	default:
		fmt.Fprintf(&b, "Recommended max_packet_tokens: %d\n\n", r.RecommendedCap)
		b.WriteString("This is the largest size where every depth was recalled, not where the\n" +
			"average was good. A packet builder cannot choose where the needed slice\n" +
			"lands, so a size that works at the edges and fails in the middle fails.\n")
	}
	return b.String()
}

func orUnknown(s string) string {
	if s == "" {
		return "(provider reported no model name)"
	}
	return s
}
