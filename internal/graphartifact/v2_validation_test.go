package graphartifact

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"strings"
	"testing"
	"testing/quick"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func v2WithEvidence() *graphv2.Artifact {
	a := minimalV2()
	a.Nodes[0].Location = &graphv2.Location{Path: proto.String("a.ts"), Start: &graphv2.Position{Line: proto.Int32(0), Character: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(1), Character: proto.Int32(2)}}
	a.Edges = []*graphv2.Edge{{Occurrence: "call:1", Source: "declaration:1", Target: "declaration:2", Kind: graphv2.EdgeKind_EDGE_KIND_CALLS}}
	a.Files = []*graphv2.File{{Path: "a.ts", ContentHash: strings.Repeat("b", 64), Generated: proto.Bool(false)}}
	a.Files[0].Errors = &graphv2.Extension{Namespace: "codegraph.extraction-errors", Json: []byte(`[{"message":"partial","severity":"warning","line":2,"code":"parse","native":{"detail":true}}]`)}
	a.Nodes[0].Documentation = proto.String("")
	a.Nodes[0].Decorators = &graphv2.StringList{}
	a.Nodes[0].TypeParameters = &graphv2.StringList{Values: []string{"T", "U"}}
	a.Unresolved = []*graphv2.UnresolvedReference{{Occurrence: "ref:1", Source: "declaration:1", Name: "unknown", Kind: "function_ref"}}
	a.Diagnostics = []*graphv2.Diagnostic{{Occurrence: "error:1", Message: "incomplete source", Severity: "warning", Code: proto.String("parse"), Location: &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(1)}}}}
	a.Extensions = []*graphv2.Extension{{Namespace: "codegraph.fixture", Json: []byte(`{"large":9007199254740993,"values":[true,null,"é"]}`)}}
	return a
}

