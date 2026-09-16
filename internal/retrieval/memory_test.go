package retrieval_test

import (
	"context"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/memory"
	"github.com/akynte/local-engineer/internal/retrieval"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

func memStore(t *testing.T, notes ...memory.Note) *memory.Store {
	t.Helper()
	m := memory.Open(t.TempDir(), memory.DefaultCaps())
	for _, n := range notes {
		if _, err := m.Add(n); err != nil {
			t.Fatalf("add note: %v", err)
		}
	}
	return m
}

func retrieverWith(t *testing.T, m *memory.Store) *retrieval.Retriever {
	t.Helper()
	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.CloseAll() })
	st, err := root.OpenWorkspace(context.Background(),
		workspace.DeriveID("/retrieval/test", "", t.Name()))
	if err != nil {
		t.Fatal(err)
	}
	return retrieval.New(st).WithMemory(m)
}

func advice(text string) memory.Note {
	return memory.Note{Kind: memory.KindAdvice, Text: text,
		Provenance: memory.Provenance{Source: "operator"}}
}

// §8.2 adopts durable memory to steer future work. The store, the CLI and the
// caps all existed; nothing read them into a packet, so writing an advice note
// changed nothing about how a task ran.
func TestAnAdviceNoteReachesThePacket(t *testing.T) {
	r := retrieverWith(t, memStore(t, advice("migrations run in order; never renumber one")))

	pkt, err := r.Build(context.Background(), retrieval.Request{Query: "anything"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(pkt.Notes) != 1 {
		t.Fatalf("the packet carries %d note(s), want 1", len(pkt.Notes))
	}
	if !strings.Contains(pkt.Notes[0].Text, "never renumber") {
		t.Errorf("wrong note reached the packet: %q", pkt.Notes[0].Text)
	}
	// Notes cost tokens like everything else: a section the budget cannot see
	// is a section that overflows the window.
	if pkt.Tokens == 0 {
		t.Error("the note was not counted against the packet budget")
	}
}

// The three kinds are not interchangeable — an observation is something seen
// once, advice is a rule meant to steer — and the reader has to be able to
// tell them apart or a one-off becomes a law.
func TestEveryKindReachesThePacketLabelled(t *testing.T) {
	m := memStore(t,
		memory.Note{Kind: memory.KindIntent, Text: "the goal is a working offline mode",
			Provenance: memory.Provenance{Source: "operator"}},
		memory.Note{Kind: memory.KindObservation, Text: "the integration test is flaky on a cold cache",
			Provenance: memory.Provenance{Source: "t-123"}},
		advice("prefer the sqlc-generated queries"),
	)
	r := retrieverWith(t, m)

	pkt, err := r.Build(context.Background(), retrieval.Request{Query: "anything"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(pkt.Notes) != 3 {
		t.Fatalf("want all three kinds, got %d", len(pkt.Notes))
	}
	// memory.Kinds is documented as "the order a reader should see them".
	want := []memory.Kind{memory.KindIntent, memory.KindObservation, memory.KindAdvice}
	for i, k := range want {
		if pkt.Notes[i].Kind != k {
			t.Errorf("note %d is %s, want %s: the kinds must arrive in reading order",
				i, pkt.Notes[i].Kind, k)
		}
	}
	// A rule whose source is invisible cannot be judged (§11).
	for _, n := range pkt.Notes {
		if n.Provenance.Source == "" {
			t.Errorf("note %q lost its provenance on the way to the packet", n.Text)
		}
	}
}

// §11 calls the playbook pattern "good, easy to abuse". Notes accumulate, and
// a packet where memory has crowded out the code is the abuse. The cap has to
// bite, and dropping has to be visible rather than a quiet truncation.
func TestNotesCannotCrowdOutTheCode(t *testing.T) {
	var many []memory.Note
	for range 40 {
		many = append(many, advice(strings.Repeat("a long standing rule about this repository ", 20)))
	}
	r := retrieverWith(t, memStore(t, many...))

	const budget = 4000
	pkt, err := r.Build(context.Background(), retrieval.Request{Query: "anything", TokenBudget: budget})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if pkt.NotesDropped == 0 {
		t.Fatal("40 long notes fit a 4000-token packet; the memory cap is not applied")
	}
	max := int(float64(budget) * retrieval.MemoryBudgetFraction)
	if pkt.Tokens > max {
		t.Errorf("notes took %d tokens of a %d budget, over the %d cap: memory must not "+
			"be most of a packet", pkt.Tokens, budget, max)
	}
}

// A repository with no notes is the common case, and a retriever with no
// memory attached is every caller that has not opted in. Neither is an error.
func TestNoMemoryIsNotAnError(t *testing.T) {
	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.CloseAll() })
	st, err := root.OpenWorkspace(context.Background(),
		workspace.DeriveID("/retrieval/test", "", t.Name()))
	if err != nil {
		t.Fatal(err)
	}

	pkt, err := retrieval.New(st).Build(context.Background(), retrieval.Request{Query: "x"})
	if err != nil {
		t.Fatalf("a retriever with no memory must still build: %v", err)
	}
	if len(pkt.Notes) != 0 {
		t.Error("notes appeared without a memory store")
	}

	empty := retrieverWith(t, memStore(t))
	if pkt, err = empty.Build(context.Background(), retrieval.Request{Query: "x"}); err != nil {
		t.Fatalf("an empty memory store must still build: %v", err)
	}
	if len(pkt.Notes) != 0 {
		t.Error("an empty store produced notes")
	}
}
