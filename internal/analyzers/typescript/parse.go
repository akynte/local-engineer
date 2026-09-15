package typescript

import (
	"strings"
)

// Import is one module specifier a file pulls in.
type Import struct {
	// Specifier is the text as written: "./user", "react", "@scope/pkg".
	Specifier string
	// Names are the imported bindings, empty for a side-effect import.
	Names []string
	Line  int
	// TypeOnly records `import type`, which the bundler erases. It is kept
	// because a type-only edge does not create a runtime dependency, and an
	// impact report that cannot tell the two apart over-reports.
	TypeOnly bool
	// Dynamic records `import(...)` or `require(...)`.
	Dynamic bool
}

// Decl is a top-level declaration.
type Decl struct {
	Name string
	// Kind is "function", "class", "interface", "type", "const", "enum".
	Kind string
	Line int
	// Exported reports whether the declaration leaves the module. A symbol that
	// is not exported cannot be a consumer relationship across files, so the
	// distinction keeps the graph from claiming edges that cannot exist.
	Exported bool
	// Extends and Implements are the names in the heritage clause.
	Extends    []string
	Implements []string
}

// EnvRead is a process.env lookup.
type EnvRead struct {
	Key  string
	Line int
}

// File is everything the scanner found in one source file.
type File struct {
	Imports  []Import
	Decls    []Decl
	EnvReads []EnvRead
	// Calls are identifiers used in call position. They are lexical and
	// therefore ambiguous: a local variable shadowing an import produces a
	// false positive. Every edge built from these is `inferred`.
	Calls map[string]int
}

// Parse scans one source file. It never fails: a file it cannot make sense of
// yields whatever it did recognise, because a partial graph is more useful than
// an absent one and the evidence category already says the reading is lexical.
func Parse(src string) File {
	code := strip(src)
	out := File{Calls: map[string]int{}}

	for i := 0; i < len(code); i++ {
		switch {
		case wordAt(code, i, "import"):
			if imp, next, ok := parseImport(src, code, i); ok {
				out.Imports = append(out.Imports, imp)
				i = next
				continue
			}
		case wordAt(code, i, "export"):
			// `export ... from "x"` is both an export and an import edge.
			if imp, next, ok := parseExportFrom(src, code, i); ok {
				out.Imports = append(out.Imports, imp)
				i = next
				continue
			}
			if d, next, ok := parseDecl(src, code, i, true); ok {
				out.Decls = append(out.Decls, d)
				i = next
				continue
			}
		case wordAt(code, i, "require"):
			if spec, next, ok := parseCallStringArg(src, code, i, "require"); ok {
				out.Imports = append(out.Imports, Import{
					Specifier: spec, Line: lineOf(src, i), Dynamic: true,
				})
				i = next
				continue
			}
		case wordAt(code, i, "function"), wordAt(code, i, "class"),
			wordAt(code, i, "interface"), wordAt(code, i, "enum"):
			if d, next, ok := parseDecl(src, code, i, false); ok {
				out.Decls = append(out.Decls, d)
				i = next
				continue
			}
		}

		// process.env.KEY and process.env["KEY"]
		if wordAt(code, i, "process") {
			if key, next, ok := parseEnvRead(src, code, i); ok {
				out.EnvReads = append(out.EnvReads, EnvRead{Key: key, Line: lineOf(src, i)})
				i = next
				continue
			}
		}

		// An identifier immediately followed by '(' is a call site.
		if isIdentStart(code[i]) && (i == 0 || !isIdentPart(code[i-1])) {
			name := identifierAt(code, i)
			if name == "" {
				continue
			}
			j := skipSpace(code, i+len(name))
			if j < len(code) && code[j] == '(' && !isKeyword(name) {
				if _, seen := out.Calls[name]; !seen {
					out.Calls[name] = lineOf(src, i)
				}
			}
			i += len(name) - 1
		}
	}
	return out
}

// parseImport handles every static import form plus dynamic import().
func parseImport(src, code string, i int) (Import, int, bool) {
	imp := Import{Line: lineOf(src, i)}
	j := skipSpace(code, i+len("import"))
	if j >= len(code) {
		return imp, i, false
	}

	// import("x") — dynamic.
	if code[j] == '(' {
		if spec, next, ok := parseCallStringArg(src, code, i, "import"); ok {
			return Import{Specifier: spec, Line: imp.Line, Dynamic: true}, next, true
		}
		return imp, i, false
	}

	// import "x" — side effect only.
	if q := quoteAt(code, j); q > 0 {
		spec, end, ok := readString(src, code, j)
		if !ok {
			return imp, i, false
		}
		imp.Specifier = spec
		return imp, end, true
	}

	if wordAt(code, j, "type") {
		imp.TypeOnly = true
		j = skipSpace(code, j+len("type"))
	}

	// Everything up to the `from` keyword is the binding list.
	from := indexWord(code, j, "from")
	if from < 0 {
		return imp, i, false
	}
	imp.Names = bindings(code[j:from])
	k := skipSpace(code, from+len("from"))
	spec, end, ok := readString(src, code, k)
	if !ok {
		return imp, i, false
	}
	imp.Specifier = spec
	return imp, end, true
}

