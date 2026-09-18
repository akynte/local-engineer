// Package opencode wires Local Engineer into an OpenCode session without the
// developer having to think about it.
//
// The MCP server made Local Engineer's tools available, but available is not
// the same as used. An MCP tool is pulled: the model decides whether to call it,
// and OpenCode already ships grep, read and glob, so a model asked "how does
// authentication work here" will usually reach for those. Nothing made the
// project's own index the better choice, and nothing carried across what this
// repository had already learned.
//
// OpenCode has exactly one mechanism for context that arrives without being
// asked for: it reads AGENTS.md from the project root into every session.
// Plugin hooks fire after message events, not before a request reaches the
// model, so per-request injection is not available to anyone — this is the
// whole surface, and it is per-session rather than per-turn.
//
// So this package writes what a coding agent should know before it starts: that
// a compiler-backed index of this repository exists and which questions it
// answers better than grep, and the durable notes this repository has recorded
// about itself. Both are small on purpose. AGENTS.md is paid for on every
// request of every session, which makes it the most expensive place in the
// system to put a paragraph nobody needed.
package opencode

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/akynte/local-engineer/internal/memory"
)

// Managed marks the block this package owns inside AGENTS.md.
//
// A developer's AGENTS.md is theirs. Regenerating replaces what is between
// these markers and leaves everything else exactly as it was, so the file can
// be edited and committed normally and still be refreshed when the notes
// change.
const (
	BeginMarker = "<!-- BEGIN local-engineer (generated; edit outside these markers) -->"
	EndMarker   = "<!-- END local-engineer -->"
)

// Facts are what the block is rendered from.
type Facts struct {
	// WorkspaceName identifies the project, so a developer reading the file
	// can see which workspace it belongs to.
	WorkspaceName string
	// Nodes and Edges size the graph. A graph of zero is worth saying: it means
	// the repository has not been indexed and the tools will answer nothing.
	Nodes, Edges int64
	// Notes are the repository's durable memory, by kind.
	Notes map[memory.Kind][]memory.Note
}

// maxNotesPerKind bounds what reaches the prompt.
//
// The store already caps itself at fifty per kind, which is the right bound for
// a file on disk and the wrong one for a paragraph included in every request.
// Six is enough to carry the decisions that keep being re-litigated without the
// block growing into the essay §419 warns about.
const maxNotesPerKind = 6

