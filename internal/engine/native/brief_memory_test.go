package native

import (
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/memory"
	"github.com/akynte/local-engineer/internal/retrieval"
)

// Carrying notes in the packet struct is not the same as the model seeing
// them. Every wiring defect in this repository has had that shape: a value
// computed correctly and never reaching its consumer. This asserts the last
// step.
func TestNotesReachTheModelsBrief(t *testing.T) {
	e := &Engine{}
	req := engine.Request{
		Objective: "do the thing",
		Packet: &retrieval.Packet{
			Notes: []memory.Note{
				{Kind: memory.KindAdvice, Text: "NOTE-MARKER: never renumber a migration",
					Provenance: memory.Provenance{Source: "SOURCE-MARKER"}},
			},
			NotesDropped: 2,
		},
	}

	brief := e.brief(req)

	if !strings.Contains(brief, "NOTE-MARKER") {
		t.Error("the note is in the packet but never reaches the model")
	}
	// A rule whose source is invisible cannot be judged (§11).
	if !strings.Contains(brief, "SOURCE-MARKER") {
		t.Error("the note's provenance was dropped on the way to the model")
	}
	// The kind has to survive: an observation read as advice is how a one-off
	// becomes a law.
	if !strings.Contains(brief, string(memory.KindAdvice)) {
		t.Error("the note's kind was dropped, so every note reads alike")
	}
	// A playbook quietly losing its tail is how advice stops matching what
	// people think the system was told.
	if !strings.Contains(brief, "2 more note") {
		t.Error("dropped notes are not reported to the model")
	}
}

// Notes are the stable part of a packet, and §8.2 puts the stable prefix first
// so the provider's prompt cache survives the tool loop.
func TestNotesComeBeforeTheCode(t *testing.T) {
	e := &Engine{}
	req := engine.Request{
		Objective: "do the thing",
		Packet: &retrieval.Packet{
			Notes:  []memory.Note{{Kind: memory.KindAdvice, Text: "NOTE-MARKER"}},
			Slices: []retrieval.Slice{{Path: "CODE-MARKER.go", Body: "package p"}},
		},
	}

	brief := e.brief(req)
	if strings.Index(brief, "NOTE-MARKER") > strings.Index(brief, "CODE-MARKER") {
		t.Error("the code is rendered before the notes")
	}
}

// A packet with no notes must not grow an empty heading: a section that is
// usually empty is a section the reader learns to skip.
func TestNoHeadingWithoutNotes(t *testing.T) {
	e := &Engine{}
	brief := e.brief(engine.Request{Objective: "x", Packet: &retrieval.Packet{}})
	if strings.Contains(brief, "recorded") {
		t.Error("an empty memory section was rendered")
	}
}
