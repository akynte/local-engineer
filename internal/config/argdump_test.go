package config_test

import (
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/config"
)

// Not an assertion — a readable record of what each profile actually starts, so
// a change to the argument builder shows up in a diff a person can judge.
func TestShowGeneratedCommandLines(t *testing.T) {
	cfg := config.Default()
	cfg.Inference.Mode = config.ModeEmbedded
	cfg.Inference.Model = "model.gguf"

	for _, name := range config.Embedded() {
		p, err := config.LoadEmbeddedProfile(name)
		if err != nil {
			t.Fatal(err)
		}
		args, err := config.LlamaArgs(cfg, &p, "/data/models")
		if err != nil {
			t.Logf("%-30s (no embedded server: %v)", name, err)
			continue
		}
		t.Logf("%-30s llama-server %s", name, strings.Join(args, " "))
	}
}
