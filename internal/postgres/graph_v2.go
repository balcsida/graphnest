package postgres

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
)

var (
	ErrGraphPrecondition     = errors.New("graph generation or indexed commit changed")
	ErrGraphProviderConflict = errors.New("graph provider change requires explicit replacement")
)

// GraphPublication is trusted publication context, separate from producer facts.
// Zero ExpectedActiveID means no active generation, not an unconditional write.
// These storage APIs are deliberately absent from production upload interfaces.
type GraphPublication struct {
	Publisher           string
	Capabilities        []string
	ExpectedActiveID    int64
	AllowProviderChange bool
}

func (s *Store) ReplaceGraphV2(ctx context.Context, repositoryID int64, publication GraphPublication, artifact *graphv2.Artifact) (GraphReplacement, error) {
	if err := graphartifact.ValidateV2(artifact, graphartifact.Limits{}); err != nil {
		return GraphReplacement{}, err
	}
	if publication.ExpectedActiveID < 0 || !validGraphPublisher(publication.Publisher) || len(publication.Capabilities) > 64 {
		return GraphReplacement{}, graphartifact.ErrInvalidArtifact
	}
	seen := make(map[string]bool, len(publication.Capabilities))
	for _, capability := range publication.Capabilities {
		if !validGraphPublisher(capability) || seen[capability] {
			return GraphReplacement{}, graphartifact.ErrInvalidArtifact
		}
		seen[capability] = true
	}
	// Clone only after validating bounds. Never rewrite the producer's repository,
	// supplied semantic hash, or caller-owned messages.
	artifact = proto.Clone(artifact).(*graphv2.Artifact)
	if len(artifact.ContentHash) == 0 {
		hash, err := graphartifact.SemanticHashV2(artifact, graphartifact.Limits{})
		if err != nil {
			return GraphReplacement{}, err
		}
		artifact.ContentHash = hash
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return GraphReplacement{}, err
	}
	defer tx.Rollback(ctx)
	var indexedSHA string
	var publicID int64
	err = tx.QueryRow(ctx, `select coalesce(r.indexed_sha,''),r.github_id from repositories r
 join installations i on i.id=r.installation_id
 where r.id=$1 and r.enabled and not r.archived and i.status='active' for update of r`, repositoryID).Scan(&indexedSHA, &publicID)
	if err != nil {
		return GraphReplacement{}, err
	}
	if artifact.Repository != strconv.FormatInt(publicID, 10) {
		return GraphReplacement{}, graphartifact.ErrInvalidArtifact
	}
	var currentID int64
	var producer []byte
	var source GraphSource
	err = tx.QueryRow(ctx, `select id,case when schema_version=2 then producer_name else convert_to(analyzer_name,'UTF8') end,source from graph_uploads where repository_id=$1 and active`, repositoryID).Scan(&currentID, &producer, &source)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return GraphReplacement{}, err
	}
	if currentID != publication.ExpectedActiveID || artifact.Commit != indexedSHA {
		return GraphReplacement{}, ErrGraphPrecondition
	}
	if currentID != 0 && (source != GraphSourceExternal || string(producer) != artifact.Producer.Name) && !publication.AllowProviderChange {
		return GraphReplacement{}, ErrGraphProviderConflict
	}
	header := &graphv2.Artifact{SchemaVersion: artifact.SchemaVersion, Repository: artifact.Repository, Commit: artifact.Commit, Producer: artifact.Producer, ContentHash: artifact.ContentHash, ImportedAt: artifact.ImportedAt, Metadata: artifact.Metadata, Extensions: artifact.Extensions}
	headerBytes, err := proto.Marshal(header)
	if err != nil {
		return GraphReplacement{}, err
	}
	// ponytail: immutable history grows until offline cleanup; reader-aware retention belongs with future pinning.
	if _, err := tx.Exec(ctx, `update graph_uploads set active=false,retired_at=now() where id=$1`, currentID); err != nil {
		return GraphReplacement{}, err
	}
	upload := GraphUpload{RepositoryID: repositoryID, Commit: artifact.Commit, SchemaVersion: 2, Source: GraphSourceExternal, NodeCount: len(artifact.Nodes), EdgeCount: len(artifact.Edges)}
	capabilities := publication.Capabilities
	if capabilities == nil {
		capabilities = []string{}
	}
	err = tx.QueryRow(ctx, `insert into graph_uploads
 (repository_id,commit,schema_version,source,analyzer_name,analyzer_version,content_hash,node_count,edge_count,publisher,capabilities,public_repository,producer_name,producer_version,producer_configuration,artifact_header)
 values($1,$2,2,'external','','',$5,$6,$7,$8,$9,$10,$3,$4,$11,$12) returning id`, repositoryID, artifact.Commit, []byte(artifact.Producer.Name), []byte(artifact.Producer.Version), artifact.ContentHash, len(artifact.Nodes), len(artifact.Edges), publication.Publisher, capabilities, artifact.Repository, []byte(artifact.Producer.Configuration), headerBytes).Scan(&upload.ID)
	if err != nil {
		return GraphReplacement{}, err
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"graph_v2_nodes"}, []string{"upload_id", "occurrence", "ordinal", "kind", "name", "qualified_name", "path", "language", "visibility", "is_exported", "payload"}, pgx.CopyFromSlice(len(artifact.Nodes), func(i int) ([]any, error) {
		n := artifact.Nodes[i]
		payload, err := proto.Marshal(n)
		return []any{upload.ID, []byte(n.Occurrence), i, n.Kind, []byte(n.Name), []byte(n.QualifiedName), graphOptionalBytes(n.Path), []byte(n.Language), graphOptionalBytes(n.Visibility), n.IsExported, payload}, err
	})); err != nil {
		return GraphReplacement{}, err
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"graph_v2_edges"}, []string{"upload_id", "occurrence", "ordinal", "source", "target", "kind", "confidence", "payload"}, pgx.CopyFromSlice(len(artifact.Edges), func(i int) ([]any, error) {
		e := artifact.Edges[i]
		payload, err := proto.Marshal(e)
		return []any{upload.ID, []byte(e.Occurrence), i, []byte(e.Source), []byte(e.Target), int16(e.Kind), e.Confidence, payload}, err
	})); err != nil {
		return GraphReplacement{}, err
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"graph_v2_files"}, []string{"upload_id", "path", "ordinal", "content_hash", "language", "size", "generated", "payload"}, pgx.CopyFromSlice(len(artifact.Files), func(i int) ([]any, error) {
		f := artifact.Files[i]
		payload, err := proto.Marshal(f)
		return []any{upload.ID, []byte(f.Path), i, f.ContentHash, []byte(f.Language), f.Size, f.Generated, payload}, err
	})); err != nil {
		return GraphReplacement{}, err
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"graph_v2_unresolved"}, []string{"upload_id", "occurrence", "ordinal", "source", "kind", "path", "payload"}, pgx.CopyFromSlice(len(artifact.Unresolved), func(i int) ([]any, error) {
		r := artifact.Unresolved[i]
		payload, err := proto.Marshal(r)
		return []any{upload.ID, []byte(r.Occurrence), i, []byte(r.Source), r.Kind, graphOptionalBytes(r.Path), payload}, err
	})); err != nil {
		return GraphReplacement{}, err
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"graph_v2_diagnostics"}, []string{"upload_id", "occurrence", "ordinal", "payload"}, pgx.CopyFromSlice(len(artifact.Diagnostics), func(i int) ([]any, error) {
		d := artifact.Diagnostics[i]
		payload, err := proto.Marshal(d)
		return []any{upload.ID, []byte(d.Occurrence), i, payload}, err
	})); err != nil {
		return GraphReplacement{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return GraphReplacement{}, err
	}
	return GraphReplacement{Upload: upload, Applied: true}, nil
}

