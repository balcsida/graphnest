package graphartifact

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	graphv1 "github.com/balcsida/graphnest/internal/graphartifact/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

var testLimits = Limits{MaxNodes: 10, MaxEdges: 10, MaxPathBytes: 4096, MaxIdentifierBytes: 16384}

func TestParseArtifactV1(t *testing.T) {
	got, err := Parse(marshalArtifact(t, validWireArtifact()), testLimits)
	if err != nil || got.RepositoryID != 101 || len(got.Nodes) != 3 || len(got.Edges) != 2 {
		t.Fatalf("Parse() = %#v, %v", got, err)
	}
}

func TestIdentityPrefersSCIP(t *testing.T) {
	got, err := Identity(Node{SCIPSymbol: "scip-go gomod example.com/a v1 A#"})
	if err != nil || got != "scip-go gomod example.com/a v1 A#" {
		t.Fatalf("Identity() = %q, %v", got, err)
	}
}

func TestIdentityFallbackIsLengthPrefixed(t *testing.T) {
	got, err := Identity(Node{Language: "go", Path: "a.go", Kind: NodeSymbol, QualifiedName: "a.A", Signature: "func()"})
	if err != nil || got != "131c60d753f498c62bd30a3378a2ec4120d51d9bbc0f6de11c739bbc892d5227" {
		t.Fatalf("Identity() = %q, %v", got, err)
	}
}

func TestIdentityRejectsOversizedCanonicalFields(t *testing.T) {
	oversized := strings.Repeat("a", 16385)
	for _, node := range []Node{
		{SCIPSymbol: oversized},
		{Language: "go", Path: "a.go", Kind: NodeSymbol, QualifiedName: oversized},
	} {
		if _, err := Identity(node); !errors.Is(err, ErrInvalidArtifact) {
			t.Fatalf("Identity(%#v) error = %v, want ErrInvalidArtifact", node, err)
		}
	}
}

func TestParseUsesDefaultsAndRejectsLimitsAboveHardCaps(t *testing.T) {
	data := marshalArtifact(t, validWireArtifact())
	if _, err := Parse(data, Limits{}); err != nil {
		t.Fatalf("Parse() with defaults error = %v", err)
	}
	if _, err := Parse(data, Limits{MaxNodes: 2_000_001, MaxEdges: 10, MaxPathBytes: 4096, MaxIdentifierBytes: 16384}); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("Parse() with excessive node limit error = %v, want ErrInvalidArtifact", err)
	}
	if _, err := Parse(data, Limits{MaxNodes: 10, MaxEdges: 10_000_001, MaxPathBytes: 4096, MaxIdentifierBytes: 16384}); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("Parse() with excessive edge limit error = %v, want ErrInvalidArtifact", err)
	}
}

func TestParseRejectsPathologicalNodeCountBeforeDecoding(t *testing.T) {
	data := bytes.Repeat(protowire.AppendBytes(protowire.AppendTag(nil, 6, protowire.BytesType), nil), 100_000)
	allocations := testing.AllocsPerRun(1, func() {
		if _, err := Parse(data, Limits{MaxNodes: 1, MaxEdges: 1}); !errors.Is(err, ErrInvalidArtifact) {
			t.Fatalf("Parse() error = %v, want ErrInvalidArtifact", err)
		}
	})
	if allocations > 100 {
		t.Fatalf("Parse() allocations = %.0f, want at most 100", allocations)
	}
}

func TestParseRejectsInvalidArtifacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*graphv1.Artifact)
		limits Limits
	}{
		{"unsupported schema", func(a *graphv1.Artifact) { a.SchemaVersion = 2 }, testLimits},
		{"bad commit", func(a *graphv1.Artifact) { a.Commit = strings.Repeat("A", 40) }, testLimits},
		{"bad hash", func(a *graphv1.Artifact) { a.ContentHash = []byte{1} }, testLimits},
		{"unclean path", func(a *graphv1.Artifact) { a.Nodes[1].Path = "dir/../a.go" }, testLimits},
		{"invalid range", func(a *graphv1.Artifact) { a.Nodes[2].Range = &graphv1.Range{StartLine: 1, EndLine: 0} }, testLimits},
		{"duplicate uid", func(a *graphv1.Artifact) { a.Nodes = append(a.Nodes, proto.Clone(a.Nodes[1]).(*graphv1.Node)) }, testLimits},
		{"duplicate edge", func(a *graphv1.Artifact) { a.Edges = append(a.Edges, proto.Clone(a.Edges[0]).(*graphv1.Edge)) }, testLimits},
		{"missing endpoint", func(a *graphv1.Artifact) { a.Edges[0].TargetUid = "missing" }, testLimits},
		{"illegal node kind", func(a *graphv1.Artifact) { a.Nodes[0].Kind = graphv1.NodeKind_NODE_KIND_UNSPECIFIED }, testLimits},
		{"illegal edge kind", func(a *graphv1.Artifact) { a.Edges[0].Kind = graphv1.EdgeKind_EDGE_KIND_UNSPECIFIED }, testLimits},
		{"illegal confidence", func(a *graphv1.Artifact) { a.Edges[0].Confidence = 1.1 }, testLimits},
		{"node overflow", func(a *graphv1.Artifact) {}, Limits{MaxNodes: 2, MaxEdges: 10, MaxPathBytes: 4096, MaxIdentifierBytes: 16384}},
		{"edge overflow", func(a *graphv1.Artifact) {}, Limits{MaxNodes: 10, MaxEdges: 1, MaxPathBytes: 4096, MaxIdentifierBytes: 16384}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artifact := validWireArtifact()
			test.mutate(artifact)
			if _, err := Parse(marshalArtifact(t, artifact), test.limits); !errors.Is(err, ErrInvalidArtifact) {
				t.Fatalf("Parse() error = %v, want ErrInvalidArtifact", err)
			}
		})
	}
}

func validWireArtifact() *graphv1.Artifact {
	return &graphv1.Artifact{
		SchemaVersion: 1, RepositoryId: 101, Commit: strings.Repeat("a", 40), ContentHash: bytes.Repeat([]byte{1}, sha256.Size),
		Analyzer: &graphv1.Analyzer{Name: "graphnest-scanner", Version: "1"},
		Nodes: []*graphv1.Node{
			{Uid: "repository:101", Kind: graphv1.NodeKind_NODE_KIND_REPOSITORY},
			{Uid: "file:a.go", Kind: graphv1.NodeKind_NODE_KIND_FILE, Path: "a.go"},
			{Uid: "symbol:a", Kind: graphv1.NodeKind_NODE_KIND_SYMBOL, Path: "a.go", QualifiedName: "a.A"},
		},
		Edges: []*graphv1.Edge{
			{SourceUid: "repository:101", TargetUid: "file:a.go", Kind: graphv1.EdgeKind_EDGE_KIND_CONTAINS, Confidence: 1},
			{SourceUid: "file:a.go", TargetUid: "symbol:a", Kind: graphv1.EdgeKind_EDGE_KIND_CONTAINS, Confidence: 1},
		},
	}
}

func marshalArtifact(t *testing.T, artifact *graphv1.Artifact) []byte {
	t.Helper()
	data, err := proto.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseRejectsEnumsBeforeNarrowing(t *testing.T) {
	for _, value := range []int32{257, 258, 259, 260, -255, 65537} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			a := validWireArtifact()
			a.Nodes[0].Kind = graphv1.NodeKind(value)
			if _, err := Parse(marshalArtifact(t, a), testLimits); !errors.Is(err, ErrInvalidArtifact) {
				t.Errorf("node kind %d accepted: %v", value, err)
			}
			a = validWireArtifact()
			a.Edges[0].Kind = graphv1.EdgeKind(value)
			if _, err := Parse(marshalArtifact(t, a), testLimits); !errors.Is(err, ErrInvalidArtifact) {
				t.Errorf("edge kind %d accepted: %v", value, err)
			}
		})
	}
}
