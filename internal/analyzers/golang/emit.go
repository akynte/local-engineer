package golang

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// emitModule produces module nodes and the module dependency graph. §3.2
// sources this from `go list` / `go mod graph`; the loader has already done
// that work, so the module information comes back with the packages.
func (b *builder) emitModule(pkgs []*packages.Package, local map[string]*packages.Package) {
	var self *packages.Module
	for _, p := range pkgs {
		if p.Module != nil && !p.Module.Indirect {
			self = p.Module
			break
		}
	}
	if self == nil {
		return
	}
	b.addNode(graph.Node{
		Kind: graph.KindModule, Name: self.Path, FQN: moduleFQN(self.Path),
		Attrs: attrsJSON(map[string]string{"version": self.Version, "go": self.GoVersion}),
	})

	// Direct requirements only. The full transitive module graph is large and
	// adds nothing the package-level import edges do not already carry.
	seen := map[string]bool{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Module == nil || p.Module.Path == self.Path || seen[p.Module.Path] {
			return
		}
		// Only modules something in this repository actually imports.
		if !importedLocally(p.PkgPath, local) {
			return
		}
		seen[p.Module.Path] = true
		b.addNode(graph.Node{
			Kind: graph.KindDependency, Name: p.Module.Path, FQN: moduleFQN(p.Module.Path),
			Attrs: attrsJSON(map[string]string{"version": p.Module.Version}),
		})
		b.addEdge(index.PendingEdge{
			SrcKind: graph.KindModule, SrcFQN: moduleFQN(self.Path),
			DstKind: graph.KindDependency, DstFQN: moduleFQN(p.Module.Path),
			Kind: graph.EdgeDependsOn, Evidence: graph.Resolved,
		})
	})
}

func importedLocally(pkgPath string, local map[string]*packages.Package) bool {
	for _, p := range local {
		if _, ok := p.Imports[pkgPath]; ok {
			return true
		}
	}
	return false
}

// emitPackage produces the package node and its import edges. §3.2: "import to
// dependency | compilers | resolved".
func (b *builder) emitPackage(p *packages.Package, local map[string]*packages.Package) {
	b.addNode(graph.Node{
		Kind: graph.KindPackage, Name: p.Name, FQN: packageFQN(p.PkgPath),
		Attrs: attrsJSON(map[string]string{"files": fmt.Sprint(len(p.GoFiles))}),
	})

	paths := make([]string, 0, len(p.Imports))
	for path := range p.Imports {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		if _, isLocal := local[path]; isLocal {
			b.addEdge(index.PendingEdge{
				SrcKind: graph.KindPackage, SrcFQN: packageFQN(p.PkgPath),
				DstKind: graph.KindPackage, DstFQN: packageFQN(path),
				Kind: graph.EdgeImports, Evidence: graph.Resolved,
			})
			continue
		}
		// An external import becomes a dependency node, so "what does this
		// package rely on" is answerable without leaving the graph.
		b.addNode(graph.Node{
			Kind: graph.KindDependency, Name: lastSegment(path), FQN: packageFQN(path),
			Attrs: attrsJSON(map[string]string{"external": "true", "stdlib": fmt.Sprint(isStdlib(path))}),
		})
		b.addEdge(index.PendingEdge{
			SrcKind: graph.KindPackage, SrcFQN: packageFQN(p.PkgPath),
			DstKind: graph.KindDependency, DstFQN: packageFQN(path),
			Kind: graph.EdgeImports, Evidence: graph.Resolved,
		})
	}
}

