package golang

import (
	"go/ast"
	"go/constant"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// configReaders are the standard-library entry points that read an
// environment variable. §3.2 sources configuration edges from these.
var configReaders = map[string]int{
	"os.Getenv":              0,
	"os.LookupEnv":           0,
	"syscall.Getenv":         0,
	"os.ExpandEnv":           -1, // whole-string expansion: no single key to name
	"os.Setenv":              0,
	"(*flag.FlagSet).String": -1,
}

// emitConfigRead recognises a configuration read and emits the key node and a
// reads_config edge. It returns true when the call was one.
//
// Only a literal key produces an edge. A key built at runtime is real, but
// naming it would require guessing, and a wrong config_key node is worse than
// a missing one: it would make an impact report confidently incomplete.
func (b *builder) emitConfigRead(p *packages.Package, callerFQN string, ck graph.NodeKind,
	fn *types.Func, call *ast.CallExpr) bool {

	name := qualifiedPath(fn)
	argIdx, ok := configReaders[name]
	if !ok {
		return false
	}
	if argIdx < 0 || argIdx >= len(call.Args) {
		return true // recognised, but no single key to attribute it to
	}
	key, ok := literalString(p, call.Args[argIdx])
	if !ok || key == "" {
		return true
	}

	b.addNode(graph.Node{
		Kind: graph.KindConfigKey, Name: key, FQN: configFQN(key),
		Attrs: attrsJSON(map[string]string{"source": "environment"}),
	})
	// The reader points at the key, matching the deployment and infrastructure
	// analyzers, so a change to the key finds the code and the manifests in
	// one traversal.
	b.addEdge(index.PendingEdge{
		SrcKind: ck, SrcFQN: callerFQN,
		DstKind: graph.KindConfigKey, DstFQN: configFQN(key),
		Kind: graph.EdgeReadsConfig, Evidence: graph.Resolved,
		Attrs: attrsJSON(map[string]string{"via": name}),
	})
	return true
}

// routeRegistrars are the method names that register an HTTP route. The
// receiver is not checked against a specific router type on purpose: the
// ecosystem has many, they all share this shape, and requiring a known
// receiver would silently produce nothing on an unrecognised one.
//
// This is why these edges are `inferred`, not `resolved`: the shape is a
// convention, not a compiler fact.
var routeRegistrars = map[string]bool{
	"HandleFunc": true, "Handle": true,
	"Get": true, "Post": true, "Put": true, "Patch": true, "Delete": true,
	"Head": true, "Options": true, "Any": true, "All": true, "Method": true,
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

// httpMethods lets a "GET /path" pattern be split, as Go 1.22+ ServeMux uses.
var httpMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
	"HEAD": true, "OPTIONS": true, "CONNECT": true, "TRACE": true,
}

// emitRoute recognises an HTTP route registration and emits a route node plus
// a routes_to edge to the handler. It returns true when the call was one.
func (b *builder) emitRoute(p *packages.Package, callerFQN string, ck graph.NodeKind,
	fn *types.Func, call *ast.CallExpr) bool {

	if !routeRegistrars[fn.Name()] || len(call.Args) < 2 {
		return false
	}
	// The first argument must be a literal pattern. A computed route is real
	// but unnameable, and a guessed one would be worse than none.
	pattern, ok := literalString(p, call.Args[0])
	if !ok || !strings.Contains(pattern, "/") {
		return false
	}

	method := strings.ToUpper(fn.Name())
	if !httpMethods[method] {
		method = ""
	}
	// Go 1.22 ServeMux patterns carry the method inline: "GET /payments/{id}".
	if head, rest, found := strings.Cut(pattern, " "); found && httpMethods[strings.ToUpper(head)] {
		method, pattern = strings.ToUpper(head), rest
	}

	handler := handlerOf(p, call.Args[1:])
	if handler == nil {
		return false
	}
	handlerFQN := objectFQN(handler)
	if handlerFQN == "" {
		return false
	}
	handlerKind := graph.KindFunction
	if handler.Signature().Recv() != nil {
		handlerKind = graph.KindMethod
	}

	fqn := routeFQN(method, pattern)
	b.addNode(graph.Node{
		Kind: graph.KindRoute, Name: strings.TrimSpace(method + " " + pattern), FQN: fqn,
		Attrs: attrsJSON(map[string]string{"method": method, "pattern": pattern, "registrar": fn.Name()}),
	})
	b.addEdge(index.PendingEdge{
		SrcKind: graph.KindRoute, SrcFQN: fqn,
		DstKind: handlerKind, DstFQN: handlerFQN,
		Kind: graph.EdgeRoutesTo, Evidence: graph.Inferred,
		Attrs: attrsJSON(map[string]string{
			"assumption": "a call named " + fn.Name() + " with a literal path and a handler argument registers a route",
		}),
	})
	// The function doing the registering contains the route, so "which file
	// wires this endpoint" is one hop.
	b.addEdge(index.PendingEdge{
		SrcKind: ck, SrcFQN: callerFQN,
		DstKind: graph.KindRoute, DstFQN: fqn,
		Kind: graph.EdgeHandles, Evidence: graph.Resolved,
	})
	return true
}

// handlerOf finds the function referenced by a handler argument, through the
// common wrappers.
func handlerOf(p *packages.Package, args []ast.Expr) *types.Func {
	for _, arg := range args {
		switch e := ast.Unparen(arg).(type) {
		case *ast.Ident:
			if fn, ok := p.TypesInfo.Uses[e].(*types.Func); ok {
				return fn
			}
		case *ast.SelectorExpr:
			if sel, ok := p.TypesInfo.Selections[e]; ok {
				if fn, ok := sel.Obj().(*types.Func); ok {
					return fn
				}
			}
			if fn, ok := p.TypesInfo.Uses[e.Sel].(*types.Func); ok {
				return fn
			}
		case *ast.CallExpr:
			// http.HandlerFunc(h) and similar single-argument conversions.
			if len(e.Args) == 1 {
				if fn := handlerOf(p, e.Args); fn != nil {
					return fn
				}
			}
		}
	}
	return nil
}

// qualifiedPath renders a function as "path.Func" or "(*path.T).Method" using
// the full import path.
//
// The path, not the package name: names collide freely, and a lookup table
// keyed on "sql.DB" would match anybody's package called sql — which is how a
// method on an unrelated type gets mistaken for a database call.
func qualifiedPath(fn *types.Func) string {
	if fn.Pkg() == nil {
		return fn.Name()
	}
	q := func(p *types.Package) string { return p.Path() }
	if recv := fn.Signature().Recv(); recv != nil {
		return "(" + types.TypeString(recv.Type(), q) + ")." + fn.Name()
	}
	return fn.Pkg().Path() + "." + fn.Name()
}

// literalString evaluates an expression to a constant string, following
// constant identifiers. It returns false for anything computed at runtime.
func literalString(p *packages.Package, expr ast.Expr) (string, bool) {
	tv, ok := p.TypesInfo.Types[ast.Unparen(expr)]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(tv.Value), true
}
