package postgres

import (
	"context"
	"strings"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/internal/graphquery"
	"github.com/jackc/pgx/v5"
)

var _ graphquery.Store = (*Store)(nil)

func (s *Store) Health(ctx context.Context) error {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	return s.pool.Ping(ctx)
}

func (s *Store) Manifests(ctx context.Context) (map[int64]graphartifact.Manifest, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	manifests, err := s.GraphManifests(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[int64]graphartifact.Manifest, len(manifests))
	for _, manifest := range manifests {
		result[manifest.RepositoryID] = manifest
	}
	return result, nil
}

func (s *Store) Symbols(ctx context.Context, query graphquery.SymbolQuery) ([]graphprotocol.Symbol, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	repositoryIDs, uploadIDs, commits := graphScope(query.Snapshots)
	rows, err := s.pool.Query(ctx, `with scope as (
		select * from unnest($1::bigint[], $2::bigint[], $3::text[]) as value(repository_id, upload_id, commit)
	)
	select scope.repository_id, node.uid, node.qualified_name, node.path, node.language,
		node.symbol_kind, node.signature, node.start_line, node.start_character, node.end_line, node.end_character
	from scope
	join graph_uploads upload on upload.id=scope.upload_id and upload.repository_id=scope.repository_id and upload.commit=scope.commit
	join graph_nodes node on node.upload_id=upload.id and node.kind=$4
	where ($5='' or node.uid=$5) and ($6='' or node.qualified_name=$6)
		and ($7='' or node.path=$7) and ($8='' or node.symbol_kind=$8)
	order by scope.repository_id, node.uid
	limit $9`, repositoryIDs, uploadIDs, commits, int16(graphartifact.NodeSymbol), query.UID, query.Name, query.FilePath, query.Kind, query.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var symbols []graphprotocol.Symbol
	for rows.Next() {
		symbol, scanErr := scanGraphSymbol(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		symbols = append(symbols, symbol)
	}
	return symbols, rows.Err()
}

func (s *Store) Neighbors(ctx context.Context, query graphquery.NeighborQuery) ([]graphquery.Neighbor, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	kind, ok := graphRelationKind(query.Relation)
	if !ok || query.Direction != "incoming" && query.Direction != "outgoing" {
		return nil, graphquery.ErrInvalidRequest
	}
	repositoryIDs, uploadIDs, commits := graphScope(query.Snapshots)
	frontierRepositories := make([]int64, 0, len(query.Frontier))
	frontierUIDs := make([]string, 0, len(query.Frontier))
	for _, ref := range query.Frontier {
		frontierRepositories = append(frontierRepositories, ref.RepositoryID)
		frontierUIDs = append(frontierUIDs, ref.UID)
	}
	statement := outgoingNeighborsSQL
	if query.Direction == "incoming" {
		statement = incomingNeighborsSQL
	}
	rows, err := s.pool.Query(ctx, statement, repositoryIDs, uploadIDs, commits,
		frontierRepositories, frontierUIDs, int16(kind), query.MinConfidence, query.Offset, query.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var neighbors []graphquery.Neighbor
	for rows.Next() {
		neighbor, scanErr := scanGraphNeighbor(rows, query.Relation, query.Direction)
		if scanErr != nil {
			return nil, scanErr
		}
		neighbors = append(neighbors, neighbor)
	}
	return neighbors, rows.Err()
}

const graphNeighborScopeSQL = `with scope as (
	select * from unnest($1::bigint[], $2::bigint[], $3::text[]) as value(repository_id, upload_id, commit)
), frontier as (
	select * from unnest($4::bigint[], $5::text[]) as value(repository_id, uid)
) `

const graphNeighborColumns = `scope.repository_id, node.uid, node.qualified_name, node.path, node.language,
	node.symbol_kind, node.signature, node.start_line, node.start_character, node.end_line, node.end_character,
	scope.repository_id, parent.uid, edge.source_uid, edge.target_uid, edge.confidence, edge.path,
	edge.start_line, edge.start_character, edge.end_line, edge.end_character, edge.resolution_reason`

const outgoingNeighborsSQL = graphNeighborScopeSQL + `select ` + graphNeighborColumns + `
	from scope
	join graph_uploads upload on upload.id=scope.upload_id and upload.repository_id=scope.repository_id and upload.commit=scope.commit
	join graph_edges edge on edge.upload_id=upload.id and edge.kind=$6 and edge.confidence >= $7
	join frontier on frontier.repository_id=scope.repository_id and frontier.uid=edge.source_uid
	join graph_nodes parent on parent.upload_id=upload.id and parent.uid=edge.source_uid
	join graph_nodes node on node.upload_id=upload.id and node.uid=edge.target_uid and node.kind=3
	order by scope.repository_id, node.uid, parent.uid offset $8 limit $9`

const incomingNeighborsSQL = graphNeighborScopeSQL + `select ` + graphNeighborColumns + `
	from scope
	join graph_uploads upload on upload.id=scope.upload_id and upload.repository_id=scope.repository_id and upload.commit=scope.commit
	join graph_edges edge on edge.upload_id=upload.id and edge.kind=$6 and edge.confidence >= $7
	join frontier on frontier.repository_id=scope.repository_id and frontier.uid=edge.target_uid
	join graph_nodes parent on parent.upload_id=upload.id and parent.uid=edge.target_uid
	join graph_nodes node on node.upload_id=upload.id and node.uid=edge.source_uid and node.kind=3
	order by scope.repository_id, node.uid, parent.uid offset $8 limit $9`

func graphScope(snapshots []graphquery.QuerySnapshot) ([]int64, []int64, []string) {
	repositoryIDs := make([]int64, 0, len(snapshots))
	uploadIDs := make([]int64, 0, len(snapshots))
	commits := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		repositoryIDs = append(repositoryIDs, snapshot.RepositoryID)
		uploadIDs = append(uploadIDs, snapshot.UploadID)
		commits = append(commits, snapshot.Commit)
	}
	return repositoryIDs, uploadIDs, commits
}

func graphRelationKind(relation string) (graphartifact.EdgeKind, bool) {
	kinds := map[string]graphartifact.EdgeKind{
		"calls": graphartifact.EdgeCalls, "references": graphartifact.EdgeReferences,
		"extends": graphartifact.EdgeExtends, "implements": graphartifact.EdgeImplements,
	}
	kind, ok := kinds[relation]
	return kind, ok
}

type graphRow interface{ Scan(...any) error }

func scanGraphSymbol(row graphRow) (graphprotocol.Symbol, error) {
	var symbol graphprotocol.Symbol
	err := row.Scan(&symbol.RepositoryID, &symbol.UID, &symbol.Name, &symbol.FilePath, &symbol.Language,
		&symbol.Kind, &symbol.Signature, &symbol.Range.StartLine, &symbol.Range.StartCharacter,
		&symbol.Range.EndLine, &symbol.Range.EndCharacter)
	symbol.Test = graphTestPath(symbol.FilePath)
	return symbol, err
}

func scanGraphNeighbor(row pgx.Row, relation, direction string) (graphquery.Neighbor, error) {
	var neighbor graphquery.Neighbor
	var parentRepositoryID int64
	var sourceUID, targetUID string
	var confidence float32
	err := row.Scan(&neighbor.Symbol.RepositoryID, &neighbor.Symbol.UID, &neighbor.Symbol.Name,
		&neighbor.Symbol.FilePath, &neighbor.Symbol.Language, &neighbor.Symbol.Kind, &neighbor.Symbol.Signature,
		&neighbor.Symbol.Range.StartLine, &neighbor.Symbol.Range.StartCharacter,
		&neighbor.Symbol.Range.EndLine, &neighbor.Symbol.Range.EndCharacter,
		&parentRepositoryID, &neighbor.Parent.UID, &sourceUID, &targetUID, &confidence,
		&neighbor.Edge.Path, &neighbor.Edge.Range.StartLine, &neighbor.Edge.Range.StartCharacter,
		&neighbor.Edge.Range.EndLine, &neighbor.Edge.Range.EndCharacter, &neighbor.Edge.ResolutionReason)
	if err != nil {
		return graphquery.Neighbor{}, err
	}
	neighbor.Symbol.Test = graphTestPath(neighbor.Symbol.FilePath)
	neighbor.Parent.RepositoryID = parentRepositoryID
	neighbor.Edge = graphprotocol.Relationship{
		SourceRepositoryID: parentRepositoryID, TargetRepositoryID: neighbor.Symbol.RepositoryID,
		SourceUID: sourceUID, TargetUID: targetUID, Kind: relation, Path: neighbor.Edge.Path,
		Range: neighbor.Edge.Range, Confidence: float64(confidence), ResolutionReason: neighbor.Edge.ResolutionReason,
	}
	if direction == "incoming" {
		neighbor.Edge.SourceRepositoryID = neighbor.Symbol.RepositoryID
		neighbor.Edge.TargetRepositoryID = parentRepositoryID
	}
	return neighbor, nil
}

func graphTestPath(path string) bool {
	return strings.Contains(path, "/test/") || strings.Contains(path, "/tests/") ||
		strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".test.ts") ||
		strings.HasSuffix(path, ".test.js")
}
