package config_test

import (
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/config"
)

func argsFor(t *testing.T, cfg config.Config, p *config.Profile) []string {
	t.Helper()
	args, err := config.LlamaArgs(cfg, p, "/data/models")
	if err != nil {
		t.Fatal(err)
	}
	return args
}

func has(args []string, flag, value string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == value {
			return true
		}
	}
	return false
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func embedded() config.Config {
	c := config.Default()
	c.Inference.Mode = config.ModeEmbedded
	c.Inference.Model = "model.gguf"
	c.Inference.Port = 8080
	return c
}

// The gap this closes: every runtime setting in every shipped profile was dead
// configuration. Selecting a 24 GB profile over a CPU-only one changed packet
// sizes and started an identical server.
func TestProfileRuntimeReachesTheCommandLine(t *testing.T) {
	p := &config.Profile{
		Name: "test", ContextTokens: 32768,
		Runtime: config.RuntimeKnobs{
			Threads: 8, ThreadsBatch: 16, GPULayers: 999,
			BatchSize: 2048, UBatchSize: 512, FlashAttn: true,
			CacheTypeK: "q8_0", CacheTypeV: "q8_0", Slots: 2,
			ExtraArgs: []string{"--n-cpu-moe", "999"},
		},
	}
	args := argsFor(t, embedded(), p)

	for _, c := range []struct{ flag, value string }{
		{"--threads", "8"},
		{"--threads-batch", "16"},
		{"--n-gpu-layers", "999"},
		{"--batch-size", "2048"},
		{"--ubatch-size", "512"},
		{"--cache-type-k", "q8_0"},
		{"--cache-type-v", "q8_0"},
		{"--n-cpu-moe", "999"},
	} {
		if !has(args, c.flag, c.value) {
			t.Errorf("%s %s did not reach the command line: %v", c.flag, c.value, args)
		}
	}
	if !hasFlag(args, "--flash-attn") {
		t.Error("flash attention was not enabled")
	}
}

// llama.cpp's context is per slot, and the profile's context_tokens is what one
// task gets. Multiplying is what makes both numbers mean what they say.
func TestContextIsMultipliedByTheSlotCount(t *testing.T) {
	p := &config.Profile{Name: "t", ContextTokens: 32768,
		Runtime: config.RuntimeKnobs{Slots: 2}}
	args := argsFor(t, embedded(), p)
	if !has(args, "--ctx-size", "65536") {
		t.Errorf("context was not scaled to the slot count: %v", args)
	}
	if !has(args, "--parallel", "2") {
		t.Errorf("the slot count did not reach --parallel: %v", args)
	}
}

func TestSingleSlotDoesNotPassParallel(t *testing.T) {
	p := &config.Profile{Name: "t", ContextTokens: 8192,
		Runtime: config.RuntimeKnobs{Slots: 1}}
	args := argsFor(t, embedded(), p)
	if !has(args, "--ctx-size", "8192") {
		t.Errorf("context wrong for one slot: %v", args)
	}
	if hasFlag(args, "--parallel") {
		t.Errorf("--parallel was passed for a single slot: %v", args)
	}
}

// Operator arguments come last so they win: llama-server takes the last
// occurrence of a repeated flag, and an operator overriding one setting should
// not have to restate the rest.
func TestOperatorArgumentsComeLast(t *testing.T) {
	cfg := embedded()
	cfg.Inference.Args = []string{"--threads", "2"}
	p := &config.Profile{Name: "t", ContextTokens: 8192,
		Runtime: config.RuntimeKnobs{Threads: 8}}

	args := argsFor(t, cfg, p)
	lastThreads := ""
	for i, a := range args {
		if a == "--threads" && i+1 < len(args) {
			lastThreads = args[i+1]
		}
	}
	if lastThreads != "2" {
		t.Errorf("the operator's --threads did not win: %v", args)
	}
}

// A CPU-only profile and a GPU profile must not produce the same command line.
// That equivalence was the bug.
func TestDifferentProfilesProduceDifferentServers(t *testing.T) {
	cpu := &config.Profile{Name: "cpu", ContextTokens: 8192,
		Runtime: config.RuntimeKnobs{Threads: 8, GPULayers: 0, Slots: 1}}
	gpu := &config.Profile{Name: "gpu", ContextTokens: 65536,
		Runtime: config.RuntimeKnobs{Threads: 8, GPULayers: 999, Slots: 2, FlashAttn: true}}

	a := strings.Join(argsFor(t, embedded(), cpu), " ")
	b := strings.Join(argsFor(t, embedded(), gpu), " ")
	if a == b {
		t.Fatalf("two very different profiles produced the same server:\n%s", a)
	}
	if strings.Contains(a, "--n-gpu-layers") {
		t.Errorf("a CPU-only profile asked for GPU offload: %s", a)
	}
}

// A relative model name resolves under the models directory; an absolute path
// is taken as given.
func TestModelPathResolution(t *testing.T) {
	cfg := embedded()
	if !has(argsFor(t, cfg, nil), "--model", "/data/models/model.gguf") {
		t.Error("a relative model name did not resolve under the models directory")
	}
	cfg.Inference.Model = "/elsewhere/other.gguf"
	if !has(argsFor(t, cfg, nil), "--model", "/elsewhere/other.gguf") {
		t.Error("an absolute model path was rewritten")
	}
}

// Embedded mode with no model at all is a misconfiguration worth naming: the
// server would start and serve nothing.
func TestEmbeddedWithoutAModelIsRefused(t *testing.T) {
	cfg := config.Default()
	cfg.Inference.Mode = config.ModeEmbedded
	if _, err := config.LlamaArgs(cfg, nil, "/data/models"); err == nil {
		t.Fatal("embedded mode with no model was accepted")
	}
}

// With no profile the server still starts, but nothing is invented: guessing
// thread counts here would put back in code exactly what §9.3 moved out.
func TestNoProfileInventsNoTuning(t *testing.T) {
	args := argsFor(t, embedded(), nil)
	for _, flag := range []string{"--threads", "--n-gpu-layers", "--batch-size", "--ctx-size"} {
		if hasFlag(args, flag) {
			t.Errorf("%s was invented with no profile to take it from: %v", flag, args)
		}
	}
}

// Every shipped profile must produce a usable command line, or a profile that
// validates is still one nobody can run.
func TestEveryShippedProfileProducesArgs(t *testing.T) {
	dir := findProfilesDir(t)
	names, err := config.ListProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			p, err := config.LoadProfile(dir, name)
			if err != nil {
				t.Fatal(err)
			}
			args, err := config.LlamaArgs(embedded(), &p, "/data/models")
			if err != nil {
				t.Fatalf("%s produces no command line: %v", name, err)
			}
			if !hasFlag(args, "--model") || !hasFlag(args, "--port") {
				t.Errorf("%s is missing the basics: %v", name, args)
			}
		})
	}
}
