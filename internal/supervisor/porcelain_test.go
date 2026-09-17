package supervisor

import (
	"reflect"
	"testing"
)

// The first two columns of porcelain output are status codes, and a file
// modified but not staged begins with a space. Trimming the whole blob before
// splitting shifts that line left and eats the first character of the path —
// which reported a modified limit.go as "imit.go", a defect that reads as a
// rendering quirk until someone tries to open the file.
func TestPorcelainKeepsTheFirstPathIntact(t *testing.T) {
	got := parsePorcelain(" M limit.go\n?? .le/\n")
	want := []string{".le/", "limit.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsePorcelain = %q, want %q", got, want)
	}
}

// Every status code keeps its path whole, whichever column is occupied.
func TestPorcelainHandlesEveryStatusShape(t *testing.T) {
	got := parsePorcelain("M  staged.go\n M unstaged.go\nMM both.go\nA  added.go\n?? new.go\n D gone.go\n")
	want := []string{"added.go", "both.go", "gone.go", "new.go", "staged.go", "unstaged.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsePorcelain = %q, want %q", got, want)
	}
}

// A rename reports both names; the one that exists now is the useful one.
func TestPorcelainReportsTheNewNameOfARename(t *testing.T) {
	got := parsePorcelain("R  old/name.go -> new/name.go\n")
	want := []string{"new/name.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsePorcelain = %q, want %q", got, want)
	}
}

// A clean tree has no changes, not one empty path.
func TestPorcelainOnACleanTreeIsEmpty(t *testing.T) {
	if got := parsePorcelain(""); len(got) != 0 {
		t.Errorf("a clean tree produced %q", got)
	}
}
