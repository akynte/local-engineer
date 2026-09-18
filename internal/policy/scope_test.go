package policy

import "testing"

func TestScopeMatchesWithoutEscaping(t *testing.T) {
	for _, tc := range []struct {
		pattern, file string
		want          bool
	}{
		{"src", "src/sub/a.go", true},
		{"src", "src-other/a.go", false},
		{"src/*.go", "src/a.go", true},
		{"src/*.go", "src/sub/a.go", false},
		{"src/**", "src/sub/a.go", true},
		{".", "src/a.go", true},
		{"../src", "src/a.go", false},
		{"/src", "src/a.go", false},
		{"[", "src/a.go", false},
		{"", "src/a.go", false},
	} {
		if got := Covers([]string{tc.pattern}, tc.file); got != tc.want {
			t.Errorf("%q against %q: %v", tc.pattern, tc.file, got)
		}
	}
}