func TestV2RejectInvalidEvidence(t *testing.T) {
	for name, change := range map[string]func(*graphv2.Artifact){
		"missing producer":       func(a *graphv2.Artifact) { a.Producer = nil },
		"unknown kind":           func(a *graphv2.Artifact) { a.Nodes[0].Kind = "future-kind" },
		"wrapped enum":           func(a *graphv2.Artifact) { a.Edges[0].Kind = 257 },
		"negative enum":          func(a *graphv2.Artifact) { a.Edges[0].Kind = -255 },
		"duplicate declaration":  func(a *graphv2.Artifact) { a.Nodes[1].Occurrence = a.Nodes[0].Occurrence },
		"duplicate edge":         func(a *graphv2.Artifact) { a.Edges = append(a.Edges, proto.Clone(a.Edges[0]).(*graphv2.Edge)) },
		"missing endpoint":       func(a *graphv2.Artifact) { a.Edges[0].Target = "absent" },
		"negative location":      func(a *graphv2.Artifact) { a.Nodes[0].Location.Start.Line = proto.Int32(-1) },
		"reversed range":         func(a *graphv2.Artifact) { a.Nodes[0].Location.Start.Line = proto.Int32(2) },
		"empty position":         func(a *graphv2.Artifact) { a.Nodes[0].Location.Start = &graphv2.Position{} },
		"end without start":      func(a *graphv2.Artifact) { a.Nodes[0].Location.Start = nil },
		"inconsistent path":      func(a *graphv2.Artifact) { a.Nodes[0].Path = proto.String("b.ts") },
		"unclean path":           func(a *graphv2.Artifact) { a.Files[0].Path = "../a.ts" },
		"nul path":               func(a *graphv2.Artifact) { a.Nodes[0].Path = proto.String("a\x00.ts") },
		"nan confidence":         func(a *graphv2.Artifact) { a.Edges[0].Confidence = proto.Float64(math.NaN()) },
		"infinite confidence":    func(a *graphv2.Artifact) { a.Edges[0].Confidence = proto.Float64(math.Inf(1)) },
		"confidence exceeds one": func(a *graphv2.Artifact) { a.Edges[0].Confidence = proto.Float64(1.001) },
		"negative size":          func(a *graphv2.Artifact) { a.Files[0].Size = -1 },
		"invalid file hash":      func(a *graphv2.Artifact) { a.Files[0].ContentHash = strings.Repeat("z", 64) },
		"missing file hash":      func(a *graphv2.Artifact) { a.Files[0].ContentHash = "" },
		"nul file path":          func(a *graphv2.Artifact) { a.Files[0].Path = "a\x00.ts" },
		"duplicate file":         func(a *graphv2.Artifact) { a.Files = append(a.Files, proto.Clone(a.Files[0]).(*graphv2.File)) },
		"unresolved endpoint":    func(a *graphv2.Artifact) { a.Unresolved[0].Source = "absent" },
		"unresolved kind":        func(a *graphv2.Artifact) { a.Unresolved[0].Kind = "unknown" },
		"duplicate unresolved": func(a *graphv2.Artifact) {
			a.Unresolved = append(a.Unresolved, proto.Clone(a.Unresolved[0]).(*graphv2.UnresolvedReference))
		},
		"diagnostic severity": func(a *graphv2.Artifact) { a.Diagnostics[0].Severity = "fatal" },
		"duplicate diagnostic": func(a *graphv2.Artifact) {
			a.Diagnostics = append(a.Diagnostics, proto.Clone(a.Diagnostics[0]).(*graphv2.Diagnostic))
		},
		"duplicate extension": func(a *graphv2.Artifact) {
			a.Extensions = append(a.Extensions, proto.Clone(a.Extensions[0]).(*graphv2.Extension))
		},
		"unscoped extension": func(a *graphv2.Artifact) { a.Extensions[0].Namespace = "metadata" },
		"invalid json":       func(a *graphv2.Artifact) { a.Extensions[0].Json = []byte(`{"broken"`) },
		"duplicate json key": func(a *graphv2.Artifact) { a.Extensions[0].Json = []byte(`{"x":1,"x":2}`) },
		"deep json": func(a *graphv2.Artifact) {
			a.Extensions[0].Json = []byte(strings.Repeat("[", 40) + "0" + strings.Repeat("]", 40))
		},
		"extreme json exponent": func(a *graphv2.Artifact) { a.Extensions[0].Json = []byte(`1e999999999999999999`) },
		"wrong hash":            func(a *graphv2.Artifact) { a.ContentHash = bytes.Repeat([]byte{1}, 32) },
		"unknown field": func(a *graphv2.Artifact) {
			a.ProtoReflect().SetUnknown(protowire.AppendTag(nil, 30, protowire.VarintType))
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := v2WithEvidence()
			change(a)
			if err := ValidateV2(a, Limits{}); !errors.Is(err, ErrInvalidArtifact) {
				t.Fatalf("accepted invalid model: %v", err)
			}
			data, err := proto.Marshal(a)
			if err == nil {
				if _, err := ParseV2(data, Limits{}); !errors.Is(err, ErrInvalidArtifact) {
					t.Fatalf("accepted invalid wire: %v", err)
				}
			}
		})
	}
}