func validGraphPublisher(value string) bool {
	return len(value) > 0 && len(value) <= 256 && utf8.ValidString(value) && !strings.ContainsRune(value, 0) && strings.TrimSpace(value) == value
}

// LoadGraphV2 loads an immutable generation, including retired generations.
// The caller must authorize the repository; this is not a public download API.
func (s *Store) LoadGraphV2(ctx context.Context, repositoryID, uploadID int64) (*graphv2.Artifact, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var header []byte
	if err = tx.QueryRow(ctx, `select artifact_header from graph_uploads where id=$1 and repository_id=$2 and schema_version=2`, uploadID, repositoryID).Scan(&header); err != nil {
		return nil, err
	}
	artifact := new(graphv2.Artifact)
	if err = proto.Unmarshal(header, artifact); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `select kind,payload from (
 select 1 kind,ordinal,payload from graph_v2_nodes where upload_id=$1 union all
 select 2,ordinal,payload from graph_v2_edges where upload_id=$1 union all
 select 3,ordinal,payload from graph_v2_files where upload_id=$1 union all
 select 4,ordinal,payload from graph_v2_unresolved where upload_id=$1 union all
 select 5,ordinal,payload from graph_v2_diagnostics where upload_id=$1
 ) facts order by kind,ordinal`, uploadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind int
		var payload []byte
		if err = rows.Scan(&kind, &payload); err != nil {
			return nil, err
		}
		var message proto.Message
		switch kind {
		case 1:
			n := new(graphv2.Node)
			artifact.Nodes = append(artifact.Nodes, n)
			message = n
		case 2:
			e := new(graphv2.Edge)
			artifact.Edges = append(artifact.Edges, e)
			message = e
		case 3:
			f := new(graphv2.File)
			artifact.Files = append(artifact.Files, f)
			message = f
		case 4:
			r := new(graphv2.UnresolvedReference)
			artifact.Unresolved = append(artifact.Unresolved, r)
			message = r
		case 5:
			d := new(graphv2.Diagnostic)
			artifact.Diagnostics = append(artifact.Diagnostics, d)
			message = d
		}
		if err = proto.Unmarshal(payload, message); err != nil {
			return nil, err
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = graphartifact.ValidateV2(artifact, graphartifact.Limits{}); err != nil {
		return nil, err
	}
	return artifact, tx.Commit(ctx)
}

// A present empty protobuf string must remain an empty bytea, not SQL NULL.
func graphOptionalBytes(value *string) []byte {
	if value == nil {
		return nil
	}
	return []byte(*value)
}
