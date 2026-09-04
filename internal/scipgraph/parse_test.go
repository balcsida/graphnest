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
		{"unknown position encoding", func(index *scip.Index) {
			index.Documents[0].PositionEncoding = scip.PositionEncoding(99)
		}},
		{"duplicate document", func(index *scip.Index) {
			index.Documents = append(index.Documents, proto.Clone(index.Documents[0]).(*scip.Document))
		}},
		{"unclean path", func(index *scip.Index) { index.Documents[0].RelativePath = "pkg/../a.go" }},
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
		{"external symbols", bytes.Repeat(field(3, nil), 500_001), true},
		{"document symbols", document(bytes.Repeat(field(3, nil), 500_001)), true},
		{"diagnostics", document(field(2, bytes.Repeat(field(6, nil), 2_000_001))), true},
		{"signature occurrences", field(3, field(7, bytes.Repeat(field(2, nil), 2_000_001))), true},
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

func TestWirePreflightEnforcesCombinedLimits(t *testing.T) {
	limits := wireLimits{documents: 1, occurrences: 2, relationships: 2, symbols: 2, diagnostics: 2, pathBytes: 4, symbolBytes: 8}
	occurrence := wireBytes(2, []byte("local 0"))
	signature := func(occurrences ...[]byte) []byte {
		var value []byte
		for _, occurrence := range occurrences {
			value = append(value, wireBytes(2, occurrence)...)
		}
		return wireBytes(7, value)
	}
	relationship := wireBytes(4, wireBytes(1, []byte("local 2")))
	symbol := func(value ...[]byte) []byte { return wireBytes(3, bytes.Join(value, nil)) }
	external := func(value ...[]byte) []byte { return wireBytes(3, bytes.Join(value, nil)) }
	document := func(value ...[]byte) []byte { return wireBytes(2, bytes.Join(value, nil)) }

	tests := []struct {
		name string
		data []byte
	}{
		{"occurrences", document(wireBytes(2, occurrence), symbol(signature(occurrence, occurrence)))},
		{"symbols", append(document(symbol(nil), symbol(nil)), external(nil)...)},
		{"relationships", append(document(symbol(relationship)), external(relationship, relationship)...)},
		{"diagnostics", document(wireBytes(2, append(occurrence, wireBytes(6, nil)...)), symbol(signature(append(occurrence, wireBytes(6, nil)...), append(occurrence, wireBytes(6, nil)...))))},
		{"duplicate signature fields", external(signature(occurrence), signature(occurrence), signature(occurrence))},
		{"signature symbol", external(signature(wireBytes(2, []byte("123456789"))))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if validWireLimits(test.data, limits) {
				t.Fatal("validWireLimits() = true, want false")
			}
		})
	}
}

func TestWirePreflightAcceptsExactLimits(t *testing.T) {
	limits := wireLimits{documents: 1, occurrences: 2, relationships: 2, symbols: 2, diagnostics: 2, pathBytes: 4, symbolBytes: 8}
	occurrence := append(wireBytes(2, []byte("local 0")), wireBytes(6, nil)...)
	relationship := wireBytes(4, wireBytes(1, []byte("local 2")))
	documentSymbol := append(wireBytes(1, []byte("local 1")), relationship...)
	documentSymbol = append(documentSymbol, wireBytes(7, wireBytes(2, occurrence))...)
	document := append(wireBytes(1, []byte("a.go")), wireBytes(2, occurrence)...)
	document = append(document, wireBytes(3, documentSymbol)...)
	external := append(wireBytes(1, []byte("local 3")), relationship...)
	data := append(wireBytes(2, document), wireBytes(3, external)...)
	if !validWireLimits(data, limits) {
		t.Fatal("validWireLimits() = false at exact limits")
	}
}

