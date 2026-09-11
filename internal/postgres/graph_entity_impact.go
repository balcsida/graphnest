package postgres

import (
	"context"
	"regexp"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"google.golang.org/protobuf/proto"
)

var _ graphquery.AnalysisStore = (*Store)(nil)

const analysisEntitiesSQL = `select u.repository_id,u.public_repository,u.producer_name,u.producer_version,u.producer_configuration,
 case when octet_length(n.payload)<=$7 then n.payload end
 from graph_uploads u join graph_v2_nodes n on n.upload_id=u.id
 where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2
 and (coalesce(cardinality($4::bytea[]),0)=0 or n.path=any($4::bytea[]))
 and (coalesce(cardinality($5::text[]),0)=0 or n.kind=any($5::text[]))
 and ($6::boolean is null or n.is_exported=$6)
 order by array_position($5::text[],n.kind),n.occurrence limit $8`

const analysisQualifiedLiteralSQL = `with candidates as materialized (
 select u.id upload_id,n.occurrence_key,n.occurrence,array_position($5::text[],n.kind) kind_order
 from graph_uploads u join graph_v2_discovery d on d.upload_id=u.id
 join graph_v2_nodes n on n.upload_id=d.upload_id and n.occurrence_key=d.occurrence_key
 where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2 and u.discovery_version>=4
 and (coalesce(cardinality($4::bytea[]),0)=0 or n.path=any($4::bytea[]))
 and (coalesce(cardinality($5::text[]),0)=0 or n.kind=any($5::text[]))
 and sha256(d.original_qualified_name)=sha256($6::bytea) and d.original_qualified_name=$6::bytea
 and ($7::boolean is null or n.is_exported=$7)
 order by kind_order,n.occurrence limit $9
 ) select u.repository_id,u.public_repository,u.producer_name,u.producer_version,u.producer_configuration,
 case when octet_length(n.payload)<=$8 then n.payload end
 from candidates c join graph_uploads u on u.id=c.upload_id
 join graph_v2_nodes n on n.upload_id=c.upload_id and n.occurrence_key=c.occurrence_key
 order by c.kind_order,c.occurrence`

const analysisQualifiedWildcardSQL = `with candidates as materialized (
 select u.id upload_id,n.occurrence_key,n.occurrence,array_position($5::text[],n.kind) kind_order
 from graph_uploads u join graph_v2_discovery d on d.upload_id=u.id
 join graph_v2_nodes n on n.upload_id=d.upload_id and n.occurrence_key=d.occurrence_key
 where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2 and u.discovery_version>=4
 and (coalesce(cardinality($4::bytea[]),0)=0 or n.path=any($4::bytea[]))
 and (coalesce(cardinality($5::text[]),0)=0 or n.kind=any($5::text[]))
 and not exists(select from unnest($6::bytea[]) part where position(part in d.original_qualified_name)=0)
 and (octet_length($7::bytea)=0 or substring(d.original_qualified_name from 1 for octet_length($7::bytea))=$7::bytea)
 and (octet_length($8::bytea)=0 or substring(d.original_qualified_name from octet_length(d.original_qualified_name)-octet_length($8::bytea)+1)=$8::bytea)
 and ($9::boolean is null or n.is_exported=$9)
 order by kind_order,n.occurrence limit $11
 ) select u.repository_id,u.public_repository,u.producer_name,u.producer_version,u.producer_configuration,
 case when octet_length(n.payload)<=$10 then n.payload end
 from candidates c join graph_uploads u on u.id=c.upload_id
 join graph_v2_nodes n on n.upload_id=c.upload_id and n.occurrence_key=c.occurrence_key
 order by c.kind_order,c.occurrence`

func (s *Store) qualifiedNamesReady(ctx context.Context, snapshot graphquery.QuerySnapshot) error {
	var available bool
	if err := s.pool.QueryRow(ctx, `select exists(select from graph_uploads where repository_id=$1 and id=$2 and commit=$3 and schema_version=2 and discovery_version>=4)`, snapshot.RepositoryID, snapshot.UploadID, snapshot.Commit).Scan(&available); err != nil {
		return err
	}
	if available {
		return ctx.Err()
	}
	var selected bool
	if err := s.pool.QueryRow(ctx, `select exists(select from graph_uploads where repository_id=$1 and id=$2 and commit=$3 and schema_version=2)`, snapshot.RepositoryID, snapshot.UploadID, snapshot.Commit).Scan(&selected); err != nil {
		return err
	}
	if selected {
		return graphquery.ErrDiscoveryUnavailable
	}
	return ctx.Err()
}

