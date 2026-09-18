package retrieval

import (
	"math"
	"testing"
)

func TestPageRankUsesReferencesAndConservesMass(t *testing.T) {
	r := pageRank(3, [][2]int{{0, 2}, {1, 2}})
	if r[2] <= r[0] || r[2] <= r[1] {
		t.Fatalf("referenced declaration not ranked first: %v", r)
	}
	if math.Abs(r[0]+r[1]+r[2]-1) > 1e-9 {
		t.Fatalf("rank mass lost: %v", r)
	}
}