// parseExportFrom handles `export {a} from "x"` and `export * from "x"`.
func parseExportFrom(src, code string, i int) (Import, int, bool) {
	j := skipSpace(code, i+len("export"))
	if j >= len(code) {
		return Import{}, i, false
	}
	// `export {a} from`, `export * from`, `export type {a} from` — anything
	// else after `export` is a declaration, not a re-export.
	reExport := code[j] == '{' || code[j] == '*' || wordAt(code, j, "type")
	if !reExport {
		return Import{}, i, false
	}
	from := indexWord(code, j, "from")
	if from < 0 {
		return Import{}, i, false
	}
	// Only look within this statement: a later `from` in another line must not
	// be captured.
	if strings.Contains(code[j:from], ";") {
		return Import{}, i, false
	}
	k := skipSpace(code, from+len("from"))
	spec, end, ok := readString(src, code, k)
	if !ok {
		return Import{}, i, false
	}
	return Import{
		Specifier: spec, Names: bindings(code[j:from]),
		Line: lineOf(src, i), TypeOnly: wordAt(code, j, "type"),
	}, end, true
}

// parseDecl reads a declaration, optionally behind `export`.
func parseDecl(src, code string, i int, exported bool) (Decl, int, bool) {
	j := i
	if exported {
		j = skipSpace(code, i+len("export"))
		if wordAt(code, j, "default") {
			j = skipSpace(code, j+len("default"))
		}
		if wordAt(code, j, "declare") {
			j = skipSpace(code, j+len("declare"))
		}
		if wordAt(code, j, "abstract") {
			j = skipSpace(code, j+len("abstract"))
		}
		if wordAt(code, j, "async") {
			j = skipSpace(code, j+len("async"))
		}
	}

	var kind string
	switch {
	case wordAt(code, j, "function"):
		kind = "function"
	case wordAt(code, j, "class"):
		kind = "class"
	case wordAt(code, j, "interface"):
		kind = "interface"
	case wordAt(code, j, "enum"):
		kind = "enum"
	case exported && wordAt(code, j, "type"):
		kind = "type"
	case exported && (wordAt(code, j, "const") || wordAt(code, j, "let") || wordAt(code, j, "var")):
		kind = "const"
	default:
		return Decl{}, i, false
	}

	k := skipSpace(code, j+len(kindKeyword(kind, code, j)))
	if k < len(code) && code[k] == '*' { // function*
		k = skipSpace(code, k+1)
	}
	name := identifierAt(code, k)
	if name == "" {
		return Decl{}, i, false
	}

	d := Decl{Name: name, Kind: kind, Line: lineOf(src, i), Exported: exported}
	if kind == "class" || kind == "interface" {
		d.Extends, d.Implements = heritage(code, k+len(name))
	}
	return d, k + len(name) - 1, true
}

// kindKeyword returns the keyword actually present, since "const"/"let"/"var"
// all map to the const kind.
func kindKeyword(kind, code string, j int) string {
	if kind != "const" {
		return kind
	}
	for _, w := range []string{"const", "let", "var"} {
		if wordAt(code, j, w) {
			return w
		}
	}
	return "const"
}

// heritage reads the extends and implements clauses that follow a class or
// interface name, stopping at the body.
func heritage(code string, i int) (ext, impl []string) {
	end := i
	depth := 0
	for end < len(code) {
		c := code[end]
		if c == '<' {
			depth++
		} else if c == '>' && depth > 0 {
			depth--
		} else if depth == 0 && (c == '{' || c == ';' || c == '\n' && !continues(code, end)) {
			break
		}
		end++
	}
	clause := code[i:end]

	if at := indexWord(clause, 0, "implements"); at >= 0 {
		impl = names(clause[at+len("implements"):])
		clause = clause[:at]
	}
	if at := indexWord(clause, 0, "extends"); at >= 0 {
		ext = names(clause[at+len("extends"):])
	}
	return ext, impl
}

