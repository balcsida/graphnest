package graphartifact

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var ErrUnsupportedVersion = errors.New("unsupported graph artifact version")

// ParseV2 is deliberately separate from the v1 storage/publication reader.
// No protobuf objects or extension JSON trees are allocated before wire bounds.
func ParseV2(data []byte, limits Limits) (*graphv2.Artifact, error) {
	limits, ok := normalizedV2Limits(limits)
	if !ok || len(data) > limits.MaxArtifactBytes {
		return nil, ErrInvalidArtifact
	}
	b := v2Budget{limits: limits}
	if !b.wire(data, (&graphv2.Artifact{}).ProtoReflect().Descriptor()) {
		return nil, ErrInvalidArtifact
	}
	a := new(graphv2.Artifact)
	if err := (proto.UnmarshalOptions{RecursionLimit: 16}).Unmarshal(data, a); err != nil {
		return nil, ErrInvalidArtifact
	}
	if err := ValidateV2(a, limits); err != nil {
		return nil, err
	}
	return a, nil
}

func MarshalV2(a *graphv2.Artifact, limits Limits) ([]byte, error) {
	if err := ValidateV2(a, limits); err != nil {
		return nil, err
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(a)
}

func normalizedV2Limits(l Limits) (Limits, bool) {
	var ok bool
	if l, ok = normalizedLimits(l); !ok {
		return l, false
	}
	for _, bound := range []struct {
		value    *int
		def, max int
	}{
		{&l.MaxArtifactBytes, 128 << 20, 256 << 20},
		{&l.MaxFiles, 100_000, HardMaxNodes},
		{&l.MaxUnresolved, DefaultMaxEdges, HardMaxEdges},
		{&l.MaxDiagnostics, 100_000, HardMaxNodes},
		{&l.MaxMetadataBytes, 4 << 20, 16 << 20},
		{&l.MaxExtensionBytes, 64 << 10, 1 << 20},
		{&l.MaxCollectionItems, 1024, 4096},
	} {
		if *bound.value == 0 {
			*bound.value = bound.def
		}
		if *bound.value < 1 || *bound.value > bound.max {
			return l, false
		}
	}
	return l, true
}

type v2Budget struct {
	limits         Limits
	metadata, size int
}

func (b *v2Budget) countLimit(d protoreflect.MessageDescriptor, f protoreflect.FieldDescriptor) int {
	if d.FullName() == "graphnest.graph.v2.Artifact" {
		switch f.Number() {
		case 6:
			return b.limits.MaxNodes
		case 7:
			return b.limits.MaxEdges
		case 8:
			return b.limits.MaxFiles
		case 9:
			return b.limits.MaxUnresolved
		case 10:
			return b.limits.MaxDiagnostics
		}
	}
	return b.limits.MaxCollectionItems
}

func (b *v2Budget) byteLimit(f protoreflect.FieldDescriptor) int {
	switch f.Name() {
	case "json":
		return b.limits.MaxExtensionBytes
	case "documentation", "message":
		return 256 << 10
	case "path":
		return b.limits.MaxPathBytes
	case "content_hash":
		return b.limits.MaxIdentifierBytes
	}
	return b.limits.MaxIdentifierBytes
}

// The v2 schema has bounded depth and no maps/packed scalar lists. Reject unknown
// fields and duplicate singulars: accepting either would make semantic hashes
// ambiguous across readers. Future fields require a new supported contract.
func (b *v2Budget) wire(data []byte, d protoreflect.MessageDescriptor) bool {
	var counts [32]int
	for len(data) > 0 {
		n, t, k := protowire.ConsumeTag(data)
		if k < 0 || n < 1 || n >= 32 {
			return false
		}
		data = data[k:]
		f := d.Fields().ByNumber(n)
		if f == nil {
			return false
		}
		counts[n]++
		if (!f.IsList() && counts[n] > 1) || counts[n] > b.countLimit(d, f) {
			return false
		}
		want := protowire.VarintType
		switch f.Kind() {
		case protoreflect.MessageKind, protoreflect.StringKind, protoreflect.BytesKind:
			want = protowire.BytesType
		case protoreflect.DoubleKind:
			want = protowire.Fixed64Type
		}
		if t != want {
			return false
		}
		// Match message's aggregate charge before descending or allocating.
		// Empty nested messages still consume a model/list-element budget.
		b.size += 16
		if b.size > b.limits.MaxArtifactBytes {
			return false
		}
		if t == protowire.BytesType {
			v, size := protowire.ConsumeBytes(data)
			if size < 0 {
				return false
			}
			if f.Kind() == protoreflect.MessageKind {
				if !b.wire(v, f.Message()) {
					return false
				}
			} else {
				b.size += len(v)
				if len(v) > b.byteLimit(f) || b.size > b.limits.MaxArtifactBytes {
					return false
				}
				if f.Name() == "json" || d.FullName() == "graphnest.graph.v2.MetadataEntry" {
					b.metadata += len(v)
					if b.metadata > b.limits.MaxMetadataBytes {
						return false
					}
				}
			}
			data = data[size:]
		} else {
			if t == protowire.VarintType {
				v, size := protowire.ConsumeVarint(data)
				if size < 0 {
					return false
				}
				switch f.Kind() {
				case protoreflect.EnumKind:
					if v < 1 || v > 13 {
						return false
					}
				case protoreflect.Int32Kind:
					if v > math.MaxInt32 {
						return false
					}
				case protoreflect.Uint32Kind:
					if v > math.MaxUint32 {
						return false
					}
				case protoreflect.BoolKind:
					if v > 1 {
						return false
					}
				}
			}
			size := protowire.ConsumeFieldValue(n, t, data)
			if size < 0 {
				return false
			}
			data = data[size:]
		}
	}
	return true
}

func (b *v2Budget) message(m protoreflect.Message) bool {
	if !m.IsValid() || len(m.GetUnknown()) != 0 {
		return false
	}
	ok := true
	m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		check := func(v protoreflect.Value) bool {
			b.size += 16 // conservative upper bound for each tag/length/scalar
			switch f.Kind() {
			case protoreflect.MessageKind:
				if !b.message(v.Message()) {
					return false
				}
			case protoreflect.StringKind:
				if !utf8.ValidString(v.String()) || len(v.String()) > b.byteLimit(f) {
					return false
				}
				b.size += len(v.String())
				if m.Descriptor().FullName() == "graphnest.graph.v2.MetadataEntry" {
					b.metadata += len(v.String())
					if b.metadata > b.limits.MaxMetadataBytes {
						return false
					}
				}
			case protoreflect.BytesKind:
				if len(v.Bytes()) > b.byteLimit(f) {
					return false
				}
				b.size += len(v.Bytes())
				if f.Name() == "json" {
					b.metadata += len(v.Bytes())
					if b.metadata > b.limits.MaxMetadataBytes {
						return false
					}
				}
			}
			return b.size <= b.limits.MaxArtifactBytes
		}
		if f.IsList() {
			list := v.List()
			if list.Len() > b.countLimit(m.Descriptor(), f) {
				ok = false
				return false
			}
			for i := 0; i < list.Len(); i++ {
				if !check(list.Get(i)) {
					ok = false
					return false
				}
			}
		} else {
			ok = check(v)
		}
		return ok
	})
	return ok
}

