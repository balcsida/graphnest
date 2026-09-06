package graphartifact

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"path"
	"strings"

	graphv1 "github.com/balcsida/graphnest/internal/graphartifact/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

var ErrInvalidArtifact = errors.New("invalid graph artifact")

func Parse(data []byte, limits Limits) (Artifact, error) {
	limits, ok := normalizedLimits(limits)
	if !ok || exceedsWireCounts(data, limits) {
		return Artifact{}, ErrInvalidArtifact
	}
	var wire graphv1.Artifact
	if err := proto.Unmarshal(data, &wire); err != nil {
		return Artifact{}, ErrInvalidArtifact
	}
	// Check int32 wire enums before converting them to the uint8 v1 model.
	for _, node := range wire.Nodes {
		if node == nil || node.Kind < graphv1.NodeKind_NODE_KIND_REPOSITORY || node.Kind > graphv1.NodeKind_NODE_KIND_SYMBOL {
			return Artifact{}, ErrInvalidArtifact
		}
	}
	for _, edge := range wire.Edges {
		if edge == nil || edge.Kind < graphv1.EdgeKind_EDGE_KIND_CONTAINS || edge.Kind > graphv1.EdgeKind_EDGE_KIND_IMPLEMENTS {
			return Artifact{}, ErrInvalidArtifact
		}
	}
	artifact := fromWire(&wire)
	return artifact, Validate(artifact, limits)
}

func exceedsWireCounts(data []byte, limits Limits) bool {
	var nodes, edges int
	for len(data) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(data)
		if tagLength < 0 {
			return true
		}
		data = data[tagLength:]
		switch number {
		case 6:
			nodes++
		case 7:
			edges++
		}
		if nodes > limits.MaxNodes || edges > limits.MaxEdges {
			return true
		}
		valueLength := protowire.ConsumeFieldValue(number, wireType, data)
		if valueLength < 0 {
			return true
		}
		data = data[valueLength:]
	}
	return false
}

func Validate(artifact Artifact, limits Limits) error {
	var ok bool
	if limits, ok = normalizedLimits(limits); !ok {
		return ErrInvalidArtifact
	}
	if artifact.SchemaVersion != 1 || artifact.RepositoryID <= 0 || !validCommit(artifact.Commit) || len(artifact.ContentHash) != sha256.Size ||
		!validIdentifier(artifact.Analyzer.Name, limits) || !validIdentifier(artifact.Analyzer.Version, limits) ||
		len(artifact.Nodes) > limits.MaxNodes || len(artifact.Edges) > limits.MaxEdges {
		return ErrInvalidArtifact
	}
	nodes := make(map[string]struct{}, len(artifact.Nodes))
	for _, node := range artifact.Nodes {
		if !validNode(node, limits) {
			return ErrInvalidArtifact
		}
		if _, ok := nodes[node.UID]; ok {
			return ErrInvalidArtifact
		}
		nodes[node.UID] = struct{}{}
	}
	edges := make(map[edgeKey]struct{}, len(artifact.Edges))
	for _, edge := range artifact.Edges {
		if !validEdge(edge, limits) {
			return ErrInvalidArtifact
		}
		if _, ok := nodes[edge.SourceUID]; !ok {
			return ErrInvalidArtifact
		}
		if _, ok := nodes[edge.TargetUID]; !ok {
			return ErrInvalidArtifact
		}
		key := edgeKey{source: edge.SourceUID, target: edge.TargetUID, kind: edge.Kind, path: edge.Path, range_: edge.Range}
		if _, ok := edges[key]; ok {
			return ErrInvalidArtifact
		}
		edges[key] = struct{}{}
	}
	return nil
}

