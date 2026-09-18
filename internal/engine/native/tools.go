// Package native is the engine that turns a retrieved packet into edits,
// driving a tool loop against the provider boundary of design v3 §9.1.
//
// The tool surface is deliberately small and bounded (§8.2: "Just-in-time tool
// reads with paging"; §9.3: tool-surface size is profile configuration, not a
// constant). Every file operation is confined to the task's worktree by this
// package *and* by the sandbox, because a single layer of path checking is one
// bug away from being no layer at all.
package native

import (
	"encoding/json"
	"sort"

	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/worktree"
)

// ErrOutsideWorktree is returned when a tool argument names a path outside the
// task's checkout. It wraps the worktree package's own error, so the check and
// the message stay in step.
var ErrOutsideWorktree = worktree.ErrOutside

// Tool names. They are constants because they appear in the journal, in
// telemetry and in prompts, and a typo in any of those is a silent failure.
const (
	ToolReadFile   = "read_file"
	ToolWriteFile  = "write_file"
	ToolEditFile   = "edit_file"
	ToolListFiles  = "list_files"
	ToolSearch     = "search_code"
	ToolFindSymbol = "find_symbol"
	ToolImpact     = "impact_of"
	ToolRunRecipe  = "run_verification"
	ToolGitTouch   = "git_touch"
	ToolDone       = "done"
)

// Definitions returns the tool declarations, capped at most.
//
// The cap comes from the hardware profile: a small model given twenty tools
// picks the wrong one far more often than one given eight. Ordering is by
// importance so that a cap drops the least useful tools rather than an
// arbitrary subset.
// Wired reports which optional subsystems an engine actually has.
//
// It exists because advertising a tool the engine cannot run is not a neutral
// mistake. The model spends a call discovering the tool fails, and under a
// wall-clock budget those calls come out of the work. In an ablation it is
// worse than that: an arm built by leaving retrieval or verification unwired
// would still be offered the tools, so the baseline is charged for the
// component it was supposed to be measured without, and the comparison
// flatters whatever is being ablated.
type Wired struct {
	// Retrieval backs search_code.
	Retrieval bool
	// Graph backs find_symbol and impact_of.
	Graph bool
	// Recipes backs run_verification.
	Recipes bool
}

// AllWired is every subsystem present, for callers that wire the full set.
func AllWired() Wired { return Wired{Retrieval: true, Graph: true, Recipes: true} }

// needs maps a tool to the subsystem it cannot run without.
func (w Wired) has(name string) bool {
	switch name {
	case ToolSearch:
		return w.Retrieval
	case ToolFindSymbol, ToolImpact, ToolGitTouch:
		// git_touch reads commit-to-file edges, which the gitlog analyzer
		// writes into the graph. No graph, no history.
		return w.Graph
	case ToolRunRecipe:
		return w.Recipes
	}
	// Reading, editing and listing need nothing but the worktree.
	return true
}

