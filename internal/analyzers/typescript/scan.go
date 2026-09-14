package typescript

import (
	"strings"
)

// strip removes comments and string bodies, replacing them with spaces so that
// every byte offset and line number is preserved.
//
// It exists because every pattern below would otherwise match inside a comment
// or a string literal. An analyzer that reports an import because the word
// appeared in a doc comment is worse than one that reports nothing: the graph's
// value is that an edge in it means something.
func strip(src string) string {
	out := []byte(src)
	const (
		code = iota
		lineComment
		blockComment
		single
		double
		backtick
		regex
	)
	state := code
	// prev is the last significant code byte, used to tell a division from the
	// start of a regular expression.
	var prev byte
	for i := 0; i < len(out); i++ {
		c := out[i]
		switch state {
		case code:
			switch {
			case c == '/' && i+1 < len(out) && out[i+1] == '/':
				state = lineComment
				out[i], out[i+1] = ' ', ' '
				i++
			case c == '/' && i+1 < len(out) && out[i+1] == '*':
				state = blockComment
				out[i], out[i+1] = ' ', ' '
				i++
			case c == '\'':
				state = single
			case c == '"':
				state = double
			case c == '`':
				state = backtick
			case c == '/' && startsRegex(prev):
				state = regex
			}
			if !isSpace(c) {
				prev = c
			}
		case lineComment:
			if c == '\n' {
				state = code
			} else {
				out[i] = ' '
			}
		case blockComment:
			if c == '*' && i+1 < len(out) && out[i+1] == '/' {
				out[i], out[i+1] = ' ', ' '
				i++
				state = code
			} else if c != '\n' {
				out[i] = ' '
			}
		case single, double, backtick, regex:
			if c == '\\' {
				// Blank the escape and what it escapes, so a closing quote
				// inside an escape does not end the literal early.
				if i+1 < len(out) {
					if out[i+1] != '\n' {
						out[i+1] = ' '
					}
					out[i] = ' '
					i++
				}
				continue
			}
			closes := (state == single && c == '\'') ||
				(state == double && c == '"') ||
				(state == backtick && c == '`') ||
				(state == regex && c == '/') ||
				(state != backtick && state != regex && c == '\n')
			if closes {
				state = code
				prev = c
				continue
			}
			if c != '\n' {
				out[i] = ' '
			}
		}
	}
	return string(out)
}

// startsRegex reports whether a '/' at this position begins a regular
// expression rather than a division. The test is the usual one: a regex can
// only follow an operator or an opening bracket, never a value.
func startsRegex(prev byte) bool {
	switch prev {
	case 0, '(', ',', '=', ':', '[', '!', '&', '|', '?', '{', '}', ';', '+', '-', '*', '%', '<', '>', '~', '^':
		return true
	}
	return false
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// lineOf reports the 1-based line containing a byte offset.
func lineOf(src string, offset int) int {
	if offset < 0 || offset > len(src) {
		return 0
	}
	return strings.Count(src[:offset], "\n") + 1
}

// identifierAt reads the identifier starting at i, or "" if there is none.
func identifierAt(s string, i int) string {
	if i >= len(s) || !isIdentStart(s[i]) {
		return ""
	}
	j := i
	for j < len(s) && isIdentPart(s[j]) {
		j++
	}
	return s[i:j]
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

// skipSpace advances past whitespace.
func skipSpace(s string, i int) int {
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	return i
}

// wordAt reports whether s has the keyword w at i, bounded so that "classy"
// does not match "class".
func wordAt(s string, i int, w string) bool {
	if i+len(w) > len(s) || s[i:i+len(w)] != w {
		return false
	}
	if i > 0 && isIdentPart(s[i-1]) {
		return false
	}
	if i+len(w) < len(s) && isIdentPart(s[i+len(w)]) {
		return false
	}
	return true
}
