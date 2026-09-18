package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/akynte/local-engineer/internal/firewall"
	"github.com/akynte/local-engineer/internal/retrieval"
	"github.com/akynte/local-engineer/internal/workflow"
	"github.com/akynte/local-engineer/internal/worktree"
)

var localizationSchema = json.RawMessage(`{"type":"object","properties":{"files":{"type":"array","items":{"type":"string"}},"symbols":{"type":"array","items":{"type":"string"}},"hypothesis":{"type":"string"}},"required":["files","symbols","hypothesis"],"additionalProperties":false}`)

// localize separates structure selection, symbol selection and confirmation.
// Source bodies enter only the last call, after deterministic read checks.
func (r *Runner) localize(ctx context.Context, t *Task, wt *worktree.Worktree, s *workflow.State) error {
	access := firewall.Access{Protected: r.Policies}
	var paths []string
	err := filepath.WalkDir(wt.Path, func(full string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(wt.Path, full)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".le", "node_modules", "vendor", "dist", "target":
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || access.Check(wt.Path, rel, false) != nil {
			return nil
		}
		paths = append(paths, filepath.ToSlash(rel))
		if len(paths) > 4000 {
			return fmt.Errorf("repository structure exceeds localization budget")
		}
		return nil
	})
	if err != nil {
		return err
	}
	type selection struct {
		Files      []string `json:"files"`
		Symbols    []string `json:"symbols"`
		Hypothesis string   `json:"hypothesis"`
	}
	// §6.3's structure pass takes the repository skeleton and the files a
	// lexical search already liked, not the whole tree. Four thousand paths is
	// several thousand tokens spent proving that most of a repository is
	// irrelevant, which the model then has to read past.
	hits := r.lexicalHits(ctx, wt.Path, t.Title)
	evidence := map[string]any{"files": paths}
	if len(hits.Files) > 0 {
		evidence["lexical_hits"] = hits
	}

	var chosen selection
	// The ranked repository map is P2 of the frozen prefix and is already in
	// front of the model. Repeating it here would spend the structure pass's
	// whole log budget restating what the prefix says.
	if err := r.decide(ctx, t, s, "Select relevant files from repository structure. Return a preliminary hypothesis and symbol names if known.", evidence, localizationSchema, &chosen); err != nil {
		return err
	}
	packet, err := r.Retriever.Build(ctx, retrieval.Request{Root: wt.Path, Symbols: chosen.Symbols, TokenBudget: 6000})
	if err != nil {
		return err
	}
	for i := range packet.Slices {
		packet.Slices[i].Body = ""
	}
	skeleton, err := r.Retriever.Skeleton(ctx, chosen.Files)
	if err != nil {
		return err
	}
	if err := r.decide(ctx, t, s, "Refine the file and symbol selection using signatures. Do not claim confirmation yet.", map[string]any{"selection": chosen, "skeleton": skeleton, "symbols": packet}, localizationSchema, &chosen); err != nil {
		return err
	}
	if len(chosen.Files) == 0 || len(chosen.Files) > 12 {
		return fmt.Errorf("localization requires 1–12 files")
	}
	skeleton, err = r.Retriever.Skeleton(ctx, chosen.Files)
	if err != nil {
		return err
	}
	bodies := map[string]string{}
	total := 0
	for _, file := range chosen.Files {
		if err := access.Check(wt.Path, file, false); err != nil {
			return err
		}
		full, err := worktree.Resolve(wt.Path, file)
		if err != nil {
			return err
		}
		info, err := os.Stat(full)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 2<<20 {
			return fmt.Errorf("localization body budget exceeded for %s", file)
		}
		body, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		selected := localizationBody(file, string(body), chosen.Symbols, skeleton)
		if total+len(selected) > 36000 {
			return fmt.Errorf("localization evidence exceeds 12K token budget; narrow the selected symbols")
		}
		bodies[file] = selected
		total += len(selected)
	}
	if err := r.decide(ctx, t, s, "Confirm the root cause against source bodies. Identify the files and symbols the plan must address.", map[string]any{"selection": chosen, "bodies": bodies, "failures": s.Feedback}, localizationSchema, &chosen); err != nil {
		return err
	}
	if strings.TrimSpace(chosen.Hypothesis) == "" || len(chosen.Files) == 0 || len(chosen.Symbols) == 0 {
		return fmt.Errorf("localization did not confirm a hypothesis, files and symbols")
	}
	s.Files, s.Symbols, s.Hypothesis = chosen.Files, chosen.Symbols, chosen.Hypothesis
	s.Bodies = bodies
	return nil
}

