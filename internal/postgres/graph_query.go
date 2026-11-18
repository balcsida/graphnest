package postgres

import (
	"context"
	"sort"
	"strings"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/jackc/pgx/v5"
)

var _ graphquery.Store = (*Store)(nil)

func (s *Store) Health(ctx context.Context) error {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	return s.pool.Ping(ctx)
}

// Manifests picks, per repository, the generation that answers context, impact
// and trace at the indexed commit: a published v2 generation, then a managed or
// external v1 graph, then the SCIP-derived v2 generation, then the SCIP-derived
// v1 graph. An explicit v1 graph outranks the SCIP-derived v2 generation
// because only the former records call edges.
func (s *Store) Manifests(ctx context.Context) (map[int64]graphartifact.Manifest, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `select distinct on (repositories.id) repositories.id, repositories.indexed_sha,
		graph_uploads.id, graph_uploads.schema_version, graph_uploads.content_hash, graph_uploads.source
		from installations join repositories on repositories.installation_id=installations.id
		join graph_uploads on graph_uploads.repository_id=repositories.id and graph_uploads.commit=repositories.indexed_sha and graph_uploads.active
		where installations.status='active' and repositories.enabled and not repositories.archived and repositories.indexed_sha is not null
		order by repositories.id, case
			when graph_uploads.schema_version=2 and graph_uploads.source='external' then 0
			when graph_uploads.schema_version=1 and graph_uploads.source<>'scip' then 1
			when graph_uploads.schema_version=2 then 2
			else 3 end`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int64]graphartifact.Manifest{}
	for rows.Next() {
		var manifest graphartifact.Manifest
		if err := rows.Scan(&manifest.RepositoryID, &manifest.Commit, &manifest.UploadID, &manifest.SchemaVersion, &manifest.ContentHash, &manifest.Source); err != nil {
			return nil, err
		}
		result[manifest.RepositoryID] = manifest
	}
	return result, rows.Err()
}

// Symbols reads each snapshot from the generation Manifests picked for it.
// A repository is in exactly one generation, so results merge by repository.
func (s *Store) Symbols(ctx context.Context, query graphquery.SymbolQuery) ([]graphprotocol.Symbol, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	legacy, current := splitGenerations(query.Snapshots)
	if len(current) == 0 {
		return s.symbolsV1(ctx, query)
	}
	query.Snapshots = current
	symbols, err := s.symbolsV2(ctx, query)
	if err != nil || len(legacy) == 0 {
		return symbols, err
	}
	query.Snapshots = legacy
	more, err := s.symbolsV1(ctx, query)
	if err != nil {
		return nil, err
	}
	symbols = append(symbols, more...)
	sort.SliceStable(symbols, func(i, j int) bool { return symbols[i].RepositoryID < symbols[j].RepositoryID })
	return symbols[:min(len(symbols), max(query.Limit, 0))], nil
}

// Neighbors is the frontier counterpart of Symbols.
func (s *Store) Neighbors(ctx context.Context, query graphquery.NeighborQuery) ([]graphquery.Neighbor, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	legacy, current := splitGenerations(query.Snapshots)
	if len(current) == 0 {
		return s.neighborsV1(ctx, query)
	}
	if len(legacy) == 0 {
		return s.neighborsV2(ctx, query)
	}
	// Both generations in one scope: page after merging.
	offset, limit := max(query.Offset, 0), max(query.Limit, 0)
	query.Offset, query.Limit = 0, offset+limit
	query.Snapshots = legacy
	neighbors, err := s.neighborsV1(ctx, query)
	if err != nil {
		return nil, err
	}
	query.Snapshots = current
	more, err := s.neighborsV2(ctx, query)
	if err != nil {
		return nil, err
	}
	neighbors = append(neighbors, more...)
	sort.SliceStable(neighbors, func(i, j int) bool { return neighbors[i].Symbol.RepositoryID < neighbors[j].Symbol.RepositoryID })
	return neighbors[min(len(neighbors), offset):min(len(neighbors), offset+limit)], nil
}

func splitGenerations(snapshots []graphquery.QuerySnapshot) (legacy, current []graphquery.QuerySnapshot) {
	for _, snapshot := range snapshots {
		if snapshot.SchemaVersion == 2 {
			current = append(current, snapshot)
		} else {
			legacy = append(legacy, snapshot)
		}
	}
	return legacy, current
}

func (s *Store) symbolsV1(ctx context.Context, query graphquery.SymbolQuery) ([]graphprotocol.Symbol, error) {
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

func (s *Store) neighborsV1(ctx context.Context, query graphquery.NeighborQuery) ([]graphquery.Neighbor, error) {
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
	order by scope.repository_id, node.uid, parent.uid,
		edge.source_uid, edge.target_uid, edge.kind, edge.path,
		edge.start_line, edge.start_character, edge.end_line, edge.end_character,
		edge.confidence, edge.resolution_reason, edge.id
	offset $8 limit $9`

const incomingNeighborsSQL = graphNeighborScopeSQL + `select ` + graphNeighborColumns + `
	from scope
	join graph_uploads upload on upload.id=scope.upload_id and upload.repository_id=scope.repository_id and upload.commit=scope.commit
	join graph_edges edge on edge.upload_id=upload.id and edge.kind=$6 and edge.confidence >= $7
	join frontier on frontier.repository_id=scope.repository_id and frontier.uid=edge.target_uid
	join graph_nodes parent on parent.upload_id=upload.id and parent.uid=edge.target_uid
	join graph_nodes node on node.upload_id=upload.id and node.uid=edge.source_uid and node.kind=3
	order by scope.repository_id, node.uid, parent.uid,
		edge.source_uid, edge.target_uid, edge.kind, edge.path,
		edge.start_line, edge.start_character, edge.end_line, edge.end_character,
		edge.confidence, edge.resolution_reason, edge.id
	offset $8 limit $9`

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
