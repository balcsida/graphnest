package graphquery

import (
	"context"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphprotocol"
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
