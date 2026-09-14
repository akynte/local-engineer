package retrieval

// Fuzz targets for the code that parses untrusted-shaped input. User queries
// reach an FTS5 MATCH expression, so a query that escapes into FTS5 syntax
// would be both a crash and an injection.

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzFTSQuery(f *testing.F) {
	for _, seed := range []string{
		"GetUser", "user service", `"quoted"`, "a OR b", "NEAR(a b)", "*", "a*",
		"col:value", "-negated", "^caret", "a AND NOT b", "", "  ", "ünïcödé",
		`"; DROP TABLE chunks; --`, strings.Repeat("a", 4096),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		got := ftsQuery(in)
		if got == "" {
			return // an empty expression is a valid outcome
		}
		if !utf8.ValidString(got) {
			t.Fatalf("produced invalid UTF-8 from %q", in)
		}
		// Every term must be quoted, so no input can be read as FTS5 syntax.
		// Splitting on " OR " is safe because that is the only separator the
		// builder emits.
		for _, term := range strings.Split(got, " OR ") {
			if !strings.HasPrefix(term, `"`) || !strings.HasSuffix(term, `"`) {
				t.Fatalf("term %q from input %q is not quoted", term, in)
			}
			inner := term[1 : len(term)-1]
			// Any quote inside must be doubled, which is FTS5's escape.
			for i := 0; i < len(inner); i++ {
				if inner[i] != '"' {
					continue
				}
				if i+1 >= len(inner) || inner[i+1] != '"' {
					t.Fatalf("unescaped quote in %q from input %q", term, in)
				}
				i++
			}
		}
	})
}

func FuzzSliceValidate(f *testing.F) {
	f.Add("ws", "repo", "path.go", "hash", 1, "lexical_anchor")
	f.Add("", "", "", "", 0, "")

	f.Fuzz(func(t *testing.T, ws, repo, path, hash string, indexVersion int, origin string) {
		s := Slice{
			WorkspaceID: idOf(ws), RepositoryID: repo, Path: path,
			ContentHash: hash, IndexVersion: indexVersion, Origin: Origin(origin),
		}
		err := s.Validate()
		// Validate must never panic, and must reject anything missing a
		// provenance field: that is the §2.3 contract.
		complete := ws != "" && repo != "" && path != "" && hash != "" && indexVersion != 0 && origin != ""
		if complete && err != nil {
			t.Fatalf("a complete slice was rejected: %v", err)
		}
		if !complete && err == nil {
			t.Fatalf("an incomplete slice was accepted: %+v", s)
		}
	})
}
