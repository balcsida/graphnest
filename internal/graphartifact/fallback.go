package graphartifact

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/balcsida/graphnest/internal/scipgraph"
	"github.com/scip-code/scip/bindings/go/scip"
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
	// A symbol node sits at its definition. Occurrences arrive in document
	// order, so the first sighting is usually a reference in some other file;
	// the definition-role occurrence replaces it whenever one exists.
	defined := map[string]bool{}
	addSymbol := func(symbol, path string, r Range, definition bool) string {
		uid := "symbol:" + symbol
		if _, ok := nodes[uid]; !ok || definition && !defined[uid] {
			name, kind := symbolNameAndKind(symbol)
			addNode(Node{UID: uid, Kind: NodeSymbol, Path: path, SymbolKind: kind, QualifiedName: name, SCIPSymbol: symbol, Range: r})
			defined[uid] = defined[uid] || definition
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
		symbolUID := addSymbol(occurrence.Symbol, occurrence.Path, Range{occurrence.StartLine, occurrence.StartCharacter, occurrence.EndLine, occurrence.EndCharacter}, occurrence.Roles&int32(scip.SymbolRole_Definition) != 0)
		addEdge(Edge{SourceUID: fileUID, TargetUID: symbolUID, Kind: EdgeContains, Path: occurrence.Path, Range: Range{occurrence.StartLine, occurrence.StartCharacter, occurrence.EndLine, occurrence.EndCharacter}, Confidence: 1})
	}
	for _, relationship := range relationships {
		sourceUID := addSymbol(relationship.Source, relationship.Path, Range{}, false)
		targetUID := addSymbol(relationship.Target, relationship.Path, Range{}, false)
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

// symbolNameAndKind exposes what a caller can actually name: the innermost SCIP
// descriptor and the kind its suffix encodes. Context, impact and trace match
// symbols by exact name, and nobody has the full SCIP symbol string in hand.
// Local and unparsable symbols keep the raw string so they stay addressable.
func symbolNameAndKind(symbol string) (string, string) {
	parsed, err := scip.ParseSymbol(symbol)
	if err != nil || len(parsed.Descriptors) == 0 {
		return symbol, ""
	}
	descriptor := parsed.Descriptors[len(parsed.Descriptors)-1]
	kind := ""
	switch descriptor.Suffix {
	case scip.Descriptor_Namespace:
		kind = "namespace"
	case scip.Descriptor_Type:
		kind = "type"
	case scip.Descriptor_Term:
		// SCIP folds fields, variables and constants into one suffix; a term
		// nested in a type is a field, a top-level term a variable.
		kind = "variable"
		if len(parsed.Descriptors) > 1 && parsed.Descriptors[len(parsed.Descriptors)-2].Suffix == scip.Descriptor_Type {
			kind = "field"
		}
	case scip.Descriptor_Method:
		kind = "function"
		if len(parsed.Descriptors) > 1 && parsed.Descriptors[len(parsed.Descriptors)-2].Suffix == scip.Descriptor_Type {
			kind = "method"
		}
	case scip.Descriptor_TypeParameter:
		kind = "type_parameter"
	case scip.Descriptor_Parameter:
		kind = "parameter"
	case scip.Descriptor_Meta:
		kind = "meta"
	case scip.Descriptor_Macro:
		kind = "macro"
	case scip.Descriptor_Local:
		kind = "local"
	}
	if descriptor.Name == "" {
		return symbol, kind
	}
	return descriptor.Name, kind
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
