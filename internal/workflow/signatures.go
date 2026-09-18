package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/policy"
	"github.com/akynte/local-engineer/internal/treesitter"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"sort"
	"strings"
)

// ChangedGoSignatures compares public contracts without parameter names or
// bodies. Compiler errors are surfaced rather than guessed from text matches.
func ChangedGoSignatures(before, after []byte) ([]string, error) {
	old, err := goSignatures(before)
	if err != nil {
		return nil, err
	}
	next, err := goSignatures(after)
	if err != nil {
		return nil, err
	}
	var changed []string
	seen := map[string]bool{}
	for name, signature := range old {
		if next[name] != signature {
			if dot := strings.LastIndex(name, "."); dot >= 0 {
				name = name[dot+1:]
			}
			if !seen[name] {
				changed = append(changed, name)
				seen[name] = true
			}
		}
	}
	sort.Strings(changed)
	return changed, nil
}
func goSignatures(body []byte) (map[string]string, error) {
	out := map[string]string{}
	if len(body) == 0 {
		return out, nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "source.go", body, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	clearNames := func(fields *ast.FieldList) {
		if fields != nil {
			var expanded []*ast.Field
			for _, field := range fields.List {
				for n := 0; n < max(1, len(field.Names)); n++ {
					copy := *field
					copy.Names = nil
					copy.Doc = nil
					copy.Comment = nil
					expanded = append(expanded, &copy)
				}
			}
			fields.List = expanded
		}
	}
	for _, decl := range file.Decls {
		switch node := decl.(type) {
		case *ast.FuncDecl:
			if !ast.IsExported(node.Name.Name) {
				continue
			}
			clearNames(node.Type.Params)
			clearNames(node.Type.Results)
			var signature bytes.Buffer
			_ = format.Node(&signature, fset, node.Type)
			name := node.Name.Name
			if node.Recv != nil && len(node.Recv.List) > 0 {
				var receiver bytes.Buffer
				_ = format.Node(&receiver, fset, node.Recv.List[0].Type)
				name = receiver.String() + "." + name
			}
			out[name] = signature.String()
		case *ast.GenDecl:
			for _, spec := range node.Specs {
				if typ, ok := spec.(*ast.TypeSpec); ok && ast.IsExported(typ.Name.Name) {
					var signature bytes.Buffer
					_ = format.Node(&signature, fset, typ.Type)
					out[typ.Name.Name] = signature.String()
				}
			}
		}
	}
	return out, nil
}

// ValidateObligations requires an explicit disposition for every discovered
// consumer. A planner may explain compatibility but cannot silently omit it.
func (p Plan) ValidateObligations(impact *graph.Impact) error {
	if impact == nil {
		return nil
	}
	for _, consumer := range impact.Consumers {
		if consumer.Node.Path == "" {
			continue
		}
		found := false
		for _, o := range p.Obligations {
			if o.Path != consumer.Node.Path || (o.Symbol != consumer.Node.FQN && o.Symbol != consumer.Node.Name) {
				continue
			}
			if o.Resolution == "edit" && policy.Covers(p.WriteAllowlist, o.Path) {
				found = true
			}
			if strings.HasPrefix(o.Resolution, "no change needed:") && len(strings.TrimSpace(strings.TrimPrefix(o.Resolution, "no change needed:"))) > 0 {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("unresolved impact obligation: %s in %s", consumer.Node.FQN, consumer.Node.Path)
		}
	}
	return nil
}

// ChangedSignatures reports the declarations whose signature differs between
// two versions of a file, in any language this build can parse.
//
// Go keeps go/parser: it is exact, it understands the receiver forms, and it is
// already covered by tests. Everything else goes through tree-sitter, which is
// what closes the hole this function exists to close — before it, the caller
// checked `.go` and skipped every other file, so an attempt could change a
// public Rust function or a TypeScript method and the obligations check would
// find no callers to account for, having never looked. A silent no is the worst
// answer a safety check can give.
func ChangedSignatures(file string, before, after []byte) ([]string, error) {
	if strings.HasSuffix(file, ".go") {
		return ChangedGoSignatures(before, after)
	}
	names, err := treesitter.ChangedSignatures(file, before, after)
	if errors.Is(err, treesitter.ErrUnsupported) {
		// A language with no grammar contributes no obligations. That is a
		// known limit, not a clean bill of health, and Supported reports it so
		// a caller can say which files were not examined.
		return nil, nil
	}
	return names, err
}

// SignaturesSupported reports whether a file's language can be checked for
// signature changes at all.
func SignaturesSupported(file string) bool {
	return strings.HasSuffix(file, ".go") || treesitter.Supports(file)
}
