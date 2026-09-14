package deploy

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// Kubernetes and Helm manifests (design v3 §3.2: "deployment component to
// service | compose, Swarm stack, Helm and Kubernetes manifests | declared").
//
// A Helm template is not valid YAML — it is Go template syntax that produces
// YAML — so it is recognised and reported as present rather than parsed.
// Rendering a chart would mean running Helm with values this analyzer does not
// have, and a half-rendered template parsed as YAML produces confident
// nonsense.

// k8sObject is the subset of a manifest this analyzer reads.
type k8sObject struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name      string            `yaml:"name"`
		Namespace string            `yaml:"namespace"`
		Labels    map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Spec struct {
		Template struct {
			Spec podSpec `yaml:"spec"`
		} `yaml:"template"`
		// A bare Pod has its containers directly on spec.
		Containers []container `yaml:"containers"`
		Selector   any         `yaml:"selector"`
	} `yaml:"spec"`
	Data map[string]string `yaml:"data"`
}

type podSpec struct {
	Containers []container `yaml:"containers"`
}

type container struct {
	Name  string `yaml:"name"`
	Image string `yaml:"image"`
	Env   []struct {
		Name      string `yaml:"name"`
		Value     string `yaml:"value"`
		ValueFrom any    `yaml:"valueFrom"`
	} `yaml:"env"`
	EnvFrom []any `yaml:"envFrom"`
}

// workloadKinds are the object kinds that actually run something. A Service or
// an Ingress is a manifest too, but it does not deploy code.
var workloadKinds = map[string]bool{
	"Deployment": true, "StatefulSet": true, "DaemonSet": true,
	"Job": true, "CronJob": true, "Pod": true, "ReplicaSet": true,
}

// isHelmTemplate recognises a chart template by its path and its syntax.
func isHelmTemplate(path string, body []byte) bool {
	if strings.Contains(filepath.ToSlash(path), "/templates/") {
		return true
	}
	return bytes.Contains(body, []byte("{{")) && bytes.Contains(body, []byte("}}"))
}

// analyzeKubernetes emits deployment nodes for workloads and the services and
// configuration they reference.
func (a *Analyzer) analyzeKubernetes(path string, body []byte, res *index.Result) {
	if isHelmTemplate(path, body) {
		// Recorded as present but not parsed. Rendering needs values this
		// analyzer does not have, and a half-rendered template read as YAML
		// produces confident nonsense.
		res.Nodes = append(res.Nodes, graph.Node{
			Kind: graph.KindDeployment, Name: filepath.Base(path), FQN: "helm:" + path,
			Attrs: attrs(map[string]string{
				"kind": "helm_template", "file": path,
				"note": "not parsed: rendering requires chart values",
			}),
		})
		res.Edges = append(res.Edges, index.PendingEdge{
			SrcKind: graph.KindFile, SrcFQN: path,
			DstKind: graph.KindDeployment, DstFQN: "helm:" + path,
			Kind: graph.EdgeContains, Evidence: graph.Resolved,
		})
		return
	}

	// A manifest file may hold several documents.
	for i, doc := range splitYAMLDocuments(body) {
		var obj k8sObject
		if err := yaml.Unmarshal(doc, &obj); err != nil {
			continue // one bad document must not lose the others
		}
		if obj.Kind == "" || obj.Metadata.Name == "" {
			continue
		}
		a.emitK8sObject(path, i, obj, res)
	}
}

