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

func TestTrimFitsTheBudgetAndKeepsThePrefixAndNewestExchange(t *testing.T) {
	msgs := conversation(12, "a file body that costs real tokens: "+string(make([]byte, 400)))
	const budget = 500

	trimmed, dropped := trimMessages(msgs, nil, budget)

	if dropped == 0 {
		t.Fatalf("a conversation over the budget should have been trimmed")
	}
	if got := estimateTokens(trimmed, nil); got > budget {
		t.Errorf("trimmed conversation is %d tokens, over the %d budget", got, budget)
	}
	// The stable prefix is what the prompt cache depends on (§8.2).
	if trimmed[0].Role != "system" || trimmed[1].Role != "user" {
		t.Errorf("the stable prefix was dropped: got %q, %q", trimmed[0].Role, trimmed[1].Role)
	}
	// The newest exchange is the one the model is mid-way through.
	last := msgs[len(msgs)-1]
	if got := trimmed[len(trimmed)-1]; got.ToolCallID != last.ToolCallID {
		t.Errorf("the newest exchange was dropped: last tool_call_id is %q, want %q",
			got.ToolCallID, last.ToolCallID)
	}
}

// An orphaned tool_call_id is rejected by an OpenAI-compatible provider with
// the same hard 400 the trim exists to prevent, so dropping a tool message
// without its assistant message trades one failure for another.
func TestTrimLeavesNoOrphanedToolResult(t *testing.T) {
	msgs := conversation(12, "a file body that costs real tokens: "+string(make([]byte, 400)))

	trimmed, _ := trimMessages(msgs, nil, 500)

	answered := map[string]bool{}
	for _, m := range trimmed {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				answered[tc.ID] = true
			}
		}
	}
	for i, m := range trimmed {
		if m.Role == "tool" && !answered[m.ToolCallID] {
			t.Errorf("message %d answers tool_call_id %q whose assistant message was dropped",
				i, m.ToolCallID)
		}
	}
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

// Counting the tools inside estimateTokens is worth nothing if the trim calls
// it without them. This fixes a budget between the two totals: the messages
// alone fit, the messages plus the definitions do not. A trim that discards
// its tools argument sees the first number and drops nothing.
func TestTrimAccountsForTheToolDefinitions(t *testing.T) {
	msgs := conversation(6, "a file body that costs real tokens: "+string(make([]byte, 200)))
	var tools []llm.ToolDef
	for i := range 10 {
		tools = append(tools, llm.ToolDef{
			Name:        fmt.Sprintf("tool_%d", i),
			Description: "a tool description of the length the real surface carries",
			Schema: []byte(`{"type":"object","properties":{"path":{"type":"string"},` +
				`"start_line":{"type":"integer"},"end_line":{"type":"integer"}}}`),
		})
	}

	msgsOnly := estimateTokens(msgs, nil)
	withTools := estimateTokens(msgs, tools)
	if withTools <= msgsOnly {
		t.Fatalf("fixture is wrong: tools added %d tokens", withTools-msgsOnly)
	}
	// Fits without the definitions, overflows with them.
	budget := (msgsOnly + withTools) / 2

	if _, dropped := trimMessages(msgs, nil, budget); dropped != 0 {
		t.Fatalf("fixture is wrong: the messages alone should fit, but %d were dropped", dropped)
	}
	if _, dropped := trimMessages(msgs, tools, budget); dropped == 0 {
		t.Error("the trim ignored the tool definitions: the request overflows the budget " +
			"once they are counted, but nothing was dropped")
	}
}

// Trimming must not corrupt the caller's slice: Step keeps using it, and a
// test that inspected the original afterwards would read rearranged data.
func TestTrimDoesNotMutateItsInput(t *testing.T) {
	msgs := conversation(12, "a file body that costs real tokens: "+string(make([]byte, 400)))
	before := make([]llm.Message, len(msgs))
	copy(before, msgs)

	if _, dropped := trimMessages(msgs, nil, 500); dropped == 0 {
		t.Fatal("expected this conversation to be trimmed")
	}

	for i := range before {
		if msgs[i].Role != before[i].Role || msgs[i].ToolCallID != before[i].ToolCallID {
			t.Fatalf("trimMessages mutated its input at index %d", i)
		}
	}
}

func TestTrimIsSkippedWithoutABudget(t *testing.T) {
	msgs := conversation(4, "body")
	for _, budget := range []int{0, -1} {
		got, dropped := trimMessages(msgs, nil, budget)
		if dropped != 0 || len(got) != len(msgs) {
			t.Errorf("budget %d: trimmed %d message(s); a budget of zero disables trimming",
				budget, dropped)
		}
	}
}

// A conversation that cannot be trimmed below the budget must still return,
// rather than looping while it has nothing left that it is allowed to drop.
func TestTrimTerminatesWhenOnlyTheNewestExchangeRemains(t *testing.T) {
	msgs := conversation(1, string(make([]byte, 8000)))

	got, dropped := trimMessages(msgs, nil, 10)

	if dropped != 0 || len(got) != len(msgs) {
		t.Errorf("the only exchange must be kept: dropped %d of %d", dropped, len(msgs))
	}
}
