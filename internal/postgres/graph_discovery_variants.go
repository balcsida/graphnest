package postgres

import (
	"context"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"google.golang.org/protobuf/proto"
)

func (s *Store) discoveryVariantsReady(ctx context.Context, snapshots []graphquery.QuerySnapshot) error {
	ids, uploads, commits := graphScope(snapshots)
	var ready int
	if err := s.pool.QueryRow(ctx, `with scope as (select * from unnest($1::bigint[],$2::bigint[],$3::text[]) as v(repository_id,upload_id,commit)) select count(*) from scope join graph_uploads u on u.id=scope.upload_id and u.repository_id=scope.repository_id and u.commit=scope.commit and u.discovery_version>=2`, ids, uploads, commits).Scan(&ready); err != nil {
		return err
	}
	if ready != len(snapshots) {
		return graphquery.ErrDiscoveryUnavailable
	}
	return ctx.Err()
}

func (s *Store) QuerySegments(ctx context.Context, q graphquery.SegmentSearch) ([]graphprotocol.SegmentMatch, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	if q.Limit <= 0 || q.Limit > 101 || len(q.Variants) > 96 || len(q.Variants) != len(q.Words) {
		return nil, graphquery.ErrInvalidRequest
	}
	if err := s.discoveryVariantsReady(ctx, q.Snapshots); err != nil {
		return nil, err
	}
	result := []graphprotocol.SegmentMatch{}
	if len(q.Variants) == 0 {
		return result, ctx.Err()
	}
	ids, uploads, commits := graphScope(q.Snapshots)
	rows, err := s.pool.Query(ctx, `with scope as (select * from unnest($1::bigint[],$2::bigint[],$3::text[]) as v(repository_id,upload_id,commit)),
 words as (select * from unnest($4::text[],$5::text[]) as v(segment,word)),
 live as materialized (
 select distinct on (d.original_name) u.repository_id,u.id upload_id,d.occurrence_key,d.original_name,d.name_size,d.segments,
 array(select distinct word from words where segment=any(d.segments) order by word) matched
 from scope join graph_uploads u on u.id=scope.upload_id and u.repository_id=scope.repository_id and u.commit=scope.commit and u.discovery_version>=2
 join graph_v2_discovery d on d.upload_id=u.id
 join graph_v2_nodes n on n.upload_id=d.upload_id and n.occurrence_key=d.occurrence_key and n.name=d.original_name
 where d.segments && $4::text[] and n.kind not in ('file','import')
 order by d.original_name,u.repository_id,n.path,n.occurrence),
 tier_a as materialized (select * from live where cardinality(matched)>=2),
 rare as (select w.segment,w.word from words w join live l on w.segment=any(l.segments) where char_length(w.word)>=5 group by w.segment,w.word having count(*) between 2 and 25),
 eligible as (
 select l.repository_id,l.upload_id,l.occurrence_key,l.name_size,
 case when exists(select from tier_a) then l.matched else array(select distinct word from rare where segment=any(l.segments) order by word) end matched
 from live l where (exists(select from tier_a) and cardinality(l.matched)>=2)
 or (not exists(select from tier_a) and cardinality(l.segments)>=2 and exists(select from rare where segment=any(l.segments)))),
 ranked as materialized (select * from eligible order by cardinality(matched) desc,name_size,repository_id,occurrence_key limit $6)
 select r.repository_id,u.public_repository,u.producer_name,u.producer_version,u.producer_configuration,
 case when octet_length(n.payload)<=$7 then n.payload end,r.matched
 from ranked r join graph_uploads u on u.id=r.upload_id join graph_v2_nodes n on n.upload_id=r.upload_id and n.occurrence_key=r.occurrence_key
 order by cardinality(r.matched) desc,r.name_size,r.repository_id,r.occurrence_key`, ids, uploads, commits, q.Variants, q.Words, q.Limit, graphquery.MaxEntityQueryBytes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	used := 0
	for rows.Next() {
		var m graphprotocol.SegmentMatch
		var repository string
		var name, version, configuration, payload []byte
		if err = rows.Scan(&m.Entity.RepositoryID, &repository, &name, &version, &configuration, &payload, &m.MatchedWords); err != nil {
			return nil, err
		}
		if payload == nil {
			return nil, graphquery.ErrQuerySize
		}
		m.Entity.Fact = new(graphv2.Node)
		if err = proto.Unmarshal(payload, m.Entity.Fact); err != nil {
			return nil, err
		}
		m.Entity.ID, err = graphartifact.IdentityV2(&graphv2.Producer{Name: string(name), Version: string(version), Configuration: string(configuration)}, repository, m.Entity.Fact.SourceId, m.Entity.Fact.Occurrence)
		if err != nil {
			return nil, err
		}
		if err = graphquery.AddEntityQueryBytes(&used, m); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}
