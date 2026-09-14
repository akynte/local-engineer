package golang

import (
	"go/ast"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// typeIndex holds every named type and interface declared in the repository,
// so interface satisfaction and dynamic dispatch can be answered without
// re-walking the program for each question.
type typeIndex struct {
	// interfaces are every non-empty interface declared locally.
	interfaces []*types.TypeName
	// concrete are every locally declared non-interface named type.
	concrete []*types.TypeName
	// implementers maps an interface FQN to the FQNs of local types that
	// satisfy it. Computed once, used by both emitImplements and the call
	// graph's dynamic-dispatch resolution.
	implementers map[string][]*types.TypeName
	// localFuncs maps an object FQN to true for functions declared here, so
	// edges to the standard library are dropped rather than dangling.
	localFuncs map[string]bool
}

func newTypeIndex(local map[string]*packages.Package) *typeIndex {
	idx := &typeIndex{implementers: map[string][]*types.TypeName{}, localFuncs: map[string]bool{}}

	for _, p := range sortedPackages(local) {
		if p.Types == nil {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			switch obj := scope.Lookup(name).(type) {
			case *types.TypeName:
				if types.IsInterface(obj.Type()) {
					// An empty interface is satisfied by everything, which
					// makes it useless as a relationship.
					if iface, ok := obj.Type().Underlying().(*types.Interface); ok && iface.NumMethods() > 0 {
						idx.interfaces = append(idx.interfaces, obj)
					}
					continue
				}
				if _, ok := obj.Type().(*types.Named); ok {
					idx.concrete = append(idx.concrete, obj)
				}
			case *types.Func:
				idx.localFuncs[objectFQN(obj)] = true
				if named, ok := obj.Type().(*types.Named); ok {
					_ = named
				}
			}
		}
		// Methods are not in package scope; record them too.
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			if named, ok := tn.Type().(*types.Named); ok {
				for i := 0; i < named.NumMethods(); i++ {
					idx.localFuncs[objectFQN(named.Method(i))] = true
				}
			}
		}
	}

	sort.Slice(idx.interfaces, func(i, j int) bool {
		return objectFQN(idx.interfaces[i]) < objectFQN(idx.interfaces[j])
	})
	sort.Slice(idx.concrete, func(i, j int) bool {
		return objectFQN(idx.concrete[i]) < objectFQN(idx.concrete[j])
	})

	// Precompute satisfaction. types.Implements is the compiler's own answer,
	// which is what makes these edges `resolved` rather than a name-matching
	// guess.
	for _, iface := range idx.interfaces {
		ifaceType, ok := iface.Type().Underlying().(*types.Interface)
		if !ok {
			continue
		}
		for _, c := range idx.concrete {
			if implements(c.Type(), ifaceType) {
				idx.implementers[objectFQN(iface)] = append(idx.implementers[objectFQN(iface)], c)
			}
		}
	}
	return idx
}

// implements reports whether a type, or a pointer to it, satisfies an
// interface. Checking both matters: a method set declared on *T does not make
// T satisfy the interface, and reporting only one of them would miss real
// implementations.
func implements(t types.Type, iface *types.Interface) bool {
	if types.Implements(t, iface) {
		return true
	}
	return types.Implements(types.NewPointer(t), iface)
}

// emitImplements produces the interface-to-implementation edges of §3.2,
// sourced from types.Implements.
func (b *builder) emitImplements(p *packages.Package, idx *typeIndex) {
	if p.Types == nil {
		return
	}
	scope := p.Types.Scope()
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || !types.IsInterface(tn.Type()) {
			continue
		}
		ifaceFQN := objectFQN(tn)
		for _, impl := range idx.implementers[ifaceFQN] {
			// Direction: the implementation is a consumer of the interface, so
			// changing the interface reaches the implementation on a reverse
			// walk. That is what makes "add a method" report every type that
			// must grow one.
			b.addEdge(index.PendingEdge{
				SrcKind: graph.KindInterface, SrcFQN: ifaceFQN,
				DstKind: graph.KindType, DstFQN: objectFQN(impl),
				Kind: graph.EdgeImplements, Evidence: graph.Resolved,
			})
		}
	}
}

// emitReferences walks every function body once, producing call edges, type
// usage, configuration reads, route registrations and test coverage.
func (b *builder) emitReferences(p *packages.Package, local map[string]*packages.Package, idx *typeIndex) {
	if p.TypesInfo == nil {
		return
	}
	isTestFile := func(f *ast.File) bool {
		if p.Fset == nil {
			return false
		}
		return strings.HasSuffix(p.Fset.Position(f.Pos()).Filename, "_test.go")
	}

	for _, file := range p.Syntax {
		inTest := isTestFile(file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			obj, ok := p.TypesInfo.Defs[fn.Name].(*types.Func)
			if !ok {
				continue
			}
			b.walkBody(p, obj, fn.Body, local, idx, inTest)
		}
	}
}

// callerKind reports the node kind an enclosing function was emitted under, so
// edges reference the same (kind, fqn) pair the declaration produced.
func callerKind(fn *types.Func) graph.NodeKind {
	name := fn.Name()
	if strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") ||
		strings.HasPrefix(name, "Fuzz") || strings.HasPrefix(name, "Example") {
		return graph.KindTest
	}
	if fn.Signature().Recv() != nil {
		return graph.KindMethod
	}
	return graph.KindFunction
}

