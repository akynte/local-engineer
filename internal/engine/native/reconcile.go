package native

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/ledger"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/trust"
)

// reconcile restores recorded results, never re-executes a possibly completed
// side effect. A never-started remainder is closed with an explicit boundary.
func (e *Engine) reconcile(ctx context.Context, req engine.Request) error {
	tr := req.Transcript
	if req.Journal == nil {
		return fmt.Errorf("native: interrupted tool call requires a journal for reconciliation")
	}
	fence, err := trust.RestoreFence(tr.FenceToken)
	if err != nil {
		return err
	}
	e.fence = fence
	current, err := ledger.ContentManifest(req.Worktree)
	if err != nil {
		return err
	}
	start := -1
	for i := len(tr.Messages) - 1; i >= 0; i-- {
		if tr.Messages[i].Role == "assistant" && len(tr.Messages[i].ToolCalls) > 0 {
			start = i
			break
		}
	}
	if start < 0 {
		return fmt.Errorf("native: pending transcript has no tool batch")
	}
	completed := map[string]bool{}
	for _, message := range tr.Messages[start+1:] {
		if message.Role == "tool" {
			completed[message.ToolCallID] = true
		}
	}
	ops, err := req.Journal.Operations(ctx, req.TaskID)
	if err != nil {
		return err
	}
	lastCandidate := tr.Candidate
	var messages []llm.Message
	var recovered []Result
	var recoveredCalls []llm.ToolCall
	boundary := false
	for _, call := range tr.Messages[start].ToolCalls {
		if completed[call.ID] {
			continue
		}
		var match *ledger.Operation
		for i := range ops {
			var intent struct {
				Session   string `json:"session"`
				Step      int    `json:"step"`
				ID        string `json:"call_id"`
				Arguments string `json:"arguments"`
			}
			if json.Unmarshal(ops[i].Intent, &intent) == nil && intent.Session == tr.FenceToken && intent.Step == tr.Steps && intent.ID == call.ID && intent.Arguments == string(call.Arguments) {
				match = &ops[i]
			}
		}
		if match == nil {
			boundary = true
			messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Name: call.Name, Content: "Not executed: process stopped before this action was journalled."})
			continue
		}
		if match.Uncertain() {
			return fmt.Errorf("native: tool %s has an uncertain side effect; explicit recovery is required", call.ID)
		}
		var result Result
		if err := json.Unmarshal(match.Outcome, &result); err != nil {
			return err
		}
		if match.CandidateBefore != lastCandidate {
			return fmt.Errorf("native: interrupted action candidate chain does not match")
		}
		lastCandidate = match.CandidateAfter
		messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Name: call.Name, Content: fence.Wrap("result of "+call.Name, result.Content)})
		recovered = append(recovered, result)
		recoveredCalls = append(recoveredCalls, call)
	}
	if lastCandidate == "" || lastCandidate != current {
		return fmt.Errorf("native: candidate changed outside the recorded actions")
	}
	tr.Messages = append(tr.Messages, messages...)
	for i, result := range recovered {
		tr.Tools++
		if result.Invalid {
			tr.InvalidCalls++
		}
		if recoveredCalls[i].Name == ToolRunRecipe {
			tr.VerifyRuns++
		}
		if result.Done {
			tr.Closed = true
			tr.ClaimsDone = true
			tr.Summary = result.Summary
		}
	}
	tr.Pending = false
	tr.Candidate = current
	if boundary {
		tr.Closed = true
		tr.BudgetExhausted = true
		tr.Summary = "interrupted batch reconciled; unfinished calls require a new phase"
	}
	if req.SaveTranscript != nil {
		return req.SaveTranscript(ctx, tr)
	}
	return nil
}
