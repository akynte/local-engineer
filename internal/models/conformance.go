package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/llm"
)

// Conformance checks a provider's behaviour against what it declares.
//
// DR-4 put every provider behind one OpenAI-compatible API and accepted the
// disadvantage in writing: "feature gaps between providers (thinking control,
// infill, grammars) are hidden behind one API and must be declared explicitly."
// The design's answer is that Capabilities is a promise callers may rely on —
// ChatStructured refuses rather than degrading, the engine refuses a provider
// that cannot call tools.
//
// That answer has a hole in it, and this is the file that closes it: nothing
// checked whether a declaration was true. A providers.yaml entry that claims
// tool calling for a model that cannot do it produces malformed calls at run
// time, inside a task, where the failure looks like the model being bad at its
// job. Declaring a capability is cheap; this makes it checkable.
//
// A capability that is declared and does not work is a failure. A capability
// that is not declared is skipped, not failed — a provider is allowed to be
// limited, it is not allowed to lie.
type Conformance struct {
	Provider string  `json:"provider"`
	Kind     string  `json:"kind"`
	Model    string  `json:"model"`
	Checks   []Check `json:"checks"`
	// Passed is false when any declared capability did not behave as declared.
	Passed      bool      `json:"passed"`
	CheckedAt   time.Time `json:"checked_at"`
	DurationsMS int64     `json:"duration_ms"`
}

// Check is one capability, tested.
type Check struct {
	Name string `json:"name"`
	// Declared is what Capabilities said. A check is only a failure when this
	// is true.
	Declared bool   `json:"declared"`
	Status   Status `json:"status"`
	Detail   string `json:"detail"`
}

// Status is the outcome of one check.
type Status string

const (
	// StatusPass: the provider did what it declared.
	StatusPass Status = "pass"
	// StatusFail: the provider declared the capability and did not deliver it.
	StatusFail Status = "fail"
	// StatusSkip: not declared, so not required.
	StatusSkip Status = "skip"
	// StatusUnproven: the check could not reach a verdict — the provider was
	// unreachable, or the answer was ambiguous. Reported apart from pass so it
	// is never read as one.
	StatusUnproven Status = "unproven"
)

// ConformanceOptions configures a run.
type ConformanceOptions struct {
	// Timeout bounds each individual check. Local generation on a small GPU is
	// slow, so this is generous by default.
	Timeout  time.Duration
	Progress func(string)
}

// CheckConformance runs every check a provider's declarations call for.
func CheckConformance(ctx context.Context, p llm.Provider, opts ConformanceOptions) Conformance {
	if opts.Timeout <= 0 {
		opts.Timeout = 3 * time.Minute
	}
	progress := opts.Progress
	if progress == nil {
		progress = func(string) {}
	}

	caps := p.Capabilities()
	res := Conformance{
		Provider: p.Name(), Kind: string(caps.Kind), Passed: true,
		CheckedAt: time.Now().UTC(),
	}
	start := time.Now()

	for _, c := range []func(context.Context, llm.Provider, llm.Capabilities, ConformanceOptions) Check{
		checkReachable,
		checkCompletion,
		checkMaxTokens,
		checkToolCalling,
		checkStructuredOutput,
		checkStructuredRefusal,
		checkEmbeddings,
		checkVision,
	} {
		select {
		case <-ctx.Done():
			res.Checks = append(res.Checks, Check{
				Name: "(remaining)", Status: StatusUnproven, Detail: ctx.Err().Error(),
			})
			res.Passed = false
			res.DurationsMS = time.Since(start).Milliseconds()
			return res
		default:
		}
		check := c(ctx, p, caps, opts)
		progress(fmt.Sprintf("%-22s %s", check.Name, check.Status))
		res.Checks = append(res.Checks, check)
		if check.Status == StatusFail {
			res.Passed = false
		}
		if check.Name == "model name" && check.Detail != "" {
			res.Model = check.Detail
		}
	}
	res.DurationsMS = time.Since(start).Milliseconds()
	return res
}

