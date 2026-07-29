package graphartifact

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/grepnest/grepnest/internal/scipgraph"
)

type Manifest struct {
	RepositoryID, UploadID int64
	Commit, Source         string
	SchemaVersion          uint32
	ContentHash            []byte
}

type SCIPRepository struct {
	ID     int64
	Commit string
}

func FromSCIP(repository SCIPRepository, occurrences []scipgraph.Occurrence, relationships []scipgraph.Relationship) (Artifact, error) {
	artifact := Artifact{SchemaVersion: 1, RepositoryID: repository.ID, Commit: repository.Commit, Analyzer: Analyzer{Name: "scip", Version: "1"}}
	nodes := map[string]Node{}
	edges := map[string]Edge{}
	addNode := func(node Node) { nodes[node.UID] = node }
	addEdge := func(edge Edge) {
		edges[fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%d\x00%d\x00%d\x00%d", edge.SourceUID, edge.TargetUID, edge.Kind, edge.Path, edge.Range.StartLine, edge.Range.StartCharacter, edge.Range.EndLine, edge.Range.EndCharacter)] = edge
	}
	repositoryUID := fmt.Sprintf("repository:%d", repository.ID)
	addNode(Node{UID: repositoryUID, Kind: NodeRepository})
	addSymbol := func(symbol, path string, r Range) string {
		uid := "symbol:" + symbol
		if _, ok := nodes[uid]; !ok {
			addNode(Node{UID: uid, Kind: NodeSymbol, Path: path, QualifiedName: symbol, SCIPSymbol: symbol, Range: r})
		}
		return uid
	}
	addFile := func(path string) string {
		uid := "file:" + path
		if _, ok := nodes[uid]; !ok {
			addNode(Node{UID: uid, Kind: NodeFile, Path: path})
			addEdge(Edge{SourceUID: repositoryUID, TargetUID: uid, Kind: EdgeContains, Path: path, Confidence: 1})
		}
		return uid
	}
	for _, occurrence := range occurrences {
		fileUID := addFile(occurrence.Path)
		symbolUID := addSymbol(occurrence.Symbol, occurrence.Path, Range{occurrence.StartLine, occurrence.StartCharacter, occurrence.EndLine, occurrence.EndCharacter})
		addEdge(Edge{SourceUID: fileUID, TargetUID: symbolUID, Kind: EdgeContains, Path: occurrence.Path, Range: Range{occurrence.StartLine, occurrence.StartCharacter, occurrence.EndLine, occurrence.EndCharacter}, Confidence: 1})
	}
	for _, relationship := range relationships {
		sourceUID := addSymbol(relationship.Source, relationship.Path, Range{})
		targetUID := addSymbol(relationship.Target, relationship.Path, Range{})
		for _, kind := range []struct {
			enabled bool
			kind    EdgeKind
		}{{relationship.Reference, EdgeReferences}, {relationship.TypeDefinition, EdgeExtends}, {relationship.Implementation, EdgeImplements}} {
			if kind.enabled {
				addEdge(Edge{SourceUID: sourceUID, TargetUID: targetUID, Kind: kind.kind, Path: relationship.Path, Confidence: 1, ResolutionReason: "scip"})
			}
		}
	}
	for _, node := range nodes {
		artifact.Nodes = append(artifact.Nodes, node)
	}
	for _, edge := range edges {
		artifact.Edges = append(artifact.Edges, edge)
	}
	sort.Slice(artifact.Nodes, func(i, j int) bool { return artifact.Nodes[i].UID < artifact.Nodes[j].UID })
	sort.Slice(artifact.Edges, func(i, j int) bool { return fallbackEdgeKey(artifact.Edges[i]) < fallbackEdgeKey(artifact.Edges[j]) })
	artifact.ContentHash = fallbackHash(artifact)
	return artifact, Validate(artifact, Limits{})
}

func fallbackEdgeKey(edge Edge) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%d\x00%d\x00%d\x00%d", edge.SourceUID, edge.TargetUID, edge.Kind, edge.Path, edge.Range.StartLine, edge.Range.StartCharacter, edge.Range.EndLine, edge.Range.EndCharacter)
}

func fallbackHash(artifact Artifact) []byte {
	hash := sha256.New()
	write := func(values ...string) {
		for _, value := range values {
			var length [8]byte
			binary.BigEndian.PutUint64(length[:], uint64(len(value)))
			_, _ = hash.Write(length[:])
			_, _ = hash.Write([]byte(value))
		}
	}
	write(fmt.Sprint(artifact.SchemaVersion), artifact.Analyzer.Name, artifact.Analyzer.Version, fmt.Sprint(artifact.RepositoryID), artifact.Commit)
	for _, node := range artifact.Nodes {
		write(node.UID, node.Path, node.Language, node.SymbolKind, node.QualifiedName, node.Signature, node.SCIPSymbol, fmt.Sprint(node.Kind), fmt.Sprint(node.Range))
	}
	for _, edge := range artifact.Edges {
		write(edge.SourceUID, edge.TargetUID, edge.Path, edge.ResolutionReason, fmt.Sprint(edge.Kind), fmt.Sprint(edge.Range), fmt.Sprint(edge.Confidence))
	}
	return hash.Sum(nil)
}
