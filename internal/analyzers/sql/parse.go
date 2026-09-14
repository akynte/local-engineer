package sql

import (
	"strings"
)

// Statement is one parsed DDL statement. Only the shapes that define a schema
// are represented; everything else is reported as unparsed.
type Statement struct {
	Kind StatementKind
	// Table is the object the statement acts on, schema-qualified when the
	// source qualified it.
	Table string
	// Columns are the columns a CREATE TABLE declares or an ALTER TABLE adds.
	Columns []Column
	// DroppedColumns are the columns an ALTER TABLE drops.
	DroppedColumns []string
	// Index is the index name for CREATE INDEX.
	Index string
	// IndexColumns are the columns an index covers.
	IndexColumns []string
	// References are the tables a foreign key points at.
	References []string
	// Raw is the statement's leading words, for reporting what was skipped.
	Raw string
}

// StatementKind enumerates what the parser recognises.
type StatementKind string

const (
	CreateTable StatementKind = "create_table"
	AlterTable  StatementKind = "alter_table"
	CreateIndex StatementKind = "create_index"
	CreateView  StatementKind = "create_view"
	DropTable   StatementKind = "drop_table"
	DropIndex   StatementKind = "drop_index"
	// Unparsed marks a statement the parser deliberately did not interpret.
	// It is reported rather than dropped, so the fraction of a migration the
	// graph does not cover is visible instead of invisible.
	Unparsed StatementKind = "unparsed"
)

// Column is a declared column.
type Column struct {
	Name string
	Type string
	// NotNull and PrimaryKey are the constraints that change compatibility:
	// adding a NOT NULL column without a default breaks existing writers.
	NotNull    bool
	PrimaryKey bool
	HasDefault bool
	References string
}

// Parse turns SQL text into statements.
func Parse(src string) []Statement {
	var out []Statement
	for _, stmt := range statements(lex(src)) {
		out = append(out, parseStatement(stmt))
	}
	return out
}

func parseStatement(ts []token) Statement {
	if len(ts) == 0 {
		return Statement{Kind: Unparsed}
	}
	raw := leadingWords(ts, 4)

	switch ts[0].upper {
	case "CREATE":
		return parseCreate(ts, raw)
	case "ALTER":
		if len(ts) > 1 && ts[1].upper == "TABLE" {
			return parseAlterTable(ts, raw)
		}
	case "DROP":
		return parseDrop(ts, raw)
	}
	return Statement{Kind: Unparsed, Raw: raw}
}

func parseCreate(ts []token, raw string) Statement {
	i := 1
	// CREATE [OR REPLACE] [UNIQUE] [MATERIALIZED] [TEMP|TEMPORARY] …
	for i < len(ts) {
		switch ts[i].upper {
		case "OR", "REPLACE", "UNIQUE", "MATERIALIZED", "TEMP", "TEMPORARY", "GLOBAL", "LOCAL":
			i++
			continue
		}
		break
	}
	if i >= len(ts) {
		return Statement{Kind: Unparsed, Raw: raw}
	}

	switch ts[i].upper {
	case "TABLE":
		return parseCreateTable(ts, i+1, raw)
	case "INDEX":
		return parseCreateIndex(ts, i+1, raw)
	case "VIEW":
		name, _ := qualifiedName(ts, skipIfNotExists(ts, i+1))
		return Statement{Kind: CreateView, Table: name, Raw: raw}
	}
	return Statement{Kind: Unparsed, Raw: raw}
}

func parseCreateTable(ts []token, i int, raw string) Statement {
	i = skipIfNotExists(ts, i)
	name, i := qualifiedName(ts, i)
	if name == "" {
		return Statement{Kind: Unparsed, Raw: raw}
	}
	st := Statement{Kind: CreateTable, Table: name, Raw: raw}

	// The column list is the first parenthesised group.
	open := indexOf(ts, i, "(")
	if open < 0 {
		return st // CREATE TABLE … AS SELECT, or LIKE: no column list here
	}
	close := matchParen(ts, open)
	if close < 0 {
		return st
	}

	for _, item := range splitTopLevel(ts[open+1 : close]) {
		if len(item) == 0 {
			continue
		}
		// A table-level constraint, not a column.
		switch item[0].upper {
		case "PRIMARY", "UNIQUE", "CHECK", "EXCLUDE", "CONSTRAINT":
			if ref := foreignKeyTarget(item); ref != "" {
				st.References = append(st.References, ref)
			}
			// PRIMARY KEY (a, b) marks those columns.
			if item[0].upper == "PRIMARY" {
				for _, col := range parenthesisedNames(item) {
					for j := range st.Columns {
						if st.Columns[j].Name == col {
							st.Columns[j].PrimaryKey = true
						}
					}
				}
			}
			continue
		case "FOREIGN":
			if ref := foreignKeyTarget(item); ref != "" {
				st.References = append(st.References, ref)
			}
			continue
		case "LIKE":
			continue
		}

		col := parseColumn(item)
		if col.Name != "" {
			if col.References != "" {
				st.References = append(st.References, col.References)
			}
			st.Columns = append(st.Columns, col)
		}
	}
	return st
}