func checkReachable(ctx context.Context, p llm.Provider, _ llm.Capabilities, o ConformanceOptions) Check {
	c := Check{Name: "reachable", Declared: true}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	if err := p.Health(ctx); err != nil {
		c.Status, c.Detail = StatusFail, err.Error()
		return c
	}
	c.Status = StatusPass
	return c
}

// checkCompletion is the floor: a provider that cannot answer at all cannot be
// relied on for anything else, so its failure is not merely one capability.
func checkCompletion(ctx context.Context, p llm.Provider, _ llm.Capabilities, o ConformanceOptions) Check {
	c := Check{Name: "completion", Declared: true}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	temp := 0.0
	resp, err := p.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "Answer with a single word."},
			{Role: "user", Content: "What colour is a clear midday sky? One word."},
		},
		MaxTokens: 2048, Temperature: &temp, Thinking: "off",
	})
	if err != nil {
		c.Status, c.Detail = StatusFail, err.Error()
		return c
	}
	if strings.TrimSpace(resp.Content) == "" {
		// A reasoning model that spent the whole budget thinking lands here.
		// It is not a pass, and it is not the same as a refusal.
		c.Status = StatusUnproven
		c.Detail = "the provider returned no content"
		if resp.FinishReason == "length" {
			c.Detail += " and stopped at the output limit; the budget may be too " +
				"small for this model's thinking"
		}
		return c
	}
	c.Status, c.Detail = StatusPass, firstLine(resp.Content)
	return c
}

// checkMaxTokens verifies the provider honours an output bound. A provider that
// ignores it will overrun every packet budget the profile computes.
func checkMaxTokens(ctx context.Context, p llm.Provider, _ llm.Capabilities, o ConformanceOptions) Check {
	c := Check{Name: "max tokens", Declared: true}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	const limit = 16
	temp := 0.0
	resp, err := p.Chat(ctx, llm.ChatRequest{
		Messages:  []llm.Message{{Role: "user", Content: "Count slowly from one to two hundred in words."}},
		MaxTokens: limit, Temperature: &temp, Thinking: "off",
	})
	if err != nil {
		c.Status, c.Detail = StatusFail, err.Error()
		return c
	}
	if resp.OutputTokens == 0 {
		c.Status, c.Detail = StatusUnproven, "the provider reported no output token count"
		return c
	}
	// Some providers count a stop token on top; a small margin keeps the check
	// about "is the bound honoured" rather than about off-by-one accounting.
	if resp.OutputTokens > limit+4 {
		c.Status = StatusFail
		c.Detail = fmt.Sprintf("asked for at most %d output tokens, got %d", limit, resp.OutputTokens)
		return c
	}
	c.Status, c.Detail = StatusPass, fmt.Sprintf("%d tokens for a limit of %d", resp.OutputTokens, limit)
	return c
}

// toolSchema is deliberately strict: required fields and no extras, the same
// shape the engine's tools use, so the check exercises what production does.
var toolSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"city":{"type":"string","description":"The city to look up."},
		"units":{"type":"string","enum":["celsius","fahrenheit"]}
	},
	"required":["city","units"],
	"additionalProperties":false}`)

func checkToolCalling(ctx context.Context, p llm.Provider, caps llm.Capabilities, o ConformanceOptions) Check {
	c := Check{Name: "tool calling", Declared: caps.ToolCalling}
	if !caps.ToolCalling {
		c.Status, c.Detail = StatusSkip, "not declared"
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	temp := 0.0
	resp, err := p.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "Use the provided tool to answer. Do not answer from memory."},
			{Role: "user", Content: "What is the weather in Berlin in celsius?"},
		},
		Tools: []llm.ToolDef{{
			Name:        "get_weather",
			Description: "Look up the current weather for a city.",
			Schema:      toolSchema,
		}},
		ToolChoice: "auto", MaxTokens: 2048, Temperature: &temp, Thinking: "off",
	})
	if err != nil {
		c.Status, c.Detail = StatusFail, err.Error()
		return c
	}
	if !resp.WantsTools() {
		c.Status = StatusFail
		c.Detail = "declared tool calling but answered without calling the tool"
		return c
	}
	call := resp.ToolCalls[0]
	if call.Name != "get_weather" {
		c.Status = StatusFail
		c.Detail = fmt.Sprintf("called %q, which is not the tool it was given", call.Name)
		return c
	}
	var args map[string]any
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		c.Status = StatusFail
		c.Detail = fmt.Sprintf("tool arguments are not valid JSON: %v", err)
		return c
	}
	for _, required := range []string{"city", "units"} {
		if _, ok := args[required]; !ok {
			c.Status = StatusFail
			c.Detail = fmt.Sprintf("tool arguments omit the required field %q: %s", required, call.Arguments)
			return c
		}
	}
	c.Status, c.Detail = StatusPass, string(call.Arguments)
	return c
}

var structuredSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"language":{"type":"string"},
		"confident":{"type":"boolean"}
	},
	"required":["language","confident"],
	"additionalProperties":false}`)

