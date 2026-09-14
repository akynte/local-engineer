package memory_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akynte/local-engineer/internal/memory"
)

func store(t *testing.T) (*memory.Store, string) {
	t.Helper()
	dir := t.TempDir()
	return memory.Open(dir, memory.DefaultCaps()), dir
}

// A rule with no source cannot be judged, and a system that writes rules about
// its own work will write flattering ones. Provenance is not optional.
func TestNoteWithoutProvenanceIsRefused(t *testing.T) {
	s, _ := store(t)
	_, err := s.Add(memory.Note{Kind: memory.KindAdvice, Text: "always retry twice"})
	if !errors.Is(err, memory.ErrNoProvenance) {
		t.Fatalf("got %v, want ErrNoProvenance", err)
	}
}

func TestNotesRoundTrip(t *testing.T) {
	s, dir := store(t)
	added, err := s.Add(memory.Note{
		Kind: memory.KindAdvice, Text: "the flaky test needs a real clock",
		Provenance: memory.Provenance{Source: "task-42", Evidence: "ev-7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if added.ID == "" {
		t.Error("no id was assigned")
	}
	// It lives in the repository, so it travels with it (§2.2).
	if _, err := os.Stat(filepath.Join(dir, ".le", "memory", "advice.yaml")); err != nil {
		t.Fatalf("notes are not kept in the repository: %v", err)
	}

	back, err := s.List(memory.KindAdvice)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0].Text != added.Text {
		t.Fatalf("round trip lost the note: %+v", back)
	}
	if back[0].Provenance.Source != "task-42" {
		t.Errorf("provenance was not kept: %+v", back[0].Provenance)
	}
}

// Kinds are separate files and must not bleed into one another: letting an
// observation drift into advice is how a one-off becomes a law.
func TestKindsAreSeparate(t *testing.T) {
	s, _ := store(t)
	for _, k := range memory.Kinds() {
		if _, err := s.Add(memory.Note{
			Kind: k, Text: "a " + string(k) + " note",
			Provenance: memory.Provenance{Source: "operator"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range memory.Kinds() {
		notes, err := s.List(k)
		if err != nil {
			t.Fatal(err)
		}
		if len(notes) != 1 {
			t.Errorf("%s has %d notes, want 1", k, len(notes))
		}
		if notes[0].Kind != k {
			t.Errorf("a %s note came back as %s", k, notes[0].Kind)
		}
	}
}

// A playbook that grows without bound stops being read and starts being
// pasted, and its oldest entries are the most likely to be stale.
func TestCapDropsTheOldestFirst(t *testing.T) {
	s := memory.Open(t.TempDir(), memory.Caps{PerKind: 3, MaxTextBytes: 1000})
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		if _, err := s.Add(memory.Note{
			Kind: memory.KindAdvice, Text: "rule " + string(rune('A'+i)),
			Provenance: memory.Provenance{Source: "operator", At: base.Add(time.Duration(i) * time.Minute)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	notes, err := s.List(memory.KindAdvice)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 3 {
		t.Fatalf("cap of 3 kept %d notes", len(notes))
	}
	if notes[0].Text != "rule C" {
		t.Errorf("the oldest notes were not dropped first: %s is still here", notes[0].Text)
	}
}

func TestOverlongNoteIsRefused(t *testing.T) {
	s := memory.Open(t.TempDir(), memory.Caps{PerKind: 10, MaxTextBytes: 20})
	_, err := s.Add(memory.Note{
		Kind: memory.KindObservation, Text: strings.Repeat("x", 100),
		Provenance: memory.Provenance{Source: "operator"},
	})
	if !errors.Is(err, memory.ErrTooLong) {
		t.Fatalf("got %v, want ErrTooLong", err)
	}
}

func TestRemove(t *testing.T) {
	s, _ := store(t)
	n, err := s.Add(memory.Note{
		Kind: memory.KindIntent, Text: "ship the billing migration",
		Provenance: memory.Provenance{Source: "operator"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := s.Remove(memory.KindIntent, n.ID)
	if err != nil || !ok {
		t.Fatalf("remove reported %v, %v", ok, err)
	}
	notes, _ := s.List(memory.KindIntent)
	if len(notes) != 0 {
		t.Errorf("the note survived removal: %+v", notes)
	}
	if ok, _ := s.Remove(memory.KindIntent, "nope"); ok {
		t.Error("removing a missing note reported success")
	}
}

// §2.2: cross-project lessons are "an explicit opt-in feature that copies text
// you have read, never an automatic channel". An unconfirmed import is the
// automatic channel, so it is refused.
func TestUnconfirmedImportIsRefused(t *testing.T) {
	s, _ := store(t)
	b := memory.Bundle{Version: memory.BundleVersion, From: "other-repo", Notes: []memory.Note{{
		Kind: memory.KindAdvice, Text: "prefer table tests",
		Provenance: memory.Provenance{Source: "task-1"},
	}}}
	if _, err := s.Import(b, memory.ImportOptions{}); !errors.Is(err, memory.ErrNotConfirmed) {
		t.Fatalf("got %v, want ErrNotConfirmed", err)
	}
	notes, _ := s.List(memory.KindAdvice)
	if len(notes) != 0 {
		t.Error("an unconfirmed import copied notes anyway")
	}
}

// A rule learned elsewhere must never read as one this repository established.
func TestImportedNotesCarryTheirOrigin(t *testing.T) {
	s, _ := store(t)
	b := memory.Bundle{Version: memory.BundleVersion, From: "billing-service", Notes: []memory.Note{{
		Kind: memory.KindAdvice, Text: "prefer table tests",
		Provenance: memory.Provenance{Source: "task-1"},
	}}}
	added, err := s.Import(b, memory.ImportOptions{Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 {
		t.Fatalf("imported %d notes, want 1", len(added))
	}
	src := added[0].Provenance.Source
	if !strings.Contains(src, "imported") || !strings.Contains(src, "billing-service") {
		t.Errorf("provenance %q does not say the note came from elsewhere", src)
	}
	if !strings.Contains(src, "task-1") {
		t.Errorf("provenance %q lost the original source; the chain is broken", src)
	}
	var tagged bool
	for _, tag := range added[0].Tags {
		if tag == "imported" {
			tagged = true
		}
	}
	if !tagged {
		t.Error("the imported note is not tagged as imported")
	}
}

// The preview is the "text you have read" half, so it must actually show the
// text and say what importing does.
func TestPreviewShowsWhatWouldBeCopied(t *testing.T) {
	b := memory.Bundle{Version: memory.BundleVersion, From: "billing", Notes: []memory.Note{{
		Kind: memory.KindAdvice, Text: "never retry a non-idempotent write",
		Provenance: memory.Provenance{Source: "task-9"},
	}}}
	out := memory.Preview(b, nil)
	if !strings.Contains(out, "never retry a non-idempotent write") {
		t.Error("the preview does not show the note text")
	}
	if !strings.Contains(out, "task-9") {
		t.Error("the preview does not show where the note came from")
	}
	if !strings.Contains(out, "steer") {
		t.Error("the preview does not say what importing will do")
	}
}

func TestBundleRoundTrip(t *testing.T) {
	s, _ := store(t)
	if _, err := s.Add(memory.Note{
		Kind: memory.KindAdvice, Text: "run the race detector on the scheduler",
		Provenance: memory.Provenance{Source: "task-3"},
	}); err != nil {
		t.Fatal(err)
	}
	b, err := s.Export("this-repo", []memory.Kind{memory.KindAdvice})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "lessons.yaml")
	if err := memory.ExportTo(path, b); err != nil {
		t.Fatal(err)
	}
	back, err := memory.ImportFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Notes) != 1 || back.Notes[0].Text != b.Notes[0].Text {
		t.Fatalf("round trip lost the note: %+v", back.Notes)
	}
	if back.From != "this-repo" {
		t.Errorf("the bundle lost where it came from: %q", back.From)
	}
}

// A future format must not be silently misread.
func TestNewerBundleVersionIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.yaml")
	if err := os.WriteFile(path, []byte("version: 99\nfrom: later\nnotes: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.ImportFrom(path); err == nil {
		t.Fatal("a newer bundle version was accepted")
	}
}
