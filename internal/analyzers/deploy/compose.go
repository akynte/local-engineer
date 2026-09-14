// Package deploy contributes the deployment, build and infrastructure rows of
// design v3 §3.2: compose and Kubernetes manifests, Dockerfiles and Makefiles.
//
// Every edge here is `declared` rather than `resolved`. That distinction is
// the point: a manifest states an intent, and whether the deployment matches
// it is a question about a running system, not about the file. An impact
// report that said "resolved" about a compose service would be claiming to
// know something it cannot.
//
// The one exception is containment within a file, which the filesystem does
// establish.
package deploy

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// composeFile is the subset of a compose file this analyzer reads. Unknown
// fields are ignored rather than rejected: a compose file carries far more
// than the graph needs, and failing on an unfamiliar key would make the
// analyzer useless on real projects.
type composeFile struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
	Volumes  map[string]any            `yaml:"volumes"`
	Networks map[string]any            `yaml:"networks"`
}

type composeService struct {
	Image       string   `yaml:"image"`
	Build       any      `yaml:"build"`
	Command     any      `yaml:"command"`
	Environment any      `yaml:"environment"`
	EnvFile     any      `yaml:"env_file"`
	DependsOn   any      `yaml:"depends_on"`
	Ports       []any    `yaml:"ports"`
	Volumes     []any    `yaml:"volumes"`
	Profiles    []string `yaml:"profiles"`
}

// isComposeFile recognises a compose file by name. Content sniffing is done
// afterwards, because plenty of YAML files are named docker-compose-ish and
// plenty of compose files are not.
func isComposeFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if !strings.HasSuffix(base, ".yml") && !strings.HasSuffix(base, ".yaml") {
		return false
	}
	return strings.HasPrefix(base, "docker-compose") || strings.HasPrefix(base, "compose")
}

// analyzeCompose emits deployment nodes for services and the edges between
// them.
func (a *Analyzer) analyzeCompose(path string, body []byte, res *index.Result) {
	var f composeFile
	if err := yaml.Unmarshal(body, &f); err != nil {
		a.warn("deploy: %s: %v", path, err)
		return
	}
	if len(f.Services) == 0 {
		return
	}

	stack := f.Name
	if stack == "" {
		stack = filepath.Base(filepath.Dir(path))
		if stack == "." || stack == "/" {
			stack = "compose"
		}
	}
	stackFQN := "deployment:" + stack + "@" + path
	res.Nodes = append(res.Nodes, graph.Node{
		Kind: graph.KindDeployment, Name: stack, FQN: stackFQN,
		Attrs: attrs(map[string]string{"kind": "compose", "file": path,
			"services": itoa(len(f.Services))}),
	})
	// The file contains the stack: that much the filesystem does establish.
	res.Edges = append(res.Edges, index.PendingEdge{
		SrcKind: graph.KindFile, SrcFQN: path,
		DstKind: graph.KindDeployment, DstFQN: stackFQN,
		Kind: graph.EdgeContains, Evidence: graph.Resolved,
	})

	names := make([]string, 0, len(f.Services))
	for name := range f.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		svc := f.Services[name]
		svcFQN := "service:" + name
		res.Nodes = append(res.Nodes, graph.Node{
			Kind: graph.KindService, Name: name, FQN: svcFQN,
			Attrs: attrs(map[string]string{
				"image": svc.Image, "declared_in": path,
				"build": buildContext(svc.Build),
			}),
		})
		res.Edges = append(res.Edges, index.PendingEdge{
			SrcKind: graph.KindDeployment, SrcFQN: stackFQN,
			DstKind: graph.KindService, DstFQN: svcFQN,
			Kind: graph.EdgeDeploys, Evidence: graph.Declared,
			Attrs: attrs(map[string]string{"source": "compose"}),
		})

		// A build context ties the service to a Dockerfile in this repository,
		// which is the link that makes "what deploys this code" answerable.
		if ctx := buildContext(svc.Build); ctx != "" {
			dockerfile := dockerfileFor(path, ctx, svc.Build)
			res.Edges = append(res.Edges, index.PendingEdge{
				SrcKind: graph.KindService, SrcFQN: svcFQN,
				DstKind: graph.KindBuildTarget, DstFQN: "dockerfile:" + dockerfile,
				Kind: graph.EdgeBuilds, Evidence: graph.Declared,
				Attrs: attrs(map[string]string{"context": ctx}),
			})
		}

		// depends_on is the stated ordering between services.
		for _, dep := range stringsOf(svc.DependsOn) {
			res.Edges = append(res.Edges, index.PendingEdge{
				SrcKind: graph.KindService, SrcFQN: svcFQN,
				DstKind: graph.KindService, DstFQN: "service:" + dep,
				Kind: graph.EdgeDependsOn, Evidence: graph.Declared,
			})
		}

		// Environment mappings are the other half of the configuration row:
		// the Go analyzer finds who reads a key, this finds who sets it.
		for key, value := range envPairs(svc.Environment) {
			res.Nodes = append(res.Nodes, graph.Node{
				Kind: graph.KindConfigKey, Name: key, FQN: "env:" + key,
				Attrs: attrs(map[string]string{"source": "environment"}),
			})
			res.Edges = append(res.Edges, index.PendingEdge{
				SrcKind: graph.KindService, SrcFQN: svcFQN,
				DstKind: graph.KindConfigKey, DstFQN: "env:" + key,
				Kind: graph.EdgeReadsConfig, Evidence: graph.Declared,
				Attrs: attrs(map[string]string{"set_in": path, "has_value": yesNo(value != "")}),
			})
		}
	}
}

// buildContext extracts the build context from compose's two spellings: a
// bare string, or a mapping with a context key.
func buildContext(build any) string {
	switch b := build.(type) {
	case string:
		return b
	case map[string]any:
		if ctx, ok := b["context"].(string); ok {
			return ctx
		}
	}
	return ""
}

// dockerfileFor resolves the Dockerfile a service builds from, relative to the
// repository root.
func dockerfileFor(composePath, ctx string, build any) string {
	name := "Dockerfile"
	if m, ok := build.(map[string]any); ok {
		if d, ok := m["dockerfile"].(string); ok && d != "" {
			name = d
		}
	}
	dir := filepath.Dir(composePath)
	return filepath.ToSlash(filepath.Clean(filepath.Join(dir, ctx, name)))
}

// envPairs normalises compose's two environment spellings: a mapping, or a
// list of KEY=VALUE strings.
func envPairs(env any) map[string]string {
	out := map[string]string{}
	switch e := env.(type) {
	case map[string]any:
		for k, v := range e {
			out[k] = fmt.Sprint(v)
		}
	case []any:
		for _, item := range e {
			s, ok := item.(string)
			if !ok {
				continue
			}
			key, value, _ := strings.Cut(s, "=")
			if key != "" {
				out[key] = value
			}
		}
	}
	return out
}

// stringsOf normalises compose's list-or-mapping spellings.
func stringsOf(v any) []string {
	var out []string
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	case map[string]any:
		for k := range t {
			out = append(out, k)
		}
	case string:
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func yesNo(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
