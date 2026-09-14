package main

import (
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/store"
)

func graphFor(st *store.Store) graph.Graph { return graph.New(st) }

func parseChange(s string) (graph.ChangeKind, error) { return graph.ParseChangeKind(s) }
