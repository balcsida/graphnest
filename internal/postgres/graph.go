package postgres

import (
	"context"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/jackc/pgx/v5"
)

type GraphSource string

const (
	GraphSourceManaged  GraphSource = "managed"
	GraphSourceExternal GraphSource = "external"
)

type GraphUpload struct {
	ID, RepositoryID                    int64
	Commit                              string
	SchemaVersion, NodeCount, EdgeCount int
	Source                              GraphSource
}

type GraphReplacement struct {
	Upload  GraphUpload
	Applied bool
}

func (s *Store) ReplaceGraph(ctx context.Context, repositoryID int64, source GraphSource, artifact graphartifact.Artifact) (GraphReplacement, error) {
	if artifact.RepositoryID != repositoryID || (source != GraphSourceManaged && source != GraphSourceExternal) || graphartifact.Validate(artifact, graphartifact.Limits{}) != nil {
		return GraphReplacement{}, graphartifact.ErrInvalidArtifact
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return GraphReplacement{}, err
	}
	defer tx.Rollback(ctx)

	var indexedSHA string
	if err := tx.QueryRow(ctx, `select coalesce(indexed_sha, '') from repositories where id=$1 for update`, repositoryID).Scan(&indexedSHA); err != nil {
		return GraphReplacement{}, err
	}
	var current GraphUpload
	err = tx.QueryRow(ctx, `select id, repository_id, commit, schema_version, source, node_count, edge_count
		from graph_uploads where repository_id=$1 for update`, repositoryID).Scan(
		&current.ID, &current.RepositoryID, &current.Commit, &current.SchemaVersion, &current.Source, &current.NodeCount, &current.EdgeCount)
	if err != nil && err != pgx.ErrNoRows {
		return GraphReplacement{}, err
	}
	if artifact.Commit != indexedSHA || current.Source == GraphSourceExternal && current.Commit == indexedSHA && source == GraphSourceManaged {
		return GraphReplacement{Upload: current}, nil
	}
	if current.ID != 0 {
		if _, err := tx.Exec(ctx, `delete from graph_uploads where id=$1`, current.ID); err != nil {
			return GraphReplacement{}, err
		}
	}
	var upload GraphUpload
	err = tx.QueryRow(ctx, `insert into graph_uploads
		(repository_id, commit, schema_version, source, analyzer_name, analyzer_version, content_hash, node_count, edge_count)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		returning id, repository_id, commit, schema_version, source, node_count, edge_count`,
		repositoryID, artifact.Commit, artifact.SchemaVersion, source, artifact.Analyzer.Name, artifact.Analyzer.Version,
		artifact.ContentHash, len(artifact.Nodes), len(artifact.Edges)).Scan(
		&upload.ID, &upload.RepositoryID, &upload.Commit, &upload.SchemaVersion, &upload.Source, &upload.NodeCount, &upload.EdgeCount)
	if err != nil {
		return GraphReplacement{}, err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"graph_nodes"},
		[]string{"upload_id", "uid", "kind", "path", "language", "symbol_kind", "qualified_name", "signature", "scip_symbol", "start_line", "start_character", "end_line", "end_character"},
		pgx.CopyFromSlice(len(artifact.Nodes), func(index int) ([]any, error) {
			node := artifact.Nodes[index]
			return []any{upload.ID, node.UID, node.Kind, node.Path, node.Language, node.SymbolKind, node.QualifiedName, node.Signature, node.SCIPSymbol,
				node.Range.StartLine, node.Range.StartCharacter, node.Range.EndLine, node.Range.EndCharacter}, nil
		})); err != nil {
		return GraphReplacement{}, err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"graph_edges"},
		[]string{"upload_id", "source_uid", "target_uid", "kind", "path", "start_line", "start_character", "end_line", "end_character", "confidence", "resolution_reason"},
		pgx.CopyFromSlice(len(artifact.Edges), func(index int) ([]any, error) {
			edge := artifact.Edges[index]
			return []any{upload.ID, edge.SourceUID, edge.TargetUID, edge.Kind, edge.Path, edge.Range.StartLine, edge.Range.StartCharacter,
				edge.Range.EndLine, edge.Range.EndCharacter, edge.Confidence, edge.ResolutionReason}, nil
		})); err != nil {
		return GraphReplacement{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return GraphReplacement{}, err
	}
	return GraphReplacement{Upload: upload, Applied: true}, nil
}

func (s *Store) LoadGraph(ctx context.Context, uploadID int64) (graphartifact.Artifact, error) {
	var artifact graphartifact.Artifact
	err := s.pool.QueryRow(ctx, `select schema_version, repository_id, commit, analyzer_name, analyzer_version, content_hash
		from graph_uploads where id=$1`, uploadID).Scan(&artifact.SchemaVersion, &artifact.RepositoryID, &artifact.Commit,
		&artifact.Analyzer.Name, &artifact.Analyzer.Version, &artifact.ContentHash)
	if err != nil {
		return graphartifact.Artifact{}, err
	}
	nodes, err := s.pool.Query(ctx, `select uid, kind, path, language, symbol_kind, qualified_name, signature, scip_symbol,
		start_line, start_character, end_line, end_character from graph_nodes where upload_id=$1 order by id`, uploadID)
	if err != nil {
		return graphartifact.Artifact{}, err
	}
	defer nodes.Close()
	for nodes.Next() {
		var node graphartifact.Node
		var kind int
		if err := nodes.Scan(&node.UID, &kind, &node.Path, &node.Language, &node.SymbolKind, &node.QualifiedName, &node.Signature, &node.SCIPSymbol,
			&node.Range.StartLine, &node.Range.StartCharacter, &node.Range.EndLine, &node.Range.EndCharacter); err != nil {
			return graphartifact.Artifact{}, err
		}
		node.Kind = graphartifact.NodeKind(kind)
		artifact.Nodes = append(artifact.Nodes, node)
	}
	if err := nodes.Err(); err != nil {
		return graphartifact.Artifact{}, err
	}
	edges, err := s.pool.Query(ctx, `select source_uid, target_uid, kind, path, start_line, start_character, end_line, end_character,
		confidence, resolution_reason from graph_edges where upload_id=$1 order by id`, uploadID)
	if err != nil {
		return graphartifact.Artifact{}, err
	}
	defer edges.Close()
	for edges.Next() {
		var edge graphartifact.Edge
		var kind int
		if err := edges.Scan(&edge.SourceUID, &edge.TargetUID, &kind, &edge.Path, &edge.Range.StartLine, &edge.Range.StartCharacter,
			&edge.Range.EndLine, &edge.Range.EndCharacter, &edge.Confidence, &edge.ResolutionReason); err != nil {
			return graphartifact.Artifact{}, err
		}
		edge.Kind = graphartifact.EdgeKind(kind)
		artifact.Edges = append(artifact.Edges, edge)
	}
	if err := edges.Err(); err != nil {
		return graphartifact.Artifact{}, err
	}
	return artifact, nil
}
