package graphquery

import (
	"context"

	"github.com/balcsida/graphnest/internal/graphprotocol"
)

// EntityStore is the v2 query path; legacy manifests intentionally exclude v2.
// Callers supply authorized internal repository snapshots. Stores never expand
// that scope by resolving equal names or occurrences in other repositories.
type EntityStore interface {
	EntityGenerations(context.Context, []QuerySnapshot) ([]graphprotocol.Generation, error)
	QueryEntities(context.Context, EntityQuery) ([]graphprotocol.Entity, error)
	EntityNeighbors(context.Context, EntityNeighborQuery) ([]EntityNeighbor, error)
}

type EntityQuery struct {
	Snapshots     []QuerySnapshot
	Selector      graphprotocol.EntitySelector
	Offset, Limit int
}

type EntityNeighborQuery struct {
	Snapshot      QuerySnapshot
	Occurrence    string
	Relation      string
	Direction     string
	MinConfidence float64
	Limit         int
}

type EntityNeighbor struct {
	Entity graphprotocol.Entity
	Edge   graphprotocol.Evidence
}