func parseColumn(ts []token) Column {
	if len(ts) == 0 || (ts[0].kind != tokenWord && ts[0].kind != tokenIdent) {
		return Column{}
	}
	col := Column{Name: ts[0].text}

	// The type runs until a constraint keyword, and may carry a parenthesised
	// precision or an array suffix.
	var typeParts []string
	i := 1
	for i < len(ts) {
		u := ts[i].upper
		if isConstraintKeyword(u) {
			break
		}
		if ts[i].kind == tokenPunct && ts[i].text == "(" {
			end := matchParen(ts, i)
			if end < 0 {
				break
			}
			var inner []string
			for _, t := range ts[i+1 : end] {
				inner = append(inner, t.text)
			}
			typeParts = append(typeParts, "("+strings.Join(inner, ",")+")")
			i = end + 1
			continue
		}
		typeParts = append(typeParts, ts[i].text)
		i++
	}
	col.Type = strings.Join(typeParts, " ")

	for ; i < len(ts); i++ {
		switch ts[i].upper {
		case "NOT":
			if i+1 < len(ts) && ts[i+1].upper == "NULL" {
				col.NotNull = true
			}
		case "PRIMARY":
			col.PrimaryKey, col.NotNull = true, true
		case "DEFAULT":
			col.HasDefault = true
		case "REFERENCES":
			if name, _ := qualifiedName(ts, i+1); name != "" {
				col.References = name
			}
		case "GENERATED":
			col.HasDefault = true
		}
	}
	return col
}

func parseAlterTable(ts []token, raw string) Statement {
	i := skipIfExists(ts, 2)
	// ALTER TABLE [ONLY] name …
	if i < len(ts) && ts[i].upper == "ONLY" {
		i++
	}
	name, i := qualifiedName(ts, i)
	if name == "" {
		return Statement{Kind: Unparsed, Raw: raw}
	}
	st := Statement{Kind: AlterTable, Table: name, Raw: raw}

	for _, action := range splitTopLevel(ts[i:]) {
		if len(action) == 0 {
			continue
		}
		switch action[0].upper {
		case "ADD":
			j := 1
			if j < len(action) && action[j].upper == "COLUMN" {
				j++
			}
			if j < len(action) && action[j].upper == "CONSTRAINT" {
				if ref := foreignKeyTarget(action); ref != "" {
					st.References = append(st.References, ref)
				}
				continue
			}
			if col := parseColumn(action[j:]); col.Name != "" {
				if col.References != "" {
					st.References = append(st.References, col.References)
				}
				st.Columns = append(st.Columns, col)
			}
		case "DROP":
			j := 1
			if j < len(action) && action[j].upper == "COLUMN" {
				j++
			}
			j = skipIfExists(action, j)
			if j < len(action) {
				st.DroppedColumns = append(st.DroppedColumns, action[j].text)
			}
		}
	}
	return st
}

func parseCreateIndex(ts []token, i int, raw string) Statement {
	if i < len(ts) && ts[i].upper == "CONCURRENTLY" {
		i++
	}
	i = skipIfNotExists(ts, i)

	st := Statement{Kind: CreateIndex, Raw: raw}
	// The name is optional in PostgreSQL.
	if i < len(ts) && ts[i].upper != "ON" {
		st.Index, i = qualifiedName(ts, i)
	}
	on := indexOf(ts, i, "ON")
	if on < 0 {
		return st
	}
	st.Table, i = qualifiedName(ts, on+1)

	if open := indexOf(ts, i, "("); open >= 0 {
		if close := matchParen(ts, open); close > open {
			for _, item := range splitTopLevel(ts[open+1 : close]) {
				if len(item) > 0 && (item[0].kind == tokenWord || item[0].kind == tokenIdent) {
					st.IndexColumns = append(st.IndexColumns, item[0].text)
				}
			}
		}
	}
	return st
}

