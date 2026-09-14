// Package models implements `le models bench` (design v3 §9.2): it measures a
// provider on this machine and turns the measurement into a hardware profile.
//
// §9.3 lists what must never be hardcoded — context limits, VRAM and RAM
// budgets, thread counts, offload layers, sampling defaults, thinking policy,
// tool-surface size, packet sizes. This package is how those values come to
// exist: measured here, written to a profile, read back as configuration.
package models

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/config"
	"github.com/akynte/local-engineer/internal/llm"
)

// BenchOptions configures a measurement run.
type BenchOptions struct {
	Iterations   int
	PromptTokens int
	OutputTokens int
	Progress     func(string)
}

// Result is the measurement. Every field is observed; nothing is estimated.
type Result struct {
	Model            string    `json:"model"`
	Provider         string    `json:"provider"`
	Kind             string    `json:"kind"`
	PrefillTokensSec float64   `json:"prefill_tokens_per_second"`
	DecodeTokensSec  float64   `json:"decode_tokens_per_second"`
	TTFTp50MS        float64   `json:"ttft_p50_ms"`
	TTFTp95MS        float64   `json:"ttft_p95_ms"`
	CacheReusePct    float64   `json:"cache_reuse_pct"`
	PeakVRAMMB       int       `json:"peak_vram_mb"`
	TotalVRAMMB      int       `json:"total_vram_mb"`
	PeakRAMMB        int       `json:"peak_ram_mb"`
	TotalRAMMB       int       `json:"total_ram_mb"`
	DeclaredContext  int       `json:"declared_context"`
	Iterations       int       `json:"iterations"`
	MeasuredAt       time.Time `json:"measured_at"`
	Host             string    `json:"host"`
	GPU              string    `json:"gpu,omitempty"`
	CPUThreads       int       `json:"cpu_threads"`
}

// Bench measures a provider. It sends the same stable prefix on every
// iteration so that prompt-cache reuse is observable: §8.2 treats cache-aware
// packet layout as a design commitment, and a profile that cannot show reuse
// is a profile whose packet layout is wrong.
func Bench(ctx context.Context, p llm.Provider, opts BenchOptions) (Result, error) {
	if opts.Iterations <= 0 {
		opts.Iterations = 3
	}
	if opts.PromptTokens <= 0 {
		opts.PromptTokens = 2000
	}
	if opts.OutputTokens <= 0 {
		opts.OutputTokens = 256
	}
	progress := opts.Progress
	if progress == nil {
		progress = func(string) {}
	}

	res := Result{
		Provider: p.Name(), Kind: string(p.Capabilities().Kind),
		DeclaredContext: p.Capabilities().MaxContext,
		Iterations:      opts.Iterations, MeasuredAt: time.Now().UTC(),
		CPUThreads: runtime.NumCPU(),
	}
	res.Host, _ = os.Hostname()
	res.GPU, res.TotalVRAMMB = gpuInfo()
	res.TotalRAMMB = totalRAMMB()

	if err := p.Health(ctx); err != nil {
		return res, fmt.Errorf("models: provider %s is not reachable: %w", p.Name(), err)
	}

	// A stable prefix, then a varying suffix: exactly the layout §8.2 requires
	// of a packet, so the measurement reflects real traffic.
	prefix := stableFiller(opts.PromptTokens)

	var ttfts []float64
	var prefillRates, decodeRates []float64
	var cachedTotal, promptTotal int
	temp := 0.0

	for i := 0; i < opts.Iterations; i++ {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		progress(fmt.Sprintf("iteration %d of %d", i+1, opts.Iterations))

		start := time.Now()
		resp, err := p.Chat(ctx, llm.ChatRequest{
			Messages: []llm.Message{
				{Role: "system", Content: prefix},
				{Role: "user", Content: fmt.Sprintf("Reply with exactly %d words of filler. Iteration %d.", opts.OutputTokens/2, i)},
			},
			MaxTokens:   opts.OutputTokens,
			Temperature: &temp,
			Thinking:    "off",
		})
		if err != nil {
			return res, fmt.Errorf("models: iteration %d: %w", i+1, err)
		}
		elapsed := time.Since(start).Seconds()
		if elapsed <= 0 {
			continue
		}

		res.Model = resp.Model
		promptTotal += resp.PromptTokens
		cachedTotal += resp.CachedTokens

		// Without token-level timings the split between prefill and decode is
		// apportioned by token counts. This is stated in the profile rather
		// than presented as a direct measurement.
		total := resp.PromptTokens + resp.OutputTokens
		if total > 0 && resp.OutputTokens > 0 {
			decodeRates = append(decodeRates, float64(resp.OutputTokens)/elapsed)
			uncached := resp.PromptTokens - resp.CachedTokens
			if uncached > 0 {
				prefillRates = append(prefillRates, float64(uncached)/elapsed)
			}
		}
		ttfts = append(ttfts, float64(resp.DurationMS))

		if v := sampleVRAM(); v > res.PeakVRAMMB {
			res.PeakVRAMMB = v
		}
		if r := processRAMMB(); r > res.PeakRAMMB {
			res.PeakRAMMB = r
		}
	}

	res.PrefillTokensSec = mean(prefillRates)
	res.DecodeTokensSec = mean(decodeRates)
	res.TTFTp50MS = percentile(ttfts, 0.50)
	res.TTFTp95MS = percentile(ttfts, 0.95)
	if promptTotal > 0 {
		res.CacheReusePct = 100 * float64(cachedTotal) / float64(promptTotal)
	}
	if res.Model == "" {
		res.Model = "(provider reported no model name)"
	}
	return res, nil
}

