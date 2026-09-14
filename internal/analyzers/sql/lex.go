// Package sql contributes the schema row of design v3 §3.2: "schema to
// application code | pg_query_go on migrations and embedded SQL | resolved
// for parsed SQL, inferred for dynamic SQL".
//
// # A deviation from the design, and why
//
// The design names pg_query_go, which embeds PostgreSQL's own parser through
// cgo. That would give complete fidelity, and it would also end the CGO-free
// build — which is load-bearing: DR-1 ships one static binary, and the images
// are built for amd64 and arm64 from a single runner.
//
// What is here instead is a focused DDL parser covering the statements that
// actually define a schema: CREATE TABLE, ALTER TABLE, CREATE INDEX, CREATE
// VIEW and their DROPs. It is deliberately narrow, and what it cannot parse it
// reports as unparsed rather than guessing — so the graph says "not
// discovered" instead of describing a schema that is not there.
//
// If the unparsed fraction ever turns out to matter, pg_query_go behind a
// build tag is the replacement path, and the CGO-free default stays intact.
package sql

import (
	"strings"
	"unicode"
)

// tokenKind classifies a lexeme.
type tokenKind int

const (
	tokenEOF tokenKind = iota
	tokenWord
	tokenString // a quoted literal
	tokenIdent  // a "quoted identifier"
	tokenNumber
	tokenPunct
)

type token struct {
	kind tokenKind
	text string
	// upper is the uppercased text, cached because keyword comparison is the
	// hot path of the parser.
	upper string
}

// lex turns SQL into tokens, handling the three comment forms and the string
// quoting rules that make a naive split on whitespace wrong.
//
// Getting this right matters more than it looks: a semicolon inside a string
// literal or a dollar-quoted function body would otherwise split a statement
// in half, and the parser would read the remainder as a new statement.
func lex(src string) []token {
	var out []token
	runes := []rune(src)
	i := 0

	for i < len(runes) {
		c := runes[i]

		switch {
		case unicode.IsSpace(c):
			i++

		// -- line comment
		case c == '-' && i+1 < len(runes) && runes[i+1] == '-':
			for i < len(runes) && runes[i] != '\n' {
				i++
			}

		// /* block comment */, which nests in PostgreSQL
		case c == '/' && i+1 < len(runes) && runes[i+1] == '*':
			depth := 1
			i += 2
			for i < len(runes) && depth > 0 {
				if runes[i] == '/' && i+1 < len(runes) && runes[i+1] == '*' {
					depth++
					i += 2
					continue
				}
				if runes[i] == '*' && i+1 < len(runes) && runes[i+1] == '/' {
					depth--
					i += 2
					continue
				}
				i++
			}

		// 'string literal', where '' is an escaped quote
		case c == '\'':
			start := i
			i++
			for i < len(runes) {
				if runes[i] == '\'' {
					if i+1 < len(runes) && runes[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			out = append(out, token{kind: tokenString, text: string(runes[start:i])})

		// "quoted identifier"
		case c == '"':
			start := i
			i++
			for i < len(runes) {
				if runes[i] == '"' {
					if i+1 < len(runes) && runes[i+1] == '"' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			text := string(runes[start:i])
			text = strings.TrimSuffix(strings.TrimPrefix(text, `"`), `"`)
			text = strings.ReplaceAll(text, `""`, `"`)
			out = append(out, token{kind: tokenIdent, text: text, upper: strings.ToUpper(text)})

		// $tag$ dollar-quoted body $tag$ — how function bodies are written,
		// and full of semicolons that are not statement separators.
		case c == '$':
			if tag, end, ok := dollarQuote(runes, i); ok {
				out = append(out, token{kind: tokenString, text: tag})
				i = end
				continue
			}
			out = append(out, token{kind: tokenPunct, text: "$"})
			i++

		case unicode.IsDigit(c):
			start := i
			for i < len(runes) && (unicode.IsDigit(runes[i]) || runes[i] == '.') {
				i++
			}
			out = append(out, token{kind: tokenNumber, text: string(runes[start:i])})

		case unicode.IsLetter(c) || c == '_':
			start := i
			for i < len(runes) && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) ||
				runes[i] == '_' || runes[i] == '$') {
				i++
			}
			text := string(runes[start:i])
			out = append(out, token{kind: tokenWord, text: text, upper: strings.ToUpper(text)})

		default:
			out = append(out, token{kind: tokenPunct, text: string(c)})
			i++
		}
	}
	return append(out, token{kind: tokenEOF})
}

// dollarQuote matches a $tag$…$tag$ body starting at i, returning the whole
// span and the index just past it.
func dollarQuote(runes []rune, i int) (body string, end int, ok bool) {
	j := i + 1
	for j < len(runes) && (unicode.IsLetter(runes[j]) || unicode.IsDigit(runes[j]) || runes[j] == '_') {
		j++
	}
	if j >= len(runes) || runes[j] != '$' {
		return "", 0, false
	}
	tag := string(runes[i : j+1]) // includes both dollars
	rest := string(runes[j+1:])
	closeAt := strings.Index(rest, tag)
	if closeAt < 0 {
		// Unterminated: treat the remainder as the body rather than
		// mis-splitting the file on a semicolon inside it.
		return rest, len(runes), true
	}
	return rest[:closeAt], j + 1 + closeAt + len([]rune(tag)), true
}

// statements splits a token stream on top-level semicolons.
func statements(tokens []token) [][]token {
	var out [][]token
	var current []token
	depth := 0

	for _, t := range tokens {
		switch {
		case t.kind == tokenEOF:
			if len(current) > 0 {
				out = append(out, current)
			}
			return out
		case t.kind == tokenPunct && t.text == "(":
			depth++
		case t.kind == tokenPunct && t.text == ")":
			depth--
		case t.kind == tokenPunct && t.text == ";" && depth <= 0:
			if len(current) > 0 {
				out = append(out, current)
				current = nil
			}
			continue
		}
		current = append(current, t)
	}
	if len(current) > 0 {
		out = append(out, current)
	}
	return out
}