func localizationBody(file, body string, symbols []string, skeleton []retrieval.Slice) string {
	lines := strings.Split(body, "\n")
	selected := make([]bool, len(lines))
	matched := false
	for _, decl := range skeleton {
		if decl.Path != file {
			continue
		}
		wanted := false
		for _, name := range symbols {
			wanted = wanted || name == decl.Symbol
		}
		if !wanted || decl.StartLine < 1 || decl.EndLine < decl.StartLine {
			continue
		}
		matched = true
		start := max(0, decl.StartLine-4)
		end := min(len(lines), decl.EndLine+3)
		for i := start; i < end; i++ {
			selected[i] = true
		}
	}
	if !matched {
		for i := 0; i < min(80, len(lines)); i++ {
			selected[i] = true
		}
	}
	var out strings.Builder
	gap := false
	for i, line := range lines {
		if !selected[i] {
			gap = true
			continue
		}
		if gap {
			out.WriteString("[unselected lines omitted]\n")
			gap = false
		}
		fmt.Fprintf(&out, "%d: %s\n", i+1, line)
	}
	if gap {
		out.WriteString("[unselected lines omitted]\n")
	}
	return out.String()
}

// lexicalHits is §6.2's concept route: the task text expanded into the
// spellings source actually uses, searched live against the working tree.
//
// It is best-effort by design. ripgrep is the cheapest layer and the only one
// that sees uncommitted edits, but it is not required: a machine without it, or
// a search that outran its budget, falls back to the index and the repository
// map, which is a worse ranking rather than a failed phase. What is never done
// is reporting a truncated search as a complete one.
func (r *Runner) lexicalHits(ctx context.Context, root, query string) lexicalReport {
	var report lexicalReport
	seen := map[string]int{}
	for _, term := range retrieval.ExpandTerms(query) {
		res, err := retrieval.Grep(ctx, root, term, true)
		if err != nil {
			if errors.Is(err, retrieval.ErrRipgrepMissing) {
				return lexicalReport{}
			}
			r.logf("lexical search for %q: %v", term, err)
			continue
		}
		report.Truncated = report.Truncated || res.Truncated
		for _, hit := range res.Hits {
			seen[hit.Path]++
		}
	}
	for path, count := range seen {
		report.Files = append(report.Files, lexicalFile{Path: path, Matches: count})
	}
	// A file matching several of the expanded spellings is a better candidate
	// than one matching a single term many times, so the count of distinct
	// matching lines orders them and the path breaks ties deterministically.
	sort.Slice(report.Files, func(i, j int) bool {
		if report.Files[i].Matches != report.Files[j].Matches {
			return report.Files[i].Matches > report.Files[j].Matches
		}
		return report.Files[i].Path < report.Files[j].Path
	})
	if len(report.Files) > 20 {
		report.Files = report.Files[:20]
		report.Truncated = true
	}
	return report
}

// lexicalReport is what the structure pass is told about the live search.
type lexicalReport struct {
	Files []lexicalFile `json:"files"`
	// Truncated says the search was cut short, so an absent file is not
	// evidence that nothing matched there.
	Truncated bool `json:"truncated,omitempty"`
}

type lexicalFile struct {
	Path    string `json:"path"`
	Matches int    `json:"matches"`
}
