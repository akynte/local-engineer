package retrieval

import (
	"path/filepath"
	"sort"
	"strings"
)

// Context-retrieval misses (design v3 §8.3: "Context-retrieval misses — a
// needed symbol or consumer absent from the packet, discovered later by a
// failure — are a primary metric").
//
// The definition is what makes this measurable rather than a feeling. A miss is
// not "the model did badly" and not "verification failed". It is specifically:
// the packet did not contain a file, the model could not have known about it,
// and a later failure pointed at exactly that file.
//
// That distinction matters because the two failure modes need opposite fixes. A
// model that had the right slice and misused it is a model problem, and a
// bigger packet makes it worse. A model that never saw the slice is a retrieval
// problem, and no amount of prompting fixes it. Counting them together produces
// a number that cannot drive either decision, which is why §8.3 makes this one
// its own metric.

// Miss is one file a failure pointed at that the packet did not carry.
type Miss struct {
	// Path is the file the failure named.
	Path string `json:"path"`
	// Detail is the finding that revealed it, so a miss is diagnosable rather
	// than a bare count.
	Detail string `json:"detail"`
	// Recipe is which check found it.
	Recipe string `json:"recipe"`
}

// Failure is the shape this needs from a verification result. It is declared
// here rather than importing internal/recipe so that retrieval does not depend
// on the verification layer: the direction of that dependency is what would
// make packets aware of recipes.
type Failure struct {
	Recipe   string
	Findings []FailureFinding
}

// FailureFinding is one located problem.
type FailureFinding struct {
	File    string
	Message string
}

// DetectMisses reports which failures pointed at files the packet did not hold.
//
// Paths are compared after normalisation, and a finding with no file is skipped
// rather than guessed at: a miss that might not be one is worse than an
// uncounted miss, because this number is supposed to drive a decision about
// retrieval.
func DetectMisses(pkt *Packet, failures []Failure) []Miss {
	if pkt == nil {
		return nil
	}
	carried := map[string]bool{}
	for _, s := range pkt.Slices {
		carried[normalisePath(s.Path)] = true
	}

	seen := map[string]bool{}
	var out []Miss
	for _, f := range failures {
		for _, finding := range f.Findings {
			path := normalisePath(finding.File)
			if path == "" || carried[path] {
				continue
			}
			// A file the packet did not carry, named by something that failed.
			// The model could not have accounted for it.
			key := f.Recipe + "\x00" + path
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Miss{
				Path: path, Recipe: f.Recipe,
				Detail: strings.TrimSpace(finding.Message),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Recipe < out[j].Recipe
	})
	return out
}

// normalisePath makes two spellings of the same file compare equal. A miss
// reported because one side said "./x.go" and the other "x.go" would be a false
// positive in the metric that decides whether retrieval needs work.
func normalisePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(filepath.Clean(p))
	p = strings.TrimPrefix(p, "./")
	return p
}

// MissRate is misses over packets built, which is the form §8.3 wants: a count
// alone says nothing without knowing how many chances there were.
type MissRate struct {
	Packets int `json:"packets"`
	Misses  int `json:"misses"`
}

// Rate returns misses per packet, or 0 when nothing has been built.
func (m MissRate) Rate() float64 {
	if m.Packets == 0 {
		return 0
	}
	return float64(m.Misses) / float64(m.Packets)
}