func TestV2PreservesPartialAndMissingEvidence(t *testing.T) {
	for _, p := range []*graphv2.Position{nil, {Line: proto.Int32(0)}, {Character: proto.Int32(0)}, {Line: proto.Int32(0), Character: proto.Int32(0)}} {
		a := v2WithEvidence()
		a.Edges[0].Location = nil
		if p != nil {
			a.Edges[0].Location = &graphv2.Location{Start: p}
		}
		for _, confidence := range []*float64{nil, proto.Float64(0), proto.Float64(1)} {
			a.Edges[0].Confidence = confidence
			data, err := MarshalV2(a, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := ParseV2(data, Limits{})
			if err != nil || !proto.Equal(a, got) {
				t.Fatal("partial evidence", err)
			}
		}
	}
	a := v2WithEvidence()
	a.Nodes[1].SourceId = a.Nodes[0].SourceId
	a.Nodes[1].Name = a.Nodes[0].Name
	a.Edges = append(a.Edges, proto.Clone(a.Edges[0]).(*graphv2.Edge))
	a.Edges[1].Occurrence = "second-call-with-same-endpoints"
	if err := ValidateV2(a, Limits{}); err != nil {
		t.Fatal("distinct declarations/calls collapsed", err)
	}
}

func TestV2PredecodeBounds(t *testing.T) {
	for field, limits := range map[protowire.Number]Limits{6: {MaxNodes: 1}, 7: {MaxEdges: 1}, 8: {MaxFiles: 1}, 9: {MaxUnresolved: 1}, 10: {MaxDiagnostics: 1}, 11: {MaxCollectionItems: 1}, 13: {MaxCollectionItems: 1}} {
		data := bytes.Repeat(protowire.AppendBytes(protowire.AppendTag(nil, field, protowire.BytesType), nil), 100_000)
		allocations := testing.AllocsPerRun(1, func() {
			if _, err := ParseV2(data, limits); !errors.Is(err, ErrInvalidArtifact) {
				t.Fatal("accepted excessive collection", field, err)
			}
		})
		if allocations > 10 {
			t.Fatalf("field %d allocated before rejecting: %.0f", field, allocations)
		}
	}
	for name, limits := range map[string]Limits{"bytes": {MaxArtifactBytes: 1}, "metadata": {MaxMetadataBytes: 1}, "extension": {MaxExtensionBytes: 1}, "identifier": {MaxIdentifierBytes: 1}, "negative": {MaxFiles: -1}, "above hard cap": {MaxArtifactBytes: 257 << 20}} {
		t.Run(name, func(t *testing.T) {
			a := v2WithEvidence()
			data, _ := proto.Marshal(a)
			if _, err := ParseV2(data, limits); !errors.Is(err, ErrInvalidArtifact) {
				t.Fatal(err)
			}
			if err := ValidateV2(a, limits); !errors.Is(err, ErrInvalidArtifact) {
				t.Fatal(err)
			}
		})
	}
	a := v2WithEvidence()
	a.Extensions = nil
	a.Metadata = []*graphv2.MetadataEntry{{Key: "large", Value: strings.Repeat("x", 50)}}
	data, _ := proto.Marshal(a)
	if _, err := ParseV2(data, Limits{MaxMetadataBytes: 20}); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatal("unbounded metadata entries")
	}
	if err := ValidateV2(a, Limits{MaxMetadataBytes: 20}); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatal("unbounded metadata model")
	}
	// Reject enum/position/schema values wider than their protobuf scalar, too.
	for _, test := range []struct {
		field      protowire.Number
		value      uint64
		descriptor proto.Message
	}{
		{5, 1<<32 | 1, &graphv2.Edge{}}, {1, 1 << 32, &graphv2.Position{}}, {1, 1<<32 | 2, &graphv2.Artifact{}},
	} {
		b := v2Budget{limits: func() Limits { l, _ := normalizedV2Limits(Limits{}); return l }()}
		data := protowire.AppendVarint(protowire.AppendTag(nil, test.field, protowire.VarintType), test.value)
		if b.wire(data, test.descriptor.ProtoReflect().Descriptor()) {
			t.Fatal("accepted narrowing wire value")
		}
	}
}

func TestV2PredecodeModelBudget(t *testing.T) {
	t.Run("matching model cost", func(t *testing.T) {
		a := v2WithEvidence()
		data, err := proto.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		limits, _ := normalizedV2Limits(Limits{})
		wire, model := v2Budget{limits: limits}, v2Budget{limits: limits}
		if !wire.wire(data, a.ProtoReflect().Descriptor()) || !model.message(a.ProtoReflect()) || wire.size != model.size {
			t.Fatalf("wire/model accounting differs: %d != %d", wire.size, model.size)
		}
	})
	t.Run("empty extensions", func(t *testing.T) {
		limits, _ := normalizedV2Limits(Limits{MaxArtifactBytes: 16})
		node := &graphv2.Node{Extensions: []*graphv2.Extension{{}, {}}}
		data := []byte{0x9a, 0x01, 0x00, 0x9a, 0x01, 0x00}
		model := v2Budget{limits: limits}
		if model.message(node.ProtoReflect()) {
			t.Fatal("counterexample must exceed the model budget")
		}
		wire := v2Budget{limits: limits}
		if wire.wire(data, node.ProtoReflect().Descriptor()) {
			t.Fatalf("wire accepted %d encoded bytes despite model cost %d exceeding limit %d", len(data), model.size, limits.MaxArtifactBytes)
		}
	})
	t.Run("reject before allocation", func(t *testing.T) {
		a := minimalV2()
		for i := range 32 {
			e := &graphv2.Edge{Occurrence: string(rune('a' + i)), Source: "declaration:1", Target: "declaration:2", Kind: graphv2.EdgeKind_EDGE_KIND_CALLS}
			for range 128 {
				e.Extensions = append(e.Extensions, &graphv2.Extension{})
			}
			a.Edges = append(a.Edges, e)
		}
		data, err := proto.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		limits := Limits{MaxArtifactBytes: 16 << 10}
		if len(data) >= limits.MaxArtifactBytes {
			t.Fatal("counterexample must fit the encoded byte limit")
		}
		allocations := testing.AllocsPerRun(1, func() {
			if _, err := ParseV2(data, limits); !errors.Is(err, ErrInvalidArtifact) {
				t.Fatal("accepted amplified model", err)
			}
		})
		if allocations > 10 {
			t.Fatalf("ParseV2 allocated before aggregate rejection: %.0f allocations for %d encoded bytes", allocations, len(data))
		}
		t.Logf("rejected %d encoded bytes with %.0f allocations", len(data), allocations)
	})
}

