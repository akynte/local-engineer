package deploy

import (
	"bufio"
	"bytes"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// Makefile parsing (design v3 §3.2: "build target to dependency | Makefile
// targets | resolved plus inferred").
//
// Targets and their prerequisites are structure the file states outright, so
// they are `resolved`. What a recipe *does* is a shell script this analyzer
// does not execute, so anything derived from a recipe line is `inferred` and
// says so.

func isMakefile(path string) bool {
	base := filepath.Base(path)
	return base == "Makefile" || base == "makefile" || base == "GNUmakefile" ||
		strings.HasSuffix(base, ".mk")
}

// targetLine matches "name: prereqs" but not a variable assignment such as
// "VAR := value", which shares the colon.
var targetLine = regexp.MustCompile(`^([A-Za-z0-9_./%$(){}-]+)\s*:([^=].*)?$`)

// analyzeMakefile emits a build target per rule, with its prerequisites.
func (a *Analyzer) analyzeMakefile(path string, body []byte, res *index.Result) {
	fileFQN := "makefile:" + path
	res.Nodes = append(res.Nodes, graph.Node{
		Kind: graph.KindBuildTarget, Name: filepath.Base(path), FQN: fileFQN,
		Attrs: attrs(map[string]string{"kind": "makefile", "file": path}),
	})
	res.Edges = append(res.Edges, index.PendingEdge{
		SrcKind: graph.KindFile, SrcFQN: path,
		DstKind: graph.KindBuildTarget, DstFQN: fileFQN,
		Kind: graph.EdgeContains, Evidence: graph.Resolved,
	})

	declared := map[string]bool{}
	var pending []index.PendingEdge

	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		// A recipe line begins with a tab. The analyzer does not execute
		// recipes, so their contents produce nothing.
		if strings.HasPrefix(line, "\t") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		m := targetLine.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		name := m[1]
		// .PHONY and friends are directives, not targets.
		if strings.HasPrefix(name, ".") {
			continue
		}
		targetFQN := fileFQN + "#" + name
		if !declared[targetFQN] {
			declared[targetFQN] = true
			res.Nodes = append(res.Nodes, graph.Node{
				Kind: graph.KindBuildTarget, Name: name, FQN: targetFQN,
				Attrs: attrs(map[string]string{"file": path, "target": name}),
			})
			res.Edges = append(res.Edges, index.PendingEdge{
				SrcKind: graph.KindBuildTarget, SrcFQN: fileFQN,
				DstKind: graph.KindBuildTarget, DstFQN: targetFQN,
				Kind: graph.EdgeContains, Evidence: graph.Resolved,
			})
		}

		for _, prereq := range strings.Fields(m[2]) {
			// A variable reference resolves to something this analyzer cannot
			// know without evaluating the Makefile.
			if strings.ContainsAny(prereq, "$%") {
				continue
			}
			pending = append(pending, index.PendingEdge{
				SrcKind: graph.KindBuildTarget, SrcFQN: targetFQN,
				DstKind: graph.KindBuildTarget, DstFQN: fileFQN + "#" + prereq,
				Kind: graph.EdgeDependsOn, Evidence: graph.Resolved,
			})
			// A prerequisite may also be a file rather than a target. Both
			// edges are emitted; the one whose node does not exist is dropped
			// when the result is written, which is the honest outcome.
			pending = append(pending, index.PendingEdge{
				SrcKind: graph.KindBuildTarget, SrcFQN: targetFQN,
				DstKind: graph.KindFile, DstFQN: filepath.ToSlash(
					filepath.Clean(filepath.Join(filepath.Dir(path), prereq))),
				Kind: graph.EdgeBuilds, Evidence: graph.Inferred,
				Attrs: attrs(map[string]string{
					"assumption": "a prerequisite that is not a declared target is treated as a file",
				}),
			})
		}
	}
	res.Edges = append(res.Edges, pending...)
}
