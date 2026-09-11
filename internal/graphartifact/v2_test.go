package graphartifact

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"google.golang.org/protobuf/proto"
)

func minimalV2() *graphv2.Artifact {
	return &graphv2.Artifact{SchemaVersion: 2, Repository: "https://github.com/example/project", Commit: strings.Repeat("a", 40), Producer: &graphv2.Producer{Name: "codegraph", Version: "1.6.0", Configuration: "portable"}, Nodes: []*graphv2.Node{{SourceId: "a", Occurrence: "declaration:1", Kind: "function"}, {SourceId: "b", Occurrence: "declaration:2", Kind: "class"}}}
}

func TestV2Contract(t *testing.T) {
	a := minimalV2()
	a.Edges = []*graphv2.Edge{{SourceId: "sql:42", Occurrence: "call:1", Source: "declaration:1", Target: "declaration:2", Kind: graphv2.EdgeKind_EDGE_KIND_CALLS}}
	data, err := MarshalV2(a, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseV2(data, Limits{})
	if err != nil || !proto.Equal(a, got) {
		t.Fatalf("roundtrip: %v, %v", got, err)
	}
	if got.Nodes[0].Location != nil || got.Edges[0].Location != nil || got.Edges[0].Confidence != nil {
		t.Fatal("invented evidence")
	}
	if _, err := Parse(data, Limits{}); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("v1 accepted v2: %v", err)
	}
	a.SchemaVersion = 3
	data, _ = proto.Marshal(a)
	if _, err := ParseV2(data, Limits{}); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("unknown version: %v", err)
	}
}

func TestV2SemanticHash(t *testing.T) {
	a := minimalV2()
	a.Extensions = []*graphv2.Extension{{Namespace: "codegraph.metadata", Json: []byte(`{"b":2,"a":1.00}`)}}
	b := proto.Clone(a).(*graphv2.Artifact)
	b.ImportedAt = 123
	b.Nodes[0].UpdatedAt = proto.Int64(900)
	b.Extensions[0].Json = []byte(`{ "a":1, "b":2e0 }`)
	b.Nodes[0], b.Nodes[1] = b.Nodes[1], b.Nodes[0]
	x, err := SemanticHashV2(a, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	y, err := SemanticHashV2(b, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(x, y) {
		t.Fatalf("semantic hash changed: %x != %x", x, y)
	}
	b.Nodes[0].Name = "changed"
	y, _ = SemanticHashV2(b, Limits{})
	if bytes.Equal(x, y) {
		t.Fatal("semantic change not hashed")
	}
}

func TestV2ProducerScopedIdentity(t *testing.T) {
	a := minimalV2()
	first, err := IdentityV2(a.Producer, a.Repository, a.Nodes[0].SourceId, a.Nodes[0].Occurrence)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*graphv2.Artifact){func(b *graphv2.Artifact) { b.Repository += "other" }, func(b *graphv2.Artifact) { b.Producer.Name = "scip" }, func(b *graphv2.Artifact) { b.Nodes[0].Occurrence = "overload:2" }} {
		b := proto.Clone(a).(*graphv2.Artifact)
		change(b)
		other, err := IdentityV2(b.Producer, b.Repository, b.Nodes[0].SourceId, b.Nodes[0].Occurrence)
		if err != nil || first == other {
			t.Fatalf("collapsed identity: %v", err)
		}
	}
}