func ValidateV2(a *graphv2.Artifact, limits Limits) error {
	limits, ok := normalizedV2Limits(limits)
	if !ok || a == nil {
		return ErrInvalidArtifact
	}
	if a.SchemaVersion != 2 {
		return errors.Join(ErrInvalidArtifact, ErrUnsupportedVersion)
	}
	b := v2Budget{limits: limits}
	if !b.message(a.ProtoReflect()) || !validIdentifier(a.Repository, limits) || !validCommit(a.Commit) || !validProducer(a.Producer, limits) || (len(a.ContentHash) != 0 && len(a.ContentHash) != 32) {
		return ErrInvalidArtifact
	}
	nodes := make(map[string]struct{}, len(a.Nodes))
	for _, n := range a.Nodes {
		if n == nil || !uniqueOccurrence(nodes, n.Occurrence, limits) || !validIdentifier(n.SourceId, limits) || !validV2NodeKind(n.Kind) || !validV2Path(n.Path, limits) || !validLocation(n.Location, limits) || !matchingPath(n.Path, n.Location) || !validExtensions(n.Extensions) {
			return ErrInvalidArtifact
		}
	}
	edges := make(map[string]struct{}, len(a.Edges))
	for _, e := range a.Edges {
		if e == nil {
			return ErrInvalidArtifact
		}
		_, kindOK := RelationshipFromWire(e.Kind)
		_, sourceOK := nodes[e.Source]
		_, targetOK := nodes[e.Target]
		if !kindOK || !sourceOK || !targetOK || !uniqueOccurrence(edges, e.Occurrence, limits) || !validLocation(e.Location, limits) || !validExtensions(e.Extensions) || (e.Confidence != nil && (math.IsNaN(*e.Confidence) || *e.Confidence < 0 || *e.Confidence > 1)) {
			return ErrInvalidArtifact
		}
	}
	files := make(map[string]struct{}, len(a.Files))
	for _, f := range a.Files {
		if f == nil || !validPath(f.Path, limits.MaxPathBytes) || strings.ContainsRune(f.Path, 0) || len(f.ContentHash) != 64 || strings.Trim(f.ContentHash, "0123456789abcdef") != "" || !uniqueOccurrence(files, f.Path, limits) || f.Size < 0 || (f.NodeCount != nil && *f.NodeCount < 0) || !validExtensions(f.Extensions) || (f.Errors != nil && !validExtensions([]*graphv2.Extension{f.Errors})) {
			return ErrInvalidArtifact
		}
	}
	refs := make(map[string]struct{}, len(a.Unresolved))
	for _, r := range a.Unresolved {
		if r == nil {
			return ErrInvalidArtifact
		}
		_, kindOK := ParseRelationship(r.Kind)
		_, sourceOK := nodes[r.Source]
		if (!kindOK && r.Kind != "function_ref") || !sourceOK || !uniqueOccurrence(refs, r.Occurrence, limits) || !validIdentifier(r.Name, limits) || !validV2Path(r.Path, limits) || !validLocation(r.Location, limits) || !matchingPath(r.Path, r.Location) || !validExtensions(r.Extensions) {
			return ErrInvalidArtifact
		}
	}
	diagnostics := make(map[string]struct{}, len(a.Diagnostics))
	for _, d := range a.Diagnostics {
		if d == nil || !uniqueOccurrence(diagnostics, d.Occurrence, limits) || d.Message == "" || (d.Severity != "error" && d.Severity != "warning" && d.Severity != "info") || !validLocation(d.Location, limits) || !validExtensions(d.Extensions) {
			return ErrInvalidArtifact
		}
	}
	if !validExtensions(a.Extensions) {
		return ErrInvalidArtifact
	}
	metadata := make(map[string]struct{}, len(a.Metadata))
	for _, m := range a.Metadata {
		if m == nil || !uniqueOccurrence(metadata, m.Key, limits) {
			return ErrInvalidArtifact
		}
	}
	if len(a.ContentHash) > 0 {
		hash, err := semanticHashV2(a)
		if err != nil || !bytes.Equal(hash, a.ContentHash) {
			return fmt.Errorf("%w: semantic hash mismatch", ErrInvalidArtifact)
		}
	}
	return nil
}

