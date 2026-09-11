package postgres

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
)

var _ graphquery.AggregateStore = (*Store)(nil)

func validAggregateSnapshot(snapshot graphquery.QuerySnapshot) bool {
	return snapshot.RepositoryID > 0 && snapshot.UploadID > 0 && snapshot.Commit != ""
}

func (s *Store) AggregateStats(ctx context.Context, snapshot graphquery.QuerySnapshot) (graphprotocol.GraphStats, error) {
	if !validAggregateSnapshot(snapshot) {
		return graphprotocol.GraphStats{}, graphquery.ErrInvalidRequest
	}
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `select category,value,count(*) from graph_uploads u join lateral (
	 select convert_to('node','UTF8') category,convert_to(kind,'UTF8') value from graph_v2_nodes where upload_id=u.id
	 union all select convert_to('edge','UTF8'),convert_to(kind::text,'UTF8') from graph_v2_edges where upload_id=u.id
	 union all select convert_to('file','UTF8'),language from graph_v2_files where upload_id=u.id
	 ) facts on true where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2
	 group by category,value order by category,value`, snapshot.RepositoryID, snapshot.UploadID, snapshot.Commit)
	if err != nil {
		return graphprotocol.GraphStats{}, err
	}
	defer rows.Close()
	result := graphprotocol.GraphStats{NodesByKind: map[string]int{}, EdgesByKind: map[string]int{}, FilesByLanguage: map[string]int{}}
	for rows.Next() {
		var categoryBytes, valueBytes []byte
		var count int
		if err = rows.Scan(&categoryBytes, &valueBytes, &count); err != nil {
			return graphprotocol.GraphStats{}, err
		}
		category, value := string(categoryBytes), string(valueBytes)
		switch category {
		case "node":
			result.NodesByKind[value] = count
			result.NodeCount += count
		case "edge":
			kind := int32(0)
			if _, scanErr := fmt.Sscan(value, &kind); scanErr != nil {
				return graphprotocol.GraphStats{}, scanErr
			}
			relation, ok := graphartifact.RelationshipFromWire(graphv2.EdgeKind(kind))
			if !ok {
				return graphprotocol.GraphStats{}, graphquery.ErrGenerationChanged
			}
			result.EdgesByKind[relation.Name] = count
			result.EdgeCount += count
		case "file":
			result.FilesByLanguage[value] = count
			result.FileCount += count
		default:
			return graphprotocol.GraphStats{}, graphquery.ErrGenerationChanged
		}
	}
	if err = rows.Err(); err != nil {
		return graphprotocol.GraphStats{}, err
	}
	if err = s.pool.QueryRow(ctx, `select count(*) from graph_uploads u join graph_v2_unresolved r on r.upload_id=u.id
	 where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2`, snapshot.RepositoryID, snapshot.UploadID, snapshot.Commit).Scan(&result.UnresolvedCount); err != nil {
		return graphprotocol.GraphStats{}, err
	}
	return result, nil
}

