package native

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/ledger"
	"github.com/akynte/local-engineer/internal/llm"
)

// authorize validates the actual advertised schema, not just parseable JSON.
// The native schemas use flat objects with string/integer properties and enums.
func (e *Engine) authorize(req engine.Request, call llm.ToolCall) error {
	var def *llm.ToolDef
	for i := range e.tools {
		if e.tools[i].Name == call.Name {
			def = &e.tools[i]
			break
		}
	}
	if def == nil {
		return fmt.Errorf("tool %q is not advertised for this attempt", call.Name)
	}
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(def.Schema, &schema); err != nil {
		return fmt.Errorf("invalid supervisor tool schema: %w", err)
	}
	var args map[string]any
	if err := json.Unmarshal(call.Arguments, &args); err != nil || args == nil {
		return fmt.Errorf("arguments must be a valid JSON object matching the tool schema")
	}
	for _, key := range schema.Required {
		if _, ok := args[key]; !ok {
			return fmt.Errorf("missing required argument %q", key)
		}
	}
	for key, value := range args {
		property, ok := schema.Properties[key]
		if !ok {
			return fmt.Errorf("unknown argument %q", key)
		}
		switch property.Type {
		case "string":
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("%s must be a string", key)
			}
			if len(property.Enum) > 0 {
				found := false
				for _, choice := range property.Enum {
					found = found || choice == text
				}
				if !found {
					return fmt.Errorf("%s is not an allowed value", key)
				}
			}
		case "integer":
			n, ok := value.(float64)
			if !ok || math.Trunc(n) != n || n < 0 || n > math.MaxInt32 {
				return fmt.Errorf("%s must be a nonnegative 32-bit integer", key)
			}
		default:
			return fmt.Errorf("unsupported supervisor schema type %q", property.Type)
		}
	}
	switch call.Name {
	case ToolReadFile, ToolWriteFile, ToolEditFile:
		return req.Access.Check(req.Worktree, str(args, "path"), call.Name != ToolReadFile)
	case ToolListFiles:
		dir := str(args, "dir")
		if dir == "" {
			dir = "."
		}
		return req.Access.Check(req.Worktree, dir, false)
	}
	return nil
}

// exec records decisions before a tool can have side effects. Journal failure
// stops the attempt; it must not turn into an unrecorded edit.
func (e *Engine) exec(ctx context.Context, req engine.Request, call llm.ToolCall) (Result, error) {
	denial := e.authorize(req, call)
	before := ""
	if req.Journal != nil {
		var err error
		before, err = ledger.ContentManifest(req.Worktree)
		if err != nil {
			return Result{}, err
		}
	}
	var h *ledger.Handle
	if req.Journal != nil {
		decision, reason := "allow", ""
		if denial != nil {
			decision, reason = "deny", denial.Error()
		}
		var err error
		h, err = req.Journal.Begin(ctx, req.TaskID, ledger.KindDecision, map[string]any{
			"phase": "EDIT", "tool": call.Name, "call_id": call.ID,
			"arguments": string(call.Arguments), "decision": decision, "reason": reason,
			"session": e.ensureFence().Token(), "step": req.Transcript.Steps,
		}, before)
		if err != nil {
			return Result{}, err
		}
	}
	var result Result
	if denial != nil {
		result = failed("firewall: %v", denial)
		result.Invalid = true
	} else {
		result = e.execute(ctx, req, call)
	}
	if h != nil {
		after, err := ledger.ContentManifest(req.Worktree)
		if err != nil {
			return result, err
		}
		if err := h.Complete(ctx, result, after, ""); err != nil {
			return result, err
		}
	}
	return result, nil
}