func (b *builder) walkBody(p *packages.Package, caller *types.Func, body *ast.BlockStmt,
	local map[string]*packages.Package, idx *typeIndex, inTest bool) {
	callerFQN := objectFQN(caller)
	if callerFQN == "" {
		return
	}
	ck := callerKind(caller)

	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			b.emitCall(p, callerFQN, ck, node, local, idx, inTest)
		case *ast.Ident:
			// Type usage: §3.2 sources this from types.Info uses.
			if obj, ok := p.TypesInfo.Uses[node]; ok {
				if tn, isType := obj.(*types.TypeName); isType && tn.Pkg() != nil {
					if _, isLocal := local[tn.Pkg().Path()]; isLocal {
						dst := graph.KindType
						if types.IsInterface(tn.Type()) {
							dst = graph.KindInterface
						}
						b.addEdge(index.PendingEdge{
							SrcKind: ck, SrcFQN: callerFQN,
							DstKind: dst, DstFQN: objectFQN(tn),
							Kind: graph.EdgeUsesType, Evidence: graph.Resolved,
						})
					}
				}
			}
		}
		return true
	})
}

// emitCall resolves one call site.
//
// A static call resolves to exactly one function and is `resolved`. A call
// through an interface does not: the compiler knows only the interface method,
// and which implementation runs depends on the value. §3.2 allows a CHA-style
// over-approximation there, and requires the assumption to be recorded — so
// those edges are `inferred` and carry the assumption in their attributes,
// rather than being presented as fact.
func (b *builder) emitCall(p *packages.Package, callerFQN string, ck graph.NodeKind,
	call *ast.CallExpr, local map[string]*packages.Package, idx *typeIndex, inTest bool) {

	fn, viaInterface := calleeOf(p, call)
	if fn == nil {
		return
	}

	// A configuration read is a call to os.Getenv with a literal key.
	if b.emitConfigRead(p, callerFQN, ck, fn, call) {
		return
	}
	// A route registration is a call to an HTTP mux with a literal pattern.
	if b.emitRoute(p, callerFQN, ck, fn, call) {
		return
	}

	if fn.Pkg() == nil {
		return
	}
	if _, isLocal := local[fn.Pkg().Path()]; !isLocal {
		return // calls into dependencies are out of scope for the call graph
	}
	calleeFQN := objectFQN(fn)
	if calleeFQN == "" {
		return
	}

	dstKind := graph.KindFunction
	if fn.Signature().Recv() != nil {
		dstKind = graph.KindMethod
	}

	if !viaInterface {
		b.addEdge(index.PendingEdge{
			SrcKind: ck, SrcFQN: callerFQN,
			DstKind: dstKind, DstFQN: calleeFQN,
			Kind: graph.EdgeCalls, Evidence: graph.Resolved,
		})
		if inTest {
			b.addEdge(index.PendingEdge{
				SrcKind: ck, SrcFQN: callerFQN,
				DstKind: dstKind, DstFQN: calleeFQN,
				Kind: graph.EdgeTests, Evidence: graph.Resolved,
			})
		}
		return
	}

	// Dynamic dispatch: the declared interface method, plus every local type
	// that satisfies the interface.
	recv := fn.Signature().Recv()
	if recv == nil {
		return
	}
	ifaceFQN := namedFQN(recv.Type())
	impls := idx.implementers[ifaceFQN]
	if len(impls) == 0 {
		return
	}
	assumption := attrsJSON(map[string]string{
		"dispatch":   "interface",
		"interface":  ifaceFQN,
		"assumption": "CHA: any locally declared type satisfying this interface may receive the call",
		"candidates": itoa(len(impls)),
	})
	for _, impl := range impls {
		named, ok := impl.Type().(*types.Named)
		if !ok {
			continue
		}
		m := lookupMethod(named, fn.Name())
		if m == nil {
			continue
		}
		b.addEdge(index.PendingEdge{
			SrcKind: ck, SrcFQN: callerFQN,
			DstKind: graph.KindMethod, DstFQN: objectFQN(m),
			Kind: graph.EdgeCalls, Evidence: graph.Inferred, Attrs: assumption,
		})
		if inTest {
			b.addEdge(index.PendingEdge{
				SrcKind: ck, SrcFQN: callerFQN,
				DstKind: graph.KindMethod, DstFQN: objectFQN(m),
				Kind: graph.EdgeTests, Evidence: graph.Inferred, Attrs: assumption,
			})
		}
	}
}

func lookupMethod(named *types.Named, name string) *types.Func {
	// The method set of T and of *T together: a pointer receiver is the common
	// case and would otherwise be missed.
	for _, t := range []types.Type{named, types.NewPointer(named)} {
		ms := types.NewMethodSet(t)
		for i := 0; i < ms.Len(); i++ {
			if fn, ok := ms.At(i).Obj().(*types.Func); ok && fn.Name() == name {
				return fn
			}
		}
	}
	return nil
}

// calleeOf resolves a call expression to the function it names, and whether
// the call goes through an interface.
func calleeOf(p *packages.Package, call *ast.CallExpr) (*types.Func, bool) {
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		if fn, ok := p.TypesInfo.Uses[fun].(*types.Func); ok {
			return fn, false
		}
	case *ast.SelectorExpr:
		// A selection carries the resolved method and how it is reached.
		if sel, ok := p.TypesInfo.Selections[fun]; ok {
			fn, ok := sel.Obj().(*types.Func)
			if !ok {
				return nil, false
			}
			return fn, sel.Kind() == types.MethodVal && isInterfaceRecv(sel.Recv())
		}
		// A qualified identifier: pkg.Func.
		if fn, ok := p.TypesInfo.Uses[fun.Sel].(*types.Func); ok {
			return fn, false
		}
	}
	return nil, false
}

func isInterfaceRecv(t types.Type) bool {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	return types.IsInterface(t)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
