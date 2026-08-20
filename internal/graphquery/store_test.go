package graphquery

import (
	"context"
	"testing"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphprotocol"
)

type stubQueryStore struct{ healthy bool }

func (store *stubQueryStore) Health(context.Context) error {
	store.healthy = true
	return nil
}

func (*stubQueryStore) Manifests(context.Context) (map[int64]graphartifact.Manifest, error) {
	return map[int64]graphartifact.Manifest{}, nil
}

func (*stubQueryStore) Symbols(context.Context, SymbolQuery) ([]graphprotocol.Symbol, error) {
	return nil, nil
}

func (*stubQueryStore) Neighbors(context.Context, NeighborQuery) ([]Neighbor, error) {
	return nil, nil
}

func TestServiceUsesQueryStore(t *testing.T) {
	store := &stubQueryStore{}
	if err := (&Service{Store: store}).Health(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !store.healthy {
		t.Fatal("Health did not use query store")
	}
}
