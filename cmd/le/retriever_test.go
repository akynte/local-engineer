package main

import (
	"context"
	"testing"

	"github.com/akynte/local-engineer/internal/memory"
	"github.com/akynte/local-engineer/internal/retrieval"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// The wiring step is the one that keeps going wrong in this repository: a
// value computed correctly, a consumer that reads it, and nothing joining
// them. This asserts the join, because both halves already have their own
// tests and both passed while the feature did nothing.
func TestTheRetrieverGetsTheRepositorysNotes(t *testing.T) {
	repo := t.TempDir()
	m := memory.Open(repo, memory.DefaultCaps())
	if _, err := m.Add(memory.Note{
		Kind: memory.KindAdvice, Text: "WIRED-MARKER",
		Provenance: memory.Provenance{Source: "operator"},
	}); err != nil {
		t.Fatalf("add note: %v", err)
	}

	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.CloseAll()
	st, err := root.OpenWorkspace(context.Background(),
		workspace.DeriveID(repo, "", t.Name()))
	if err != nil {
		t.Fatal(err)
	}

	r := retrieverFor(st, &workspace.Workspace{Root: repo})
	pkt, err := r.Build(context.Background(), retrieval.Request{Query: "anything"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(pkt.Notes) == 0 {
		t.Fatal("the retriever the CLI builds carries no notes; the memory store is not attached")
	}
	if pkt.Notes[0].Text != "WIRED-MARKER" {
		t.Errorf("wrong note: %q", pkt.Notes[0].Text)
	}
}

// Not every caller has a checkout — `le eval` runs against scratch copies —
// and a missing workspace must degrade to no notes rather than panic.
func TestNoWorkspaceMeansNoNotes(t *testing.T) {
	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.CloseAll()
	st, err := root.OpenWorkspace(context.Background(),
		workspace.DeriveID("/x", "", t.Name()))
	if err != nil {
		t.Fatal(err)
	}

	pkt, err := retrieverFor(st, nil).Build(context.Background(), retrieval.Request{Query: "x"})
	if err != nil {
		t.Fatalf("a retriever with no workspace must still build: %v", err)
	}
	if len(pkt.Notes) != 0 {
		t.Error("notes appeared without a workspace to read them from")
	}
}