func parseDrop(ts []token, raw string) Statement {
	if len(ts) < 2 {
		return Statement{Kind: Unparsed, Raw: raw}
	}
	i := skipIfExists(ts, 2)
	name, _ := qualifiedName(ts, i)
	switch ts[1].upper {
	case "TABLE":
		return Statement{Kind: DropTable, Table: name, Raw: raw}
	case "INDEX":
		return Statement{Kind: DropIndex, Index: name, Raw: raw}
	}
	return Statement{Kind: Unparsed, Raw: raw}
}

// qualifiedName reads schema.table, returning the name and the index past it.
func qualifiedName(ts []token, i int) (string, int) {
	if i >= len(ts) || (ts[i].kind != tokenWord && ts[i].kind != tokenIdent) {
		return "", i
	}
	name := ts[i].text
	i++
	for i+1 < len(ts) && ts[i].kind == tokenPunct && ts[i].text == "." {
		if ts[i+1].kind != tokenWord && ts[i+1].kind != tokenIdent {
			break
		}
		name += "." + ts[i+1].text
		i += 2
	}
	return name, i
}

func skipIfNotExists(ts []token, i int) int {
	if i+2 < len(ts) && ts[i].upper == "IF" && ts[i+1].upper == "NOT" && ts[i+2].upper == "EXISTS" {
		return i + 3
	}
	return i
}

func skipIfExists(ts []token, i int) int {
	if i+1 < len(ts) && ts[i].upper == "IF" && ts[i+1].upper == "EXISTS" {
		return i + 2
	}
	return i
}

// indexOf finds the next occurrence of a token at the top nesting level.
//
// The match is tested *before* the depth is updated. Testing after would make
// an opening parenthesis unfindable: it increments the depth it is then
// checked against, so it could never be at level zero.
func indexOf(ts []token, from int, what string) int {
	depth := 0
	for i := from; i < len(ts); i++ {
		if depth == 0 && (ts[i].text == what || ts[i].upper == what) {
			return i
		}
		if ts[i].kind == tokenPunct {
			switch ts[i].text {
			case "(":
				depth++
			case ")":
				depth--
			}
		}
	}
	return -1
}

func matchParen(ts []token, open int) int {
	depth := 0
	for i := open; i < len(ts); i++ {
		if ts[i].kind != tokenPunct {
			continue
		}
		switch ts[i].text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitTopLevel splits on commas outside parentheses, which is how a column
// list and an ALTER action list are both structured.
func splitTopLevel(ts []token) [][]token {
	var out [][]token
	var current []token
	depth := 0
	for _, t := range ts {
		if t.kind == tokenPunct {
			switch t.text {
			case "(":
				depth++
			case ")":
				depth--
			case ",":
				if depth == 0 {
					out = append(out, current)
					current = nil
					continue
				}
			}
		}
		current = append(current, t)
	}
	if len(current) > 0 {
		out = append(out, current)
	}
	return out
}

// foreignKeyTarget extracts the table a REFERENCES clause points at.
func foreignKeyTarget(ts []token) string {
	if i := indexOf(ts, 0, "REFERENCES"); i >= 0 {
		name, _ := qualifiedName(ts, i+1)
		return name
	}
	return ""
}

// parenthesisedNames returns the identifiers in the first parenthesised group.
func parenthesisedNames(ts []token) []string {
	open := indexOf(ts, 0, "(")
	if open < 0 {
		return nil
	}
	close := matchParen(ts, open)
	if close < 0 {
		return nil
	}
	var out []string
	for _, item := range splitTopLevel(ts[open+1 : close]) {
		if len(item) > 0 {
			out = append(out, item[0].text)
		}
	}
	return out
}

func isConstraintKeyword(u string) bool {
	switch u {
	case "NOT", "NULL", "PRIMARY", "UNIQUE", "DEFAULT", "REFERENCES", "CHECK",
		"COLLATE", "CONSTRAINT", "GENERATED", "IDENTITY", "DEFERRABLE":
		return true
	}
	return false
}

func leadingWords(ts []token, n int) string {
	var parts []string
	for i := 0; i < len(ts) && i < n; i++ {
		parts = append(parts, ts[i].text)
	}
	return strings.Join(parts, " ")
}
