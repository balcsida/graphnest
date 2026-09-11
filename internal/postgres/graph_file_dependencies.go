package postgres

import (
	"context"
	"fmt"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphquery"
)

func (s *Store) QueryFileDependencyPairs(ctx context.Context, q graphquery.FileDependencyQuery) ([]graphquery.FileDependencyPairRow, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	if q.Snapshot.RepositoryID <= 0 || q.Snapshot.UploadID <= 0 || q.Snapshot.Commit == "" || q.Limit <= 0 || q.Limit > 5001 || q.Direction != "" && q.Direction != "source" && q.Direction != "target" || q.Direction != "" && len(q.Paths) == 0 {
		return nil, graphquery.ErrInvalidRequest
	}
	statement, args := fileDependencyStatement(q)
	rows, err := s.pool.Query(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []graphquery.FileDependencyPairRow{}
	usedBytes := 0
	for rows.Next() {
		var row graphquery.FileDependencyPairRow
		var source, target []byte
		if err := rows.Scan(&row.RepositoryID, &source, &target, &row.References); err != nil {
			return nil, err
		}
		usedBytes += len(source) + len(target)
		if usedBytes > graphquery.MaxEntityQueryBytes {
			return nil, graphquery.ErrQuerySize
		}
		row.Source, row.Target = string(source), string(target)
		result = append(result, row)
	}
	return result, rows.Err()
}

func fileDependencyStatement(q graphquery.FileDependencyQuery) (string, []any) {
	contains, _ := graphartifact.ParseRelationship("contains")
	args := []any{q.Snapshot.RepositoryID, q.Snapshot.UploadID, q.Snapshot.Commit, int16(contains.Kind), q.MinConfidence, q.Limit}
	from := ` from graph_uploads u join graph_v2_edges e on e.upload_id=u.id
	 join lateral (select path from graph_v2_nodes where upload_id=e.upload_id and occurrence_key=e.source_key and occurrence=e.source and path is not null offset 0) sn on true
	 join lateral (select path from graph_v2_nodes where upload_id=e.upload_id and occurrence_key=e.target_key and occurrence=e.target and path is not null offset 0) tn on true`
	where := ` where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2
	 and e.kind<>$4 and (e.confidence is null or e.confidence >= $5) and sn.path<>tn.path`
	if q.Direction != "" {
		args = append(args, stringsToBytes(q.Paths))
		if q.Direction == "source" {
			from = ` from graph_uploads u join unnest($7::bytea[]) requested(path) on true
			 join lateral (select occurrence_key,occurrence,path from graph_v2_nodes where upload_id=u.id and sha256(path)=sha256(requested.path) and path=requested.path offset 0) sn on true
			 join lateral (select upload_id,target_key,target from graph_v2_edges where upload_id=u.id and source_key=sn.occurrence_key and source=sn.occurrence and kind<>$4 and (confidence is null or confidence >= $5) offset 0) e on true
			 join lateral (select path from graph_v2_nodes where upload_id=e.upload_id and occurrence_key=e.target_key and occurrence=e.target and path is not null offset 0) tn on true`
		} else {
			from = ` from graph_uploads u join unnest($7::bytea[]) requested(path) on true
			 join lateral (select occurrence_key,occurrence,path from graph_v2_nodes where upload_id=u.id and sha256(path)=sha256(requested.path) and path=requested.path offset 0) tn on true
			 join lateral (select upload_id,source_key,source from graph_v2_edges where upload_id=u.id and target_key=tn.occurrence_key and target=tn.occurrence and kind<>$4 and (confidence is null or confidence >= $5) offset 0) e on true
			 join lateral (select path from graph_v2_nodes where upload_id=e.upload_id and occurrence_key=e.source_key and occurrence=e.source and path is not null offset 0) sn on true`
		}
		where = ` where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2 and sn.path<>tn.path`
	}
	return fmt.Sprintf(`select u.repository_id,sn.path,tn.path,count(*)%s%s group by u.repository_id,sn.path,tn.path order by sn.path,tn.path limit $6`, from, where), args
}

func stringsToBytes(values []string) [][]byte {
	result := make([][]byte, 0, len(values))
	for _, value := range values {
		result = append(result, []byte(value))
	}
	return result
}
