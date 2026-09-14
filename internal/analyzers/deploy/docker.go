package deploy

import (
	"bufio"
	"bytes"
	"path/filepath"
	"strings"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// Dockerfile parsing (design v3 §3.2: "build target to dependency | Docker
// COPY/RUN parsing").
//
// A build stage is a build target; what it COPYs from the repository is what
// it depends on. That is the edge that answers "does changing this file mean
// rebuilding this image", which is the question a build-target graph exists
// for.

func isDockerfile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == "dockerfile" || strings.HasPrefix(base, "dockerfile.") ||
		strings.HasSuffix(base, ".dockerfile")
}

// analyzeDockerfile emits a build target per stage, with its base image and
// the paths it copies in.
func (a *Analyzer) analyzeDockerfile(path string, body []byte, res *index.Result) {
	targetFQN := "dockerfile:" + path
	res.Nodes = append(res.Nodes, graph.Node{
		Kind: graph.KindBuildTarget, Name: filepath.Base(path), FQN: targetFQN,
		Attrs: attrs(map[string]string{"kind": "dockerfile", "file": path}),
	})
	res.Edges = append(res.Edges, index.PendingEdge{
		SrcKind: graph.KindFile, SrcFQN: path,
		DstKind: graph.KindBuildTarget, DstFQN: targetFQN,
		Kind: graph.EdgeContains, Evidence: graph.Resolved,
	})

	var stage string
	var stageFQN string
	seenStage := map[string]bool{}

	for _, line := range logicalLines(body) {
		instruction, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)

		switch strings.ToUpper(instruction) {
		case "FROM":
			base, name := parseFrom(rest)
			stage = name
			if stage == "" {
				stage = base
			}
			stageFQN = targetFQN + "#" + stage
			if seenStage[stageFQN] {
				continue
			}
			seenStage[stageFQN] = true

			res.Nodes = append(res.Nodes, graph.Node{
				Kind: graph.KindBuildTarget, Name: stage, FQN: stageFQN,
				Attrs: attrs(map[string]string{"stage": stage, "base": base, "file": path}),
			})
			res.Edges = append(res.Edges, index.PendingEdge{
				SrcKind: graph.KindBuildTarget, SrcFQN: targetFQN,
				DstKind: graph.KindBuildTarget, DstFQN: stageFQN,
				Kind: graph.EdgeContains, Evidence: graph.Resolved,
			})
			// A stage built FROM another stage depends on it.
			if seenStage[targetFQN+"#"+base] {
				res.Edges = append(res.Edges, index.PendingEdge{
					SrcKind: graph.KindBuildTarget, SrcFQN: stageFQN,
					DstKind: graph.KindBuildTarget, DstFQN: targetFQN + "#" + base,
					Kind: graph.EdgeDependsOn, Evidence: graph.Resolved,
				})
			} else if base != "" {
				res.Nodes = append(res.Nodes, graph.Node{
					Kind: graph.KindDependency, Name: base, FQN: "image:" + base,
					Attrs: attrs(map[string]string{"kind": "container_image"}),
				})
				res.Edges = append(res.Edges, index.PendingEdge{
					SrcKind: graph.KindBuildTarget, SrcFQN: stageFQN,
					DstKind: graph.KindDependency, DstFQN: "image:" + base,
					Kind: graph.EdgeDependsOn, Evidence: graph.Resolved,
				})
			}

		case "COPY", "ADD":
			if stageFQN == "" {
				continue
			}
			for _, src := range copySources(rest) {
				// A copy from another stage is a stage dependency, not a file.
				if stageRef := copyFromStage(rest); stageRef != "" {
					res.Edges = append(res.Edges, index.PendingEdge{
						SrcKind: graph.KindBuildTarget, SrcFQN: stageFQN,
						DstKind: graph.KindBuildTarget, DstFQN: targetFQN + "#" + stageRef,
						Kind: graph.EdgeDependsOn, Evidence: graph.Resolved,
					})
					break
				}
				rel := resolveCopy(path, src)
				if rel == "" {
					continue
				}
				// The path may be a glob or a directory; the edge is to a
				// file node, and index.writeResult drops it when no such node
				// exists. A missing edge means not discovered, which is the
				// honest outcome for a glob.
				res.Edges = append(res.Edges, index.PendingEdge{
					SrcKind: graph.KindBuildTarget, SrcFQN: stageFQN,
					DstKind: graph.KindFile, DstFQN: rel,
					Kind: graph.EdgeBuilds, Evidence: graph.Resolved,
					Attrs: attrs(map[string]string{"instruction": strings.ToUpper(instruction)}),
				})
			}

		case "ENV", "ARG":
			if stageFQN == "" {
				continue
			}
			for _, key := range envKeys(rest) {
				res.Nodes = append(res.Nodes, graph.Node{
					Kind: graph.KindConfigKey, Name: key, FQN: "env:" + key,
					Attrs: attrs(map[string]string{"source": "environment"}),
				})
				res.Edges = append(res.Edges, index.PendingEdge{
					SrcKind: graph.KindBuildTarget, SrcFQN: stageFQN,
					DstKind: graph.KindConfigKey, DstFQN: "env:" + key,
					Kind: graph.EdgeReadsConfig, Evidence: graph.Declared,
					Attrs: attrs(map[string]string{"set_in": path,
						"instruction": strings.ToUpper(instruction)}),
				})
			}
		}
	}
}

