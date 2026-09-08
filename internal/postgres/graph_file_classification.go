package postgres

import (
	"context"
	"fmt"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphquery"
	"google.golang.org/protobuf/proto"
)

func (s *Store) ClassifyFiles(ctx context.Context, q graphquery.FileClassificationQuery) ([]graphquery.FileClassificationRow, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	if len(q.Snapshots) != 1 || len(q.Paths) == 0 || len(q.Paths) > 64 {
		return nil, graphquery.ErrInvalidRequest
	}
	sql, args := fileClassificationSQL(q)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]graphquery.FileClassificationRow, 0, len(q.Paths))
	for rows.Next() {
		var row graphquery.FileClassificationRow
		var generated, flagPresent bool
		var payload []byte
		if err = rows.Scan(&row.RepositoryID, &row.Path, &row.Present, &generated, &flagPresent, &row.Ambient, &payload); err != nil {
			return nil, err
		}
		if row.Present {
			if len(payload) == 0 {
				return nil, fmt.Errorf("decode graph file %q: empty payload", row.Path)
			}
			var file graphv2.File
			if err = proto.Unmarshal(payload, &file); err != nil {
				return nil, fmt.Errorf("decode graph file %q: %w", row.Path, err)
			}
			row.Ambient = row.Ambient && file.Errors == nil
		}
		if flagPresent {
			row.PersistedGenerated = &generated
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func fileClassificationSQL(q graphquery.FileClassificationQuery) (string, []any) {
	ids, uploads, commits := graphScope(q.Snapshots)
	paths := make([][]byte, len(q.Paths))
	for i := range q.Paths {
		paths[i] = []byte(q.Paths[i])
	}
	return `with scope as (select * from unnest($1::bigint[],$2::bigint[],$3::text[]) as v(repository_id,upload_id,commit)),
 candidates as (select * from unnest($4::bytea[]) with ordinality as v(path,ordinal)),
 candidate_nodes as materialized (
  select u.repository_id,u.id upload_id,c.path,c.ordinal,n.occurrence_key,n.occurrence,n.kind
  from scope join graph_uploads u on u.id=scope.upload_id and u.repository_id=scope.repository_id and u.commit=scope.commit and u.schema_version=2
  cross join candidates c
  join graph_v2_nodes n on n.upload_id=u.id and sha256(n.path)=sha256(c.path) and n.path=c.path),
 declarations as (
  select repository_id,upload_id,path,ordinal,
   count(*) filter (where kind not in ('file','import','export','parameter')) declared,
   count(*) filter (where kind in ('interface','type_alias','enum','enum_member','namespace')) type_declared
  from candidate_nodes group by repository_id,upload_id,path,ordinal),
 behavior as (
  select distinct n.repository_id,n.upload_id,n.ordinal
  from candidate_nodes n join graph_v2_edges e on e.upload_id=n.upload_id and e.source_key=n.occurrence_key and e.source=n.occurrence
  where e.kind in (4,10)),
 incoming as (
  select distinct target.repository_id,target.upload_id,target.ordinal
  from candidate_nodes target
  join graph_v2_edges e on e.upload_id=target.upload_id and e.target_key=target.occurrence_key and e.target=target.occurrence
  join graph_v2_nodes source on source.upload_id=e.upload_id and source.occurrence_key=e.source_key and source.occurrence=e.source
  where source.path is not null and source.path<>target.path)
 select u.repository_id,convert_from(c.path,'UTF8'),f.path is not null,coalesce(f.generated,false),f.generated is not null,
       coalesce(f.path is not null and d.declared>0 and d.declared=d.type_declared and b.ordinal is null and i.ordinal is null,false),f.payload
 from scope join graph_uploads u on u.id=scope.upload_id and u.repository_id=scope.repository_id and u.commit=scope.commit and u.schema_version=2
 cross join candidates c
 left join graph_v2_files f on f.upload_id=u.id and f.path_key=sha256(c.path) and f.path=c.path
 left join declarations d on d.repository_id=u.repository_id and d.upload_id=u.id and d.ordinal=c.ordinal
 left join behavior b on b.repository_id=u.repository_id and b.upload_id=u.id and b.ordinal=c.ordinal
 left join incoming i on i.repository_id=u.repository_id and i.upload_id=u.id and i.ordinal=c.ordinal
 order by c.ordinal`, []any{ids, uploads, commits, paths}
}

func (s *Store) CountGeneratedFiles(ctx context.Context, snapshots []graphquery.QuerySnapshot) (generated, total int64, err error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	if len(snapshots) != 1 {
		return 0, 0, graphquery.ErrInvalidRequest
	}
	ids, uploads, commits := graphScope(snapshots)
	err = s.pool.QueryRow(ctx, `with scope as (select * from unnest($1::bigint[],$2::bigint[],$3::text[]) as v(repository_id,upload_id,commit))
 select count(*) filter (where f.generated is true),count(*)
 from scope join graph_uploads u on u.id=scope.upload_id and u.repository_id=scope.repository_id and u.commit=scope.commit and u.schema_version=2
 join graph_v2_files f on f.upload_id=u.id`, ids, uploads, commits).Scan(&generated, &total)
	return generated, total, err
}