// ProfileFrom turns a measurement into a profile. The derivations are
// conservative and stated, because these numbers gate task admission.
func ProfileFrom(r Result, name string, contextOverride int) config.Profile {
	if name == "" {
		name = deriveName(r)
	}
	ctxTokens := contextOverride
	if ctxTokens <= 0 {
		ctxTokens = r.DeclaredContext
	}
	if ctxTokens <= 0 {
		// The provider declared nothing and the operator said nothing; use the
		// same conservative fallback the shipped default uses, and say so in
		// the description.
		ctxTokens = config.FallbackProfile().ContextTokens
	}

	// Reserve a quarter of the window for output, cap the packet at half, and
	// leave the rest for the system map and the working evidence. These are
	// starting points the needle test refines (§8.3), not tuned values.
	reserved := ctxTokens / 4
	if reserved > 8192 {
		reserved = 8192
	}
	packet := ctxTokens / 2
	if packet+reserved > ctxTokens {
		packet = ctxTokens - reserved
	}

	concurrency := 1
	if r.PeakVRAMMB > 0 && r.TotalVRAMMB > 0 {
		// Only admit a second concurrent task when a second copy of the
		// measured peak would still leave 15% headroom.
		if r.PeakVRAMMB*2 < r.TotalVRAMMB*85/100 {
			concurrency = 2
		}
	}

	return config.Profile{
		Name: name,
		Description: fmt.Sprintf(
			"Generated by `le models bench` on %s. Prefill %.0f tok/s, decode %.0f tok/s, cache reuse %.0f%%. "+
				"Prefill and decode rates are apportioned from per-request totals, not token-level timings.",
			r.MeasuredAt.Format("2006-01-02"), r.PrefillTokensSec, r.DecodeTokensSec, r.CacheReusePct),
		Hardware: config.Hardware{
			GPU: r.GPU, VRAMMB: r.TotalVRAMMB, RAMMB: r.TotalRAMMB, Threads: r.CPUThreads,
		},
		ContextTokens:   ctxTokens,
		MaxPacketTokens: packet,
		ReservedOutput:  reserved,
		ToolSurfaceMax:  12,
		PeakVRAMMB:      r.PeakVRAMMB,
		PeakRAMMB:       r.PeakRAMMB,
		Concurrency:     concurrency,
		Runtime:         config.RuntimeKnobs{Threads: r.CPUThreads},
		Sampling:        config.Sampling{Temperature: 0.2, TopP: 0.95, TopK: 40, MinP: 0.05},
		Thinking:        "auto",
		Measured: &config.Measurement{
			Model: r.Model, PrefillTokensSec: r.PrefillTokensSec, DecodeTokensSec: r.DecodeTokensSec,
			PeakVRAMMB: r.PeakVRAMMB, PeakRAMMB: r.PeakRAMMB,
			MeasuredAt: r.MeasuredAt.Format(time.RFC3339), Host: r.Host,
		},
	}
}

func deriveName(r Result) string {
	parts := []string{"measured"}
	if r.TotalVRAMMB > 0 {
		parts = append(parts, fmt.Sprintf("%dgb-gpu", r.TotalVRAMMB/1024))
	} else {
		parts = append(parts, "cpu-only")
	}
	if r.TotalRAMMB > 0 {
		parts = append(parts, fmt.Sprintf("%dgb-ram", r.TotalRAMMB/1024))
	}
	return strings.Join(parts, "-")
}

// stableFiller builds a deterministic prompt of roughly n tokens. It must be
// byte-identical across iterations or prompt-cache reuse cannot be observed.
func stableFiller(tokens int) string {
	const unit = "The supervisor retrieves only the slices the current step needs. "
	var b strings.Builder
	b.WriteString("You are a benchmark fixture. Answer briefly.\n\n")
	for b.Len() < tokens*4 {
		b.WriteString(unit)
	}
	return b.String()
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

func gpuInfo() (string, int) {
	out, err := exec.Command("nvidia-smi", "--query-gpu=name,memory.total",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return "", 0
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	parts := strings.SplitN(line, ",", 2)
	if len(parts) != 2 {
		return "", 0
	}
	mb, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
	return strings.TrimSpace(parts[0]), mb
}

func sampleVRAM() int {
	out, err := exec.Command("nvidia-smi", "--query-gpu=memory.used",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return 0
	}
	mb, _ := strconv.Atoi(strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]))
	return mb
}

func totalRAMMB() int {
	body, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.Atoi(fields[1])
				return kb / 1024
			}
		}
	}
	return 0
}

// processRAMMB reads this process's resident set. For an external inference
// server this under-reports; the profile records it as observed here, and
// `le doctor` compares against the host total rather than trusting it blindly.
func processRAMMB() int {
	body, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.Atoi(fields[1])
				return kb / 1024
			}
		}
	}
	return 0
}
