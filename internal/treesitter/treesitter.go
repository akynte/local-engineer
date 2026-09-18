// Package treesitter is the language-agnostic structure layer of the
// architecture review's §4 diagram and §6.1 item 2.
//
// It answers two questions the compiler-backed layers cannot, and it is worth
// being precise about which, because tree-sitter costs this project its static
// binary and should not be reached for beyond them.
//
// The first is "which declaration encloses this line". §11.1 puts
// primary_symbol in the failure fingerprint, so that a compiler error moving
// from line 412 to line 440 of the same function is recognised as the same
// failure rather than a new one. SCIP can answer it for indexed code, but the
// file that just failed to build is the file the indexer could not process.
//
// The second is "did this signature change", for languages other than Go.
// Go has go/parser, which is exact and stays in use. Rust, TypeScript and Vue
// had nothing, so signature obligations simply did not exist for them: an
// attempt could change a public Rust function and the obligations check would
// find no callers to account for, because it never looked.
//
// What this is not: a replacement for SCIP or for a language server. It parses
// one file at a time with no knowledge of imports, types or other files, so it
// can say a function's signature changed and never who calls it. Resolution
// stays with the compiler-backed layers.
package treesitter

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	ts "github.com/tree-sitter/go-tree-sitter"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tsrust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	tstypescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// Symbol is one declaration found in a file.
type Symbol struct {
	// Name is the declared name, receiver-qualified for a method so that
	// Checker.Check and Limiter.Check are different symbols.
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Signature is the declaration without its body: everything a caller
	// depends on, and nothing that is merely how it is implemented.
	Signature string `json:"signature"`
	// StartLine and EndLine are 1-based and inclusive, matching every other
	// line number in this system and in the compiler output it reads.
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
}

// grammar is one language's configuration.
type grammar struct {
	name string
	lang *ts.Language
	// decls maps a node kind to the symbol kind it declares. A kind absent
	// from this map is not a declaration as far as this package is concerned.
	decls map[string]string
	// receiverField names the field holding a method's receiver or owning
	// type, where the grammar has one.
	receiverField string
}

var (
	once     sync.Once
	grammars map[string]*grammar
)

// load builds the grammar table once. ts.NewLanguage allocates on the C side,
// so the languages are made once and shared; parsers are not, because a parser
// holds mutable state and is not safe to use from two goroutines.
func load() {
	goLang := &grammar{
		name: "go",
		lang: ts.NewLanguage(tsgo.Language()),
		decls: map[string]string{
			"function_declaration": "function",
			"method_declaration":   "method",
			"type_spec":            "type",
		},
		receiverField: "receiver",
	}
	rust := &grammar{
		name: "rust",
		lang: ts.NewLanguage(tsrust.Language()),
		decls: map[string]string{
			"function_item":           "function",
			"function_signature_item": "function",
			"struct_item":             "type",
			"enum_item":               "type",
			"union_item":              "type",
			"type_item":               "type",
			"trait_item":              "interface",
			"const_item":              "constant",
			"static_item":             "variable",
			"macro_definition":        "macro",
		},
	}
	typescript := &grammar{
		name: "typescript",
		lang: ts.NewLanguage(tstypescript.LanguageTypescript()),
		decls: map[string]string{
			"function_declaration":           "function",
			"generator_function_declaration": "function",
			"method_definition":              "method",
			"class_declaration":              "type",
			"abstract_class_declaration":     "type",
			"interface_declaration":          "interface",
			"type_alias_declaration":         "type",
			"enum_declaration":               "type",
		},
	}
	tsx := &grammar{name: "tsx", lang: ts.NewLanguage(tstypescript.LanguageTSX()), decls: typescript.decls}

	grammars = map[string]*grammar{
		".go":  goLang,
		".rs":  rust,
		".ts":  typescript,
		".mts": typescript,
		".cts": typescript,
		".tsx": tsx,
		".jsx": tsx,
		".js":  tsx,
		".mjs": tsx,
		".cjs": tsx,
	}
}

func grammarFor(file string) *grammar {
	once.Do(load)
	return grammars[strings.ToLower(path.Ext(file))]
}

// Supports reports whether a file's language has a grammar here.
//
// Vue single-file components deliberately do not: the published grammar has no
// Go module, so a `.vue` file is covered by the TypeScript sidecar's view of
// its script block or not at all. Saying so is better than a grammar that
// parses the template and silently reports no declarations.
func Supports(file string) bool { return grammarFor(file) != nil }