// isStdlib reports whether an import path is in the standard library. The
// heuristic is the documented one: no dot in the first path segment.
func isStdlib(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

func lastSegment(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// emitDeclarations produces nodes for every package-level type, function and
// method, plus the containment edges from their package.
func (b *builder) emitDeclarations(p *packages.Package) {
	if p.Types == nil {
		return
	}
	scope := p.Types.Scope()
	names := scope.Names()
	sort.Strings(names)

	for _, name := range names {
		obj := scope.Lookup(name)
		switch o := obj.(type) {
		case *types.TypeName:
			b.emitTypeName(p, o)
		case *types.Func:
			b.emitFunc(p, o, "")
		case *types.Var, *types.Const:
			kind := graph.KindVariable
			if _, isConst := obj.(*types.Const); isConst {
				kind = graph.KindConstant
			}
			b.addNode(graph.Node{
				Kind: kind, Name: name, FQN: objectFQN(obj),
				Signature: types.TypeString(obj.Type(), relativeTo(p.Types)),
				StartLine: line(p, obj), Visibility: visibility(name),
			})
			b.addEdge(index.PendingEdge{
				SrcKind: graph.KindPackage, SrcFQN: packageFQN(p.PkgPath),
				DstKind: kind, DstFQN: objectFQN(obj),
				Kind: graph.EdgeContains, Evidence: graph.Resolved,
			})
		}
	}
}

func (b *builder) emitTypeName(p *packages.Package, tn *types.TypeName) {
	kind := graph.KindType
	if types.IsInterface(tn.Type()) {
		kind = graph.KindInterface
	}
	fqn := objectFQN(tn)
	b.addNode(graph.Node{
		Kind: kind, Name: tn.Name(), FQN: fqn,
		Signature:  types.TypeString(tn.Type().Underlying(), relativeTo(p.Types)),
		StartLine:  line(p, tn),
		Visibility: visibility(tn.Name()),
	})
	b.addEdge(index.PendingEdge{
		SrcKind: graph.KindPackage, SrcFQN: packageFQN(p.PkgPath),
		DstKind: kind, DstFQN: fqn,
		Kind: graph.EdgeContains, Evidence: graph.Resolved,
	})

	named, ok := tn.Type().(*types.Named)
	if !ok {
		return
	}
	// Methods hang off their receiver type, which is what makes "what calls
	// this method" answerable without a separate lookup.
	for i := 0; i < named.NumMethods(); i++ {
		b.emitFunc(p, named.Method(i), fqn)
	}
	// Struct fields, so a field rename has consumers.
	if st, ok := named.Underlying().(*types.Struct); ok {
		for i := 0; i < st.NumFields(); i++ {
			f := st.Field(i)
			ffqn := fqn + "." + f.Name()
			b.addNode(graph.Node{
				Kind: graph.KindField, Name: f.Name(), FQN: ffqn,
				Signature: types.TypeString(f.Type(), relativeTo(p.Types)),
				StartLine: line(p, f), Visibility: visibility(f.Name()),
				Attrs: attrsJSON(map[string]string{"tag": st.Tag(i), "embedded": fmt.Sprint(f.Embedded())}),
			})
			b.addEdge(index.PendingEdge{
				SrcKind: kind, SrcFQN: fqn,
				DstKind: graph.KindField, DstFQN: ffqn,
				Kind: graph.EdgeContains, Evidence: graph.Resolved,
			})
			if f.Embedded() {
				if dst := namedFQN(f.Type()); dst != "" {
					b.addEdge(index.PendingEdge{
						SrcKind: kind, SrcFQN: fqn,
						DstKind: graph.KindType, DstFQN: dst,
						Kind: graph.EdgeEmbeds, Evidence: graph.Resolved,
					})
				}
			}
		}
	}
}

func (b *builder) emitFunc(p *packages.Package, fn *types.Func, ownerFQN string) {
	kind := graph.KindFunction
	if fn.Signature().Recv() != nil {
		kind = graph.KindMethod
	}
	fqn := objectFQN(fn)
	if fqn == "" {
		return
	}
	isTest := strings.HasPrefix(fn.Name(), "Test") || strings.HasPrefix(fn.Name(), "Benchmark") ||
		strings.HasPrefix(fn.Name(), "Fuzz") || strings.HasPrefix(fn.Name(), "Example")
	if isTest {
		kind = graph.KindTest
	}

	b.addNode(graph.Node{
		Kind: kind, Name: fn.Name(), FQN: fqn,
		Signature:  types.TypeString(fn.Signature(), relativeTo(p.Types)),
		StartLine:  line(p, fn),
		Visibility: visibility(fn.Name()),
	})

	src, srcFQN := graph.KindPackage, packageFQN(p.PkgPath)
	if ownerFQN != "" {
		src, srcFQN = graph.KindType, ownerFQN
	}
	b.addEdge(index.PendingEdge{
		SrcKind: src, SrcFQN: srcFQN,
		DstKind: kind, DstFQN: fqn,
		Kind: graph.EdgeContains, Evidence: graph.Resolved,
	})

	// Signature types are usage: a change to a parameter or result type has
	// every function in its signature as a consumer.
	sig := fn.Signature()
	for _, tuple := range []*types.Tuple{sig.Params(), sig.Results()} {
		for i := 0; i < tuple.Len(); i++ {
			if dst := namedFQN(tuple.At(i).Type()); dst != "" {
				edgeKind := graph.EdgeAccepts
				if tuple == sig.Results() {
					edgeKind = graph.EdgeReturns
				}
				b.addEdge(index.PendingEdge{
					SrcKind: kind, SrcFQN: fqn,
					DstKind: graph.KindType, DstFQN: dst,
					Kind: edgeKind, Evidence: graph.Resolved,
				})
			}
		}
	}
}

// namedFQN unwraps pointers, slices, maps and channels to the underlying named
// type's FQN, or "" when there is no named type to point at.
func namedFQN(t types.Type) string {
	for i := 0; i < 8; i++ { // bounded: no type nests this deep in practice
		switch u := t.(type) {
		case *types.Pointer:
			t = u.Elem()
		case *types.Slice:
			t = u.Elem()
		case *types.Array:
			t = u.Elem()
		case *types.Chan:
			t = u.Elem()
		case *types.Map:
			t = u.Elem()
		case *types.Named:
			if u.Obj().Pkg() == nil {
				return "" // a builtin such as error
			}
			return objectFQN(u.Obj())
		default:
			return ""
		}
	}
	return ""
}

func relativeTo(pkg *types.Package) types.Qualifier {
	return func(other *types.Package) string {
		if other == pkg {
			return ""
		}
		return other.Name()
	}
}

func visibility(name string) string {
	if name == "" {
		return ""
	}
	if ast.IsExported(name) {
		return "exported"
	}
	return "unexported"
}

func line(p *packages.Package, obj types.Object) int {
	if p.Fset == nil || !obj.Pos().IsValid() {
		return 0
	}
	return p.Fset.Position(obj.Pos()).Line
}

func attrsJSON(kv map[string]string) string {
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
	var sb strings.Builder
	sb.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "%q:%q", k, kv[k])
	}
	sb.WriteByte('}')
	return sb.String()
}
