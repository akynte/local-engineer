package mcp

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akynte/local-engineer/internal/firewall"
	"github.com/akynte/local-engineer/internal/policy"
	"github.com/akynte/local-engineer/internal/task"
	"github.com/akynte/local-engineer/internal/trust"
	"github.com/akynte/local-engineer/internal/worktree"
)

// The editing tools exist so the session's file access goes through the
// firewall instead of around it (architecture review §4, §9).
//
// An OpenCode session has its own read and edit tools, and they answer to
// OpenCode's permission system — which the review is explicit about: its
// enforcement has documented bypasses, so it is a convenience layer and not the
// boundary. The sandbox keeps the session inside the worktree, which is the
// containment that actually holds. What the sandbox cannot express is the rest
// of §9's path policy: a `.env` committed inside the repository is inside the
// worktree, and so is a generated file, and so is every path the plan did not
// declare.
//
// So the confined session runs with those built-in tools denied and these in
// their place. Every call goes through the same firewall.Access the native
// editor uses, which is the point: one path policy, one implementation, and no
// second route that has to be kept in step with it.

// readIn is the proxied read.
type readIn struct {
	Path      string `json:"path" jsonschema:"repository-relative path to read"`
	StartLine int    `json:"start_line,omitempty" jsonschema:"first line, 1-based; omit for the start"`
	EndLine   int    `json:"end_line,omitempty" jsonschema:"last line, inclusive; omit for the end"`
	Root      string `json:"root,omitempty" jsonschema:"a subdirectory of the open repository; defaults to its root"`
}

type readOut struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}

// maxReadBytes bounds one read. A model that asks for a 40,000-line generated
// file gets the beginning of it and a note, rather than a phase whose context
// budget is gone.
const maxReadBytes = 256 << 10

func (s *Server) readFile(ctx context.Context, _ *mcp.CallToolRequest, in readIn) (*mcp.CallToolResult, readOut, error) {
	sess, err := s.resolve(ctx, in.Root)
	if err != nil {
		return fail("%v", err), readOut{}, nil
	}
	defer sess.Close() //nolint:contextcheck // cleanup must not take the request context: a cancelled call would then skip closing the databases.

	root := sess.Workspace.Root
	access := firewall.Access{Protected: protectedSet(root)}
	if err := access.Check(root, in.Path, false); err != nil {
		return fail("%v", err), readOut{}, nil
	}
	body, err := worktree.ReadWithin(root, in.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return fail("%s does not exist", in.Path), readOut{}, nil
		}
		return fail("reading %s: %v", in.Path, err), readOut{}, nil
	}
	out := readOut{Path: in.Path, StartLine: 1}
	if len(body) > maxReadBytes {
		body, out.Truncated = body[:maxReadBytes], true
	}
	lines := strings.Split(string(body), "\n")
	start, end := 1, len(lines)
	if in.StartLine > 0 {
		start = in.StartLine
	}
	if in.EndLine > 0 && in.EndLine < end {
		end = in.EndLine
	}
	if start > len(lines) {
		return fail("%s has %d lines; start_line %d is past the end", in.Path, len(lines), start), readOut{}, nil
	}
	out.StartLine, out.EndLine = start, end
	out.Content = strings.Join(lines[start-1:end], "\n")

	// Fenced with its provenance, like every other route that puts repository
	// content in front of a model. A file that says "ignore your instructions"
	// is a finding, not an instruction, and the marker is what says so.
	fence, err := trust.NewFence()
	if err != nil {
		return fail("%v", err), readOut{}, nil
	}
	origin := fmt.Sprintf("source=file path=%s lines=%d-%d", in.Path, start, end)
	return text(fence.Preamble() + "\n" + fence.Wrap(origin, out.Content)), out, nil
}

// editIn is the proxied edit: exact string replacement, not a whole-file write.
type editIn struct {
	TaskID string `json:"task_id" jsonschema:"the id le_task_start returned; its declared scope is what bounds this write"`
	Path   string `json:"path" jsonschema:"repository-relative path to change"`
	Old    string `json:"old" jsonschema:"exact text to replace, including indentation. It must appear exactly once"`
	New    string `json:"new" jsonschema:"replacement text"`
	Root   string `json:"root,omitempty" jsonschema:"a subdirectory of the open repository; defaults to its root"`
}

type editOut struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
}

func (s *Server) editFile(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
	if strings.TrimSpace(in.TaskID) == "" {
		return fail("task_id is required: a write outside a supervised task has nothing to bound it. Call le_task_start first"), editOut{}, nil
	}
	if in.Old == in.New {
		return fail("old and new are identical; nothing to do"), editOut{}, nil
	}
	sess, err := s.resolve(ctx, in.Root)
	if err != nil {
		return fail("%v", err), editOut{}, nil
	}
	defer sess.Close() //nolint:contextcheck // cleanup must not take the request context: a cancelled call would then skip closing the databases.

	t, err := task.NewStore(sess.Store).Get(ctx, in.TaskID)
	if err != nil {
		return fail("%v", err), editOut{}, nil
	}
	root := sess.Workspace.Root
	access := firewall.Access{WriteScope: t.Budget.Scope, Protected: protectedSet(root)}
	if err := access.Check(root, in.Path, true); err != nil {
		// The refusal names the rule, so the next attempt is a re-plan rather
		// than the same edit with a different spelling.
		return fail("%v", err), editOut{}, nil
	}

	body, err := worktree.ReadWithin(root, in.Path)
	if err != nil {
		if os.IsNotExist(err) {
			// A new file is a write too, and the scope check above already
			// decided whether this one is allowed.
			if in.Old != "" {
				return fail("%s does not exist; pass an empty old to create it", in.Path), editOut{}, nil
			}
			if err := worktree.WriteWithin(root, in.Path, []byte(in.New)); err != nil {
				return fail("creating %s: %v", in.Path, err), editOut{}, nil
			}
			return text(fmt.Sprintf("Created %s.", in.Path)), editOut{Path: in.Path, Replacements: 1}, nil
		}
		return fail("reading %s: %v", in.Path, err), editOut{}, nil
	}

	// Exactly once, or not at all. A replacement that matched twice would
	// change a line the model never read, and one that matched zero times
	// means it is working from a version of the file that no longer exists —
	// both are worth a refusal the model can act on.
	switch count := strings.Count(string(body), in.Old); count {
	case 1:
	case 0:
		return fail("that exact text is not in %s. Read it again: it may have changed since you last saw it", in.Path), editOut{}, nil
	default:
		return fail("that text appears %d times in %s. Include enough surrounding context to name one of them", count, in.Path), editOut{}, nil
	}
	updated := strings.Replace(string(body), in.Old, in.New, 1)
	if err := worktree.WriteWithin(root, in.Path, []byte(updated)); err != nil {
		return fail("writing %s: %v", in.Path, err), editOut{}, nil
	}
	return text(fmt.Sprintf("Replaced one occurrence in %s. Call le_verify when the change is complete.", in.Path)),
		editOut{Path: in.Path, Replacements: 1}, nil
}

// protectedSet loads the repository's own policy rules, so a proxied write
// answers to the same operator configuration a supervised task does.
func protectedSet(root string) policy.Set {
	set, err := policy.Load(root + "/policies")
	if err != nil {
		return policy.Set{}
	}
	return set
}