func (s *Store) QueryAnalysisEntities(ctx context.Context, query graphquery.AnalysisEntityQuery) ([]graphprotocol.Entity, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	if query.Snapshot.RepositoryID <= 0 || query.Snapshot.UploadID <= 0 || query.Snapshot.Commit == "" || query.Limit <= 0 || query.Limit > 1_001 || len(query.Paths) > 1_000 || len(query.Kinds) > 32 {
		return nil, graphquery.ErrInvalidRequest
	}
	var matcher *regexp.Regexp
	if query.QualifiedPattern != "" {
		partsBytes := 0
		for _, part := range query.QualifiedParts {
			partsBytes += len(part)
		}
		if len(query.QualifiedPattern) > 32_768 || len(query.QualifiedParts) > 8_192 || partsBytes > 32_768 || len(query.QualifiedPrefix) > 16_384 || len(query.QualifiedSuffix) > 16_384 {
			return nil, graphquery.ErrInvalidRequest
		}
		var compileErr error
		matcher, compileErr = regexp.Compile(query.QualifiedPattern)
		if compileErr != nil {
			return nil, graphquery.ErrInvalidRequest
		}
	}
	paths := make([][]byte, len(query.Paths))
	for i, value := range query.Paths {
		paths[i] = []byte(value)
	}
	sql := analysisEntitiesSQL
	args := []any{query.Snapshot.RepositoryID, query.Snapshot.UploadID, query.Snapshot.Commit, paths, query.Kinds, query.Exported, graphquery.MaxEntityQueryBytes, query.Limit}
	if matcher != nil {
		if err := s.qualifiedNamesReady(ctx, query.Snapshot); err != nil {
			return nil, err
		}
		if query.QualifiedLiteral != nil {
			sql = analysisQualifiedLiteralSQL
			args = []any{query.Snapshot.RepositoryID, query.Snapshot.UploadID, query.Snapshot.Commit, paths, query.Kinds, []byte(*query.QualifiedLiteral), query.Exported, graphquery.MaxEntityQueryBytes, 1_002}
		} else {
			parts := make([][]byte, len(query.QualifiedParts))
			for i, part := range query.QualifiedParts {
				parts[i] = []byte(part)
			}
			sql = analysisQualifiedWildcardSQL
			args = []any{query.Snapshot.RepositoryID, query.Snapshot.UploadID, query.Snapshot.Commit, paths, query.Kinds, parts, []byte(query.QualifiedPrefix), []byte(query.QualifiedSuffix), query.Exported, graphquery.MaxEntityQueryBytes, 1_002}
		}
	}
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []graphprotocol.Entity{}
	usedBytes := 0
	candidates := 0
	for rows.Next() {
		entity, scanErr := scanV2Entity(rows, &usedBytes)
		if scanErr != nil {
			return nil, scanErr
		}
		candidates++
		if matcher != nil && candidates > 1_001 {
			return nil, graphquery.ErrQuerySize
		}
		if matcher != nil && !matcher.MatchString(entity.Fact.QualifiedName) {
			continue
		}
		if len(result) < query.Limit {
			result = append(result, entity)
		}
	}
	return result, rows.Err()
}

const analysisEvidenceSQL = `with selected as materialized (
 select occurrence,sha256(occurrence) occurrence_key from unnest($4::bytea[]) occurrence
 ) select u.repository_id,u.public_repository,u.producer_name,u.producer_version,u.producer_configuration,
 case when octet_length(sn.payload)<=$5 then sn.payload end,
 case when octet_length(tn.payload)<=$5 then tn.payload end,
 case when octet_length(e.payload)<=$5 then e.payload end
 from graph_uploads u join graph_v2_edges e on e.upload_id=u.id
 join selected ss on ss.occurrence_key=e.source_key and ss.occurrence=e.source
 join selected st on st.occurrence_key=e.target_key and st.occurrence=e.target
 join graph_v2_nodes sn on sn.upload_id=u.id and sn.occurrence_key=e.source_key and sn.occurrence=e.source
 join graph_v2_nodes tn on tn.upload_id=u.id and tn.occurrence_key=e.target_key and tn.occurrence=e.target
 where u.repository_id=$1 and u.id=$2 and u.commit=$3 and u.schema_version=2
 order by e.source,e.target,e.occurrence limit $6`

func (s *Store) QueryAnalysisEvidence(ctx context.Context, query graphquery.AnalysisEvidenceQuery) ([]graphprotocol.Evidence, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	if query.Snapshot.RepositoryID <= 0 || query.Snapshot.UploadID <= 0 || query.Snapshot.Commit == "" || query.Limit <= 0 || query.Limit > 5_001 || len(query.Occurrences) == 0 || len(query.Occurrences) > 1_000 {
		return nil, graphquery.ErrInvalidRequest
	}
	occurrences := make([][]byte, len(query.Occurrences))
	for i, value := range query.Occurrences {
		occurrences[i] = []byte(value)
	}
	rows, err := s.pool.Query(ctx, analysisEvidenceSQL, query.Snapshot.RepositoryID, query.Snapshot.UploadID, query.Snapshot.Commit, occurrences, graphquery.MaxEntityQueryBytes, query.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []graphprotocol.Evidence{}
	usedBytes := 0
	for rows.Next() {
		var evidence graphprotocol.Evidence
		var repository string
		var name, version, configuration, sourceBytes, targetBytes, edgeBytes []byte
		if err = rows.Scan(&evidence.RepositoryID, &repository, &name, &version, &configuration, &sourceBytes, &targetBytes, &edgeBytes); err != nil {
			return nil, err
		}
		if sourceBytes == nil || targetBytes == nil || edgeBytes == nil {
			return nil, graphquery.ErrQuerySize
		}
		source, target := new(graphv2.Node), new(graphv2.Node)
		evidence.Fact = new(graphv2.Edge)
		for _, value := range []struct {
			data []byte
			fact proto.Message
		}{{sourceBytes, source}, {targetBytes, target}, {edgeBytes, evidence.Fact}} {
			if err = proto.Unmarshal(value.data, value.fact); err != nil {
				return nil, err
			}
		}
		producer := &graphv2.Producer{Name: string(name), Version: string(version), Configuration: string(configuration)}
		evidence.SourceID, err = graphartifact.IdentityV2(producer, repository, source.SourceId, source.Occurrence)
		if err != nil {
			return nil, err
		}
		evidence.TargetID, err = graphartifact.IdentityV2(producer, repository, target.SourceId, target.Occurrence)
		if err != nil {
			return nil, err
		}
		if err = graphquery.AddEntityQueryBytes(&usedBytes, evidence); err != nil {
			return nil, err
		}
		result = append(result, evidence)
	}
	return result, rows.Err()
}
