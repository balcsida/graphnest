package ladybug

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/grepnest/grepnest/internal/graphartifact"
)

const (
	storeBatchSize  = 1_000
	storeBatchBytes = 256 << 10
)

func (db *Database) Manifests(ctx context.Context) (map[int64]graphartifact.Manifest, error) {
	manifests := map[int64]graphartifact.Manifest{}
	err := db.View(ctx, func(session *Session) error {
		var after int64
		for {
			result, err := session.Execute(ctx, `MATCH (r:Repository) WHERE r.id > $after RETURN r.id, r.upload_id, r.commit, r.source, r.schema_version, r.content_hash ORDER BY r.id LIMIT $limit`, map[string]any{"after": after, "limit": int64(defaultMaxRows)}, QueryLimits{MaxRows: defaultMaxRows, MaxBytes: defaultMaxBytes})
			if err != nil {
				return err
			}
			for _, row := range result.Rows {
				id := row[0].(int64)
				manifests[id] = graphartifact.Manifest{
					RepositoryID:  id,
					UploadID:      row[1].(int64),
					Commit:        row[2].(string),
					Source:        row[3].(string),
					SchemaVersion: uint32(row[4].(int32)),
					ContentHash:   bytes.Clone(row[5].([]byte)),
				}
				after = id
			}
			if result.Truncated && len(result.Rows) == 0 {
				return errors.New("ladybug manifest page exceeds result byte limit")
			}
			if !result.Truncated && len(result.Rows) < defaultMaxRows {
				return nil
			}
		}
	})
	return manifests, err
}

func (db *Database) ReplaceRepository(ctx context.Context, manifest graphartifact.Manifest, artifact graphartifact.Artifact) error {
	repository, kinds, err := validateReplacement(manifest, artifact)
	if err != nil {
		return err
	}
	return db.Update(ctx, func(session *Session) error {
		if err := deleteRepository(ctx, session, manifest.RepositoryID); err != nil {
			return err
		}
		if err := executeStore(ctx, session, `CREATE (:Repository {id: $id, name: $name, commit: $commit, upload_id: $upload_id, schema_version: $schema_version, source: $source, content_hash: BLOB($content_hash)})`, map[string]any{
			"id": manifest.RepositoryID, "name": repository.QualifiedName, "commit": manifest.Commit, "upload_id": manifest.UploadID,
			"schema_version": int32(manifest.SchemaVersion), "source": manifest.Source, "content_hash": blobLiteral(manifest.ContentHash),
		}); err != nil {
			return err
		}
		if err := insertNodes(ctx, session, manifest.RepositoryID, artifact.Nodes); err != nil {
			return err
		}
		return insertEdges(ctx, session, manifest.RepositoryID, artifact.Edges, kinds)
	})
}

func (db *Database) DeleteRepository(ctx context.Context, repositoryID int64) error {
	if repositoryID <= 0 {
		return graphartifact.ErrInvalidArtifact
	}
	return db.Update(ctx, func(session *Session) error {
		return deleteRepository(ctx, session, repositoryID)
	})
}