func (s *Store) QueryFan(ctx context.Context, q graphquery.AggregateFanQuery) ([]graphprotocol.AggregateCount, error) {
	if !validAggregateSnapshot(q.Snapshot) || len(q.Values) == 0 {
		return nil, graphquery.ErrInvalidRequest
	}
	column := "source"
	if q.Incoming {
		column = "target"
	}
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `with requested as (select distinct value from unnest($4::bytea[]) value)
 select e.`+column+`,count(*) from graph_uploads u join graph_v2_edges e on e.upload_id=u.id
 join requested r on e.`+column+`_key=sha256(r.value) and e.`+column+`=r.value
 where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2
 group by e.`+column+` order by e.`+column, q.Snapshot.RepositoryID, q.Snapshot.UploadID, q.Snapshot.Commit, stringsToBytes(q.Values))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []graphprotocol.AggregateCount{}
	for rows.Next() {
		var id []byte
		var value graphprotocol.AggregateCount
		if err = rows.Scan(&id, &value.Count); err != nil {
			return nil, err
		}
		value.ID = string(id)
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) QueryNodeMetrics(ctx context.Context, snapshot graphquery.QuerySnapshot, occurrence string) (graphprotocol.NodeMetrics, error) {
	if !validAggregateSnapshot(snapshot) {
		return graphprotocol.NodeMetrics{}, graphquery.ErrInvalidRequest
	}
	calls, _ := graphartifact.ParseRelationship("calls")
	contains, _ := graphartifact.ParseRelationship("contains")
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	var result graphprotocol.NodeMetrics
	err := s.pool.QueryRow(ctx, `with recursive ancestors(id) as (
 select $4::bytea where exists(select 1 from graph_v2_nodes where upload_id=$2 and occurrence_key=sha256($4::bytea) and occurrence=$4::bytea)
 union select e.source from ancestors a join graph_v2_edges e on e.upload_id=$2 and e.target_key=sha256(a.id) and e.target=a.id and e.kind=$6
 ) select
 (select count(*) from graph_v2_edges where upload_id=$2 and target_key=sha256($4::bytea) and target=$4::bytea),
 (select count(*) from graph_v2_edges where upload_id=$2 and source_key=sha256($4::bytea) and source=$4::bytea),
 (select count(*) from graph_v2_edges where upload_id=$2 and source_key=sha256($4::bytea) and source=$4::bytea and kind=$5),
 (select count(*) from graph_v2_edges where upload_id=$2 and target_key=sha256($4::bytea) and target=$4::bytea and kind=$5),
 (select count(*) from graph_v2_edges where upload_id=$2 and source_key=sha256($4::bytea) and source=$4::bytea and kind=$6),
 greatest((select count(*) from ancestors)-1,0)
 from graph_uploads where repository_id=$1 and id=$2 and commit=$3 and schema_version=2`, snapshot.RepositoryID, snapshot.UploadID, snapshot.Commit, []byte(occurrence), int16(calls.Kind), int16(contains.Kind)).Scan(
		&result.IncomingEdgeCount, &result.OutgoingEdgeCount, &result.CallCount, &result.CallerCount, &result.ChildCount, &result.Depth)
	return result, err
}

