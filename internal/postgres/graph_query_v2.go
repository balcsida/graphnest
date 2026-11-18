package postgres

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"google.golang.org/protobuf/proto"
)

// Context, impact and trace read a v2 generation through the legacy symbol
// model. A symbol's UID is "symbol:" plus the entity's occurrence, which for a
// SCIP-derived generation is the UID the v1 graph gave the same symbol. File
// and repository entities are not symbols, as in v1.
const legacySymbolPrefix = "symbol:"

const legacyV2Symbols = "n.kind not in ('file','repository')"

// legacyKindAliases keeps the kind filter of v1 SCIP graphs matching: they
// call every type "type" and every package "namespace", where v2 records the
// indexer's own kind.
var legacyKindAliases = map[string][]string{
	"type":      {"class", "struct", "interface", "trait", "protocol", "union", "enum", "type_alias"},
	"namespace": {"namespace", "module"},
}

func (s *Store) symbolsV2(ctx context.Context, query graphquery.SymbolQuery) ([]graphprotocol.Symbol, error) {
	occurrence, ok := strings.CutPrefix(query.UID, legacySymbolPrefix)
	if query.UID != "" && !ok {
		return nil, nil
	}
	repositoryIDs, uploadIDs, commits := graphScope(query.Snapshots)
	args := []any{repositoryIDs, uploadIDs, commits, graphquery.MaxEntityQueryBytes}
	filters := ""
	// Hashes bound the index keys; the original bytes decide the match.
	add := func(key, column, value string) {
		if value != "" {
			args = append(args, []byte(value))
			filters += fmt.Sprintf(" and %s=sha256($%d::bytea) and n.%s=$%d::bytea", key, len(args), column, len(args))
		}
	}
	add("n.occurrence_key", "occurrence", occurrence)
	add("sha256(n.name)", "name", query.Name)
	add("sha256(n.path)", "path", query.FilePath)
	if query.Kind != "" {
		kinds, alias := legacyKindAliases[query.Kind]
		if !alias {
			kinds = []string{query.Kind}
		}
		args = append(args, kinds)
		filters += fmt.Sprintf(" and n.kind=any($%d::text[])", len(args))
	}
	args = append(args, query.Limit)
	rows, err := s.pool.Query(ctx, `with scope as (
		select * from unnest($1::bigint[], $2::bigint[], $3::text[]) as value(repository_id, upload_id, commit)
	)
	select scope.repository_id, case when octet_length(n.payload)<=$4 then n.payload end
	from scope
	join graph_uploads u on u.id=scope.upload_id and u.repository_id=scope.repository_id and u.commit=scope.commit and u.schema_version=2
	join graph_v2_nodes n on n.upload_id=u.id
	where `+legacyV2Symbols+filters+fmt.Sprintf(`
	order by scope.repository_id, n.occurrence
	limit $%d`, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var symbols []graphprotocol.Symbol
	for rows.Next() {
		var repositoryID int64
		var payload []byte
		if err := rows.Scan(&repositoryID, &payload); err != nil {
			return nil, err
		}
		symbol, err := legacySymbol(repositoryID, payload)
		if err != nil {
			return nil, err
		}
		symbols = append(symbols, symbol)
	}
	return symbols, rows.Err()
}

func (s *Store) neighborsV2(ctx context.Context, query graphquery.NeighborQuery) ([]graphquery.Neighbor, error) {
	kind, ok := graphRelationKind(query.Relation)
	if !ok || query.Direction != "incoming" && query.Direction != "outgoing" {
		return nil, graphquery.ErrInvalidRequest
	}
	repositoryIDs, uploadIDs, commits := graphScope(query.Snapshots)
	frontierRepositories := make([]int64, 0, len(query.Frontier))
	frontierOccurrences := make([][]byte, 0, len(query.Frontier))
	for _, ref := range query.Frontier {
		if occurrence, ok := strings.CutPrefix(ref.UID, legacySymbolPrefix); ok {
			frontierRepositories = append(frontierRepositories, ref.RepositoryID)
			frontierOccurrences = append(frontierOccurrences, []byte(occurrence))
		}
	}
	rows, err := s.pool.Query(ctx, legacyV2NeighborsSQL(query.Direction), repositoryIDs, uploadIDs, commits, frontierRepositories, frontierOccurrences,
		int16(kind), query.MinConfidence, query.Offset, query.Limit, graphquery.MaxEntityQueryBytes,
		int16(graphartifact.EdgeExtends), int16(graphartifact.EdgeTypeOf))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var neighbors []graphquery.Neighbor
	for rows.Next() {
		var repositoryID int64
		var parentOccurrence, nodePayload, edgePayload []byte
		if err := rows.Scan(&repositoryID, &parentOccurrence, &nodePayload, &edgePayload); err != nil {
			return nil, err
		}
		symbol, err := legacySymbol(repositoryID, nodePayload)
		if err != nil {
			return nil, err
		}
		if edgePayload == nil {
			return nil, graphquery.ErrQuerySize
		}
		edge := new(graphv2.Edge)
		if err := proto.Unmarshal(edgePayload, edge); err != nil {
			return nil, err
		}
		confidence := 1.0
		if edge.Confidence != nil {
			confidence = *edge.Confidence
		}
		neighbors = append(neighbors, graphquery.Neighbor{
			Symbol: symbol,
			Parent: graphquery.SymbolRef{RepositoryID: repositoryID, UID: legacySymbolPrefix + string(parentOccurrence)},
			Edge: graphprotocol.Relationship{
				SourceRepositoryID: repositoryID, TargetRepositoryID: repositoryID,
				SourceUID: legacySymbolPrefix + edge.GetSource(), TargetUID: legacySymbolPrefix + edge.GetTarget(),
				Kind: query.Relation, Path: edge.GetLocation().GetPath(), Range: legacyPosition(edge.GetLocation()),
				Confidence: confidence, ResolutionReason: edge.GetResolutionReason(),
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// v1 lists several sites between one pair of symbols by file position. The
	// query can only order them by edge identity, so each pair's run is put in
	// position order here; which sites a page holds still follows the query.
	for start := 0; start < len(neighbors); {
		end := start + 1
		for end < len(neighbors) && neighbors[end].Symbol.UID == neighbors[start].Symbol.UID && neighbors[end].Parent == neighbors[start].Parent {
			end++
		}
		sort.SliceStable(neighbors[start:end], func(i, j int) bool {
			left, right := neighbors[start+i].Edge, neighbors[start+j].Edge
			if left.Path != right.Path {
				return left.Path < right.Path
			}
			return slices.Compare(
				[]int32{left.Range.StartLine, left.Range.StartCharacter, left.Range.EndLine, left.Range.EndCharacter},
				[]int32{right.Range.StartLine, right.Range.StartCharacter, right.Range.EndLine, right.Range.EndCharacter}) < 0
		})
		start = end
	}
	return neighbors, nil
}

// legacyV2NeighborsSQL joins the frontier to its edges through the
// (upload_id, source_key|target_key, kind) indexes. A v1 SCIP graph reported
// SCIP's is_type_definition as "extends"; the SCIP-derived generation stores it
// as type_of, so "extends" reads both there. An edge without a recorded
// confidence passes every threshold, as in the v2 tools.
func legacyV2NeighborsSQL(direction string) string {
	parent, neighbor := "source", "target"
	if direction == "incoming" {
		parent, neighbor = neighbor, parent
	}
	return `with scope as (
		select * from unnest($1::bigint[], $2::bigint[], $3::text[]) as value(repository_id, upload_id, commit)
	), frontier as (
		select * from unnest($4::bigint[], $5::bytea[]) as value(repository_id, occurrence)
	)
	select scope.repository_id, frontier.occurrence,
		case when octet_length(n.payload)<=$10 then n.payload end,
		case when octet_length(e.payload)<=$10 then e.payload end
	from scope
	join graph_uploads u on u.id=scope.upload_id and u.repository_id=scope.repository_id and u.commit=scope.commit and u.schema_version=2
	join frontier on frontier.repository_id=scope.repository_id
	join graph_v2_edges e on e.upload_id=u.id and e.` + parent + `_key=sha256(frontier.occurrence) and e.` + parent + `=frontier.occurrence
		and (e.kind=$6 or ($6=$11 and e.kind=$12 and u.source='scip')) and coalesce(e.confidence, 1) >= $7
	join graph_v2_nodes n on n.upload_id=u.id and n.occurrence_key=e.` + neighbor + `_key and n.occurrence=e.` + neighbor + ` and ` + legacyV2Symbols + `
	order by scope.repository_id, n.occurrence, frontier.occurrence, e.occurrence
	offset $8 limit $9`
}

// legacySymbol maps a v2 entity payload onto the v1 symbol; nil is the
// oversized-payload sentinel.
func legacySymbol(repositoryID int64, payload []byte) (graphprotocol.Symbol, error) {
	if payload == nil {
		return graphprotocol.Symbol{}, graphquery.ErrQuerySize
	}
	node := new(graphv2.Node)
	if err := proto.Unmarshal(payload, node); err != nil {
		return graphprotocol.Symbol{}, err
	}
	return graphprotocol.Symbol{
		UID: legacySymbolPrefix + node.GetOccurrence(), Name: node.GetName(), Kind: node.GetKind(),
		FilePath: node.GetPath(), Language: node.GetLanguage(), Signature: node.GetSignature(),
		RepositoryID: repositoryID, Range: legacyPosition(node.GetLocation()), Test: graphTestPath(node.GetPath()),
	}, nil
}

func legacyPosition(location *graphv2.Location) graphprotocol.Position {
	return graphprotocol.Position{
		StartLine: location.GetStart().GetLine(), StartCharacter: location.GetStart().GetCharacter(),
		EndLine: location.GetEnd().GetLine(), EndCharacter: location.GetEnd().GetCharacter(),
	}
}