func Identity(node Node) (string, error) {
	if node.SCIPSymbol != "" {
		if len(node.SCIPSymbol) > DefaultMaxIdentifierBytes {
			return "", ErrInvalidArtifact
		}
		return node.SCIPSymbol, nil
	}
	limits := Limits{MaxPathBytes: DefaultMaxPathBytes, MaxIdentifierBytes: DefaultMaxIdentifierBytes}
	if !validNodeKind(node.Kind) || !validIdentifier(node.Language, limits) || !validPath(node.Path, limits.MaxPathBytes) || !validIdentifier(node.QualifiedName, limits) ||
		!validOptionalIdentifier(node.SymbolKind, limits) || !validOptionalIdentifier(node.Signature, limits) {
		return "", ErrInvalidArtifact
	}
	hash := sha256.New()
	for _, value := range []string{node.Language, node.Path, string(rune(node.Kind)), node.QualifiedName, node.Signature} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func normalizedLimits(limits Limits) (Limits, bool) {
	if limits.MaxNodes == 0 {
		limits.MaxNodes = DefaultMaxNodes
	}
	if limits.MaxEdges == 0 {
		limits.MaxEdges = DefaultMaxEdges
	}
	if limits.MaxPathBytes == 0 {
		limits.MaxPathBytes = DefaultMaxPathBytes
	}
	if limits.MaxIdentifierBytes == 0 {
		limits.MaxIdentifierBytes = DefaultMaxIdentifierBytes
	}
	return limits, limits.MaxNodes > 0 && limits.MaxNodes <= HardMaxNodes && limits.MaxEdges > 0 && limits.MaxEdges <= HardMaxEdges &&
		limits.MaxPathBytes > 0 && limits.MaxPathBytes <= DefaultMaxPathBytes && limits.MaxIdentifierBytes > 0 && limits.MaxIdentifierBytes <= DefaultMaxIdentifierBytes
}

type edgeKey struct {
	source, target, path string
	kind                 EdgeKind
	range_               Range
}

func validNode(node Node, limits Limits) bool {
	return validNodeKind(node.Kind) && validIdentifier(node.UID, limits) && validOptionalPath(node.Path, limits.MaxPathBytes) && validRange(node.Range) &&
		validOptionalIdentifier(node.Language, limits) && validOptionalIdentifier(node.SymbolKind, limits) && validOptionalIdentifier(node.QualifiedName, limits) &&
		validOptionalIdentifier(node.Signature, limits) && validOptionalIdentifier(node.SCIPSymbol, limits)
}

func validEdge(edge Edge, limits Limits) bool {
	return validEdgeKind(edge.Kind) && validIdentifier(edge.SourceUID, limits) && validIdentifier(edge.TargetUID, limits) && validOptionalPath(edge.Path, limits.MaxPathBytes) &&
		validRange(edge.Range) && !math.IsNaN(float64(edge.Confidence)) && edge.Confidence >= 0 && edge.Confidence <= 1 && validOptionalIdentifier(edge.ResolutionReason, limits)
}

func validCommit(commit string) bool {
	if len(commit) != 40 {
		return false
	}
	for _, c := range commit {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

func validRange(r Range) bool {
	return r.StartLine >= 0 && r.StartCharacter >= 0 && r.EndLine >= r.StartLine && r.EndCharacter >= 0 && (r.EndLine != r.StartLine || r.EndCharacter >= r.StartCharacter)
}

func validOptionalPath(value string, max int) bool { return value == "" || validPath(value, max) }

func validPath(value string, max int) bool {
	clean := path.Clean(value)
	return value != "" && len(value) <= max && clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && !strings.HasPrefix(clean, "/") && !strings.Contains(value, "\\")
}

func validIdentifier(value string, limits Limits) bool {
	return value != "" && len(value) <= limits.MaxIdentifierBytes
}

func validOptionalIdentifier(value string, limits Limits) bool {
	return value == "" || len(value) <= limits.MaxIdentifierBytes
}

func validNodeKind(kind NodeKind) bool { return kind >= NodeRepository && kind <= NodeSymbol }

func validEdgeKind(kind EdgeKind) bool { return kind >= EdgeContains && kind <= EdgeImplements }

func fromWire(wire *graphv1.Artifact) Artifact {
	artifact := Artifact{SchemaVersion: wire.SchemaVersion, RepositoryID: wire.RepositoryId, Commit: wire.Commit, ContentHash: wire.ContentHash}
	if wire.Analyzer != nil {
		artifact.Analyzer = Analyzer{Name: wire.Analyzer.Name, Version: wire.Analyzer.Version}
	}
	for _, node := range wire.Nodes {
		if node == nil {
			artifact.Nodes = append(artifact.Nodes, Node{})
			continue
		}
		artifact.Nodes = append(artifact.Nodes, Node{UID: node.Uid, Kind: NodeKind(node.Kind), Path: node.Path, Language: node.Language, SymbolKind: node.SymbolKind, QualifiedName: node.QualifiedName, Signature: node.Signature, SCIPSymbol: node.ScipSymbol, Range: fromWireRange(node.Range)})
	}
	for _, edge := range wire.Edges {
		if edge == nil {
			artifact.Edges = append(artifact.Edges, Edge{})
			continue
		}
		artifact.Edges = append(artifact.Edges, Edge{SourceUID: edge.SourceUid, TargetUID: edge.TargetUid, Kind: EdgeKind(edge.Kind), Path: edge.Path, Range: fromWireRange(edge.Range), Confidence: edge.Confidence, ResolutionReason: edge.ResolutionReason})
	}
	return artifact
}

func fromWireRange(wire *graphv1.Range) Range {
	if wire == nil {
		return Range{}
	}
	return Range{StartLine: wire.StartLine, StartCharacter: wire.StartCharacter, EndLine: wire.EndLine, EndCharacter: wire.EndCharacter}
}