func TestV2RelationshipWireNumbers(t *testing.T) {
	want := []string{"contains", "imports", "references", "calls", "extends", "implements", "exports", "type_of", "returns", "instantiates", "overrides", "decorates", "navigates"}
	for i, name := range want {
		r, ok := ParseRelationship(name)
		if !ok || int(r.Kind) != i+1 || r.Outgoing == "" || r.Incoming == "" {
			t.Fatal(name, r)
		}
		wire, ok := RelationshipFromWire(r.WireKind())
		if !ok || wire != r {
			t.Fatal("wire mismatch")
		}
		if graphv2.EdgeKind_name[int32(i+1)] != "EDGE_KIND_"+strings.ToUpper(name) {
			t.Fatal("schema mismatch")
		}
	}
	for _, v := range []graphv2.EdgeKind{0, 14, 257, -255} {
		if _, ok := RelationshipFromWire(v); ok {
			t.Fatal(v)
		}
	}
	if _, ok := ParseRelationship("CALLS"); ok {
		t.Fatal("undocumented alias")
	}
}

func TestV2HashOrderProperty(t *testing.T) {
	a := fixtureV2(t, fixtureRows(t))
	want, err := SemanticHashV2(a, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	property := func(seed uint64) bool {
		b := proto.Clone(a).(*graphv2.Artifact)
		r := rand.New(rand.NewSource(int64(seed)))
		r.Shuffle(len(b.Nodes), func(i, j int) { b.Nodes[i], b.Nodes[j] = b.Nodes[j], b.Nodes[i] })
		r.Shuffle(len(b.Edges), func(i, j int) { b.Edges[i], b.Edges[j] = b.Edges[j], b.Edges[i] })
		r.Shuffle(len(b.Files), func(i, j int) { b.Files[i], b.Files[j] = b.Files[j], b.Files[i] })
		r.Shuffle(len(b.Unresolved), func(i, j int) { b.Unresolved[i], b.Unresolved[j] = b.Unresolved[j], b.Unresolved[i] })
		r.Shuffle(len(b.Metadata), func(i, j int) { b.Metadata[i], b.Metadata[j] = b.Metadata[j], b.Metadata[i] })
		b.ImportedAt = int64(seed)
		for _, n := range b.Nodes {
			n.UpdatedAt = proto.Int64(int64(seed))
		}
		for _, f := range b.Files {
			f.ModifiedAt = proto.Int64(7)
			f.IndexedAt = proto.Int64(8)
		}
		for _, e := range b.Edges {
			e.SourceId = "different-SQL-row"
		}
		for _, ref := range b.Unresolved {
			ref.SourceId = "different-SQL-row"
		}
		for _, m := range b.Metadata {
			m.UpdatedAt = proto.Int64(9)
		}
		got, err := SemanticHashV2(b, Limits{})
		return err == nil && bytes.Equal(want, got)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 50, Rand: rand.New(rand.NewSource(42))}); err != nil {
		t.Fatal(err)
	}
	a.ContentHash = want
	data, err := MarshalV2(a, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseV2(data, Limits{}); err != nil {
		t.Fatal(err)
	}
}

func TestV2ExactJSONCanonicalization(t *testing.T) {
	for input, want := range map[string]string{`1.00`: `1`, `1e2`: `1e2`, `100`: `1e2`, `-0.0`: `0`, `9007199254740993`: `9007199254740993`, `{"z":0,"a":2.0}`: `{"a":2,"z":0}`} {
		got, err := canonicalJSON([]byte(input))
		if err != nil || string(got) != want {
			t.Fatalf("%s -> %s, %v", input, got, err)
		}
	}
	got, err := canonicalJSON([]byte(`9007199254740993`))
	if err != nil {
		t.Fatal(err)
	}
	var n json.Number
	if err = json.Unmarshal(got, &n); err != nil || n.String() != "9007199254740993" {
		t.Fatal("rounded integer")
	}
}

func TestV2SourceOffsets(t *testing.T) {
	source := "é😀x\r\ny\n"
	for _, test := range []struct {
		line, col int32
		offset    int
	}{{0, 0, 0}, {0, 1, 2}, {0, 3, 6}, {0, 4, 7}, {0, 5, 8}, {1, 0, 9}, {1, 1, 10}, {2, 0, 11}} {
		got, err := SourceOffset(source, &graphv2.Position{Line: proto.Int32(test.line), Character: proto.Int32(test.col)})
		if err != nil || got != test.offset {
			t.Fatalf("%v: %d, %v", test, got, err)
		}
	}
	for _, p := range []*graphv2.Position{nil, {Line: proto.Int32(0)}, {Character: proto.Int32(0)}, {Line: proto.Int32(0), Character: proto.Int32(2)}, {Line: proto.Int32(0), Character: proto.Int32(6)}, {Line: proto.Int32(3), Character: proto.Int32(0)}} {
		if _, err := SourceOffset(source, p); err == nil {
			t.Fatal("accepted invalid coordinate", p)
		}
	}
}

func FuzzV2Parse(f *testing.F) {
	data, _ := proto.Marshal(v2WithEvidence())
	f.Add(data)
	f.Add([]byte{})
	f.Add([]byte{8, 3})
	f.Fuzz(func(t *testing.T, data []byte) {
		l := Limits{MaxNodes: 20, MaxEdges: 40, MaxFiles: 20, MaxUnresolved: 40, MaxDiagnostics: 20, MaxArtifactBytes: 65536, MaxMetadataBytes: 4096, MaxExtensionBytes: 2048, MaxCollectionItems: 20}
		a, err := ParseV2(data, l)
		if err != nil {
			return
		}
		encoded, err := MarshalV2(a, l)
		if err != nil {
			t.Fatal(err)
		}
		b, err := ParseV2(encoded, l)
		if err != nil || !proto.Equal(a, b) {
			t.Fatal("unstable roundtrip", err)
		}
		h, err := SemanticHashV2(a, l)
		if err != nil {
			t.Fatal(err)
		}
		other, err := SemanticHashV2(b, l)
		if err != nil || !bytes.Equal(h, other) {
			t.Fatal("unstable semantic hash", err)
		}
	})
}

func FuzzV2Identity(f *testing.F) {
	f.Add("source", "occurrence", "repository")
	f.Fuzz(func(t *testing.T, source, occurrence, repository string) {
		p := &graphv2.Producer{Name: "codegraph", Version: "1.6.0"}
		id, err := IdentityV2(p, repository, source, occurrence)
		if err != nil {
			return
		}
		again, err := IdentityV2(p, repository, source, occurrence)
		if err != nil || id != again {
			t.Fatal("nondeterministic identity")
		}
		if len(occurrence) < DefaultMaxIdentifierBytes {
			other, err := IdentityV2(p, repository, source, occurrence+"x")
			if err != nil || id == other {
				t.Fatal("occurrence collapsed")
			}
		}
	})
}

func FuzzV2JSON(f *testing.F) {
	for _, s := range []string{`{"n":9007199254740993}`, `1.00`, `1000e1000000`, `[null,true,"é😀"]`, `{"x":1,"x":2}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			return
		}
		canonical, err := canonicalJSON(data)
		if err != nil {
			return
		}
		again, err := canonicalJSON(canonical)
		if err != nil || !bytes.Equal(canonical, again) {
			t.Fatalf("non-idempotent JSON: %q -> %q -> %q (%v)", data, canonical, again, err)
		}
	})
}