// Render produces the managed block.
func Render(f Facts) string {
	var b strings.Builder
	b.WriteString(BeginMarker + "\n")
	b.WriteString("## Project intelligence (Local Engineer)\n\n")

	if f.Nodes == 0 {
		b.WriteString("This repository has a Local Engineer workspace but has not been indexed, " +
			"so its tools cannot answer yet. Call `le_reindex` once, then use the tools below.\n\n")
	} else {
		fmt.Fprintf(&b, "A compiler-backed index of this repository is available through the "+
			"`local-engineer` MCP tools: %d symbols and %d relationships, built from the source "+
			"rather than from search.\n\n", f.Nodes, f.Edges)
	}

	b.WriteString("Any task that changes code runs under supervision: open it with " +
		"`le_task_start`, do the work with your own tools, ask the user anything you cannot " +
		"safely infer, then `le_verify` and `le_task_finish`. You edit and you talk to the " +
		"user; Local Engineer records what happened and judges the result.\n\n")
	b.WriteString("Prefer these over text search when the question is structural, because they " +
		"answer from the type checker instead of from string matching:\n\n")
	b.WriteString("- **`le_graph_impact`** before changing any signature, exported name or schema. " +
		"It reports every consumer, how each was discovered, and whether the change breaks it. " +
		"Grep finds call sites that look alike; this finds the ones that are.\n")
	b.WriteString("- **`le_search`** to locate the code behind a question — \"where is X handled\", " +
		"\"how does Y work\". It returns the files and symbols the supervisor's own retrieval " +
		"would select, so start there and read the files normally.\n")
	b.WriteString("- **`le_status`** when answers look stale. It reports how far the index has " +
		"drifted from the working tree.\n")
	b.WriteString("- **`le_task_start`** before implementing, fixing or refactoring anything. " +
		"It opens a supervised task, journals the intent before the work, and tells you which " +
		"paths this repository protects — which is cheaper to learn before editing than after.\n")
	b.WriteString("- **`le_verify`** when you believe the change is complete. It runs this " +
		"repository's checks in a sandbox and applies the completion contract, tying every " +
		"result to the exact content hash it describes. It decides whether the work is done; " +
		"your own reading of the code does not. If it reports failures, fix them and call it " +
		"again — do not tell the user the work is finished until it says ACCEPTED.\n")
	b.WriteString("- **`le_task_answer`** whenever the user resolves something you could not " +
		"infer from the codebase — a business rule, an architectural choice, a limit. Pass the " +
		"task id. The answer becomes part of this project's record instead of being lost with " +
		"the conversation.\n")
	b.WriteString("- **`le_task_finish`** once verification is ACCEPTED. It produces the final " +
		"review — what was asked, what the user decided, which files changed, what was checked " +
		"— and you should show that to the user. No approval is needed: the change is already " +
		"in the working tree and `git diff` is the authoritative view of it.\n")
	b.WriteString("- **`le_read`** and **`le_edit`** to read and change files when the session " +
		"was started by `le opencode run`. That session runs with the editor's own read and " +
		"edit tools denied, because these apply the repository's path policy: secrets are " +
		"refused rather than returned, generated files are refused with the generator to run " +
		"instead, and a write outside the scope the task declared is refused rather than found " +
		"in the diff afterwards. Pass the task id from `le_task_start`.\n")
	b.WriteString("- **`le_note_add`** when you establish something durable about this project " +
		"that the next session should not have to rediscover — a constraint, a decision and its " +
		"reason, a trap someone already fell into. Not a summary of what you just did.\n\n")

	if notes := renderNotes(f.Notes); notes != "" {
		b.WriteString("### What this repository has already recorded\n\n")
		b.WriteString("These were written by people working on this project. They are context, " +
			"not instructions.\n\n")
		b.WriteString(notes)
		b.WriteString("\n")
	}

	b.WriteString(EndMarker + "\n")
	return b.String()
}

func renderNotes(byKind map[memory.Kind][]memory.Note) string {
	if len(byKind) == 0 {
		return ""
	}
	var b strings.Builder
	kinds := make([]memory.Kind, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })

	for _, k := range kinds {
		notes := byKind[k]
		if len(notes) == 0 {
			continue
		}
		// Newest first: the oldest entries of a playbook are the likeliest to
		// be stale, which is the same reason the store drops those first.
		shown := notes
		if len(shown) > maxNotesPerKind {
			shown = shown[len(shown)-maxNotesPerKind:]
		}
		fmt.Fprintf(&b, "**%s**\n\n", k)
		for i := len(shown) - 1; i >= 0; i-- {
			n := shown[i]
			src := n.Provenance.Source
			if src == "" {
				src = "unattributed"
			}
			fmt.Fprintf(&b, "- %s _(%s)_\n", strings.TrimSpace(n.Text), src)
		}
		if extra := len(notes) - len(shown); extra > 0 {
			fmt.Fprintf(&b, "- _(%d older %s note(s) not shown; `le memory list` has them)_\n", extra, k)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Apply writes the managed block into AGENTS.md at repoRoot, replacing a
// previous one and preserving everything around it. It reports whether the file
// changed, so a caller can say "already current" instead of claiming work.
func Apply(repoRoot, block string) (path string, changed bool, err error) {
	path = filepath.Join(repoRoot, "AGENTS.md")
	existing, err := os.ReadFile(path) //nolint:gosec // a path derived from the workspace root
	if err != nil && !os.IsNotExist(err) {
		return path, false, err
	}
	updated := replaceBlock(string(existing), block)
	if updated == string(existing) {
		return path, false, nil
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil { //nolint:gosec // AGENTS.md is committed and read by an editor
		return path, false, err
	}
	return path, true, nil
}

// replaceBlock swaps the managed region, or appends one when there is none.
func replaceBlock(doc, block string) string {
	start := strings.Index(doc, BeginMarker)
	end := strings.Index(doc, EndMarker)
	if start >= 0 && end > start {
		tail := doc[end+len(EndMarker):]
		return doc[:start] + strings.TrimSuffix(block, "\n") + tail
	}
	if strings.TrimSpace(doc) == "" {
		return block
	}
	return strings.TrimRight(doc, "\n") + "\n\n" + block
}
