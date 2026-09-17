package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ServerName is the key Local Engineer registers itself under.
const ServerName = "local-engineer"

// RegisterMCP adds Local Engineer to the repository's opencode.json, leaving
// every other setting alone.
//
// Merging rather than writing: opencode.json is the developer's file and may
// already carry a model choice, other MCP servers, or permissions. Replacing it
// to add one key would be the kind of helpfulness that loses someone's
// configuration.
//
// The file is written as .json rather than .jsonc because this marshals it, and
// marshalling a document that permitted comments would silently delete them.
// OpenCode reads both; if a .jsonc already exists this reports that rather than
// creating a second file that shadows it.
func RegisterMCP(repoRoot string, command []string) (path string, changed bool, err error) {
	if jsonc := filepath.Join(repoRoot, "opencode.jsonc"); exists(jsonc) {
		return jsonc, false, fmt.Errorf(
			"%s already exists and may contain comments this cannot preserve. Add by hand:\n"+
				"  \"mcp\": { %q: { \"type\": \"local\", \"command\": %s, \"enabled\": true } }",
			jsonc, ServerName, mustJSON(command))
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

	servers, _ := doc["mcp"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	want := map[string]any{
		"type":    "local",
		"command": toAny(command),
		"enabled": true,
	}
	if equalJSON(servers[ServerName], want) {
		return path, false, nil
	}
	servers[ServerName] = want
	doc["mcp"] = servers

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
