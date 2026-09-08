package postgres

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"google.golang.org/protobuf/proto"
)

var _ graphquery.EntityStore = (*Store)(nil)

// Payload CASE expressions return a sentinel for oversized facts. Do not turn
// them into WHERE filters: dropping facts would falsely report complete answers.
// Only the caller's authorized repository set is inspected. The repository's
// current indexed SHA and active generation are checked together in one query.
func (s *Store) EntityGenerations(ctx context.Context, snapshots []graphquery.QuerySnapshot) ([]graphprotocol.Generation, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	ids, _, commits := graphScope(snapshots)
	rows, err := s.pool.Query(ctx, `with scope as (
 select * from unnest($1::bigint[],$2::text[]) as v(repository_id,commit)
 ) select u.repository_id,u.id,u.commit,case when octet_length(u.artifact_header)<=$3 then u.artifact_header end,u.capabilities,u.node_count,u.edge_count,r.github_id,u.publisher,
 (select count(*) from graph_v2_unresolved where upload_id=u.id),
 (select count(*) from graph_v2_diagnostics where upload_id=u.id)
 from scope join repositories r on r.id=scope.repository_id and r.indexed_sha=scope.commit and r.enabled and not r.archived
 join installations i on i.id=r.installation_id and i.status='active'
 join graph_uploads u on u.repository_id=r.id and u.commit=scope.commit and u.active and u.schema_version=2
 order by u.repository_id`, ids, commits, graphquery.MaxEntityQueryBytes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []graphprotocol.Generation{}
	usedBytes := 0
	for rows.Next() {
		var g graphprotocol.Generation
		var header []byte
		var publicID int64
		if err = rows.Scan(&g.RepositoryID, &g.UploadID, &g.Commit, &header, &g.Capabilities, &g.NodeCount, &g.EdgeCount, &publicID, &g.Publisher, &g.UnresolvedCount, &g.DiagnosticCount); err != nil {
			return nil, err
		}
		a := new(graphv2.Artifact)
		if header == nil {
			return nil, graphquery.ErrQuerySize
		}
		if err = proto.Unmarshal(header, a); err != nil {
			return nil, err
		}
		if a.Repository != strconv.FormatInt(publicID, 10) {
			return nil, graphquery.ErrGenerationChanged
		}
		g.Repository = a.Repository
		g.Producer = a.Producer
		g.ContentHash = a.ContentHash
		g.Metadata = a.Metadata
		g.Extensions = a.Extensions
		if err = graphquery.AddEntityQueryBytes(&usedBytes, g); err != nil {
			return nil, err
		}
		result = append(result, g)
	}
	return result, rows.Err()
}

func (s *Store) QueryEntities(ctx context.Context, q graphquery.EntityQuery) ([]graphprotocol.Entity, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	if q.Limit <= 0 || q.Limit > 1001 || q.Offset < 0 || q.Offset > 10_000_000 {
		return nil, graphquery.ErrInvalidRequest
	}
	ids, uploads, commits := graphScope(q.Snapshots)
	args := []any{ids, uploads, commits, graphquery.MaxEntityQueryBytes}
	filters := []string{}
	join := ""
	order := "u.repository_id,n.occurrence"
	if m := q.Selector.NameMatch; m != nil {
		if err := s.discoveryVariantsReady(ctx, q.Snapshots); err != nil {
			return nil, err
		}
		join = " join graph_v2_discovery d on d.upload_id=n.upload_id and d.occurrence_key=n.occurrence_key"
		args = append(args, m.Kinds)
		filters = append(filters, "(coalesce(cardinality($5::text[]),0)=0 or n.kind=any($5::text[]))")
		if m.Mode == "prefix" {
			prefix := []byte(m.Value)[:min(len(m.Value), 128)]
			upper := append([]byte{}, prefix...)
			if len(upper) > 0 {
				upper[len(upper)-1]++
			}
			args = append(args, []byte(m.Value), prefix, upper)
			filters = append(filters, "substring(d.original_name from 1 for octet_length($6::bytea))=$6::bytea and (octet_length($7::bytea)=0 or (substring(d.original_name from 1 for 128)>=$7::bytea and substring(d.original_name from 1 for 128)<$8::bytea))")
			order = "d.original_name," + order
		} else {
			args = append(args, []byte(graphquery.FoldName(m.Value)), graphquery.NameGrams(m.Value), m.ExcludePrefix)
			filters = append(filters, "d.selector_grams @> $7::text[] and position($6::bytea in d.folded_name)>0 and (not $8::boolean or substring(d.folded_name from 1 for octet_length($6::bytea))<>$6::bytea)")
			order = "d.name_size," + order
		}
	}
	add := func(column string, value *string) {
		if value != nil {
			args = append(args, []byte(*value))
			p := fmt.Sprintf("$%d::bytea", len(args))
			filters = append(filters, "sha256(n."+column+")=sha256("+p+") and n."+column+"="+p)
		}
	}
	add("occurrence", q.Selector.Occurrence)
	add("name", q.Selector.Name)
	add("qualified_name", q.Selector.QualifiedName)
	add("path", q.Selector.Path)
	if q.Selector.Kind != "" {
		args = append(args, q.Selector.Kind)
		filters = append(filters, fmt.Sprintf("n.kind=$%d", len(args)))
	}
	where := ""
	if len(filters) > 0 {
		where = " where " + strings.Join(filters, " and ")
	}
	args = append(args, q.Offset, q.Limit)
	sql := `with scope as (select * from unnest($1::bigint[],$2::bigint[],$3::text[]) as v(repository_id,upload_id,commit))
 select u.repository_id,u.public_repository,u.producer_name,u.producer_version,u.producer_configuration,case when octet_length(n.payload)<=$4 then n.payload end
 from scope join graph_uploads u on u.id=scope.upload_id and u.repository_id=scope.repository_id and u.commit=scope.commit and u.schema_version=2
 join graph_v2_nodes n on n.upload_id=u.id` + join + where + fmt.Sprintf(" order by %s offset $%d limit $%d", order, len(args)-1, len(args))
	if q.Selector.NameMatch != nil {
		// Select bounded identities before touching original protobuf payloads.
		sql = `with scope as (select * from unnest($1::bigint[],$2::bigint[],$3::text[]) as v(repository_id,upload_id,commit)),
 name_candidates as materialized (
 select u.id upload_id,u.repository_id,n.occurrence_key,n.occurrence,d.original_name,d.name_size
 from scope join graph_uploads u on u.id=scope.upload_id and u.repository_id=scope.repository_id and u.commit=scope.commit and u.discovery_version=2
 join graph_v2_nodes n on n.upload_id=u.id` + join + where + fmt.Sprintf(" order by %s offset $%d limit $%d", order, len(args)-1, len(args)) + `)
 select u.repository_id,u.public_repository,u.producer_name,u.producer_version,u.producer_configuration,case when octet_length(n.payload)<=$4 then n.payload end
 from name_candidates d join graph_uploads u on u.id=d.upload_id join graph_v2_nodes n on n.upload_id=d.upload_id and n.occurrence_key=d.occurrence_key order by ` + order
	}
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []graphprotocol.Entity{}
	usedBytes := 0
	for rows.Next() {
		var e graphprotocol.Entity
		var repository string
		var name, version, configuration, payload []byte
		if err = rows.Scan(&e.RepositoryID, &repository, &name, &version, &configuration, &payload); err != nil {
			return nil, err
		}
		e.Fact = new(graphv2.Node)
		if payload == nil {
			return nil, graphquery.ErrQuerySize
		}
		if err = proto.Unmarshal(payload, e.Fact); err != nil {
			return nil, err
		}
		e.ID, err = graphartifact.IdentityV2(&graphv2.Producer{Name: string(name), Version: string(version), Configuration: string(configuration)}, repository, e.Fact.SourceId, e.Fact.Occurrence)
		if err != nil {
			return nil, err
		}
		if err = graphquery.AddEntityQueryBytes(&usedBytes, e); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (s *Store) EntityNeighbors(ctx context.Context, q graphquery.EntityNeighborQuery) ([]graphquery.EntityNeighbor, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	relation, ok := graphartifact.ParseRelationship(q.Relation)
	if !ok || (q.Direction != "incoming" && q.Direction != "outgoing") || q.Limit <= 0 || q.Limit > 101 || math.IsNaN(q.MinConfidence) || math.IsInf(q.MinConfidence, 0) || q.MinConfidence < 0 || q.MinConfidence > 1 {
		return nil, graphquery.ErrInvalidRequest
	}
	parent, neighbor := "source", "target"
	if q.Direction == "incoming" {
		parent, neighbor = neighbor, parent
	}
	// These identifiers are selected above, never interpolated from input. Hashes
	// bound index keys; original bytes disambiguate every lookup and endpoint join.
	sql := `select u.repository_id,u.public_repository,u.producer_name,u.producer_version,u.producer_configuration,
 case when octet_length(n.payload)<=$8 then n.payload end,
 case when octet_length(p.payload)<=$8 then p.payload end,
 case when octet_length(e.payload)<=$8 then e.payload end
 from graph_uploads u join graph_v2_edges e on e.upload_id=u.id
 join graph_v2_nodes p on p.upload_id=u.id and p.occurrence_key=e.` + parent + `_key and p.occurrence=e.` + parent + `
 join graph_v2_nodes n on n.upload_id=u.id and n.occurrence_key=e.` + neighbor + `_key and n.occurrence=e.` + neighbor + `
 where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2
 and e.` + parent + `_key=sha256($4::bytea) and e.` + parent + `=$4::bytea and e.kind=$5
 and (e.confidence is null or e.confidence >= $6)
 order by n.occurrence,e.occurrence limit $7`
	rows, err := s.pool.Query(ctx, sql, q.Snapshot.RepositoryID, q.Snapshot.UploadID, q.Snapshot.Commit, []byte(q.Occurrence), int16(relation.Kind), q.MinConfidence, q.Limit, graphquery.MaxEntityQueryBytes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []graphquery.EntityNeighbor{}
	usedBytes := 0
	for rows.Next() {
		var row graphquery.EntityNeighbor
		var repository string
		var name, version, configuration, nodeBytes, parentBytes, edgeBytes []byte
		if err = rows.Scan(&row.Entity.RepositoryID, &repository, &name, &version, &configuration, &nodeBytes, &parentBytes, &edgeBytes); err != nil {
			return nil, err
		}
		producer := &graphv2.Producer{Name: string(name), Version: string(version), Configuration: string(configuration)}
		row.Entity.Fact = new(graphv2.Node)
		p := new(graphv2.Node)
		row.Edge.Fact = new(graphv2.Edge)
		for _, pair := range []struct {
			data    []byte
			message proto.Message
		}{{nodeBytes, row.Entity.Fact}, {parentBytes, p}, {edgeBytes, row.Edge.Fact}} {
			if pair.data == nil {
				return nil, graphquery.ErrQuerySize
			}
			if err = proto.Unmarshal(pair.data, pair.message); err != nil {
				return nil, err
			}
		}
		row.Entity.ID, err = graphartifact.IdentityV2(producer, repository, row.Entity.Fact.SourceId, row.Entity.Fact.Occurrence)
		if err != nil {
			return nil, err
		}
		parentID, err := graphartifact.IdentityV2(producer, repository, p.SourceId, p.Occurrence)
		if err != nil {
			return nil, err
		}
		row.Edge.RepositoryID = row.Entity.RepositoryID
		row.Edge.SourceID = parentID
		row.Edge.TargetID = row.Entity.ID
		if q.Direction == "incoming" {
			row.Edge.SourceID, row.Edge.TargetID = row.Edge.TargetID, row.Edge.SourceID
		}
		if err = graphquery.AddEntityQueryBytes(&usedBytes, row); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
