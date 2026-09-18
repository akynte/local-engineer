package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/ledger"
)

// newTraceCmd is §19's `le trace <task>`: the record of why a task did what it
// did, in the order it happened.
//
// §21 sets the bar with one question — "why did the agent modify
// order_check_test.go?" — and the answer has to be a chain a person can follow:
// the test that failed, the fingerprint it was classified under, the repair
// decision, and the review that accepted it. A table of operation kinds does not
// answer that, which is why this is a separate command from `le task journal`
// rather than another flag on it.
//
// The JSONL form is §18's export. One object per line, so it appends, streams,
// and survives being cut off partway through — which a single JSON array does
// not, and a trace is exactly the thing you read after something went wrong.
func newTraceCmd() *cobra.Command {
	var asJSONL bool
	var outPath string
	cmd := &cobra.Command{
		Use:   "trace <task-id>",
		Short: "Show why a task did what it did",
		Long: "trace prints a task's recorded history: every operation, when it ran, what\n" +
			"it intended before the side effect, and what it recorded afterwards.\n\n" +
			"An operation with an intent and no outcome is shown as UNCERTAIN rather\n" +
			"than as a failure. That is the state the journal exists to make visible: the\n" +
			"supervisor was interrupted between deciding and recording, so whether the\n" +
			"side effect took hold has to be established by inspection.\n\n" +
			"--jsonl writes one JSON object per line, which appends and streams. Use it\n" +
			"to keep a trace beyond the workspace or to feed another tool.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)

			ops, err := ledger.New(st).Operations(ctx, args[0])
			if err != nil {
				return err
			}
			if len(ops) == 0 {
				return fmt.Errorf("no trace for task %q in this workspace", args[0])
			}

			out := cmd.OutOrStdout()
			if outPath != "" {
				f, err := os.Create(outPath) //nolint:gosec // an operator-supplied destination
				if err != nil {
					return err
				}
				defer func() { _ = f.Close() }()
				out = f
			}
			if asJSONL {
				return writeJSONL(out, ops)
			}
			return writeTraceNarrative(out, args[0], ops)
		},
	}
	cmd.Flags().BoolVar(&asJSONL, "jsonl", false, "emit one JSON object per line (§18's export form)")
	cmd.Flags().StringVar(&outPath, "out", "", "write to a `file` instead of stdout")
	return cmd
}

// writeJSONL emits the export form. Encoding one row at a time means a trace
// truncated by a full disk or a killed pipe is still parseable up to the cut.
func writeJSONL(w io.Writer, ops []ledger.Operation) error {
	enc := json.NewEncoder(w)
	for _, op := range ops {
		if err := enc.Encode(op); err != nil {
			return err
		}
	}
	return nil
}

func writeTraceNarrative(w io.Writer, taskID string, ops []ledger.Operation) error {
	fmt.Fprintf(w, "Task %s: %d operations\n\n", taskID, len(ops))
	var uncertain int
	for _, op := range ops {
		started := time.UnixMilli(op.StartedAt).UTC().Format("15:04:05")
		status := "ok"
		switch {
		case op.Uncertain():
			status, uncertain = "UNCERTAIN", uncertain+1
		case op.Error != "":
			status = "failed"
		}
		fmt.Fprintf(w, "%4d  %s  %-12s  %s", op.Seq, started, op.Kind, status)
		if op.FinishedAt > op.StartedAt {
			fmt.Fprintf(w, "  (%s)", time.Duration(op.FinishedAt-op.StartedAt)*time.Millisecond)
		}
		fmt.Fprintln(w)

		if summary := summarizeJSON(op.Intent); summary != "" {
			fmt.Fprintf(w, "        intent:  %s\n", summary)
		}
		if summary := summarizeJSON(op.Outcome); summary != "" {
			fmt.Fprintf(w, "        outcome: %s\n", summary)
		}
		if op.Error != "" {
			fmt.Fprintf(w, "        error:   %s\n", op.Error)
		}
		if op.EvidenceID != "" {
			fmt.Fprintf(w, "        evidence: %s\n", op.EvidenceID)
		}
		// A candidate change is the record that this operation actually
		// altered the working tree, which is what separates "the model said it
		// edited the file" from "the file changed".
		if op.CandidateAfter != "" && op.CandidateAfter != op.CandidateBefore {
			fmt.Fprintf(w, "        content: %s -> %s\n", short(op.CandidateBefore), short(op.CandidateAfter))
		}
	}
	if uncertain > 0 {
		fmt.Fprintf(w, "\n%d operation(s) are uncertain: interrupted between the intent and the record.\n"+
			"`le task recover` classifies them by inspecting the worktree.\n", uncertain)
	}
	return nil
}

// summarizeJSON renders a recorded payload as one line.
//
// The payloads hold whole evidence packets and model responses, and a trace
// that prints those in full is one nobody reads. Keys and short values are what
// makes a line scannable; the full row is one `--jsonl` away.
func summarizeJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return truncateTrace(strings.TrimSpace(string(raw)), 120)
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		switch v := fields[k].(type) {
		case string:
			parts = append(parts, k+"="+truncateTrace(v, 60))
		case float64, bool, nil:
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		case []any:
			parts = append(parts, fmt.Sprintf("%s[%d]", k, len(v)))
		default:
			parts = append(parts, k+"{…}")
		}
	}
	return truncateTrace(strings.Join(parts, "  "), 200)
}

func truncateTrace(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