func validProducer(p *graphv2.Producer, l Limits) bool {
	return p != nil && validIdentifier(p.Name, l) && validIdentifier(p.Version, l) && validOptionalIdentifier(p.Configuration, l)
}
func uniqueOccurrence(seen map[string]struct{}, s string, l Limits) bool {
	if !validIdentifier(s, l) {
		return false
	}
	if _, ok := seen[s]; ok {
		return false
	}
	seen[s] = struct{}{}
	return true
}
func validV2Path(p *string, l Limits) bool {
	return p == nil || validOptionalPath(*p, l.MaxPathBytes) && !strings.ContainsRune(*p, 0)
}
func matchingPath(p *string, l *graphv2.Location) bool {
	return p == nil || l == nil || l.Path == nil || *p == *l.Path
}

func validLocation(l *graphv2.Location, limits Limits) bool {
	if l == nil {
		return true
	}
	if !validV2Path(l.Path, limits) || (l.Path == nil && l.Start == nil) || (l.End != nil && l.Start == nil) {
		return false
	}
	for _, p := range []*graphv2.Position{l.Start, l.End} {
		if p != nil && (p.Line == nil && p.Character == nil || p.Line != nil && *p.Line < 0 || p.Character != nil && *p.Character < 0) {
			return false
		}
	}
	return l.Start == nil || l.End == nil || l.Start.Line == nil || l.End.Line == nil || *l.End.Line > *l.Start.Line || *l.End.Line == *l.Start.Line && (l.Start.Character == nil || l.End.Character == nil || *l.End.Character >= *l.Start.Character)
}

// SourceOffset resolves a zero-based UTF-16 position against exact UTF-8 source.
// It rejects columns inside a surrogate pair, beyond a line, and line-only data.
// CR is retained before LF, matching both CodeGraph producer implementations.
func SourceOffset(source string, p *graphv2.Position) (int, error) {
	if p == nil || p.Line == nil || *p.Line < 0 || p.Character == nil || *p.Character < 0 || !utf8.ValidString(source) {
		return 0, ErrInvalidArtifact
	}
	start := 0
	for line := int32(0); line < *p.Line; line++ {
		i := strings.IndexByte(source[start:], '\n')
		if i < 0 {
			return 0, ErrInvalidArtifact
		}
		start += i + 1
	}
	units := int32(0)
	for offset, r := range source[start:] {
		if units == *p.Character {
			return start + offset, nil
		}
		if r == '\n' {
			return 0, ErrInvalidArtifact
		}
		units++
		if r > 0xffff {
			units++
		}
		if units > *p.Character {
			return 0, ErrInvalidArtifact
		}
	}
	if units == *p.Character {
		return len(source), nil
	}
	return 0, ErrInvalidArtifact
}