func TestWirePreflightRejectsTargetedWrongWireTypes(t *testing.T) {
	wrong := func(number protowire.Number) []byte {
		return protowire.AppendVarint(protowire.AppendTag(nil, number, protowire.VarintType), 1)
	}
	fixed := func(number protowire.Number) []byte {
		return protowire.AppendFixed32(protowire.AppendTag(nil, number, protowire.Fixed32Type), 1)
	}
	tests := [][]byte{
		wrong(1), wrong(2), wrong(3),
		wireBytes(1, wrong(2)), wireBytes(1, wireBytes(2, wrong(3))),
		wireBytes(2, wrong(1)), wireBytes(2, wrong(2)), wireBytes(2, wrong(3)),
		wireBytes(2, wireBytes(2, fixed(1))), wireBytes(2, wireBytes(2, wrong(2))),
		wireBytes(2, wireBytes(2, wrong(4))), wireBytes(2, wireBytes(2, wrong(6))),
		wireBytes(2, wireBytes(2, fixed(7))), wireBytes(2, wireBytes(2, wireBytes(6, fixed(5)))),
		wireBytes(3, wrong(1)), wireBytes(3, wrong(3)), wireBytes(3, wrong(4)), wireBytes(3, wrong(7)), wireBytes(3, wrong(8)),
		wireBytes(3, wireBytes(4, wrong(1))),
		wireBytes(3, wireBytes(7, wrong(2))),
		wireBytes(2, wireBytes(2, wireBytes(1, []byte{0x80}))),
	}
	for i, data := range tests {
		if validWire(data) {
			t.Fatalf("case %d: validWire() = true, want false", i)
		}
	}
}

func TestWirePreflightRejectsOversizedPackedRange(t *testing.T) {
	packed := []byte{0, 0, 0, 0, 0}
	data := wireBytes(2, wireBytes(2, wireBytes(1, packed)))
	if validWire(data) {
		t.Fatal("five-element packed range bypassed preflight")
	}
}

func TestWirePreflightBoundsRepeatedScalars(t *testing.T) {
	limits := wireLimits{
		documents: 2, occurrences: 2, relationships: 2, symbols: 2, diagnostics: 2,
		scalarElements: 2, pathBytes: 8, symbolBytes: 8,
	}
	strings := append(append(wireBytes(3, nil), wireBytes(3, nil)...), wireBytes(3, nil)...)
	packed := []byte{0, 0, 0}
	occurrence := func(value []byte) []byte { return wireBytes(2, wireBytes(2, value)) }

	tests := []struct {
		name string
		data []byte
	}{
		{"tool arguments", wireBytes(1, wireBytes(2, strings))},
		{"symbol documentation", wireBytes(3, strings)},
		{"override documentation", occurrence(bytes.ReplaceAll(strings, []byte{0x1a}, []byte{0x22}))},
		{"packed range", occurrence(wireBytes(1, packed))},
		{"unpacked enclosing range", occurrence(bytes.Repeat(wireVarint(7, 0), 3))},
		{"diagnostic tags", occurrence(wireBytes(6, wireBytes(5, packed)))},
		{"combined fields", append(
			wireBytes(1, wireBytes(2, wireBytes(3, nil))),
			wireBytes(2, append(wireBytes(2, wireBytes(4, nil)), wireBytes(3, wireBytes(3, nil))...))...,
		)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			check := func() {
				if validWireLimits(test.data, limits) {
					t.Fatal("validWireLimits() = true, want false")
				}
			}
			check()
			if testing.AllocsPerRun(1, check) > 1 {
				t.Fatal("preflight allocated while rejecting repeated scalars")
			}
		})
	}
}

func TestWirePreflightAggregatesDuplicateRanges(t *testing.T) {
	limits := wireLimits{documents: 1, occurrences: 1, relationships: 1, symbols: 1, diagnostics: 1, scalarElements: 10, pathBytes: 8, symbolBytes: 8}
	for _, number := range []protowire.Number{1, 7} {
		value := append(wireBytes(number, []byte{0, 0}), wireVarint(number, 0)...)
		value = append(value, wireBytes(number, []byte{0, 0})...)
		if validWireLimits(wireBytes(2, wireBytes(2, value)), limits) {
			t.Fatalf("field %d accepted five elements across duplicate encodings", number)
		}
	}
}