func validateReplacement(manifest graphartifact.Manifest, artifact graphartifact.Artifact) (graphartifact.Node, map[string]graphartifact.NodeKind, error) {
	if err := graphartifact.Validate(artifact, graphartifact.Limits{}); err != nil ||
		manifest.RepositoryID != artifact.RepositoryID || manifest.UploadID <= 0 || manifest.Commit != artifact.Commit ||
		!validManifestSource(manifest.Source) || manifest.SchemaVersion != artifact.SchemaVersion || manifest.SchemaVersion != SchemaVersion ||
		!bytes.Equal(manifest.ContentHash, artifact.ContentHash) {
		return graphartifact.Node{}, nil, graphartifact.ErrInvalidArtifact
	}
	kinds := make(map[string]graphartifact.NodeKind, len(artifact.Nodes))
	var repository graphartifact.Node
	for _, node := range artifact.Nodes {
		kinds[node.UID] = node.Kind
		if node.Kind == graphartifact.NodeRepository {
			if repository.UID != "" {
				return graphartifact.Node{}, nil, graphartifact.ErrInvalidArtifact
			}
			repository = node
		}
	}
	if repository.UID == "" {
		return graphartifact.Node{}, nil, graphartifact.ErrInvalidArtifact
	}
	if repository.QualifiedName == "" {
		repository.QualifiedName = repository.UID
	}
	for _, edge := range artifact.Edges {
		source, target := kinds[edge.SourceUID], kinds[edge.TargetUID]
		legal := edge.Kind == graphartifact.EdgeContains && (source == graphartifact.NodeRepository && target == graphartifact.NodeFile || source == graphartifact.NodeFile && target == graphartifact.NodeSymbol) ||
			edge.Kind == graphartifact.EdgeImports && source == graphartifact.NodeFile && target == graphartifact.NodeFile ||
			edge.Kind >= graphartifact.EdgeReferences && edge.Kind <= graphartifact.EdgeImplements && source == graphartifact.NodeSymbol && target == graphartifact.NodeSymbol
		if !legal {
			return graphartifact.Node{}, nil, graphartifact.ErrInvalidArtifact
		}
	}
	return repository, kinds, nil
}

func validManifestSource(source string) bool {
	return source == "managed" || source == "external" || source == "scip"
}

func deleteRepository(ctx context.Context, session *Session, repositoryID int64) error {
	for _, query := range []string{
		`MATCH (a)-[r]->(b) WHERE a.repository_id = $id OR a.id = $id OR b.repository_id = $id OR b.id = $id DELETE r`,
		`MATCH (n:Symbol) WHERE n.repository_id = $id DELETE n`,
		`MATCH (n:File) WHERE n.repository_id = $id DELETE n`,
		`MATCH (n:Repository) WHERE n.id = $id DELETE n`,
	} {
		if err := executeStore(ctx, session, query, map[string]any{"id": repositoryID}); err != nil {
			return err
		}
	}
	return nil
}

func insertNodes(ctx context.Context, session *Session, repositoryID int64, nodes []graphartifact.Node) error {
	files := make([]map[string]any, 0)
	symbols := make([]map[string]any, 0)
	for _, node := range nodes {
		switch node.Kind {
		case graphartifact.NodeFile:
			files = append(files, map[string]any{"uid": storageUID(repositoryID, node.UID), "path": node.Path})
		case graphartifact.NodeSymbol:
			symbols = append(symbols, map[string]any{
				"uid": storageUID(repositoryID, node.UID), "path": node.Path, "language": node.Language, "kind": node.SymbolKind,
				"qualified_name": node.QualifiedName, "signature": node.Signature, "start_line": node.Range.StartLine,
				"start_character": node.Range.StartCharacter, "end_line": node.Range.EndLine, "end_character": node.Range.EndCharacter,
			})
		}
	}
	if err := executeBatches(ctx, session, files, `UNWIND $rows AS row CREATE (:File {uid: row.uid, repository_id: $repository_id, path: row.path})`, repositoryID); err != nil {
		return err
	}
	return executeBatches(ctx, session, symbols, `UNWIND $rows AS row CREATE (:Symbol {uid: row.uid, repository_id: $repository_id, path: row.path, language: row.language, kind: row.kind, qualified_name: row.qualified_name, signature: row.signature, start_line: row.start_line, start_character: row.start_character, end_line: row.end_line, end_character: row.end_character})`, repositoryID)
}