// Definitions returns the tools an engine with these subsystems can execute,
// ordered by importance and capped at most.
func Definitions(most int, wired Wired) []llm.ToolDef {
	all := []llm.ToolDef{
		{
			Name: ToolReadFile,
			Description: "Read part of a file in the worktree. Prefer a line range: reading a whole " +
				"large file wastes the budget that later steps need.",
			Schema: schema(`{
				"type":"object",
				"properties":{
					"path":{"type":"string","description":"Path relative to the worktree root."},
					"start_line":{"type":"integer","description":"First line, 1-based. Omit for the start."},
					"end_line":{"type":"integer","description":"Last line, inclusive. Omit for the end."}
				},
				"required":["path"],
				"additionalProperties":false}`),
		},
		{
			Name: ToolEditFile,
			Description: "Replace an exact string in a file. The old string must appear exactly once, " +
				"so include enough surrounding context to be unambiguous. Prefer this over write_file: " +
				"it cannot silently discard code you did not read.",
			Schema: schema(`{
				"type":"object",
				"properties":{
					"path":{"type":"string"},
					"old":{"type":"string","description":"Exact text to replace, including indentation."},
					"new":{"type":"string","description":"Replacement text."}
				},
				"required":["path","old","new"],
				"additionalProperties":false}`),
		},
		{
			Name: ToolRunRecipe,
			Description: "Run verification (build, vet, test) and get the findings. Use this after " +
				"every edit: the compiler and the tests are the most reliable correction available.",
			Schema: schema(`{
				"type":"object",
				"properties":{
					"kind":{"type":"string","enum":["build","vet","test","format","lint"],
						"description":"Which check to run. build is fastest; run it first."}
				},
				"required":["kind"],
				"additionalProperties":false}`),
		},
		{
			Name:        ToolSearch,
			Description: "Search the indexed repository for text. Returns file paths and line ranges.",
			Schema: schema(`{
				"type":"object",
				"properties":{
					"query":{"type":"string"},
					"limit":{"type":"integer","description":"Maximum results, default 10."}
				},
				"required":["query"],
				"additionalProperties":false}`),
		},
		{
			Name: ToolFindSymbol,
			Description: "Find a function, method, type or interface by name, with its file, line " +
				"and signature.",
			Schema: schema(`{
				"type":"object",
				"properties":{"name":{"type":"string"}},
				"required":["name"],
				"additionalProperties":false}`),
		},
		{
			Name: ToolImpact,
			Description: "Report what a change to a symbol would affect: its callers, the types that " +
				"implement it, and the tests covering it. Use this before changing a signature.",
			Schema: schema(`{
				"type":"object",
				"properties":{
					"symbol":{"type":"string"},
					"change":{"type":"string",
						"enum":["signature","behaviour","remove","rename","add_field"]}
				},
				"required":["symbol","change"],
				"additionalProperties":false}`),
		},
		{
			Name: ToolGitTouch,
			Description: "Show which commits recently touched a file or symbol, with who " +
				"changed it and when. Useful for finding why code is the way it is before " +
				"changing it, and for spotting a file that several unrelated changes keep " +
				"colliding in.",
			Schema: schema(`{
				"type":"object",
				"properties":{
					"path":{"type":"string","description":"A file path relative to the worktree root."},
					"symbol":{"type":"string","description":"A symbol name, as an alternative to a path."},
					"limit":{"type":"integer","description":"How many commits to return, default 10."}
				},
				"additionalProperties":false}`),
		},
		{
			Name:        ToolListFiles,
			Description: "List files under a directory in the worktree.",
			Schema: schema(`{
				"type":"object",
				"properties":{"dir":{"type":"string","description":"Directory relative to the worktree root; omit for the root."}},
				"additionalProperties":false}`),
		},
		{
			Name: ToolWriteFile,
			Description: "Write a file's entire contents, creating it if needed. Use only for new " +
				"files; for an existing file use edit_file.",
			Schema: schema(`{
				"type":"object",
				"properties":{"path":{"type":"string"},"content":{"type":"string"}},
				"required":["path","content"],
				"additionalProperties":false}`),
		},
		{
			Name: ToolDone,
			Description: "Declare the objective met. Only call this after verification has passed: " +
				"the supervisor checks the evidence itself, and calling done early wastes an attempt.",
			Schema: schema(`{
				"type":"object",
				"properties":{"summary":{"type":"string","description":"What you changed and why."}},
				"required":["summary"],
				"additionalProperties":false}`),
		},
	}
	// Drop what this engine cannot run before applying the cap, so the cap
	// spends its budget on tools that work rather than on ones that would be
	// rejected on first use.
	out := all[:0:0]
	for _, t := range all {
		if wired.has(t.Name) {
			out = append(out, t)
		}
	}
	if most > 0 && most < len(out) {
		out = out[:most]
	}
	return out
}

func schema(s string) json.RawMessage {
	// Compact it so the tool surface costs as few prompt tokens as possible;
	// the schemas are written readably above and shipped minified.
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic("native: malformed tool schema: " + err.Error())
	}
	out, err := json.Marshal(v)
	if err != nil {
		panic("native: " + err.Error())
	}
	return out
}

// ToolNames lists the declared tool names, for telemetry and tests.
func ToolNames(defs []llm.ToolDef) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.Name
	}
	sort.Strings(out)
	return out
}
