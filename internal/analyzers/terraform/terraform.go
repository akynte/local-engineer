// Package terraform contributes the infrastructure row of design v3 §3.2:
// "infrastructure resource to application component | Terraform (HCL parser)
// and manifests linked by names and env references | declared plus inferred".
//
// Two evidence categories appear here, and the distinction is the point.
//
// What the HCL says outright — a resource exists, a module is called, one
// resource interpolates another — is `declared`: the file states it, and
// whether the infrastructure matches is a question about a cloud account, not
// about the file.
//
// What links infrastructure to *application* code is weaker still. A
// Terraform resource setting an environment variable the code reads is a real
// relationship, but it is established by two files agreeing on a string. That
// is `inferred`, with the assumption recorded.
package terraform

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// Analyzer reads Terraform configuration.
type Analyzer struct {
	MaxFileBytes int64
	Warnf        func(format string, args ...any)
}

// New returns an analyzer with the shipped defaults.
func New() *Analyzer { return &Analyzer{MaxFileBytes: 2 << 20} }

func (a *Analyzer) Name() string { return "terraform" }

func (a *Analyzer) Handles(f index.File) bool {
	base := filepath.Base(f.Path)
	// .tfvars hold values, not structure, and .tf.json is the generated form.
	return strings.HasSuffix(base, ".tf") && !strings.HasSuffix(base, ".tfvars")
}

func (a *Analyzer) warn(format string, args ...any) {
	if a.Warnf != nil {
		a.Warnf(format, args...)
	}
}

// resourceFQN namespaces a resource so it cannot collide with anything else.
func resourceFQN(kind, typ, name string) string {
	if typ == "" {
		return "tf:" + kind + "." + name
	}
	return "tf:" + kind + "." + typ + "." + name
}

// Analyze parses every Terraform file and emits the resources it declares.
func (a *Analyzer) Analyze(ctx context.Context, repoRoot string, files []index.File) (index.Result, error) {
	var res index.Result

	sorted := append([]index.File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	// declared collects every resource address so references can be resolved
	// to real nodes rather than to names that may not exist.
	declared := map[string]bool{}
	type pendingRef struct{ from, to, path string }
	var refs []pendingRef
	// envKeys maps a config key to the resources that set it.
	envKeys := map[string][]string{}

	for _, f := range sorted {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if a.MaxFileBytes > 0 && f.Size > a.MaxFileBytes {
			continue
		}
		abs := f.AbsPath
		if abs == "" {
			abs = filepath.Join(repoRoot, filepath.FromSlash(f.Path))
		}
		body, err := os.ReadFile(abs) //nolint:gosec // a path the indexer walked inside the repository
		if err != nil {
			a.warn("terraform: %s: %v", f.Path, err)
			continue
		}

		parser := hclparse.NewParser()
		file, diags := parser.ParseHCL(body, f.Path)
		if diags.HasErrors() {
			// A file mid-edit is normal. Parse what is valid and say what was
			// not, rather than losing the whole directory.
			a.warn("terraform: %s: %s", f.Path, diags.Error())
		}
		if file == nil {
			continue
		}
		syntaxBody, ok := file.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}

		for _, block := range syntaxBody.Blocks {
			node, addr := a.emitBlock(f.Path, block, &res)
			if addr == "" {
				continue
			}
			declared[addr] = true

			// References inside the block body are dependencies between
			// resources: the file states them, so they are declared.
			for _, target := range referencedAddresses(block) {
				if target != addr {
					refs = append(refs, pendingRef{from: addr, to: target, path: f.Path})
				}
			}
			for _, key := range environmentKeys(block) {
				envKeys[key] = append(envKeys[key], addr)
			}
			_ = node
		}
	}

	for _, r := range refs {
		if !declared[r.to] {
			// A reference to something declared elsewhere — another module,
			// another repository. A missing edge means not discovered.
			continue
		}
		res.Edges = append(res.Edges, index.PendingEdge{
			SrcKind: graph.KindInfraResource, SrcFQN: r.from,
			DstKind: graph.KindInfraResource, DstFQN: r.to,
			Kind: graph.EdgeDependsOn, Evidence: graph.Declared,
			Attrs: attrs(map[string]string{"declared_in": r.path}),
		})
	}

	// The link to application code: a resource setting an environment variable
	// the code reads. Two files agreeing on a string is weaker than either
	// file's own contents, so this is inferred and says why.
	keys := make([]string, 0, len(envKeys))
	for k := range envKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		res.Nodes = append(res.Nodes, graph.Node{
			Kind: graph.KindConfigKey, Name: key, FQN: "env:" + key,
			Attrs: attrs(map[string]string{"source": "terraform"}),
		})
		for _, from := range envKeys[key] {
			res.Edges = append(res.Edges, index.PendingEdge{
				SrcKind: graph.KindInfraResource, SrcFQN: from,
				DstKind: graph.KindConfigKey, DstFQN: "env:" + key,
				Kind: graph.EdgeProvisions, Evidence: graph.Inferred,
				Attrs: attrs(map[string]string{
					"assumption": "the infrastructure sets an environment variable of this name; " +
						"whether the application reads it is established by the name alone",
				}),
			})
		}
	}
	return res, nil
}