// Languages lists the extensions this build can parse, for `le doctor`.
func Languages() []string {
	once.Do(load)
	out := make([]string, 0, len(grammars))
	for ext := range grammars {
		out = append(out, ext)
	}
	sort.Strings(out)
	return out
}

// ErrUnsupported reports a file this package has no grammar for. It is a named
// error because every caller's correct response is to fall back to the
// compiler-backed layers rather than to fail.
var ErrUnsupported = fmt.Errorf("treesitter: no grammar for this file type")

// parse runs one parser over one file. The parser and tree are closed here:
// both hold C memory, and a leak in a long-running supervisor is measured in
// gigabytes rather than in handles.
func parse(g *grammar, src []byte, fn func(root *ts.Node)) error {
	p := ts.NewParser()
	defer p.Close()
	if err := p.SetLanguage(g.lang); err != nil {
		return fmt.Errorf("treesitter: %s: %w", g.name, err)
	}
	tree := p.Parse(src, nil)
	if tree == nil {
		return fmt.Errorf("treesitter: %s: parser returned no tree", g.name)
	}
	defer tree.Close()
	fn(tree.RootNode())
	return nil
}

// Symbols returns every declaration in a file, in source order.
//
// A file that does not parse cleanly still yields what was recognised.
// tree-sitter is error-tolerant by design, and a file mid-edit — which is
// exactly when this is asked — is usually not valid source.
func Symbols(file string, src []byte) ([]Symbol, error) {
	g := grammarFor(file)
	if g == nil {
		return nil, ErrUnsupported
	}
	var out []Symbol
	err := parse(g, src, func(root *ts.Node) {
		walk(root, func(n *ts.Node) {
			if s, ok := symbolFor(g, n, src); ok {
				out = append(out, s)
			}
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// EnclosingDeclaration names the declaration containing a 1-based line.
//
// This is §11.1's primary_symbol. The innermost declaration wins: a method
// inside an impl block is reported as the method, because that is what a person
// reading the failure needs to look at.
func EnclosingDeclaration(file string, src []byte, line int) (Symbol, bool) {
	g := grammarFor(file)
	if g == nil || line < 1 {
		return Symbol{}, false
	}
	var found Symbol
	var ok bool
	_ = parse(g, src, func(root *ts.Node) {
		point := ts.Point{Row: uint(line - 1), Column: 0} //nolint:gosec // line is checked above
		node := root.DescendantForPointRange(point, point)
		for n := node; n != nil; n = n.Parent() {
			if s, isDecl := symbolFor(g, n, src); isDecl {
				found, ok = s, true
				return
			}
		}
	})
	return found, ok
}

// ChangedSignatures reports the declarations whose signature differs between
// two versions of a file.
//
// Added and removed declarations both count: a caller of something that no
// longer exists is as broken as a caller of something whose parameters moved.
// Only the signature is compared, so an implementation rewritten without
// changing what it accepts or returns produces no obligation, which is the
// distinction the obligations mechanism rests on.
func ChangedSignatures(file string, before, after []byte) ([]string, error) {
	g := grammarFor(file)
	if g == nil {
		return nil, ErrUnsupported
	}
	old, err := signatureMap(g, file, before)
	if err != nil {
		return nil, err
	}
	next, err := signatureMap(g, file, after)
	if err != nil {
		return nil, err
	}
	changed := map[string]bool{}
	for name, signature := range old {
		if next[name] != signature {
			changed[name] = true
		}
	}
	for name, signature := range next {
		if old[name] != signature {
			changed[name] = true
		}
	}
	out := make([]string, 0, len(changed))
	for name := range changed {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func signatureMap(g *grammar, file string, src []byte) (map[string]string, error) {
	out := map[string]string{}
	if len(src) == 0 {
		return out, nil
	}
	symbols, err := Symbols(file, src)
	if err != nil {
		return nil, err
	}
	for _, s := range symbols {
		// Last declaration wins for a duplicated name, which only happens in
		// source that does not compile. Either way the comparison stays
		// deterministic, which is what the caller depends on.
		out[s.Name] = s.Signature
	}
	return out, nil
}

// symbolFor turns a node into a Symbol when it declares something.
func symbolFor(g *grammar, n *ts.Node, src []byte) (Symbol, bool) {
	kind, ok := g.decls[n.Kind()]
	if !ok {
		return Symbol{}, false
	}
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return Symbol{}, false
	}
	name := nameNode.Utf8Text(src)
	if name == "" {
		return Symbol{}, false
	}
	if qualifier := ownerOf(g, n, src); qualifier != "" {
		name = qualifier + "." + name
	}
	return Symbol{
		Name:      name,
		Kind:      kind,
		Signature: signatureOf(n, src),
		StartLine: int(n.StartPosition().Row) + 1, //nolint:gosec // a file's line count fits an int
		EndLine:   int(n.EndPosition().Row) + 1,   //nolint:gosec // same
	}, true
}

// ownerOf qualifies a method with the type it belongs to.
//
// Without this, every Check in a repository is the same symbol, and a signature
// change to one would report obligations for the callers of all of them.
func ownerOf(g *grammar, n *ts.Node, src []byte) string {
	if g.receiverField != "" {
		if recv := n.ChildByFieldName(g.receiverField); recv != nil {
			return typeName(recv.Utf8Text(src))
		}
	}
	// Grammars without a receiver field nest the method inside the type: a
	// TypeScript method_definition sits in a class_body, a Rust function_item
	// in an impl_item.
	for p := n.Parent(); p != nil; p = p.Parent() {
		if _, isDecl := g.decls[p.Kind()]; isDecl {
			if name := p.ChildByFieldName("name"); name != nil {
				return typeName(name.Utf8Text(src))
			}
		}
		if p.Kind() == "impl_item" {
			if t := p.ChildByFieldName("type"); t != nil {
				return typeName(t.Utf8Text(src))
			}
		}
	}
	return ""
}

// typeName strips a receiver declaration down to its type name: `(c *Checker)`
// and `c *Checker` and `*Checker` all name Checker, and a signature change is
// not a rename of the receiver variable.
func typeName(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "(")
	text = strings.TrimSuffix(text, ")")
	if fields := strings.Fields(text); len(fields) > 0 {
		text = fields[len(fields)-1]
	}
	text = strings.TrimPrefix(text, "*")
	text = strings.TrimPrefix(text, "&")
	if i := strings.IndexAny(text, "<[("); i > 0 {
		text = text[:i]
	}
	return strings.TrimSpace(text)
}

// signatureOf is the declaration with its body removed.
//
// Everything before the body is what a caller depends on. A declaration with no
// body — a Rust trait method, a TypeScript interface member, a Go type spec —
// is entirely signature, so it is taken whole.
func signatureOf(n *ts.Node, src []byte) string {
	end := n.EndByte()
	if body := n.ChildByFieldName("body"); body != nil {
		end = body.StartByte()
	}
	return normalize(string(src[n.StartByte():end]))
}

// normalize renders a signature in a canonical form, so that reformatting is
// not a signature change.
//
// Collapsing whitespace is not enough on its own. A formatter that puts each
// parameter on its own line also indents them and adds a trailing comma before
// the closing bracket, and both survive a naive collapse: rustfmt turns
// `free(a: &str, b: &str)` into `free( a: &str, b: &str, )`. Reporting that as
// a changed signature would manufacture an obligation for every caller of every
// function anyone reformatted, which is the fastest way to make the obligations
// mechanism something people switch off.
//
// Spaces next to punctuation are dropped and trailing separators before a
// closing bracket are removed. Spaces between identifier characters are kept,
// because there they carry meaning.
func normalize(s string) string {
	fields := strings.Join(strings.Fields(s), " ")

	var b strings.Builder
	b.Grow(len(fields))
	runes := []rune(fields)
	for i, r := range runes {
		if r == ' ' {
			var prev, next rune
			if i > 0 {
				prev = runes[i-1]
			}
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			if isSignaturePunct(prev) || isSignaturePunct(next) {
				continue
			}
		}
		b.WriteRune(r)
	}

	out := b.String()
	// Repeatedly, because nested generics produce `,>,>`.
	for {
		shortened := out
		for _, closer := range []string{")", "]", "}", ">"} {
			shortened = strings.ReplaceAll(shortened, ","+closer, closer)
		}
		if shortened == out {
			return out
		}
		out = shortened
	}
}

// isSignaturePunct reports characters that never need a neighbouring space to
// stay unambiguous.
func isSignaturePunct(r rune) bool {
	return strings.ContainsRune("()[]{}<>,;:&*|", r)
}

// walk visits every node once, outermost first, so a nested declaration is
// reported after the one containing it.
func walk(n *ts.Node, visit func(*ts.Node)) {
	visit(n)
	for i := uint(0); i < n.ChildCount(); i++ {
		walk(n.Child(i), visit)
	}
}