// logicalLines joins continuations and drops comments, so a multi-line RUN or
// COPY is one instruction.
func logicalLines(body []byte) []string {
	var out []string
	var current strings.Builder

	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasSuffix(line, "\\") {
			current.WriteString(strings.TrimSuffix(line, "\\"))
			current.WriteString(" ")
			continue
		}
		current.WriteString(line)
		out = append(out, current.String())
		current.Reset()
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}

// parseFrom splits "base[:tag] [AS name]".
func parseFrom(rest string) (base, name string) {
	fields := strings.Fields(rest)
	// Skip flags such as --platform=...
	for len(fields) > 0 && strings.HasPrefix(fields[0], "--") {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return "", ""
	}
	base = fields[0]
	for i := 1; i+1 < len(fields)+1; i++ {
		if i+1 < len(fields) && strings.EqualFold(fields[i], "AS") {
			name = fields[i+1]
			break
		}
	}
	return base, name
}

// copySources returns the source arguments of a COPY, dropping flags and the
// destination.
func copySources(rest string) []string {
	fields := strings.Fields(rest)
	var args []string
	for _, f := range fields {
		if strings.HasPrefix(f, "--") {
			continue
		}
		args = append(args, f)
	}
	if len(args) < 2 {
		return nil
	}
	return args[:len(args)-1] // the last argument is the destination
}

// copyFromStage returns the stage named by --from=, or "".
func copyFromStage(rest string) string {
	for _, f := range strings.Fields(rest) {
		if v, ok := strings.CutPrefix(f, "--from="); ok {
			// --from=image is an external image, not a stage; the caller only
			// uses this for stage-to-stage edges, and a name with a slash or
			// colon is an image reference.
			if strings.ContainsAny(v, "/:") {
				return ""
			}
			return v
		}
	}
	return ""
}

// resolveCopy turns a COPY source into a repository-relative path, or "" when
// it is a glob or otherwise not a single file.
func resolveCopy(dockerfilePath, src string) string {
	if src == "." || strings.ContainsAny(src, "*?[") {
		return ""
	}
	dir := filepath.Dir(dockerfilePath)
	// A Dockerfile at the repository root has dir ".", and the build context
	// is the root, so the source is already repository-relative.
	return filepath.ToSlash(filepath.Clean(filepath.Join(dir, src)))
}

// envKeys extracts the keys set by an ENV or ARG instruction, in both the
// "KEY=value" and "KEY value" spellings.
func envKeys(rest string) []string {
	var out []string
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return nil
	}
	// "ENV KEY value" — one key, the rest is its value.
	if !strings.Contains(fields[0], "=") {
		return []string{fields[0]}
	}
	for _, f := range fields {
		if key, _, ok := strings.Cut(f, "="); ok && key != "" {
			out = append(out, key)
		}
	}
	return out
}
