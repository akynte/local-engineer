package native

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/retrieval"
	"github.com/akynte/local-engineer/internal/worktree"
)

// Result is what a tool returns to the model.
//
// A failed tool call is not an error that aborts the step: it is a *result*
// the model can act on. Telling a model "that file does not exist, here are
// the ones that do" corrects it; throwing away the turn does not.
type Result struct {
	Content string
	// Failed marks a call the model got wrong, for telemetry. The content
	// still goes back either way.
	Failed bool
	// Edited reports that the worktree changed, so the caller can recompute
	// the candidate.
	Edited bool
	// Done reports that the model called the done tool.
	Done bool
	// Summary carries the done tool's summary.
	Summary string
}

func failed(format string, args ...any) Result {
	return Result{Content: fmt.Sprintf(format, args...), Failed: true}
}

// MaxReadLines bounds a single read. §8.2's progressive disclosure: a model
// that reads a 3000-line file has spent its packet on one file.
const MaxReadLines = 400

// MaxToolOutput bounds any tool's reply, so one call cannot consume the window.
const MaxToolOutput = 8000

// Exec runs one tool call against the worktree.
func (e *Engine) Exec(ctx context.Context, wt string, call llmToolCall) Result {
	args := map[string]any{}
	if len(call.Arguments) > 0 {
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return failed("Your arguments were not valid JSON (%v). Send the arguments as a JSON object matching the tool's schema.", err)
		}
	}

	switch call.Name {
	case ToolReadFile:
		return e.readFile(wt, args)
	case ToolWriteFile:
		return e.writeFile(wt, args)
	case ToolEditFile:
		return e.editFile(wt, args)
	case ToolListFiles:
		return e.listFiles(wt, args)
	case ToolSearch:
		return e.search(ctx, args)
	case ToolFindSymbol:
		return e.findSymbol(ctx, args)
	case ToolImpact:
		return e.impact(ctx, args)
	case ToolRunRecipe:
		return e.runRecipe(ctx, wt, args)
	case ToolDone:
		return Result{Done: true, Summary: str(args, "summary"), Content: "Recorded."}
	}
	return failed("There is no tool named %q. Available tools: %s.",
		call.Name, strings.Join(ToolNames(e.tools), ", "))
}

func (e *Engine) readFile(wt string, args map[string]any) Result {
	rel := str(args, "path")
	body, err := worktree.ReadWithin(wt, rel)
	if err != nil {
		if errors.Is(err, worktree.ErrOutside) {
			return failed("%v", err)
		}
		if os.IsNotExist(err) {
			return failed("%s does not exist. Use list_files to see what is there.", rel)
		}
		return failed("could not read %s: %v", rel, err)
	}

	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	start, end := num(args, "start_line"), num(args, "end_line")
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if start > len(lines) {
		return failed("%s has %d lines; start_line %d is past the end.", rel, len(lines), start)
	}
	truncated := false
	if end-start+1 > MaxReadLines {
		end = start + MaxReadLines - 1
		truncated = true
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s lines %d-%d of %d:\n", rel, start, end, len(lines))
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i, lines[i-1])
	}
	if truncated {
		fmt.Fprintf(&b, "\n(stopped at %d lines; read the next range if you need more)\n", MaxReadLines)
	}
	return Result{Content: cap(b.String())}
}

func (e *Engine) writeFile(wt string, args map[string]any) Result {
	rel, content := str(args, "path"), str(args, "content")
	if _, err := worktree.StatWithin(wt, rel); err == nil {
		// Overwriting a file wholesale discards code the model may never have
		// read. Steering it to edit_file is a correction, not an obstruction.
		return failed("%s already exists. Use edit_file to change part of it; "+
			"write_file replaces the whole file and would discard anything you have not read.", rel)
	}
	if err := worktree.WriteWithin(wt, rel, []byte(content)); err != nil {
		return failed("%v", err)
	}
	return Result{Content: fmt.Sprintf("Wrote %s (%d bytes).", rel, len(content)), Edited: true}
}

func (e *Engine) editFile(wt string, args map[string]any) Result {
	rel, oldText, newText := str(args, "path"), str(args, "old"), str(args, "new")
	body, err := worktree.ReadWithin(wt, rel)
	if err != nil {
		if errors.Is(err, worktree.ErrOutside) {
			return failed("%v", err)
		}
		if os.IsNotExist(err) {
			return failed("%s does not exist. Use write_file to create it.", rel)
		}
		return failed("could not read %s: %v", rel, err)
	}
	if oldText == "" {
		return failed("The old string is empty. Give the exact text to replace.")
	}
	text := string(body)
	switch n := strings.Count(text, oldText); {
	case n == 0:
		return failed("That exact text does not appear in %s. Read the file first: "+
			"whitespace and indentation must match exactly.", rel)
	case n > 1:
		// Replacing an ambiguous match is how an edit silently changes the
		// wrong line.
		return failed("That text appears %d times in %s. Include more surrounding "+
			"context so it identifies exactly one place.", n, rel)
	}
	if err := worktree.WriteWithin(wt, rel, []byte(strings.Replace(text, oldText, newText, 1))); err != nil {
		return failed("%v", err)
	}
	return Result{Content: fmt.Sprintf("Edited %s.", rel), Edited: true}
}