// continues reports whether a heritage clause carries on past a newline, which
// it does when the next non-space token is a comma or a keyword.
func continues(code string, i int) bool {
	j := skipSpace(code, i)
	if j >= len(code) {
		return false
	}
	return code[j] == ',' || wordAt(code, j, "implements") || wordAt(code, j, "extends")
}

// names splits a comma-separated type list, keeping only the base identifier of
// each entry so that `Repo<User>` and `ns.Repo` both yield something usable.
func names(s string) []string {
	var out []string
	for _, part := range splitTopLevel(s) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if at := strings.IndexAny(part, "<("); at >= 0 {
			part = part[:at]
		}
		if at := strings.LastIndex(part, "."); at >= 0 {
			part = part[at+1:]
		}
		part = strings.TrimSpace(part)
		if part != "" && isIdentStart(part[0]) {
			out = append(out, part)
		}
	}
	return out
}

// splitTopLevel splits on commas that are not inside type arguments.
func splitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<', '(', '[':
			depth++
		case '>', ')', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// bindings extracts the imported names from a binding clause.
func bindings(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer("{", " ", "}", " ", "*", " ").Replace(s)
	var out []string
	for _, part := range splitTopLevel(s) {
		part = strings.TrimSpace(part)
		if part == "" || part == "as" {
			continue
		}
		// `a as b` binds b; the graph wants the local name.
		if at := indexWord(part, 0, "as"); at >= 0 {
			part = strings.TrimSpace(part[at+len("as"):])
		}
		if wordAt(part, 0, "type") {
			part = strings.TrimSpace(part[len("type"):])
		}
		if part != "" && isIdentStart(part[0]) {
			out = append(out, identifierAt(part, 0))
		}
	}
	return out
}

// parseCallStringArg reads fn("literal"), used for require and dynamic import.
func parseCallStringArg(src, code string, i int, fn string) (string, int, bool) {
	j := skipSpace(code, i+len(fn))
	if j >= len(code) || code[j] != '(' {
		return "", i, false
	}
	j = skipSpace(code, j+1)
	spec, end, ok := readString(src, code, j)
	if !ok {
		return "", i, false
	}
	return spec, end, true
}

// parseEnvRead reads process.env.KEY or process.env["KEY"].
func parseEnvRead(src, code string, i int) (string, int, bool) {
	j := skipSpace(code, i+len("process"))
	if j >= len(code) || code[j] != '.' {
		return "", i, false
	}
	j = skipSpace(code, j+1)
	if !wordAt(code, j, "env") {
		return "", i, false
	}
	j = skipSpace(code, j+len("env"))
	if j >= len(code) {
		return "", i, false
	}
	if code[j] == '.' {
		j = skipSpace(code, j+1)
		key := identifierAt(code, j)
		if key == "" {
			return "", i, false
		}
		return key, j + len(key) - 1, true
	}
	if code[j] == '[' {
		j = skipSpace(code, j+1)
		key, end, ok := readString(src, code, j)
		if !ok {
			return "", i, false
		}
		return key, end, true
	}
	return "", i, false
}

// readString reads a quoted literal from the ORIGINAL source, since strip
// blanked the contents. It returns the value and the index of the closing
// quote.
func readString(src, code string, i int) (string, int, bool) {
	q := quoteAt(code, i)
	if q == 0 {
		return "", i, false
	}
	if i >= len(src) || src[i] != q {
		return "", i, false
	}
	for j := i + 1; j < len(src); j++ {
		if src[j] == '\\' {
			j++
			continue
		}
		if src[j] == q {
			return src[i+1 : j], j, true
		}
		if src[j] == '\n' && q != '`' {
			return "", i, false
		}
	}
	return "", i, false
}

func quoteAt(code string, i int) byte {
	if i >= len(code) {
		return 0
	}
	switch code[i] {
	case '\'', '"', '`':
		return code[i]
	}
	return 0
}

// indexWord finds a keyword at a word boundary at or after i.
func indexWord(s string, i int, w string) int {
	for j := i; j+len(w) <= len(s); j++ {
		if wordAt(s, j, w) {
			return j
		}
	}
	return -1
}

// isKeyword filters control-flow words out of the call-site heuristic, which
// would otherwise report `if(...)` as a call to a function named if.
func isKeyword(name string) bool {
	switch name {
	case "if", "for", "while", "switch", "catch", "return", "typeof", "await",
		"function", "super", "this", "new", "delete", "void", "yield", "in", "of",
		"do", "else", "case", "throw", "import", "export", "class", "extends":
		return true
	}
	return false
}
