package retrieval

import "testing"

func graphSlice(score float64, body string) Slice {
	return Slice{
		WorkspaceID: "w", RepositoryID: "r", Path: "p.go", ContentHash: "h",
		IndexVersion: 1, Origin: OriginGraph, Score: score, Body: body,
	}
}

func anchorSlice(score float64, body string) Slice {
	s := graphSlice(score, body)
	s.Origin = OriginAnchor
	return s
}

// The score was computed for every slice and nothing read it, so the packet was
// filled in whatever order things were appended. A worse neighbour could
// displace a better one purely by traversal order.
func TestSlicesAreOrderedBestFirstWithinAnOrigin(t *testing.T) {
	slices := []Slice{
		graphSlice(0.25, "far"),
		graphSlice(0.50, "near"),
		graphSlice(0.10, "farther"),
	}
	sortByScore(slices)
	if slices[0].Body != "near" || slices[2].Body != "farther" {
		t.Errorf("expansion was not ordered by score: %q, %q, %q",
			slices[0].Body, slices[1].Body, slices[2].Body)
	}
}

// A lexical hit on the query is a stronger signal than being adjacent to one,
// and bm25 and inverse depth are not the same scale — so the groups must not be
// interleaved by raw score.
func TestAnchorsStayAheadOfExpansion(t *testing.T) {
	slices := []Slice{
		anchorSlice(0.9, "anchor"),
		graphSlice(0.5, "expanded"),
	}
	sortByScore(slices)
	if slices[0].Origin != OriginAnchor {
		t.Error("expansion was ordered ahead of a lexical anchor")
	}
	// A high-scoring anchor must not be reordered behind a graph slice even
	// when bm25 happens to produce a smaller number.
	slices = []Slice{anchorSlice(0.01, "anchor"), graphSlice(0.5, "expanded")}
	sortByScore(slices)
	if slices[0].Origin != OriginAnchor {
		t.Error("a low bm25 score moved an anchor behind expansion; the scales are not comparable")
	}
}