func (e *Engine) listFiles(wt string, args map[string]any) Result {
	rel := str(args, "dir")
	if rel == "" {
		rel = "."
	}
	entries, err := worktree.ListWithin(wt, rel)
	if err != nil {
		if errors.Is(err, worktree.ErrOutside) {
			return failed("%v", err)
		}
		return failed("could not list %s: %v", rel, err)
	}
	var dirs, files []string
	for _, entry := range entries {
		name := entry.Name()
		if name == ".git" || name == ".le" || name == "node_modules" {
			continue
		}
		if entry.IsDir() {
			dirs = append(dirs, name+"/")
			continue
		}
		files = append(files, name)
	}
	sort.Strings(dirs)
	sort.Strings(files)

	var b strings.Builder
	fmt.Fprintf(&b, "%s:\n", rel)
	for _, d := range append(dirs, files...) {
		fmt.Fprintf(&b, "  %s\n", d)
	}
	return Result{Content: cap(b.String())}
}

func (e *Engine) search(ctx context.Context, args map[string]any) Result {
	if e.Retriever == nil {
		return failed("Search is unavailable: the repository is not indexed.")
	}
	limit := num(args, "limit")
	if limit <= 0 || limit > 25 {
		limit = 10
	}
	pkt, err := e.Retriever.Build(ctx, retrieval.Request{Query: str(args, "query"), MaxAnchors: limit})
	if err != nil {
		return failed("search failed: %v", err)
	}
	if len(pkt.Slices) == 0 {
		return Result{Content: "No matches. Try a different term, or find_symbol for a known name."}
	}
	var b strings.Builder
	for _, s := range pkt.Slices {
		fmt.Fprintf(&b, "%s:%d-%d\n", s.Path, s.StartLine, s.EndLine)
		if body := strings.TrimSpace(s.Body); body != "" {
			fmt.Fprintf(&b, "  %s\n", firstLine(body))
		}
	}
	return Result{Content: cap(b.String())}
}

func (e *Engine) findSymbol(ctx context.Context, args map[string]any) Result {
	if e.Graph == nil {
		return failed("Symbol lookup is unavailable: the repository is not indexed.")
	}
	name := str(args, "name")
	nodes, err := e.Graph.NodesByName(ctx, name, nil, 20)
	if err != nil {
		return failed("lookup failed: %v", err)
	}
	if len(nodes) == 0 {
		return Result{Content: fmt.Sprintf("No indexed symbol named %q. Try search_code.", name)}
	}
	var b strings.Builder
	for _, n := range nodes {
		fmt.Fprintf(&b, "%s %s\n  %s:%d\n", n.Kind, n.FQN, n.Path, n.StartLine)
		if n.Signature != "" {
			fmt.Fprintf(&b, "  %s\n", n.Signature)
		}
	}
	return Result{Content: cap(b.String())}
}

func (e *Engine) impact(ctx context.Context, args map[string]any) Result {
	if e.Graph == nil {
		return failed("Impact analysis is unavailable: the repository is not indexed.")
	}
	kind, err := graph.ParseChangeKind(str(args, "change"))
	if err != nil {
		return failed("%v", err)
	}
	name := str(args, "symbol")
	nodes, err := e.Graph.NodesByName(ctx, name, nil, 20)
	if err != nil {
		return failed("lookup failed: %v", err)
	}
	if len(nodes) == 0 {
		return Result{Content: fmt.Sprintf("No indexed symbol named %q.", name)}
	}
	ids := make([]int64, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	imp, err := e.Graph.ImpactOf(ctx, ids, kind)
	if err != nil {
		return failed("impact analysis failed: %v", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", imp.Summary())
	shown := imp.Consumers
	if len(shown) > 25 {
		shown = shown[:25]
	}
	for _, c := range shown {
		fmt.Fprintf(&b, "%-12s %-10s %s\n", c.Verdict, c.Evidence, c.Node.FQN)
		if c.Migration != "" {
			fmt.Fprintf(&b, "    %s\n", c.Migration)
		}
	}
	fmt.Fprintf(&b, "\n%s\n", graph.ImpactCaveat)
	return Result{Content: cap(b.String())}
}

func (e *Engine) runRecipe(ctx context.Context, wt string, args map[string]any) Result {
	if e.Recipes == nil {
		return failed("Verification is unavailable in this step.")
	}
	want := recipe.Kind(str(args, "kind"))
	for _, r := range recipe.GoRecipes(recipe.High) {
		if r.Kind != want {
			continue
		}
		res := e.Recipes.Run(ctx, r, wt, "")
		return Result{Content: cap(describe(res)), Failed: res.Status == recipe.Fail}
	}
	return failed("No %q verification is available here. Try build, vet or test.", want)
}

// describe renders a recipe result for the model: the headline, the findings
// with their locations, and nothing else. The full output stays in the
// artifact store (§8.2, compression at source).
func describe(res recipe.Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s — %s\n", res.Recipe, res.Status, res.Summary.Headline)
	if res.Err != "" {
		fmt.Fprintf(&b, "%s\n", res.Err)
	}
	for _, f := range res.Summary.Findings {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		if f.Test != "" {
			loc = strings.TrimSpace(loc + " " + f.Test)
		}
		fmt.Fprintf(&b, "  %s %s\n", loc, f.Message)
	}
	if res.Summary.Truncated {
		b.WriteString("  (more findings omitted)\n")
	}
	return b.String()
}

func firstLine(s string) string {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			return line
		}
	}
	return ""
}

// cap bounds a tool reply so one call cannot consume the context window.
func cap(s string) string {
	if len(s) <= MaxToolOutput {
		return s
	}
	return s[:MaxToolOutput] + "\n… (output truncated)"
}

func str(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func num(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
