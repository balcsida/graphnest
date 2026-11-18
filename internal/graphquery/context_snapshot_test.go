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

type generationTestStore struct {
	stubQueryStore
	version   uint32
	snapshots [][]QuerySnapshot
}

func (s *generationTestStore) Manifests(context.Context) (map[int64]graphartifact.Manifest, error) {
	return map[int64]graphartifact.Manifest{1: {RepositoryID: 1, UploadID: 7, Commit: "sha", SchemaVersion: s.version}}, nil
}
func (s *generationTestStore) Symbols(_ context.Context, query SymbolQuery) ([]graphprotocol.Symbol, error) {
	s.snapshots = append(s.snapshots, query.Snapshots)
	return []graphprotocol.Symbol{{RepositoryID: 1, UID: "symbol"}}, nil
}
func (s *generationTestStore) Neighbors(_ context.Context, query NeighborQuery) ([]Neighbor, error) {
	s.snapshots = append(s.snapshots, query.Snapshots)
	return nil, nil
}

// The store reads whichever generation Manifests picked, so every legacy
// query has to carry that generation's schema version down to it.
func TestLegacyQueriesCarryTheSelectedGenerationToTheStore(t *testing.T) {
	for _, version := range []uint32{1, 2} {
		store := &generationTestStore{version: version}
		service := &Service{Store: store}
		if _, err := service.Context(t.Context(), graphprotocol.ContextRequest{Scope: entityTestScope(), UID: "symbol"}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Impact(t.Context(), graphprotocol.ImpactRequest{Scope: entityTestScope(), TargetUID: "symbol", Direction: "upstream"}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Trace(t.Context(), graphprotocol.TraceRequest{Scope: entityTestScope(), SourceUID: "symbol", TargetUID: "other"}); err != nil {
			t.Fatal(err)
		}
		if len(store.snapshots) < 6 {
			t.Fatalf("version %d: store saw %d queries", version, len(store.snapshots))
		}
		for _, snapshots := range store.snapshots {
			if len(snapshots) != 1 || snapshots[0] != (QuerySnapshot{RepositoryID: 1, UploadID: 7, Commit: "sha", SchemaVersion: version}) {
				t.Fatalf("version %d: store query snapshots=%#v", version, snapshots)
			}
		}
	}
}
