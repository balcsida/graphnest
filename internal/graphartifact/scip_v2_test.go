package graphartifact

import (
	"errors"
	"os"
	"strings"
	"testing"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/scipgraph"
	"google.golang.org/protobuf/proto"
)

const (
	demoCommit = "cccccccccccccccccccccccccccccccccccccccc"
	demoGreet  = "scip-go gomod github.com/oldorg/demo . `github.com/oldorg/demo/greet`/"
	demoMain   = "scip-go gomod github.com/oldorg/demo . `github.com/oldorg/demo`/main()."
	demoHello  = demoGreet + "Hello()."
	demoImpl   = demoGreet + "Impl#"
	demoFmt    = "scip-go gomod github.com/golang/go/src go1.24 fmt/"
)

func demoUpload(t *testing.T) scipgraph.Upload {
	t.Helper()
	data, err := os.ReadFile("../../test/fixtures/scip/go-demo/index.scip")
	if err != nil {
		t.Fatal(err)
	}
	upload, err := scipgraph.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return upload
}

func TestFromSCIPV2DerivesEntitiesAndReferences(t *testing.T) {
	upload := demoUpload(t)
	artifact, err := FromSCIPV2("101", demoCommit, upload, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Producer.Name != SCIPProducer || artifact.Producer.Configuration != "scip-go 0.2.7" || artifact.Repository != "101" || len(artifact.ContentHash) != 32 || len(artifact.Files) != 0 {
		t.Fatalf("header=%v files=%d", artifact.Producer, len(artifact.Files))
	}
	nodes := map[string]*graphv2.Node{}
	for _, node := range artifact.Nodes {
		nodes[node.Occurrence] = node
	}
	hello := nodes[demoHello]
	if hello == nil || hello.Kind != "function" || hello.Name != "Hello" || hello.QualifiedName != "github.com/oldorg/demo/greet.Hello" || hello.GetPath() != "greet/greet.go" || hello.Language != "go" ||
		hello.Location.Start.GetLine() != 10 || hello.Location.Start.GetCharacter() != 5 || hello.Location.End.GetCharacter() != 10 || !hello.GetIsExported() ||
		hello.GetDocumentation() != "Hello greets by name." || !strings.Contains(hello.GetSignature(), "func Hello(name string) string") || hello.GetScipSymbol() != demoHello {
		t.Fatalf("Hello=%v", hello)
	}
	for occurrence, want := range map[string]string{
		demoImpl: "struct", demoGreet + "Greeter#": "interface", demoGreet + "Impl#Greet().": "method", demoGreet + "Impl#Prefix.": "field",
		demoGreet: "module", demoMain: "function", demoFmt: "module", demoFmt + "Println().": "function", "file:main.go": "file", "file:greet/greet.go": "file",
	} {
		if node := nodes[occurrence]; node == nil || node.Kind != want {
			t.Fatalf("%s kind=%v want %s", occurrence, node, want)
		}
	}
	if external := nodes[demoFmt+"Println()."]; external.Path != nil || external.Location != nil || external.IsExported == nil || !*external.IsExported {
		t.Fatalf("external=%v", external)
	}
	if _, local := nodes["local 0"]; local {
		t.Fatal("local symbol became an entity")
	}
	edges := map[string]*graphv2.Edge{}
	for _, edge := range artifact.Edges {
		edges[edge.Kind.String()+" "+edge.Source+" -> "+edge.Target] = edge
	}
	want := map[string][2]int32{
		"EDGE_KIND_REFERENCES " + demoMain + " -> " + demoHello:                          {9, 19},
		"EDGE_KIND_REFERENCES " + demoGreet + "Impl#Greet(). -> " + demoHello:            {16, 60},
		"EDGE_KIND_REFERENCES " + demoMain + " -> " + demoFmt + "Println().":             {9, 5},
		"EDGE_KIND_REFERENCES " + demoGreet + "Impl#Greet(). -> " + demoImpl + "Prefix.": {16, 51},
		"EDGE_KIND_REFERENCES " + demoMain + " -> " + demoGreet + "Greeter#":             {10, 13},
	}
	for key, position := range want {
		edge := edges[key]
		if edge == nil || edge.Location.Start.GetLine() != position[0] || edge.Location.Start.GetCharacter() != position[1] || edge.GetConfidence() != 1 || edge.GetProvenance() != "scip" {
			t.Fatalf("%s=%v", key, edge)
		}
	}
	for _, key := range []string{
		"EDGE_KIND_IMPORTS file:main.go -> " + demoFmt,
		"EDGE_KIND_IMPORTS file:main.go -> " + demoGreet,
		"EDGE_KIND_IMPLEMENTS " + demoGreet + "Impl#Greet(). -> " + demoGreet + "Greeter#Greet.",
		"EDGE_KIND_IMPLEMENTS " + demoImpl + " -> " + demoGreet + "Greeter#",
		"EDGE_KIND_CONTAINS file:greet/greet.go -> " + demoHello,
		"EDGE_KIND_CONTAINS " + demoImpl + " -> " + demoGreet + "Impl#Greet().",
		"EDGE_KIND_CONTAINS " + demoGreet + " -> " + demoHello,
	} {
		if edges[key] == nil {
			t.Fatalf("missing %s", key)
		}
	}
	for key := range edges {
		if strings.HasPrefix(key, "EDGE_KIND_REFERENCES") && strings.Contains(key, "-> "+demoGreet+" ") || strings.Contains(key, "local ") {
			t.Fatalf("unexpected edge %s", key)
		}
	}
	again, err := FromSCIPV2("101", demoCommit, upload, Limits{})
	if err != nil || !proto.Equal(artifact, again) {
		t.Fatalf("conversion is not deterministic: %v", err)
	}
	if err := ValidateV2(artifact, Limits{}); err != nil {
		t.Fatal(err)
	}
}

func TestFromSCIPV2WithoutEnclosingRangesAttributesReferencesToFiles(t *testing.T) {
	symbol := "scip go example.com/acme v1 pkg/Item#"
	upload := scipgraph.Upload{IndexerName: "custom", Occurrences: []scipgraph.Occurrence{
		{Path: "a.go", Symbol: symbol, StartLine: 1, StartCharacter: 5, EndLine: 1, EndCharacter: 9, Roles: 1},
		{Path: "b.go", Symbol: symbol, StartLine: 3, StartCharacter: 0, EndLine: 3, EndCharacter: 4, Roles: 8},
	}}
	artifact, err := FromSCIPV2("7", demoCommit, upload, Limits{})
	if err != nil || len(artifact.Nodes) != 3 || len(artifact.Edges) != 2 {
		t.Fatalf("artifact=%v err=%v", artifact, err)
	}
	var reference *graphv2.Edge
	for _, edge := range artifact.Edges {
		if edge.Kind == graphv2.EdgeKind_EDGE_KIND_REFERENCES {
			reference = edge
		}
	}
	if reference == nil || reference.Source != "file:b.go" || reference.Target != symbol || reference.GetResolutionReason() != "scip_document" {
		t.Fatalf("reference=%v", reference)
	}
}

// Oversized producer text is clipped and long symbol pairs still get a valid
// edge identifier, so a legitimate index never fails validation; a graph the
// byte budget cannot hold is reported as too large, not invalid.
func TestFromSCIPV2BoundsProducerTextAndReportsOversizedGraphs(t *testing.T) {
	// Symbols at the parser's 8 KiB cap: their joined identifier exceeds 16 KiB.
	long := "scip go x v1 pkg/" + strings.Repeat("a", 8192-len("scip go x v1 pkg/#")) + "#"
	longer := long + "bb."
	upload := scipgraph.Upload{Occurrences: []scipgraph.Occurrence{
		{Path: "a.go", Symbol: long, EndCharacter: 1, Roles: 1},
		{Path: "a.go", Symbol: longer, StartLine: 1, EndLine: 1, EndCharacter: 1, Roles: 1},
	}, Symbols: []scipgraph.SymbolInformation{{Symbol: long, Documentation: strings.Repeat("d", 300<<10), Signature: strings.Repeat("s", 20<<10), DisplayName: strings.Repeat("n", 20<<10)}}}
	artifact, err := FromSCIPV2("7", demoCommit, upload, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var node *graphv2.Node
	for _, candidate := range artifact.Nodes {
		if candidate.Occurrence == long {
			node = candidate
		}
	}
	if node == nil || len(node.GetDocumentation()) != maxDocumentationBytes || len(node.GetSignature()) != DefaultMaxIdentifierBytes || len(node.Name) != DefaultMaxIdentifierBytes {
		t.Fatalf("node=%v", node)
	}
	var contains int
	for _, edge := range artifact.Edges {
		if edge.Kind == graphv2.EdgeKind_EDGE_KIND_CONTAINS && edge.Source == long && edge.Target == longer {
			contains++
			if len(edge.Occurrence) > DefaultMaxIdentifierBytes || !strings.HasPrefix(edge.Occurrence, "contains sha256:") {
				t.Fatalf("edge id=%q", edge.Occurrence)
			}
		}
	}
	if contains != 1 {
		t.Fatalf("contains edges=%d", contains)
	}
	if _, err := FromSCIPV2("101", demoCommit, demoUpload(t), Limits{MaxArtifactBytes: 2 << 10}); !errors.Is(err, ErrGraphTooLarge) {
		t.Fatalf("budget=%v", err)
	}
	if _, err := FromSCIPV2("101", demoCommit, demoUpload(t), Limits{MaxEdges: 1}); !errors.Is(err, ErrGraphTooLarge) {
		t.Fatalf("edge limit=%v", err)
	}
}
