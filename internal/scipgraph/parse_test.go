package scipgraph

import (
	"errors"
	"testing"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestParseNormalizesSCIP(t *testing.T) {
	data := marshalIndex(t, &scip.Index{
		Metadata: &scip.Metadata{ProjectRoot: "file:///workspace", ToolInfo: &scip.ToolInfo{Name: "scip-go", Version: "1"}},
		Documents: []*scip.Document{{
			RelativePath:     "pkg/a.go",
			PositionEncoding: scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart,
			Occurrences: []*scip.Occurrence{
				{Range: []int32{2, 4, 9}, Symbol: "scip-go gomod example.com/a v1.0.0 A#", SymbolRoles: int32(scip.SymbolRole_Definition)},
				{Range: []int32{3, 1, 4, 2}, Symbol: "local 0", SymbolRoles: int32(scip.SymbolRole_ReadAccess)},
			},
			Symbols: []*scip.SymbolInformation{{
				Symbol: "scip-go gomod example.com/a v1.0.0 B#",
				Relationships: []*scip.Relationship{{
					Symbol: "scip-go gomod example.com/a v1.0.0 A#", IsDefinition: true, IsReference: true, IsImplementation: true, IsTypeDefinition: true,
				}},
			}},
		}},
	})

	upload, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if upload.ProjectRoot != "file:///workspace" || upload.IndexerName != "scip-go" || upload.IndexerVersion != "1" {
		t.Fatalf("metadata = %#v", upload)
	}
	if len(upload.Occurrences) != 2 || upload.Occurrences[0].StartLine != 2 || upload.Occurrences[0].EndLine != 2 || upload.Occurrences[0].EndCharacter != 9 || !upload.Occurrences[1].Local {
		t.Fatalf("occurrences = %#v", upload.Occurrences)
	}
	if len(upload.Relationships) != 1 || upload.Relationships[0] != (Relationship{Source: "scip-go gomod example.com/a v1.0.0 B#", Target: "scip-go gomod example.com/a v1.0.0 A#", Definition: true, Reference: true, Implementation: true, TypeDefinition: true}) {
		t.Fatalf("relationships = %#v", upload.Relationships)
	}
}

func TestParseRejectsInvalidIndex(t *testing.T) {
	valid := func() *scip.Index {
		return &scip.Index{Metadata: &scip.Metadata{ProjectRoot: "file:///workspace", ToolInfo: &scip.ToolInfo{Name: "scip-go", Version: "1"}}, Documents: []*scip.Document{{RelativePath: "pkg/a.go", PositionEncoding: scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart}}}
	}

	tests := []struct {
		name   string
		mutate func(*scip.Index)
	}{
		{"missing metadata", func(index *scip.Index) { index.Metadata = nil }},
		{"missing tool info", func(index *scip.Index) { index.Metadata.ToolInfo = nil }},
		{"unspecified position encoding", func(index *scip.Index) {
			index.Documents[0].PositionEncoding = scip.PositionEncoding_UnspecifiedPositionEncoding
		}},
		{"duplicate document", func(index *scip.Index) {
			index.Documents = append(index.Documents, proto.Clone(index.Documents[0]).(*scip.Document))
		}},
		{"unclean path", func(index *scip.Index) { index.Documents[0].RelativePath = "pkg/../a.go" }},
		{"parent path", func(index *scip.Index) { index.Documents[0].RelativePath = ".." }},
		{"parent descendant path", func(index *scip.Index) { index.Documents[0].RelativePath = "../a.go" }},
		{"invalid symbol", func(index *scip.Index) {
			index.Documents[0].Occurrences = []*scip.Occurrence{{Range: []int32{0, 0, 1}, Symbol: "invalid"}}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			index := valid()
			test.mutate(index)
			if _, err := Parse(marshalIndex(t, index)); !errors.Is(err, ErrInvalidIndex) {
				t.Fatalf("Parse() error = %v, want ErrInvalidIndex", err)
			}
		})
	}
}

func marshalIndex(t *testing.T, index *scip.Index) []byte {
	t.Helper()
	data, err := proto.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
