package scipgraph

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/encoding/protowire"
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
	if len(upload.Occurrences) != 2 || upload.Occurrences[0].StartLine != 2 || upload.Occurrences[0].EndLine != 2 || upload.Occurrences[0].EndCharacter != 9 ||
		upload.Occurrences[0].PositionEncoding != int32(scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart) || !upload.Occurrences[1].Local {
		t.Fatalf("occurrences = %#v", upload.Occurrences)
	}
	if len(upload.Relationships) != 1 || upload.Relationships[0] != (Relationship{Path: "pkg/a.go", Source: "scip-go gomod example.com/a v1.0.0 B#", Target: "scip-go gomod example.com/a v1.0.0 A#", Definition: true, Reference: true, Implementation: true, TypeDefinition: true}) {
		t.Fatalf("relationships = %#v", upload.Relationships)
	}
}

func TestParseUsesTypedRanges(t *testing.T) {
	data := marshalIndex(t, &scip.Index{
		Metadata: &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: "scip-go"}},
		Documents: []*scip.Document{{
			RelativePath:     "a.go",
			PositionEncoding: scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart,
			Occurrences: []*scip.Occurrence{
				{
					Range: []int32{9, 9, 10},
					TypedRange: &scip.Occurrence_SingleLineRange{SingleLineRange: &scip.SingleLineRange{
						Line: 1, StartCharacter: 2, EndCharacter: 3,
					}},
					Symbol: "local 0",
				},
				{
					TypedRange: &scip.Occurrence_MultiLineRange{MultiLineRange: &scip.MultiLineRange{
						StartLine: 4, StartCharacter: 5, EndLine: 6, EndCharacter: 7,
					}},
					Symbol: "local 1",
				},
			},
		}},
	})

	upload, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	first, second := upload.Occurrences[0], upload.Occurrences[1]
	if first.StartLine != 1 || first.StartCharacter != 2 || first.EndLine != 1 || first.EndCharacter != 3 {
		t.Fatalf("single-line typed range = %#v", first)
	}
	if second.StartLine != 4 || second.StartCharacter != 5 || second.EndLine != 6 || second.EndCharacter != 7 {
		t.Fatalf("multi-line typed range = %#v", second)
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
		{"unknown position encoding", func(index *scip.Index) {
			index.Documents[0].PositionEncoding = scip.PositionEncoding(99)
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

func TestParseRejectsOversizedWireValues(t *testing.T) {
	field := func(number protowire.Number, value []byte) []byte {
		return protowire.AppendBytes(protowire.AppendTag(nil, number, protowire.BytesType), value)
	}
	document := func(value []byte) []byte { return field(2, value) }
	symbol := func(value []byte) []byte { return field(3, value) }
	index := func(path, symbol string) []byte {
		return marshalIndex(t, &scip.Index{
			Metadata: &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: "scip-go"}},
			Documents: []*scip.Document{{
				RelativePath: path, PositionEncoding: scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart,
				Occurrences: []*scip.Occurrence{{Range: []int32{0, 0, 1}, Symbol: symbol}},
			}},
		})
	}

	tests := []struct {
		name           string
		data           []byte
		checkPreflight bool
	}{
		{"documents", bytes.Repeat(document(nil), 100_001), true},
		{"occurrences", document(bytes.Repeat(field(2, nil), 2_000_001)), true},
		{"relationships", document(symbol(bytes.Repeat(field(4, nil), 2_000_001))), true},
		{"relative path", index(strings.Repeat("a", 4_097), "local 0"), false},
		{"symbol", index("a.go", "scip-go gomod example.com/a v1 "+strings.Repeat("a", 8_193)+"#"), false},
		{"enclosing symbol", marshalIndex(t, &scip.Index{
			Metadata: &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: "scip-go"}},
			Documents: []*scip.Document{{
				RelativePath: "a.go", PositionEncoding: scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart,
				Symbols: []*scip.SymbolInformation{{Symbol: "local 0", EnclosingSymbol: strings.Repeat("a", 8_193)}},
			}},
		}), false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			check := func() {
				if _, err := Parse(test.data); !errors.Is(err, ErrInvalidIndex) {
					t.Fatalf("Parse() error = %v, want ErrInvalidIndex", err)
				}
			}
			check()
			if test.checkPreflight && testing.AllocsPerRun(1, check) > 10 {
				t.Fatal("Parse() materialized an oversized protobuf")
			}
		})
	}
}

func TestParseRejectsMalformedWirePreflight(t *testing.T) {
	if _, err := Parse([]byte{0x12, 0x80}); !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("Parse() error = %v, want ErrInvalidIndex", err)
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
