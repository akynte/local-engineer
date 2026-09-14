package config

import (
	"fmt"
	"path/filepath"
	"strconv"
)

// LlamaArgs builds llama-server's command line from the active profile
// (design v3 §4.3: started "with the active profile"; §9.3: thread counts,
// offload layers, batch and cache settings "must not be hardcoded" and "live in
// the profile").
//
// Until this existed the profile's whole runtime block was dead configuration.
// Nine settings, present in every shipped profile, validated by a schema and
// written by `le models bench` — and the embedded server was started from
// inference.args alone, so selecting a 24 GB profile over a CPU-only one
// changed packet sizes and admission limits while starting an identical server.
//
// Operator args are appended last rather than replaced. llama-server takes the
// last occurrence of a repeated flag, so an operator can override anything the
// profile set without having to restate the rest — and can pass flags the
// profile has no field for.
func LlamaArgs(cfg Config, p *Profile, modelsDir string) ([]string, error) {
	if cfg.Inference.Model == "" && len(cfg.Inference.Args) == 0 {
		return nil, fmt.Errorf("config: inference.mode is embedded but no model is set; " +
			"put a GGUF under /data/models and name it in inference.model")
	}

	var args []string
	add := func(flag string, values ...string) {
		args = append(args, flag)
		args = append(args, values...)
	}

	if cfg.Inference.Model != "" {
		model := cfg.Inference.Model
		if !filepath.IsAbs(model) {
			model = filepath.Join(modelsDir, model)
		}
		add("--model", model)
	}
	add("--host", "127.0.0.1")
	add("--port", strconv.Itoa(cfg.Inference.Port))

	if p == nil {
		// No profile: the server still has to start, but say nothing about
		// tuning rather than inventing values. §9.3's rule is that these live
		// in the profile, and guessing here would put them back in the code.
		args = append(args, cfg.Inference.Args...)
		return args, nil
	}

	// Context is per slot in llama.cpp, and the profile's context_tokens is
	// what one task gets. Multiplying by the slot count is what makes both
	// numbers mean what they say.
	slots := p.Runtime.Slots
	if slots <= 0 {
		slots = 1
	}
	if p.ContextTokens > 0 {
		add("--ctx-size", strconv.Itoa(p.ContextTokens*slots))
	}
	if slots > 1 {
		add("--parallel", strconv.Itoa(slots))
	}

	r := p.Runtime
	if r.Threads > 0 {
		add("--threads", strconv.Itoa(r.Threads))
	}
	if r.ThreadsBatch > 0 {
		add("--threads-batch", strconv.Itoa(r.ThreadsBatch))
	}
	if r.GPULayers > 0 {
		add("--n-gpu-layers", strconv.Itoa(r.GPULayers))
	}
	if r.BatchSize > 0 {
		add("--batch-size", strconv.Itoa(r.BatchSize))
	}
	if r.UBatchSize > 0 {
		add("--ubatch-size", strconv.Itoa(r.UBatchSize))
	}
	if r.FlashAttn {
		add("--flash-attn", "on")
	}
	// The KV cache types are only meaningful together: quantising one and not
	// the other is a configuration nobody wants and llama.cpp will not refuse.
	if r.CacheTypeK != "" {
		add("--cache-type-k", r.CacheTypeK)
	}
	if r.CacheTypeV != "" {
		add("--cache-type-v", r.CacheTypeV)
	}
	args = append(args, r.ExtraArgs...)

	// Last, so they win.
	args = append(args, cfg.Inference.Args...)
	return args, nil
}