func (a *Analyzer) emitK8sObject(path string, docIndex int, obj k8sObject, res *index.Result) {
	name := obj.Metadata.Name
	if obj.Metadata.Namespace != "" {
		name = obj.Metadata.Namespace + "/" + name
	}
	fqn := "k8s:" + strings.ToLower(obj.Kind) + ":" + name

	switch {
	case workloadKinds[obj.Kind]:
		res.Nodes = append(res.Nodes, graph.Node{
			Kind: graph.KindDeployment, Name: name, FQN: fqn,
			Attrs: attrs(map[string]string{
				"kind": obj.Kind, "api_version": obj.APIVersion, "file": path,
				"document": itoa(docIndex),
			}),
		})
		res.Edges = append(res.Edges, index.PendingEdge{
			SrcKind: graph.KindFile, SrcFQN: path,
			DstKind: graph.KindDeployment, DstFQN: fqn,
			Kind: graph.EdgeContains, Evidence: graph.Resolved,
		})

		containers := obj.Spec.Template.Spec.Containers
		if len(containers) == 0 {
			containers = obj.Spec.Containers
		}
		for _, c := range containers {
			svcFQN := "service:" + c.Name
			res.Nodes = append(res.Nodes, graph.Node{
				Kind: graph.KindService, Name: c.Name, FQN: svcFQN,
				Attrs: attrs(map[string]string{"image": c.Image, "declared_in": path}),
			})
			res.Edges = append(res.Edges, index.PendingEdge{
				SrcKind: graph.KindDeployment, SrcFQN: fqn,
				DstKind: graph.KindService, DstFQN: svcFQN,
				Kind: graph.EdgeDeploys, Evidence: graph.Declared,
				Attrs: attrs(map[string]string{"source": "kubernetes"}),
			})
			for _, env := range c.Env {
				if env.Name == "" {
					continue
				}
				res.Nodes = append(res.Nodes, graph.Node{
					Kind: graph.KindConfigKey, Name: env.Name, FQN: "env:" + env.Name,
					Attrs: attrs(map[string]string{"source": "environment"}),
				})
				source := "literal"
				if env.ValueFrom != nil {
					source = "valueFrom"
				}
				res.Edges = append(res.Edges, index.PendingEdge{
					SrcKind: graph.KindService, SrcFQN: svcFQN,
					DstKind: graph.KindConfigKey, DstFQN: "env:" + env.Name,
					Kind: graph.EdgeReadsConfig, Evidence: graph.Declared,
					Attrs: attrs(map[string]string{"set_in": path, "value_source": source}),
				})
			}
		}

	case obj.Kind == "ConfigMap" || obj.Kind == "Secret":
		// A ConfigMap's keys are configuration keys. A Secret's key names are
		// recorded; its values are not read, and nothing here would store them
		// if they were.
		res.Nodes = append(res.Nodes, graph.Node{
			Kind: graph.KindInfraResource, Name: name, FQN: fqn,
			Attrs: attrs(map[string]string{"kind": obj.Kind, "file": path}),
		})
		res.Edges = append(res.Edges, index.PendingEdge{
			SrcKind: graph.KindFile, SrcFQN: path,
			DstKind: graph.KindInfraResource, DstFQN: fqn,
			Kind: graph.EdgeContains, Evidence: graph.Resolved,
		})
		for key := range obj.Data {
			res.Nodes = append(res.Nodes, graph.Node{
				Kind: graph.KindConfigKey, Name: key, FQN: "env:" + key,
				Attrs: attrs(map[string]string{"source": strings.ToLower(obj.Kind)}),
			})
			res.Edges = append(res.Edges, index.PendingEdge{
				SrcKind: graph.KindInfraResource, SrcFQN: fqn,
				DstKind: graph.KindConfigKey, DstFQN: "env:" + key,
				Kind: graph.EdgeProvisions, Evidence: graph.Declared,
				Attrs: attrs(map[string]string{"set_in": path}),
			})
		}

	default:
		// Services, Ingresses, PVCs and the rest: infrastructure the workloads
		// sit on, recorded so a rename has consumers.
		res.Nodes = append(res.Nodes, graph.Node{
			Kind: graph.KindInfraResource, Name: name, FQN: fqn,
			Attrs: attrs(map[string]string{"kind": obj.Kind, "file": path}),
		})
		res.Edges = append(res.Edges, index.PendingEdge{
			SrcKind: graph.KindFile, SrcFQN: path,
			DstKind: graph.KindInfraResource, DstFQN: fqn,
			Kind: graph.EdgeContains, Evidence: graph.Resolved,
		})
	}
}

// splitYAMLDocuments splits a multi-document file on its separators.
func splitYAMLDocuments(body []byte) [][]byte {
	parts := bytes.Split(body, []byte("\n---"))
	out := make([][]byte, 0, len(parts))
	for _, p := range parts {
		if len(bytes.TrimSpace(p)) > 0 {
			out = append(out, p)
		}
	}
	return out
}

// looksLikeKubernetes reports whether a YAML file carries the two fields every
// manifest has.
func looksLikeKubernetes(body []byte) bool {
	return bytes.Contains(body, []byte("apiVersion:")) && bytes.Contains(body, []byte("kind:"))
}

func itoa(n int) string { return fmt.Sprint(n) }