// emitBlock produces a node for one top-level block and returns its address.
func (a *Analyzer) emitBlock(path string, block *hclsyntax.Block, res *index.Result) (graph.Node, string) {
	var typ, name, addr string

	switch block.Type {
	case "resource", "data":
		if len(block.Labels) < 2 {
			return graph.Node{}, ""
		}
		typ, name = block.Labels[0], block.Labels[1]
		addr = resourceFQN(block.Type, typ, name)
	case "module", "variable", "output", "provider", "locals":
		if len(block.Labels) < 1 {
			if block.Type != "locals" {
				return graph.Node{}, ""
			}
			name = "locals"
		} else {
			name = block.Labels[0]
		}
		addr = resourceFQN(block.Type, "", name)
	default:
		// terraform {}, backend blocks and the rest: configuration of the tool
		// rather than of infrastructure.
		return graph.Node{}, ""
	}

	display := name
	if typ != "" {
		display = typ + "." + name
	}
	node := graph.Node{
		Kind: graph.KindInfraResource, Name: display, FQN: addr,
		Attrs: attrs(map[string]string{
			"block": block.Type, "type": typ, "file": path,
			"line": fmt.Sprint(block.TypeRange.Start.Line),
		}),
		StartLine: block.TypeRange.Start.Line,
	}
	res.Nodes = append(res.Nodes, node)
	res.Edges = append(res.Edges, index.PendingEdge{
		SrcKind: graph.KindFile, SrcFQN: path,
		DstKind: graph.KindInfraResource, DstFQN: addr,
		Kind: graph.EdgeContains, Evidence: graph.Resolved,
	})
	return node, addr
}

// referencedAddresses walks a block's expressions for references to other
// resources, returning their addresses.
func referencedAddresses(block *hclsyntax.Block) []string {
	seen := map[string]bool{}
	var out []string

	var walkBody func(*hclsyntax.Body)
	walkBody = func(b *hclsyntax.Body) {
		for _, attr := range b.Attributes {
			for _, traversal := range attr.Expr.Variables() {
				if addr := addressOf(traversal); addr != "" && !seen[addr] {
					seen[addr] = true
					out = append(out, addr)
				}
			}
		}
		for _, nested := range b.Blocks {
			walkBody(nested.Body)
		}
	}
	walkBody(block.Body)
	sort.Strings(out)
	return out
}

// addressOf turns a traversal such as aws_db_instance.main.endpoint into the
// address of the resource it names.
func addressOf(t hcl.Traversal) string {
	parts := make([]string, 0, 3)
	for _, step := range t {
		switch s := step.(type) {
		case hcl.TraverseRoot:
			parts = append(parts, s.Name)
		case hcl.TraverseAttr:
			parts = append(parts, s.Name)
		default:
			// An index or splat: everything after it is not part of an address.
		}
		if len(parts) == 3 {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	switch parts[0] {
	case "var":
		if len(parts) >= 2 {
			return resourceFQN("variable", "", parts[1])
		}
	case "module":
		if len(parts) >= 2 {
			return resourceFQN("module", "", parts[1])
		}
	case "data":
		if len(parts) >= 3 {
			return resourceFQN("data", parts[1], parts[2])
		}
	case "local", "each", "count", "path", "terraform", "self":
		return "" // not resource references
	default:
		// A bare resource reference: type.name.attribute
		if len(parts) >= 2 {
			return resourceFQN("resource", parts[0], parts[1])
		}
	}
	return ""
}

// environmentKeys finds environment variable names a block sets. Terraform has
// no single spelling for this, so the two common shapes are recognised: an
// `environment` block with `name`, and an `environment_variables` map.
func environmentKeys(block *hclsyntax.Block) []string {
	seen := map[string]bool{}
	var out []string
	add := func(k string) {
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}

	var walk func(*hclsyntax.Body)
	walk = func(b *hclsyntax.Body) {
		for name, attr := range b.Attributes {
			switch name {
			case "environment_variables", "env", "variables":
				for _, k := range mapKeys(attr.Expr) {
					add(k)
				}
			case "name":
				// Only inside an environment block; the caller's block type
				// tells us, which is handled below.
			}
		}
		for _, nested := range b.Blocks {
			if nested.Type == "environment" || nested.Type == "env" {
				if attr, ok := nested.Body.Attributes["name"]; ok {
					if v, ok := literalString(attr.Expr); ok {
						add(v)
					}
				}
			}
			walk(nested.Body)
		}
	}
	walk(block.Body)
	sort.Strings(out)
	return out
}

// mapKeys extracts the literal keys of an object expression.
func mapKeys(expr hclsyntax.Expression) []string {
	obj, ok := expr.(*hclsyntax.ObjectConsExpr)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range obj.Items {
		if k, ok := literalString(item.KeyExpr); ok {
			out = append(out, k)
		}
	}
	return out
}

// literalString evaluates an expression to a constant string, or reports that
// it cannot. An interpolated value is not named rather than guessed at.
func literalString(expr hclsyntax.Expression) (string, bool) {
	// An object key is wrapped, and a bare key is parsed as a traversal.
	if wrapped, ok := expr.(*hclsyntax.ObjectConsKeyExpr); ok {
		if name := hcl.ExprAsKeyword(wrapped); name != "" {
			return name, true
		}
		expr = wrapped.Wrapped
	}
	value, diags := expr.Value(nil)
	if diags.HasErrors() || value.IsNull() || !value.IsKnown() {
		return "", false
	}
	if value.Type().FriendlyName() != "string" {
		return "", false
	}
	return value.AsString(), true
}

func attrs(kv map[string]string) string {
	keys := make([]string, 0, len(kv))
	for k, v := range kv {
		if v != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return "{}"
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q:%q", k, kv[k])
	}
	b.WriteByte('}')
	return b.String()
}