func insertEdges(ctx context.Context, session *Session, repositoryID int64, edges []graphartifact.Edge, kinds map[string]graphartifact.NodeKind) error {
	type edgeGroup struct {
		kind       graphartifact.EdgeKind
		sourceKind graphartifact.NodeKind
	}
	grouped := map[edgeGroup][]map[string]any{}
	for _, edge := range edges {
		group := edgeGroup{kind: edge.Kind, sourceKind: kinds[edge.SourceUID]}
		grouped[group] = append(grouped[group], map[string]any{
			"source": storageUID(repositoryID, edge.SourceUID),
			"target": storageUID(repositoryID, edge.TargetUID),
		})
	}
	for group, rows := range grouped {
		query := edgeQuery(group.kind, group.sourceKind)
		if err := executeBatches(ctx, session, rows, query, repositoryID); err != nil {
			return err
		}
	}
	return nil
}

func edgeQuery(kind graphartifact.EdgeKind, sourceKind graphartifact.NodeKind) string {
	switch kind {
	case graphartifact.EdgeContains:
		if sourceKind == graphartifact.NodeRepository {
			return `UNWIND $rows AS row MATCH (a:Repository {id: $repository_id}), (b:File {uid: row.target}) CREATE (a)-[:CONTAINS]->(b)`
		}
		return `UNWIND $rows AS row MATCH (a:File {uid: row.source}), (b:Symbol {uid: row.target}) CREATE (a)-[:CONTAINS]->(b)`
	case graphartifact.EdgeImports:
		return `UNWIND $rows AS row MATCH (a:File {uid: row.source}), (b:File {uid: row.target}) CREATE (a)-[:IMPORTS]->(b)`
	case graphartifact.EdgeReferences:
		return `UNWIND $rows AS row MATCH (a:Symbol {uid: row.source}), (b:Symbol {uid: row.target}) CREATE (a)-[:REFERENCES]->(b)`
	case graphartifact.EdgeCalls:
		return `UNWIND $rows AS row MATCH (a:Symbol {uid: row.source}), (b:Symbol {uid: row.target}) CREATE (a)-[:CALLS]->(b)`
	case graphartifact.EdgeExtends:
		return `UNWIND $rows AS row MATCH (a:Symbol {uid: row.source}), (b:Symbol {uid: row.target}) CREATE (a)-[:EXTENDS]->(b)`
	default:
		return `UNWIND $rows AS row MATCH (a:Symbol {uid: row.source}), (b:Symbol {uid: row.target}) CREATE (a)-[:IMPLEMENTS]->(b)`
	}
}

func executeBatches(ctx context.Context, session *Session, rows []map[string]any, query string, repositoryID int64) error {
	for start := 0; start < len(rows); {
		end, err := batchEnd(rows, start)
		if err != nil {
			return err
		}
		if err := executeStore(ctx, session, query, map[string]any{"repository_id": repositoryID, "rows": rows[start:end]}); err != nil {
			return err
		}
		start = end
	}
	return nil
}

func batchEnd(rows []map[string]any, start int) (int, error) {
	end, size := start, 2
	for end < len(rows) && end-start < storeBatchSize {
		encoded, err := json.Marshal(rows[end])
		if err != nil {
			return 0, err
		}
		rowSize := len(encoded) + 1
		if rowSize+2 > storeBatchBytes {
			return 0, fmt.Errorf("ladybug batch row exceeds %d-byte limit", storeBatchBytes)
		}
		if end > start && size+rowSize > storeBatchBytes {
			break
		}
		size += rowSize
		end++
	}
	return end, nil
}

func executeStore(ctx context.Context, session *Session, query string, parameters map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := session.Execute(ctx, query, parameters, QueryLimits{MaxRows: 1, MaxBytes: 1_024})
	return err
}

func storageUID(repositoryID int64, uid string) string {
	return fmt.Sprintf("%d:%s", repositoryID, uid)
}

func blobLiteral(value []byte) string {
	encoded := make([]byte, hex.EncodedLen(len(value)))
	hex.Encode(encoded, value)
	out := make([]byte, 0, len(value)*4)
	for i := 0; i < len(encoded); i += 2 {
		out = append(out, '\\', 'x', encoded[i], encoded[i+1])
	}
	return string(out)
}
