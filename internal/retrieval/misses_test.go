package retrieval_test

import (
	"testing"

	"github.com/akynte/local-engineer/internal/retrieval"
)

func packetWith(paths ...string) *retrieval.Packet {
	p := &retrieval.Packet{}
	for _, path := range paths {
		p.Slices = append(p.Slices, retrieval.Slice{Path: path})
	}
	return p
}

// §8.3's definition: a needed file absent from the packet, discovered later by
// a failure. This is the case the metric exists for — retrieval did not supply
// what the work needed, and no amount of prompting would have fixed it.
func TestFailureInAFileThePacketLackedIsAMiss(t *testing.T) {
	pkt := packetWith("internal/service/user.go")
	misses := retrieval.DetectMisses(pkt, []retrieval.Failure{{
		Recipe: "go build",
		Findings: []retrieval.FailureFinding{
			{File: "internal/handler/user.go", Message: "not enough arguments in call to Email"},
		},
	}})
	if len(misses) != 1 {
		t.Fatalf("got %d misses, want 1: %+v", len(misses), misses)
	}
	if misses[0].Path != "internal/handler/user.go" {
		t.Errorf("wrong path: %s", misses[0].Path)
	}
	// A bare count cannot drive a decision; the finding has to come with it.
	if misses[0].Detail == "" || misses[0].Recipe == "" {
		t.Errorf("the miss is not diagnosable: %+v", misses[0])
	}
}

// The opposite case, and the reason the two are counted separately: the model
// had the file and still got it wrong. That is a model problem, and a bigger
// packet makes it worse rather than better.
func TestFailureInAFileThePacketCarriedIsNotAMiss(t *testing.T) {
	pkt := packetWith("internal/service/user.go", "internal/handler/user.go")
	misses := retrieval.DetectMisses(pkt, []retrieval.Failure{{
		Recipe: "go build",
		Findings: []retrieval.FailureFinding{
			{File: "internal/handler/user.go", Message: "not enough arguments"},
		},
	}})
	if len(misses) != 0 {
		t.Errorf("a file the packet carried was counted as a retrieval miss: %+v", misses)
	}
}

// Two spellings of one path must not produce a phantom miss: a false positive
// here would push someone to fix retrieval that is working.
func TestPathSpellingDoesNotCreateAPhantomMiss(t *testing.T) {
	pkt := packetWith("internal/a/a.go")
	for _, spelling := range []string{"./internal/a/a.go", "internal/a/../a/a.go", "internal/a/a.go"} {
		misses := retrieval.DetectMisses(pkt, []retrieval.Failure{{
			Recipe:   "go vet",
			Findings: []retrieval.FailureFinding{{File: spelling, Message: "x"}},
		}})
		if len(misses) != 0 {
			t.Errorf("%q was treated as a different file: %+v", spelling, misses)
		}
	}
}

// A finding with no file cannot be attributed. Guessing would put a number that
// might be wrong into the metric that decides whether retrieval needs work.
func TestFindingWithoutAFileIsNotCounted(t *testing.T) {
	misses := retrieval.DetectMisses(packetWith("a.go"), []retrieval.Failure{{
		Recipe:   "go test",
		Findings: []retrieval.FailureFinding{{Message: "FAIL\texample.com/x\t0.01s"}},
	}})
	if len(misses) != 0 {
		t.Errorf("an unattributable finding was counted: %+v", misses)
	}
}

// The same file failing twice in one recipe is one miss, not two: the metric
// counts what retrieval failed to supply, not how loudly it was noticed.
func TestRepeatedFindingsInOneFileCountOnce(t *testing.T) {
	misses := retrieval.DetectMisses(packetWith("a.go"), []retrieval.Failure{{
		Recipe: "go build",
		Findings: []retrieval.FailureFinding{
			{File: "b.go", Message: "first"},
			{File: "b.go", Message: "second"},
		},
	}})
	if len(misses) != 1 {
		t.Errorf("got %d misses for one file, want 1: %+v", len(misses), misses)
	}
}

func TestRateIsMissesOverPackets(t *testing.T) {
	r := retrieval.MissRate{Packets: 4, Misses: 1}
	if got := r.Rate(); got != 0.25 {
		t.Errorf("rate = %v, want 0.25", got)
	}
	// A count with no denominator says nothing, and dividing by zero says less.
	if (retrieval.MissRate{}).Rate() != 0 {
		t.Error("an empty rate is not zero")
	}
}

func TestNoPacketMeansNothingToJudge(t *testing.T) {
	if m := retrieval.DetectMisses(nil, []retrieval.Failure{{Recipe: "x",
		Findings: []retrieval.FailureFinding{{File: "a.go", Message: "m"}}}}); m != nil {
		t.Errorf("misses were reported with no packet to compare against: %+v", m)
	}
}
