package native

import (
	"fmt"
	"testing"

	"github.com/akynte/local-engineer/internal/llm"
)

// conversation builds a seed prefix (system + user) followed by n exchanges,
// each one assistant message with a tool_call and the tool message answering
// it. That is the shape Step accumulates, so the trim is exercised against the
// structure it actually meets rather than a hand-arranged list.
func conversation(n int, body string) []llm.Message {
	msgs := []llm.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "objective and packet"},
	}
	for i := range n {
		id := fmt.Sprintf("call-%d", i)
		msgs = append(msgs,
			llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
				{ID: id, Name: "read_file", Arguments: []byte(`{"path":"x.go"}`)},
			}},
			llm.Message{Role: "tool", ToolCallID: id, Name: "read_file", Content: body},
		)
	}
	return msgs
}

// The tool definitions travel in the same request as the messages. A budget
// that ignores them is the defect that let an oversized request through.
func TestEstimateCountsToolDefinitions(t *testing.T) {
	msgs := conversation(2, "body")
	tools := []llm.ToolDef{
		{Name: "read_file", Description: "Read a file from the worktree",
			Schema: []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`)},
		{Name: "run_verification", Description: "Run the verification recipes",
			Schema: []byte(`{"type":"object","properties":{}}`)},
	}

	without := estimateTokens(msgs, nil)
	with := estimateTokens(msgs, tools)

	if with <= without {
		t.Errorf("tool definitions are not counted: %d tokens with tools, %d without", with, without)
	}
}