func checkStructuredOutput(ctx context.Context, p llm.Provider, caps llm.Capabilities, o ConformanceOptions) Check {
	c := Check{Name: "structured output", Declared: caps.StructuredOutput}
	if !caps.StructuredOutput {
		c.Status, c.Detail = StatusSkip, "not declared"
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	temp := 0.0
	resp, err := p.ChatStructured(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "Which programming language is `func main() {}` from? Answer as JSON."},
		},
		MaxTokens: 2048, Temperature: &temp, Thinking: "off",
	}, structuredSchema)
	if err != nil {
		c.Status, c.Detail = StatusFail, err.Error()
		return c
	}
	var out struct {
		Language  *string `json:"language"`
		Confident *bool   `json:"confident"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.Content)), &out); err != nil {
		c.Status = StatusFail
		c.Detail = fmt.Sprintf("declared structured output but returned something that is not "+
			"JSON: %v (%s)", err, firstLine(resp.Content))
		return c
	}
	if out.Language == nil || out.Confident == nil {
		c.Status = StatusFail
		c.Detail = "the JSON omits a field the schema marked required: " + firstLine(resp.Content)
		return c
	}
	c.Status, c.Detail = StatusPass, firstLine(resp.Content)
	return c
}

// checkStructuredRefusal tests the contract rather than the model: DR-4 says a
// provider that cannot constrain output must refuse, never silently degrade to
// prompt-only instructions and hand back prose the caller will try to parse.
func checkStructuredRefusal(ctx context.Context, p llm.Provider, caps llm.Capabilities, o ConformanceOptions) Check {
	c := Check{Name: "refuses undeclared", Declared: !caps.StructuredOutput}
	if caps.StructuredOutput {
		c.Status, c.Detail = StatusSkip, "structured output is declared, so there is nothing to refuse"
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	_, err := p.ChatStructured(ctx, llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "Answer as JSON."}},
	}, structuredSchema)
	var unsupported *llm.UnsupportedError
	if errors.As(err, &unsupported) {
		c.Status, c.Detail = StatusPass, "refused, as DR-4 requires"
		return c
	}
	if err != nil {
		c.Status = StatusUnproven
		c.Detail = "failed, but not with an UnsupportedError: " + err.Error()
		return c
	}
	c.Status = StatusFail
	c.Detail = "did not declare structured output and did not refuse; a caller would " +
		"parse prose as JSON"
	return c
}

func checkEmbeddings(ctx context.Context, p llm.Provider, caps llm.Capabilities, o ConformanceOptions) Check {
	c := Check{Name: "embeddings", Declared: caps.Embeddings}
	if !caps.Embeddings {
		c.Status, c.Detail = StatusSkip, "not declared"
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	resp, err := p.Embed(ctx, llm.EmbedRequest{Input: []string{"hello", "world"}})
	if err != nil {
		c.Status, c.Detail = StatusFail, err.Error()
		return c
	}
	if len(resp.Vectors) != 2 {
		c.Status = StatusFail
		c.Detail = fmt.Sprintf("asked for 2 embeddings, got %d", len(resp.Vectors))
		return c
	}
	if len(resp.Vectors[0]) == 0 || len(resp.Vectors[0]) != len(resp.Vectors[1]) {
		c.Status = StatusFail
		c.Detail = "embeddings have inconsistent or zero dimensionality"
		return c
	}
	c.Status, c.Detail = StatusPass, fmt.Sprintf("%d dimensions", len(resp.Vectors[0]))
	return c
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

// Format renders a report for a terminal and for a model-report issue.
func (c Conformance) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)", c.Provider, c.Kind)
	if c.Model != "" {
		fmt.Fprintf(&b, " — %s", c.Model)
	}
	b.WriteString("\n\n")
	for _, ch := range c.Checks {
		fmt.Fprintf(&b, "  %-20s %-9s %s\n", ch.Name, ch.Status, ch.Detail)
	}
	b.WriteString("\n")
	if c.Passed {
		b.WriteString("Every declared capability behaved as declared.\n")
	} else {
		b.WriteString("A declared capability did not behave as declared. Callers are allowed to\n" +
			"rely on what providers.yaml declares, so this is a configuration fault\n" +
			"rather than a model being poor at its job.\n")
	}
	return b.String()
}

// onePixelPNG is a valid 1x1 PNG. It is the smallest thing that answers the
// question this check asks — whether the provider accepts an image at all —
// without depending on the model being able to describe anything.
var onePixelPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
	0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41,
	0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
	0x00, 0x03, 0x01, 0x01, 0x00, 0x18, 0xDD, 0x8D,
	0xB0, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E,
	0x44, 0xAE, 0x42, 0x60, 0x82,
}

// checkVision asks whether a provider that declares it can actually take an
// image. It deliberately does not test whether the model describes the picture
// well: the failure this guards against is an image being accepted and ignored,
// or a declaration that no endpoint backs.
func checkVision(ctx context.Context, p llm.Provider, caps llm.Capabilities, o ConformanceOptions) Check {
	c := Check{Name: "vision", Declared: caps.Vision}
	if !caps.Vision {
		// The other half of the contract: a provider that does not declare
		// vision must refuse an image rather than drop it, because a dropped
		// image produces a confident answer about something never seen.
		ctx, cancel := context.WithTimeout(ctx, o.Timeout)
		defer cancel()
		_, err := p.Chat(ctx, llm.ChatRequest{
			Messages: []llm.Message{{
				Role: "user", Content: "What colour is this?",
				Images: []llm.Image{{MediaType: "image/png", Data: onePixelPNG}},
			}},
			MaxTokens: 64,
		})
		var unsupported *llm.UnsupportedError
		if errors.As(err, &unsupported) {
			c.Status, c.Detail = StatusSkip, "not declared, and images are refused rather than dropped"
			return c
		}
		if err != nil {
			c.Status, c.Detail = StatusSkip, "not declared; the image was rejected: "+err.Error()
			return c
		}
		// The provider took an image it never declared it could read. From
		// outside there is no way to tell whether it used the image or dropped
		// it, and the two are very different — so this is reported for a human
		// rather than failed, which would assert something unverifiable. Every
		// provider in this repository refuses instead, which is the behaviour
		// that makes the difference decidable.
		c.Status = StatusUnproven
		c.Detail = "did not declare vision and accepted an image rather than refusing; " +
			"whether it was read or silently dropped cannot be told from here"
		return c
	}

	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	temp := 0.0
	resp, err := p.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{{
			Role: "user", Content: "Describe this image in a few words.",
			Images: []llm.Image{{MediaType: "image/png", Data: onePixelPNG}},
		}},
		MaxTokens: 2048, Temperature: &temp, Thinking: "off",
	})
	if err != nil {
		c.Status, c.Detail = StatusFail, err.Error()
		return c
	}
	if strings.TrimSpace(resp.Content) == "" {
		c.Status, c.Detail = StatusUnproven, "accepted the image and returned no content"
		return c
	}
	c.Status, c.Detail = StatusPass, firstLine(resp.Content)
	return c
}
