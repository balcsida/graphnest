package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
)

var (
	ErrGraphPrecondition     = errors.New("graph generation or indexed commit changed")
	ErrGraphProviderConflict = errors.New("graph provider change requires explicit replacement")
)

// GraphPublication is trusted publication context, separate from producer facts.
// Zero ExpectedActiveID means no active generation, not an unconditional write.
// Callers authorize the publisher first; see graphingest.Service.Publish.
type GraphPublication struct {
	Publisher           string
	Capabilities        []string
	ExpectedActiveID    int64
	AllowProviderChange bool
}

// ReplaceGraphV2 publishes an external v2 generation into the repository's v2
// slot; the v1 generation, if any, stays active for the legacy workflows.
func (s *Store) ReplaceGraphV2(ctx context.Context, repositoryID int64, publication GraphPublication, artifact *graphv2.Artifact) (GraphReplacement, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return GraphReplacement{}, err
	}
	defer tx.Rollback(ctx)
	replacement, err := replaceGraphV2(ctx, tx, repositoryID, publication, GraphSourceExternal, artifact)
	if err != nil {
		return GraphReplacement{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return GraphReplacement{}, err
	}
	return replacement, nil
}

// replaceGraphV2 publishes inside the caller's transaction. source is external
// for publishers and scip for generations derived from a SCIP upload; replacing
// a generation of another source or producer needs AllowProviderChange.
func replaceGraphV2(ctx context.Context, tx pgx.Tx, repositoryID int64, publication GraphPublication, source GraphSource, artifact *graphv2.Artifact) (GraphReplacement, error) {
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
	var indexedSHA string
	var publicID int64
	err := tx.QueryRow(ctx, `select coalesce(r.indexed_sha,''),r.github_id from repositories r
 join installations i on i.id=r.installation_id
 where r.id=$1 and r.enabled and not r.archived and i.status='active' for update of r`, repositoryID).Scan(&indexedSHA, &publicID)
	if err != nil {
		return GraphReplacement{}, err
	}
	if artifact.Repository != strconv.FormatInt(publicID, 10) {
		return GraphReplacement{}, graphartifact.ErrInvalidArtifact
	}
	var currentID int64
	var currentNodes, currentEdges int
	var producer, currentHash []byte
	var currentSource GraphSource
	err = tx.QueryRow(ctx, `select id,producer_name,source,content_hash,node_count,edge_count from graph_uploads where repository_id=$1 and active and schema_version=2`, repositoryID).Scan(&currentID, &producer, &currentSource, &currentHash, &currentNodes, &currentEdges)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return GraphReplacement{}, err
	}
	if artifact.Commit != indexedSHA {
		return GraphReplacement{}, ErrGraphPrecondition
	}
	// A retry of the active generation's exact content succeeds whatever it
	// expected: the verified semantic hash covers repository, commit and producer.
	if currentID != 0 && bytes.Equal(currentHash, artifact.ContentHash) {
		upload := GraphUpload{ID: currentID, RepositoryID: repositoryID, Commit: artifact.Commit, SchemaVersion: 2, Source: currentSource, NodeCount: currentNodes, EdgeCount: currentEdges}
		return GraphReplacement{Upload: upload, Applied: true, Deduplicated: true}, nil
	}
	if currentID != publication.ExpectedActiveID {
		return GraphReplacement{}, ErrGraphPrecondition
	}
	if currentID != 0 && (currentSource != source || string(producer) != artifact.Producer.Name) && !publication.AllowProviderChange {
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
	upload := GraphUpload{RepositoryID: repositoryID, Commit: artifact.Commit, SchemaVersion: 2, Source: source, NodeCount: len(artifact.Nodes), EdgeCount: len(artifact.Edges)}
	capabilities := publication.Capabilities
	if capabilities == nil {
		capabilities = []string{}
	}
	err = tx.QueryRow(ctx, `insert into graph_uploads
 (repository_id,commit,schema_version,source,analyzer_name,analyzer_version,content_hash,node_count,edge_count,publisher,capabilities,public_repository,producer_name,producer_version,producer_configuration,artifact_header)
 values($1,$2,2,$13,'','',$5,$6,$7,$8,$9,$10,$3,$4,$11,$12) returning id`, repositoryID, artifact.Commit, []byte(artifact.Producer.Name), []byte(artifact.Producer.Version), artifact.ContentHash, len(artifact.Nodes), len(artifact.Edges), publication.Publisher, capabilities, artifact.Repository, []byte(artifact.Producer.Configuration), headerBytes, source).Scan(&upload.ID)
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
	if err = writeGraphDiscovery(ctx, tx, upload.ID, artifact); err != nil {
		return GraphReplacement{}, err
	}
	return GraphReplacement{Upload: upload, Applied: true, ReplacedID: currentID}, nil
}

// ActiveGraphGeneration describes the repository's active v2 generation: the
// value a publisher names as its expected generation. It returns nil when no
// v2 generation is active; the v1 generation is reported by GraphStatus.
func (s *Store) ActiveGraphGeneration(ctx context.Context, repositoryID int64) (*api.GraphActiveGeneration, error) {
	var active api.GraphActiveGeneration
	var source string
	var producer, version, hash []byte
	err := s.pool.QueryRow(ctx, `select id,commit,schema_version,source,producer_name,producer_version,content_hash
 from graph_uploads where repository_id=$1 and active and schema_version=2`, repositoryID).Scan(&active.ID, &active.Commit, &active.SchemaVersion, &source, &producer, &version, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	active.Source, active.Producer, active.ProducerVersion, active.ContentHash = api.GraphSource(source), string(producer), string(version), hex.EncodeToString(hash)
	return &active, nil
}

// GraphPublicationAllowed reports whether a non-administrator subject holds a
// graph publication grant for the repository.
func (s *Store) GraphPublicationAllowed(ctx context.Context, repositoryID int64, subject string) (bool, error) {
	var allowed bool
	err := s.pool.QueryRow(ctx, `select exists(select 1 from graph_publication_grants where repository_id=$1 and subject=$2)`, repositoryID, subject).Scan(&allowed)
	return allowed, err
}

// SetGraphPublicationGrant adds or removes a repository-scoped publication grant.
func (s *Store) SetGraphPublicationGrant(ctx context.Context, repositoryID int64, subject, grantedBy string, allow bool) error {
	if !allow {
		_, err := s.pool.Exec(ctx, `delete from graph_publication_grants where repository_id=$1 and subject=$2`, repositoryID, subject)
		return err
	}
	_, err := s.pool.Exec(ctx, `insert into graph_publication_grants (repository_id, subject, granted_by) values ($1, $2, $3) on conflict do nothing`, repositoryID, subject, grantedBy)
	return err
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
