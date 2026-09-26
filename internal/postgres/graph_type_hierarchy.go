package postgres

import (
	"context"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphquery"
)

// One row per distinct direct subtype, matching the viewer list: a type tied to
// its supertype by both extends and implements counts once, as extends.
const hierarchyChildCountSQL = `select count(*), count(*) filter (where not has_extends) from (
 select bool_or(e.kind=$5) has_extends
 from graph_uploads u join graph_v2_edges e on e.upload_id=u.id
 join graph_v2_nodes t on t.upload_id=u.id and t.occurrence_key=e.target_key and t.occurrence=e.target
 join graph_v2_nodes s on s.upload_id=u.id and s.occurrence_key=e.source_key and s.occurrence=e.source
 where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2
 and e.target_key=sha256($4::bytea) and e.target=$4::bytea and e.kind in ($5,$6) and e.source<>$4::bytea
 group by e.source) children`

func (s *Store) CountHierarchyChildren(ctx context.Context, q graphquery.HierarchyCountQuery) (graphquery.HierarchyCount, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	if q.Snapshot.RepositoryID <= 0 || q.Snapshot.UploadID <= 0 || q.Snapshot.Commit == "" || q.Occurrence == "" {
		return graphquery.HierarchyCount{}, graphquery.ErrInvalidRequest
	}
	extends, _ := graphartifact.ParseRelationship("extends")
	implements, _ := graphartifact.ParseRelationship("implements")
	var count graphquery.HierarchyCount
	err := s.pool.QueryRow(ctx, hierarchyChildCountSQL, q.Snapshot.RepositoryID, q.Snapshot.UploadID, q.Snapshot.Commit, []byte(q.Occurrence), int16(extends.Kind), int16(implements.Kind)).Scan(&count.Subtypes, &count.Implementers)
	return count, err
}
