package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ServerName is the key Local Engineer registers itself under.
const ServerName = "local-engineer"

// verifyTimeoutMillis bounds one MCP call. Twenty minutes is the task budget a
// verification runs under, so a client that gives up earlier would abandon a
// call the supervisor is still honouring.
const verifyTimeoutMillis = 20 * 60 * 1000

// RegisterMCP adds Local Engineer to the repository's opencode.json, leaving
// every other setting alone.
//
// Merging rather than writing: opencode.json is the developer's file and may
// already carry a model choice, other MCP servers, or permissions. Replacing it
// to add one key would be the kind of helpfulness that loses someone's
// configuration.
func RegisterMCP(repoRoot string, command []string) (path string, changed bool, err error) {
	// A .jsonc is refused by mergeConfig, but this one route can say what to
	// paste instead of only what went wrong.
	if jsonc := filepath.Join(repoRoot, "opencode.jsonc"); exists(jsonc) {
		return jsonc, false, fmt.Errorf(
			"%s already exists and may contain comments this cannot preserve. Add by hand:\n"+
				"  \"mcp\": { %q: { \"type\": \"local\", \"command\": %s, \"enabled\": true } }",
			jsonc, ServerName, mustJSON(command))
	}
	return mergeConfig(repoRoot, func(doc map[string]any) bool {
		servers, _ := doc["mcp"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		want := map[string]any{
			"type":    "local",
			"command": toAny(command),
			"enabled": true,
			// OpenCode's default MCP timeout is five seconds. le_verify runs
			// this repository's build, vet, test and format checks in a
			// sandbox, which is minutes on anything real — at the default the
			// call is abandoned while the work is still running, and the agent
			// is told nothing rather than told it failed.
			"timeout": verifyTimeoutMillis,
		}
		if equalJSON(servers[ServerName], want) {
			return false
		}
		servers[ServerName] = want
		doc["mcp"] = servers
		return true
	})
}

// mergeConfig applies one edit to the repository's opencode.json, writing only
// when the edit changed something. apply reports whether it did.
//
// The file is written as .json rather than .jsonc because this marshals it, and
// marshalling a document that permitted comments would silently delete them.
// OpenCode reads both; if a .jsonc already exists this reports that rather than
// creating a second file that shadows it.
func mergeConfig(repoRoot string, apply func(doc map[string]any) bool) (path string, changed bool, err error) {
	if jsonc := filepath.Join(repoRoot, "opencode.jsonc"); exists(jsonc) {
		return jsonc, false, fmt.Errorf(
			"%s already exists and may contain comments this cannot preserve. "+
				"Edit it by hand, or rename it to opencode.json", jsonc)
	}
	path = filepath.Join(repoRoot, "opencode.json")

	doc := map[string]any{}
	body, err := os.ReadFile(path) //nolint:gosec // a path derived from the workspace root
	switch {
	case err == nil:
		if err := json.Unmarshal(body, &doc); err != nil {
			return path, false, fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
	case !os.IsNotExist(err):
		return path, false, err
	default:
		doc["$schema"] = "https://opencode.ai/config.json"
	}

	if !apply(doc) {
		return path, false, nil
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return path, false, err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil { //nolint:gosec // a committed editor config
		return path, false, err
	}
	return path, true, nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func equalJSON(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// ProviderName is the key the local model is registered under.
const ProviderName = "local-engineer-local"

// RegisterModel points the editor at the same local endpoint the supervisor
// uses, and selects it.
//
// Without this a developer who has followed every instruction lands in an
// editor with nothing to talk to: the tools are registered, the agent is
// defined, and the first message asks them to sign in to a cloud provider. The
// endpoint is already known — it is the one the supervisor was configured with
// — so asking the developer to transcribe it into a second file is asking them
// to get it wrong.
//
// It is skipped when a model is already chosen. A developer who has set one has
// made a decision, and quietly replacing it with a local endpoint would be the
// kind of helpfulness that loses someone's configuration.
func RegisterModel(repoRoot, baseURL, model string) (path string, changed bool, err error) {
	if baseURL == "" || model == "" {
		return filepath.Join(repoRoot, "opencode.json"), false, nil
	}
	return mergeConfig(repoRoot, func(doc map[string]any) bool {
		// A model this function set before is ours to keep current: an operator
		// who points the supervisor at a different endpoint and re-runs setup
		// means the editor to follow. A model chosen any other way is a
		// decision, and replacing it would be the kind of helpfulness that
		// loses somebody's configuration.
		if existing, ok := doc["model"].(string); ok {
			existing = strings.TrimSpace(existing)
			if existing != "" && !strings.HasPrefix(existing, ProviderName+"/") {
				return false
			}
		}
		providers, _ := doc["provider"].(map[string]any)
		if providers == nil {
			providers = map[string]any{}
		}
		// The OpenAI-compatible adapter, because that is the boundary the
		// supervisor already speaks: anything serving that API works here
		// without this file knowing which engine it is.
		// The id is a short alias, not the model string the supervisor sends.
		// That string is a filesystem path for a local GGUF, and opencode.json
		// is a committed file: a path belongs in the operator's own config, not
		// in a repository other people clone. Servers on this boundary select
		// by what they loaded rather than by this field — a single-model
		// llama-server answers to any name — so the alias costs nothing.
		id := shortModelName(model)
		want := map[string]any{
			"npm":     "@ai-sdk/openai-compatible",
			"name":    "Local (via local-engineer)",
			"options": map[string]any{"baseURL": strings.TrimSuffix(baseURL, "/") + "/v1"},
			"models":  map[string]any{id: map[string]any{"name": id}},
		}
		if equalJSON(providers[ProviderName], want) && doc["model"] == ProviderName+"/"+id {
			return false
		}
		providers[ProviderName] = want
		doc["provider"] = providers
		doc["model"] = ProviderName + "/" + id
		return true
	})
}

// shortModelName renders a file path as something readable in a model picker.
func shortModelName(model string) string {
	base := filepath.Base(model)
	return strings.TrimSuffix(base, ".gguf")
}
