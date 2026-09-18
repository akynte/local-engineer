package retrieval_test

import (
	"context"
	"testing"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/retrieval"
)

func TestPreviouslyIndexedSecretDoesNotEnterPacket(t *testing.T) {
	r, st := wired(t, 0)
	ctx := context.Background()
	var fileID int64
	err := st.Index().SQL().QueryRowContext(ctx, `
		INSERT INTO files (workspace_id, repository_id, path, content_hash, indexed_at, index_version)
		VALUES (?, 'r', 'config/credentials.go', 'h', 1, 1) RETURNING file_id`, st.ID().String()).Scan(&fileID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = graph.New(st).UpsertNode(ctx, graph.Node{
		WorkspaceID: st.ID(), RepositoryID: "r", Kind: graph.KindFunction,
		Name: "Private", FQN: "pkg.Private", FileID: fileID,
		ContentHash: "h", StartLine: 1, EndLine: 1, Signature: "private token",
	})
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := r.Build(ctx, retrieval.Request{Symbols: []string{"Private"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pkt.Slices) != 0 || pkt.Dropped != 1 {
		t.Fatalf("old secret index escaped policy: %+v", pkt)
	}
}
