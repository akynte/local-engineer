package golang

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// §3.2 row: "API to consumer | route inventory plus client call sites with
// literal prefixes | inferred; declared via catalog".
//
// The route half already existed; this is the consumer half. Without it
// `references` was an edge kind the impact analyser traversed and nothing ever
// produced, so "who calls this endpoint" always answered nobody — which reads
// as a safe change rather than as an unanswered question.
//
// The edge is `inferred` and says why. A literal URL in a client call is a
// convention, not a compiler fact: the path may be built at run time, the
// service may not be this one, and a matching pattern is a guess about routing.
// What makes it worth emitting anyway is that the alternative is silence.

// clientCallers are the standard library and common client shapes that take a
// URL as a literal argument.
var clientCallers = map[string]int{
	// name -> index of the URL argument
	"Get": 0, "Head": 0, "Post": 0, "PostForm": 0,
	"NewRequest": 1, "NewRequestWithContext": 2,
}

// methodForCaller maps the helpers that imply their own method.
var methodForCaller = map[string]string{
	"Get": "GET", "Head": "HEAD", "Post": "POST", "PostForm": "POST",
}

// emitAPICall records a client call site with a literal URL. It returns true
// when the call was one, so the caller can stop looking.
func (b *builder) emitAPICall(p *packages.Package, callerFQN string, ck graph.NodeKind,
	fn *types.Func, call *ast.CallExpr) bool {

	argIdx, ok := clientCallers[fn.Name()]
	if !ok || len(call.Args) <= argIdx {
		return false
	}
	// Only the net/http shapes, or a method on something that looks like a
	// client. Anything named Get on any type would match half a codebase.
	if !isHTTPClientCall(p, fn) {
		return false
	}
	raw, ok := literalString(p, call.Args[argIdx])
	if !ok {
		// A computed URL is real but unnameable. Guessing would be worse than
		// leaving the edge out, and the route stays discoverable by name.
		return false
	}
	path := pathOf(raw)
	if path == "" {
		return false
	}

	method := methodForCaller[fn.Name()]
	if method == "" && argIdx > 0 {
		// NewRequest(method, url, body): the method is the argument before the
		// URL, and is usually a literal or http.MethodGet.
		if m, ok := literalString(p, call.Args[argIdx-1]); ok {
			method = strings.ToUpper(m)
		}
	}

	b.apiCalls = append(b.apiCalls, apiCallRef{
		callerFQN: callerFQN, callerKind: ck,
		method: method, path: path, registrar: fn.Name(),
	})
	return true
}

// isHTTPClientCall reports whether the function belongs to net/http or to a
// type whose name ends in Client. Requiring net/http alone would miss every
// generated or wrapped client; accepting any Get would match everything.
func isHTTPClientCall(_ *packages.Package, fn *types.Func) bool {
	if fn.Pkg() != nil && fn.Pkg().Path() == "net/http" {
		return true
	}
	sig, _ := fn.Type().(*types.Signature)
	if sig == nil || sig.Recv() == nil {
		return false
	}
	recv := sig.Recv().Type().String()
	if i := strings.LastIndex(recv, "."); i >= 0 {
		recv = recv[i+1:]
	}
	recv = strings.TrimPrefix(recv, "*")
	return strings.HasSuffix(recv, "Client")
}

// pathOf extracts the path from a literal that may be a full URL, a path, or a
// prefix with a trailing format placeholder.
func pathOf(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// Strip a scheme and host if present.
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		j := strings.IndexByte(rest, '/')
		if j < 0 {
			return "/"
		}
		s = rest[j:]
	}
	if !strings.HasPrefix(s, "/") {
		return ""
	}
	// Drop query and fragment.
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

// resolveAPIConsumers matches collected client calls against collected routes
// and emits the consumer edges. It runs after every package has been walked,
// because a call site and the route it reaches are nearly always in different
// packages.
func (b *builder) resolveAPIConsumers() {
	if len(b.routes) == 0 || len(b.apiCalls) == 0 {
		return
	}
	for _, call := range b.apiCalls {
		for _, route := range b.routes {
			if !routeMatches(route, call) {
				continue
			}
			// Consumer points at what it consumes: changing the route must find
			// the call sites, and impact analysis is a reverse traversal.
			b.addEdge(index.PendingEdge{
				SrcKind: call.callerKind, SrcFQN: call.callerFQN,
				DstKind: graph.KindRoute, DstFQN: route.fqn,
				Kind: graph.EdgeReferences, Evidence: graph.Inferred,
				Attrs: attrsJSON(map[string]string{
					"assumption": "a literal URL passed to " + call.registrar +
						" reaches the route this repository serves at the same path",
					"call_path":     call.path,
					"route_pattern": route.pattern,
				}),
			})
		}
	}
}

// routeMatches reports whether a client path could reach a route pattern.
// Placeholder segments — {id}, :id — match any single segment.
func routeMatches(route routeRef, call apiCallRef) bool {
	if route.method != "" && call.method != "" && route.method != call.method {
		return false
	}
	rs := splitPath(route.pattern)
	cs := splitPath(call.path)

	// A trailing wildcard or prefix route matches anything below it.
	trailing := strings.HasSuffix(route.pattern, "/") || strings.HasSuffix(route.pattern, "...}")
	if len(rs) != len(cs) && !trailing {
		return false
	}
	if trailing && len(cs) < len(rs) {
		return false
	}
	for i := range rs {
		if i >= len(cs) {
			return false
		}
		if isPlaceholder(rs[i]) {
			continue
		}
		if rs[i] != cs[i] {
			return false
		}
	}
	return true
}

func splitPath(p string) []string {
	var out []string
	for _, seg := range strings.Split(p, "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// isPlaceholder recognises the wildcard forms the common routers use, plus a
// printf verb, which is how a path is built in a client more often than not.
func isPlaceholder(seg string) bool {
	switch {
	case strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}"):
		return true
	case strings.HasPrefix(seg, ":"):
		return true
	case seg == "*":
		return true
	case strings.HasPrefix(seg, "%") && len(seg) <= 3:
		return true
	}
	return false
}