func (s *Store) QueryAggregateNames(ctx context.Context, q graphquery.AggregateNamesQuery) ([]string, error) {
	if !validAggregateSnapshot(q.Snapshot) || len(q.Values) == 0 || q.Limit <= 0 {
		return nil, graphquery.ErrInvalidRequest
	}
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	values := stringsToBytes(q.Values)
	var rows pgx.Rows
	var err error
	switch q.Operation {
	case "ambiguous":
		contains, _ := graphartifact.ParseRelationship("contains")
		rows, err = s.pool.Query(ctx, `with requested as (select distinct value from unnest($4::bytea[]) value)
 select n.name from graph_uploads u join graph_v2_nodes n on n.upload_id=u.id
 join requested r on n.name= r.value and n.name is not null
 where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2
 group by n.name having count(*)>1 and bool_or(exists(select 1 from graph_v2_edges e where e.upload_id=u.id and e.target_key=n.occurrence_key and e.target=n.occurrence and e.kind<>$5))
 order by n.name`, q.Snapshot.RepositoryID, q.Snapshot.UploadID, q.Snapshot.Commit, values, int16(contains.Kind))
	case "exports":
		rows, err = s.pool.Query(ctx, `with requested as (select distinct value from unnest($4::bytea[]) value)
 select n.language from graph_uploads u join graph_v2_nodes n on n.upload_id=u.id join requested r on n.language=r.value
 where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2
 group by n.language having bool_or(n.is_exported) order by n.language`, q.Snapshot.RepositoryID, q.Snapshot.UploadID, q.Snapshot.Commit, values)
	case "unresolved":
		rows, err = s.pool.Query(ctx, `select r.payload from graph_uploads u join graph_v2_unresolved r on r.upload_id=u.id
 where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2 order by r.ordinal limit $4`, q.Snapshot.RepositoryID, q.Snapshot.UploadID, q.Snapshot.Commit, q.Limit)
	default:
		return nil, graphquery.ErrInvalidRequest
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	if q.Operation != "unresolved" {
		for rows.Next() {
			var value []byte
			if err = rows.Scan(&value); err != nil {
				return nil, err
			}
			result = append(result, string(value))
		}
		return result, rows.Err()
	}
	wanted, found := map[string]bool{}, map[string]bool{}
	for _, value := range q.Values {
		wanted[value] = true
	}
	count := 0
	used := 0
	for rows.Next() {
		count++
		var payload []byte
		if err = rows.Scan(&payload); err != nil {
			return nil, err
		}
		ref := new(graphv2.UnresolvedReference)
		if err = proto.Unmarshal(payload, ref); err != nil {
			return nil, err
		}
		if err = graphquery.AddEntityQueryBytes(&used, ref); err != nil {
			return nil, err
		}
		values := []string{ref.Name}
		if ref.NameTail != nil {
			values = append(values, *ref.NameTail)
		}
		for _, value := range values {
			if wanted[value] {
				found[value] = true
			}
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if count == q.Limit {
		return nil, graphquery.ErrQuerySize
	}
	for value := range found {
		result = append(result, value)
	}
	return result, nil
}

func (s *Store) QueryTopDependedOn(ctx context.Context, q graphquery.AggregateLimitQuery) ([]graphprotocol.DependedOn, error) {
	if !validAggregateSnapshot(q.Snapshot) || q.Limit <= 0 {
		return nil, graphquery.ErrInvalidRequest
	}
	contains, _ := graphartifact.ParseRelationship("contains")
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `select e.target,count(distinct e.source) from graph_uploads u join graph_v2_edges e on e.upload_id=u.id
 where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2 and e.kind<>$4 and e.source<>e.target
 group by e.target order by count(distinct e.source) desc,e.target limit $5`, q.Snapshot.RepositoryID, q.Snapshot.UploadID, q.Snapshot.Commit, int16(contains.Kind), q.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []graphprotocol.DependedOn{}
	for rows.Next() {
		var id []byte
		var value graphprotocol.DependedOn
		if err = rows.Scan(&id, &value.Dependents); err != nil {
			return nil, err
		}
		value.NodeID = string(id)
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) QueryTopCallingFiles(ctx context.Context, q graphquery.AggregateLimitQuery) ([]graphprotocol.CallingFile, error) {
	if !validAggregateSnapshot(q.Snapshot) || q.Limit <= 0 {
		return nil, graphquery.ErrInvalidRequest
	}
	calls, _ := graphartifact.ParseRelationship("calls")
	instantiates, _ := graphartifact.ParseRelationship("instantiates")
	contains, _ := graphartifact.ParseRelationship("contains")
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `with runs as (
 select e.source id,count(*) calls from graph_v2_edges e join graph_v2_nodes n on n.upload_id=e.upload_id and n.occurrence_key=e.source_key and n.occurrence=e.source
 where e.upload_id=$2 and n.kind='file' and e.kind=any($4::smallint[]) group by e.source
 ), cand as (select r.id,n.path fp,r.calls from runs r join graph_v2_nodes n on n.upload_id=$2 and n.occurrence_key=sha256(r.id) and n.occurrence=r.id),
 wires as (select sn.path fp,count(distinct tn.path) reaches from graph_v2_edges e
 join graph_v2_nodes sn on sn.upload_id=e.upload_id and sn.occurrence_key=e.source_key and sn.occurrence=e.source
 join graph_v2_nodes tn on tn.upload_id=e.upload_id and tn.occurrence_key=e.target_key and tn.occurrence=e.target
 where e.upload_id=$2 and e.kind<>$5 and sn.path<>tn.path and sn.path in(select fp from cand) group by sn.path)
 select c.id,c.fp,c.calls,coalesce(w.reaches,0),c.calls*(1+coalesce(w.reaches,0)) score
 from graph_uploads u join cand c on true left join wires w on w.fp=c.fp
 where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2
 order by score desc,c.calls desc,c.fp limit $6`, q.Snapshot.RepositoryID, q.Snapshot.UploadID, q.Snapshot.Commit, []int16{int16(calls.Kind), int16(instantiates.Kind)}, int16(contains.Kind), q.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []graphprotocol.CallingFile{}
	for rows.Next() {
		var id, path []byte
		var value graphprotocol.CallingFile
		if err = rows.Scan(&id, &path, &value.Calls, &value.Reaches, &value.Score); err != nil {
			return nil, err
		}
		value.NodeID, value.FilePath = string(id), string(path)
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) QueryFileNodes(ctx context.Context, snapshot graphquery.QuerySnapshot, paths []string) ([]graphprotocol.Entity, error) {
	if !validAggregateSnapshot(snapshot) || len(paths) == 0 {
		return nil, graphquery.ErrInvalidRequest
	}
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `with requested as (select distinct value from unnest($4::bytea[]) value)
 select u.repository_id,u.public_repository,u.producer_name,u.producer_version,u.producer_configuration,
 case when octet_length(n.payload)<=$5 then n.payload end
 from graph_uploads u join graph_v2_nodes n on n.upload_id=u.id join requested r on sha256(n.path)=sha256(r.value) and n.path=r.value
 where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2 and n.kind='file'
 order by n.path,n.occurrence`, snapshot.RepositoryID, snapshot.UploadID, snapshot.Commit, stringsToBytes(paths), graphquery.MaxEntityQueryBytes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []graphprotocol.Entity{}
	used := 0
	for rows.Next() {
		var entity graphprotocol.Entity
		var repository string
		var name, version, configuration, payload []byte
		if err = rows.Scan(&entity.RepositoryID, &repository, &name, &version, &configuration, &payload); err != nil {
			return nil, err
		}
		if payload == nil {
			return nil, graphquery.ErrQuerySize
		}
		entity.Fact = new(graphv2.Node)
		if err = proto.Unmarshal(payload, entity.Fact); err != nil {
			return nil, err
		}
		entity.ID, err = graphartifact.IdentityV2(&graphv2.Producer{Name: string(name), Version: string(version), Configuration: string(configuration)}, repository, entity.Fact.SourceId, entity.Fact.Occurrence)
		if err != nil {
			return nil, err
		}
		if err = graphquery.AddEntityQueryBytes(&used, entity); err != nil {
			return nil, err
		}
		result = append(result, entity)
	}
	return result, rows.Err()
}

const moduleAggregationSQL = `with assignments as (select * from unnest($4::bytea[],$5::text[]) as a(path,module))
 select sm.module,tm.module,e.kind,sn.name,tn.name,
 case when octet_length(e.payload)<=$6 then e.payload end
 from graph_uploads u join graph_v2_edges e on e.upload_id=u.id
 join lateral (select name,path from graph_v2_nodes where upload_id=e.upload_id and occurrence_key=e.source_key and occurrence=e.source offset 0) sn on true
 join lateral (select name,path from graph_v2_nodes where upload_id=e.upload_id and occurrence_key=e.target_key and occurrence=e.target offset 0) tn on true
 join assignments sm on sm.path=sn.path join assignments tm on tm.path=tn.path
 where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2 and e.kind=any($7::smallint[]) and sm.module<>tm.module
 order by sm.module,tm.module,e.kind,sn.name,tn.name,e.ordinal limit $8`

func (s *Store) QueryModuleAggregation(ctx context.Context, q graphquery.AggregateModuleQuery) ([]graphquery.AggregateModuleRow, error) {
	if !validAggregateSnapshot(q.Snapshot) || len(q.Assignments) == 0 || len(q.Kinds) == 0 || q.Limit <= 0 || math.IsNaN(q.MinConfidence) || math.IsInf(q.MinConfidence, 0) || q.MinConfidence < 0 || q.MinConfidence > 1 {
		return nil, graphquery.ErrInvalidRequest
	}
	paths := make([][]byte, 0, len(q.Assignments))
	modules := make([]string, 0, len(q.Assignments))
	for _, assignment := range q.Assignments {
		paths, modules = append(paths, []byte(assignment.FilePath)), append(modules, assignment.Module)
	}
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, moduleAggregationSQL, q.Snapshot.RepositoryID, q.Snapshot.UploadID, q.Snapshot.Commit, paths, modules, graphquery.MaxEntityQueryBytes, q.Kinds, q.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []graphquery.AggregateModuleRow{}
	used := 0
	for rows.Next() {
		var row graphquery.AggregateModuleRow
		var kind int16
		var from, to, payload []byte
		if err = rows.Scan(&row.Source, &row.Target, &kind, &from, &to, &payload); err != nil {
			return nil, err
		}
		if payload == nil {
			return nil, graphquery.ErrQuerySize
		}
		edge := new(graphv2.Edge)
		if err = proto.Unmarshal(payload, edge); err != nil {
			return nil, err
		}
		if int16(edge.Kind) != kind {
			return nil, graphquery.ErrGenerationChanged
		}
		relation, ok := graphartifact.RelationshipFromWire(graphv2.EdgeKind(kind))
		if !ok {
			return nil, graphquery.ErrGenerationChanged
		}
		row.Kind, row.From, row.To = relation.Name, string(from), string(to)
		confidence := 1.0
		if edge.Confidence != nil {
			confidence = *edge.Confidence
		}
		if confidence < q.MinConfidence {
			row.Uncertain = 1
		} else {
			row.Count = 1
			reason := edge.GetResolutionReason()
			if reason == "import" || reason == "qualified-name" || relation.Name == "extends" || relation.Name == "implements" || reason == "instance-method" && confidence >= .9 {
				row.Declared = 1
			}
		}
		if err = graphquery.AddEntityQueryBytes(&used, edge); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(result) == q.Limit {
		return nil, graphquery.ErrQuerySize
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Source < result[j].Source || result[i].Source == result[j].Source && (result[i].Target < result[j].Target || result[i].Target == result[j].Target && (result[i].Kind < result[j].Kind || result[i].Kind == result[j].Kind && (result[i].From < result[j].From || result[i].From == result[j].From && result[i].To < result[j].To)))
	})
	return result, nil
}

func (s *Store) QueryUnresolvedReferences(ctx context.Context, q graphquery.AggregateUnresolvedQuery) ([]graphprotocol.UnresolvedReference, error) {
	if !validAggregateSnapshot(q.Snapshot) || q.Limit <= 0 || (q.Path == "") == (len(q.Values) == 0) {
		return nil, graphquery.ErrInvalidRequest
	}
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	filter := "sha256(r.path)=sha256($4::bytea) and r.path=$4::bytea"
	argument := any([]byte(q.Path))
	if len(q.Values) > 0 {
		filter = "r.source_key=any(select sha256(value) from unnest($4::bytea[]) value) and r.source=any($4::bytea[])"
		argument = stringsToBytes(q.Values)
	}
	rows, err := s.pool.Query(ctx, `select u.repository_id,u.public_repository,u.producer_name,u.producer_version,u.producer_configuration,
 case when octet_length(r.payload)<=$6 then r.payload end
 from graph_uploads u join graph_v2_unresolved r on r.upload_id=u.id
 where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2 and `+filter+`
 order by r.ordinal limit $5`, q.Snapshot.RepositoryID, q.Snapshot.UploadID, q.Snapshot.Commit, argument, q.Limit, graphquery.MaxEntityQueryBytes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []graphprotocol.UnresolvedReference{}
	used := 0
	for rows.Next() {
		var value graphprotocol.UnresolvedReference
		var repository string
		var name, version, configuration, payload []byte
		if err = rows.Scan(&value.RepositoryID, &repository, &name, &version, &configuration, &payload); err != nil {
			return nil, err
		}
		if payload == nil {
			return nil, graphquery.ErrQuerySize
		}
		value.Fact = new(graphv2.UnresolvedReference)
		if err = proto.Unmarshal(payload, value.Fact); err != nil {
			return nil, err
		}
		value.ID, err = graphartifact.IdentityV2(&graphv2.Producer{Name: string(name), Version: string(version), Configuration: string(configuration)}, repository, value.Fact.SourceId, value.Fact.Occurrence)
		if err != nil {
			return nil, err
		}
		if err = graphquery.AddEntityQueryBytes(&used, value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if q.Path != "" {
		sort.SliceStable(result, func(i, j int) bool {
			a, b := result[i].Fact.GetLocation().GetStart(), result[j].Fact.GetLocation().GetStart()
			return a.GetLine() < b.GetLine() || a.GetLine() == b.GetLine() && a.GetCharacter() < b.GetCharacter()
		})
	}
	return slices.Clip(result), nil
}
