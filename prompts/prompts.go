// Package prompts embeds the prompts that decide how the engine behaves
// (design v3 §1.2).
//
// They live in files rather than in string literals because a prompt is
// behaviour, and behaviour should be reviewed as behaviour. A diff to a .go
// file reads as code; a diff to a prompt reads as what it is.
//
// Embedding rather than loading at runtime is deliberate. A prompt that could
// be loaded from a path is a prompt a task could reach, and the system turn is
// the one part of a packet the model is meant to trust.
package prompts

import (
	_ "embed"
	"strings"
)

//go:embed engine-system.md
var engineSystem string

// EngineSystem is the editing engine's system turn.
//
// §8.2 lays a packet out cache-first: the system turn is the stable prefix the
// prompt cache reuses across every step of a task. Anything task-specific
// belongs in the user turn, or the prefix changes and the cache is lost on
// every call.
func EngineSystem() string { return strings.TrimSpace(engineSystem) }
