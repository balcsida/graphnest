package graphquery

import (
	"context"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphprotocol"
)

type Store interface {
	Health(context.Context) error
	Manifests(context.Context) (map[int64]graphartifact.Manifest, error)
	Symbols(context.Context, SymbolQuery) ([]graphprotocol.Symbol, error)
	Neighbors(context.Context, NeighborQuery) ([]Neighbor, error)
}

type SymbolQuery struct {
	Snapshots      []QuerySnapshot
	UID, Name      string
	FilePath, Kind string
	Limit          int
}

type QuerySnapshot struct {
	RepositoryID int64
	UploadID     int64
	Commit       string
	// SchemaVersion is the generation the legacy store reads for context,
	// impact and trace: 2 for a v2 generation, otherwise v1. It stays out of
	// JSON so entity cursor fingerprints do not change.
	SchemaVersion uint32 `json:"-"`
}

type SymbolRef struct {
	RepositoryID int64
	UID          string
}

type NeighborQuery struct {
	Snapshots     []QuerySnapshot
	Frontier      []SymbolRef
	Relation      string
	Direction     string
	MinConfidence float64
	Offset, Limit int
}

type Neighbor struct {
	Symbol graphprotocol.Symbol
	Parent SymbolRef
	Edge   graphprotocol.Relationship
}
