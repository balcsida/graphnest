package postgres

import (
	"context"
	"fmt"
	"strings"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"google.golang.org/protobuf/proto"
)

func (s *Store) QueryFiles(ctx context.Context, q graphquery.FileQuery) ([]graphprotocol.IndexedFile, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	if q.Limit <= 0 || q.Limit > 101 || q.Offset < 0 || q.Offset > 10_000_000 {
		return nil, graphquery.ErrInvalidRequest
	}
	ids, uploads, commits := graphScope(q.Snapshots)
	args := []any{ids, uploads, commits, graphquery.MaxEntityQueryBytes}
	filters := []string{}
	if q.Path != nil {
		args = append(args, []byte(*q.Path))
		p := fmt.Sprintf("$%d::bytea", len(args))
		filters = append(filters, "f.path_key=sha256("+p+") and f.path="+p)
	}
	if q.Directory != "" {
		args = append(args, []byte(q.Directory+"/"))
		p := fmt.Sprintf("$%d::bytea", len(args))
		filters = append(filters, "substring(f.path from 1 for octet_length("+p+"))="+p)
	}
	if q.Pattern != "" {
		args = append(args, q.Pattern)
		filters = append(filters, fmt.Sprintf("convert_from(f.path,'UTF8') ~ $%d", len(args)))
	}
	where := ""
	if len(filters) > 0 {
		where = " where " + strings.Join(filters, " and ")
	}
	args = append(args, q.Offset, q.Limit)
	rows, err := s.pool.Query(ctx, `with scope as (select * from unnest($1::bigint[],$2::bigint[],$3::text[]) as v(repository_id,upload_id,commit))
 select u.repository_id,case when octet_length(f.payload)<=$4 then f.payload end
 from scope join graph_uploads u on u.id=scope.upload_id and u.repository_id=scope.repository_id and u.commit=scope.commit and u.schema_version=2
 join graph_v2_files f on f.upload_id=u.id`+where+fmt.Sprintf(" order by u.repository_id,f.path offset $%d limit $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []graphprotocol.IndexedFile{}
	used := 0
	for rows.Next() {
		var f graphprotocol.IndexedFile
		var payload []byte
		if err := rows.Scan(&f.RepositoryID, &payload); err != nil {
			return nil, err
		}
		if payload == nil {
			return nil, graphquery.ErrQuerySize
		}
		f.Fact = new(graphv2.File)
		if err := proto.Unmarshal(payload, f.Fact); err != nil {
			return nil, err
		}
		if err := graphquery.AddEntityQueryBytes(&used, f); err != nil {
			return nil, err
		}
		result = append(result, f)
	}
	return result, rows.Err()
}
