package golang

import (
	"go/ast"
	"go/types"
	"regexp"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// Embedded SQL (design v3 §3.2: "schema to application code | … embedded SQL
// | resolved for parsed SQL, inferred for dynamic SQL").
//
// This is the half of the schema row that makes it useful: internal/analyzers/sql
// finds what the schema *is*, and this finds which code touches it. Together
// they answer "what breaks if I drop this column", which is the question a
// schema graph exists for.
//
// The call site is type-checked, so we know for certain the string is being
// handed to a database driver. The *string* is another matter: a literal is
// `resolved`, and anything assembled at runtime is not named at all rather
// than guessed at.

// pkgInfo carries the loaded package so the SQL hook can evaluate constants.
type pkgInfo struct{ pkg *packages.Package }

// sqlSinks are the database/sql entry points that take a query string, mapped
// to the argument index carrying it.
var sqlSinks = map[string]int{
	"(*database/sql.DB).Query":             0,
	"(*database/sql.DB).QueryRow":          0,
	"(*database/sql.DB).Exec":              0,
	"(*database/sql.DB).Prepare":           0,
	"(*database/sql.DB).QueryContext":      1,
	"(*database/sql.DB).QueryRowContext":   1,
	"(*database/sql.DB).ExecContext":       1,
	"(*database/sql.DB).PrepareContext":    1,
	"(*database/sql.Tx).Query":             0,
	"(*database/sql.Tx).QueryRow":          0,
	"(*database/sql.Tx).Exec":              0,
	"(*database/sql.Tx).Prepare":           0,
	"(*database/sql.Tx).QueryContext":      1,
	"(*database/sql.Tx).QueryRowContext":   1,
	"(*database/sql.Tx).ExecContext":       1,
	"(*database/sql.Tx).PrepareContext":    1,
	"(*database/sql.Conn).QueryContext":    1,
	"(*database/sql.Conn).QueryRowContext": 1,
	"(*database/sql.Conn).ExecContext":     1,
}

// sqlSinkMethods are the same entry points recognised by method name alone,
// for the drivers that wrap database/sql with their own types — pgx, sqlx and
// the rest. Matching on name is a heuristic, so these produce `inferred`.
var sqlSinkMethods = map[string]int{
	"Query": 0, "QueryRow": 0, "Exec": 0, "Prepare": 0,
	"QueryContext": 1, "QueryRowContext": 1, "ExecContext": 1, "PrepareContext": 1,
	"Select": 1, "Get": 1, "NamedExec": 0,
}

// tableRef finds the table after FROM, JOIN, INTO or UPDATE. It is
// deliberately simple: this is a reference extractor, not a query parser, and
// a query it cannot read produces nothing rather than a wrong table.
var tableRef = regexp.MustCompile(`(?i)\b(?:FROM|JOIN|INTO|UPDATE)\s+([a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)?)`)

// looksLikeSQL reports whether a string is plausibly a query. Without this a
// literal passed to a same-named method on an unrelated type would produce
// phantom table references.
func looksLikeSQL(s string) bool {
	upper := strings.ToUpper(skipSQLComments(s))
	for _, verb := range []string{"SELECT ", "INSERT ", "UPDATE ", "DELETE ", "WITH "} {
		if strings.HasPrefix(upper, verb) {
			return true
		}
	}
	return false
}

// skipSQLComments drops leading comments and whitespace so a query that opens
// with one is still recognised.
//
// This is what §3.2 means by "sqlc generated code names": sqlc puts every query
// in a package-level constant that begins with its own directive comment,
// `-- name: GetInvoice :one`. Requiring the string to start with a verb made
// every sqlc query invisible — the call was recognised and then discarded, so a
// table change found none of the generated code that reads it. Hand-written
// queries opening with a comment were lost the same way.
func skipSQLComments(s string) string {
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
				continue
			}
			return ""
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s, "*/"); i >= 0 {
				s = s[i+2:]
				continue
			}
			return ""
		}
		return s
	}
}

// emitSQLRef recognises a database call and emits reads_schema edges to the
// tables the query names. It returns true when the call was one.
func (b *builder) emitSQLRef(p *pkgInfo, callerFQN string, ck graph.NodeKind,
	fn *types.Func, call *ast.CallExpr) bool {

	qualified := qualifiedPath(fn)
	argIdx, known := sqlSinks[qualified]
	evidence := graph.Resolved

	if !known {
		// A wrapper type: matched by method name, which is a heuristic.
		argIdx, known = sqlSinkMethods[fn.Name()]
		if !known {
			return false
		}
		evidence = graph.Inferred
	}
	if argIdx >= len(call.Args) {
		return false
	}

	query, ok := literalString(p.pkg, call.Args[argIdx])
	if !ok {
		// A query assembled at runtime. The call is recognised, but naming a
		// table would mean guessing, and a wrong schema edge makes an impact
		// report confidently incomplete.
		return true
	}
	if !looksLikeSQL(query) {
		return false
	}

	assumption := ""
	if evidence == graph.Inferred {
		assumption = "a method named " + fn.Name() + " taking a SQL-shaped literal is treated as a database call"
	}

	seen := map[string]bool{}
	for _, m := range tableRef.FindAllStringSubmatch(query, -1) {
		table := strings.ToLower(m[1])
		if seen[table] {
			continue
		}
		seen[table] = true
		b.addEdge(index.PendingEdge{
			SrcKind: ck, SrcFQN: callerFQN,
			DstKind: graph.KindTable, DstFQN: "table:" + table,
			Kind: graph.EdgeReadsSchema, Evidence: evidence,
			Attrs: attrsJSON(map[string]string{
				"via": fn.Name(), "operation": sqlVerb(query), "assumption": assumption,
			}),
		})
	}
	return true
}

func sqlVerb(query string) string {
	upper := strings.ToUpper(strings.TrimSpace(query))
	for _, verb := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "WITH"} {
		if strings.HasPrefix(upper, verb) {
			return strings.ToLower(verb)
		}
	}
	return ""
}
