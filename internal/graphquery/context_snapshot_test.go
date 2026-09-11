package graphquery

import (
	"context"
	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"testing"
)

type snapshotTestStore struct {
	stubQueryStore
	upload int64
}

func (s *snapshotTestStore) Manifests(context.Context) (map[int64]graphartifact.Manifest, error) {
	return map[int64]graphartifact.Manifest{1: {RepositoryID: 1, UploadID: s.upload, Commit: "sha"}}, nil
}
func (s *snapshotTestStore) Symbols(context.Context, SymbolQuery) ([]graphprotocol.Symbol, error) {
	s.upload = 2
	return []graphprotocol.Symbol{{RepositoryID: 1, UID: "symbol"}}, nil
}
func TestContextSnapshotCapturesQueryGeneration(t *testing.T) {
	store := &snapshotTestStore{upload: 1}
	service := &Service{Store: store}
	got, err := service.Context(t.Context(), graphprotocol.ContextRequest{Scope: entityTestScope(), UID: "symbol"})
	if err != nil || len(got.Snapshots) != 1 || got.Snapshots[0].UploadID != 1 {
		t.Fatalf("captured after query: %+v err=%v", got.Snapshots, err)
	}
	if err := service.ValidateContextSnapshots(t.Context(), got.Snapshots); err == nil {
		t.Fatal("replacement was accepted")
	}
}