func TestWirePreflightAcceptsExactScalarLimits(t *testing.T) {
	limits := wireLimits{documents: 1, occurrences: 1, relationships: 1, symbols: 1, diagnostics: 1, scalarElements: 12, pathBytes: 8, symbolBytes: 8}
	tool := wireBytes(1, wireBytes(2, wireBytes(3, nil)))
	symbol := append(wireBytes(3, nil), wireBytes(4, nil)...)
	occurrence := append(wireBytes(1, []byte{0, 0, 0, 0}), wireBytes(4, nil)...)
	occurrence = append(occurrence, wireBytes(7, []byte{0, 0, 0, 0})...)
	occurrence = append(occurrence, wireBytes(6, wireBytes(5, []byte{0}))...)
	document := append(wireBytes(2, occurrence), wireBytes(3, symbol)...)
	if !validWireLimits(append(tool, wireBytes(2, document)...), limits) {
		t.Fatal("validWireLimits() = false at exact scalar and range limits")
	}
}

func wireVarint(number protowire.Number, value uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(nil, number, protowire.VarintType), value)
}

func wireBytes(number protowire.Number, value []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, number, protowire.BytesType), value)
}

func marshalIndex(t *testing.T, index *scip.Index) []byte {
	t.Helper()
	data, err := proto.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// scip-go (the reference Go indexer) still emits documents without a
// position encoding; its offsets are UTF-8 code units, so unspecified is
// normalised to UTF-8 instead of rejecting every Go index.
func TestParseNormalizesUnspecifiedPositionEncodingToUTF8(t *testing.T) {
	index := &scip.Index{
		Metadata: &scip.Metadata{ProjectRoot: "file:///workspace", ToolInfo: &scip.ToolInfo{Name: "scip-go", Version: "0.2.7"}},
		Documents: []*scip.Document{{
			RelativePath:     "main.go",
			PositionEncoding: scip.PositionEncoding_UnspecifiedPositionEncoding,
			Occurrences:      []*scip.Occurrence{{Range: []int32{2, 4, 9}, Symbol: "scip-go gomod example.com/a v1.0.0 A#", SymbolRoles: int32(scip.SymbolRole_Definition)}},
		}},
	}
	upload, err := Parse(marshalIndex(t, index))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(upload.Occurrences) != 1 || upload.Occurrences[0].PositionEncoding != int32(scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart) {
		t.Fatalf("occurrences = %#v, want one UTF-8 occurrence", upload.Occurrences)
	}
}

// scip-go emits a synthetic document for each generated test main under the
// Go build cache, i.e. outside the checkout. Such documents cannot be served
// from the repository, so they are dropped instead of failing the upload.
func TestParseSkipsDocumentsOutsideProjectRoot(t *testing.T) {
	inside := &scip.Document{RelativePath: "pkg/a.go", PositionEncoding: scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart,
		Occurrences: []*scip.Occurrence{{Range: []int32{0, 0, 1}, Symbol: "scip-go gomod example.com/a v1.0.0 A#"}}}
	for _, outside := range []string{"..", "../a.go", "../../Library/Caches/go-build/ab/abc-d"} {
		index := &scip.Index{
			Metadata:  &scip.Metadata{ProjectRoot: "file:///workspace", ToolInfo: &scip.ToolInfo{Name: "scip-go", Version: "1"}},
			Documents: []*scip.Document{inside, {RelativePath: outside, PositionEncoding: scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart, Occurrences: []*scip.Occurrence{{Range: []int32{0, 0, 1}, Symbol: "scip-go gomod example.com/a v1.0.0 B#"}}}},
		}
		upload, err := Parse(marshalIndex(t, index))
		if err != nil {
			t.Fatalf("%s: Parse() error = %v", outside, err)
		}
		if len(upload.Occurrences) != 1 || upload.Occurrences[0].Path != "pkg/a.go" {
			t.Fatalf("%s: occurrences = %#v, want only the in-project document", outside, upload.Occurrences)
		}
	}
}
