package graphquery

import (
	"context"
	"errors"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/internal/ladybug"
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

type ladybugStore struct{ database *ladybug.Database }

func NewLadybugStore(database *ladybug.Database) Store { return &ladybugStore{database: database} }

func (store *ladybugStore) Health(ctx context.Context) error {
	if store == nil || store.database == nil {
		return errors.New("graph database is unavailable")
	}
	return store.database.Health(ctx)
}

func (store *ladybugStore) Manifests(ctx context.Context) (map[int64]graphartifact.Manifest, error) {
	return store.database.Manifests(ctx)
}

func (store *ladybugStore) Symbols(ctx context.Context, query SymbolQuery) (symbols []graphprotocol.Symbol, err error) {
	snapshots := protocolSnapshots(query.Snapshots)
	err = store.database.View(ctx, func(session *ladybug.Session) error {
		result, executeErr := session.Execute(ctx, selectSymbols, map[string]any{
			"scope": snapshotParameters(snapshots), "use_uid": query.UID != "",
			"uids": selectorUIDs(snapshots, query.UID), "name": query.Name,
			"path": query.FilePath, "kind": query.Kind, "limit": int64(query.Limit),
		}, ladybug.QueryLimits{MaxRows: query.Limit})
		if executeErr != nil {
			return executeErr
		}
		for _, row := range result.Rows {
			symbols = append(symbols, symbolFromRow(row))
		}
		return nil
	})
	return symbols, err
}

func (store *ladybugStore) Neighbors(ctx context.Context, query NeighborQuery) (neighbors []Neighbor, err error) {
	relation, ok := relationQueries[query.Relation]
	if !ok || query.Direction != "incoming" && query.Direction != "outgoing" {
		return nil, ErrInvalidRequest
	}
	statement := relation.outgoing
	if query.Direction == "incoming" {
		statement = relation.incoming
	}
	frontier := make([]nodeKey, 0, len(query.Frontier))
	for _, ref := range query.Frontier {
		frontier = append(frontier, nodeKey{repositoryID: ref.RepositoryID, uid: ref.UID})
	}
	err = store.database.View(ctx, func(session *ladybug.Session) error {
		result, executeErr := session.Execute(ctx, statement, map[string]any{
			"scope": snapshotParameters(protocolSnapshots(query.Snapshots)), "frontier": frontierParameters(frontier),
			"depth": int64(1), "min_confidence": query.MinConfidence,
			"offset": int64(query.Offset), "limit": int64(query.Limit),
		}, ladybug.QueryLimits{MaxRows: query.Limit})
		if executeErr != nil {
			return executeErr
		}
		for _, row := range result.Rows {
			direction := "downstream"
			if query.Direction == "incoming" {
				direction = "upstream"
			}
			parent := keyFromStorageUID(row[11].(string))
			neighbors = append(neighbors, Neighbor{
				Symbol: symbolFromRow(row),
				Parent: SymbolRef{RepositoryID: parent.repositoryID, UID: parent.uid},
				Edge:   relationshipFromRow(row, query.Relation, direction),
			})
		}
		return nil
	})
	return neighbors, err
}

func protocolSnapshots(snapshots []QuerySnapshot) []graphprotocol.RepositorySnapshot {
	result := make([]graphprotocol.RepositorySnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		result = append(result, graphprotocol.RepositorySnapshot{ID: snapshot.RepositoryID, Commit: snapshot.Commit})
	}
	return result
}
