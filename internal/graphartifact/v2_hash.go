package graphartifact

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// IdentityV2 scopes original identity and occurrence by public repository and
// producer. A repeated qualified name or source ID never collapses declarations.
func IdentityV2(p *graphv2.Producer, repository, sourceID, occurrence string) (string, error) {
	l, _ := normalizedV2Limits(Limits{})
	if !validProducer(p, l) || !validIdentifier(repository, l) || !validIdentifier(sourceID, l) || !validIdentifier(occurrence, l) {
		return "", ErrInvalidArtifact
	}
	h := sha256.New()
	for _, s := range []string{"graphnest-identity-v2", repository, p.Name, p.Version, p.Configuration, sourceID, occurrence} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(s)))
		h.Write(size[:])
		h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func SemanticHashV2(a *graphv2.Artifact, limits Limits) ([]byte, error) {
	if err := ValidateV2(a, limits); err != nil {
		return nil, err
	}
	return semanticHashV2(a)
}

func semanticHashV2(a *graphv2.Artifact) ([]byte, error) {
	c := proto.Clone(a).(*graphv2.Artifact)
	c.ContentHash = nil
	c.ImportedAt = 0
	for _, n := range c.Nodes {
		n.UpdatedAt = nil
	}
	for _, e := range c.Edges {
		e.SourceId = ""
	}
	for _, f := range c.Files {
		f.ModifiedAt = nil
		f.IndexedAt = nil
	}
	for _, r := range c.Unresolved {
		r.SourceId = ""
	}
	for _, m := range c.Metadata {
		m.UpdatedAt = nil
	}
	slices.SortFunc(c.Metadata, func(a, b *graphv2.MetadataEntry) int { return strings.Compare(a.Key, b.Key) })
	slices.SortFunc(c.Nodes, func(a, b *graphv2.Node) int { return strings.Compare(a.Occurrence, b.Occurrence) })
	slices.SortFunc(c.Edges, func(a, b *graphv2.Edge) int { return strings.Compare(a.Occurrence, b.Occurrence) })
	slices.SortFunc(c.Files, func(a, b *graphv2.File) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(c.Unresolved, func(a, b *graphv2.UnresolvedReference) int { return strings.Compare(a.Occurrence, b.Occurrence) })
	slices.SortFunc(c.Diagnostics, func(a, b *graphv2.Diagnostic) int { return strings.Compare(a.Occurrence, b.Occurrence) })
	if err := canonicalExtensions(c.ProtoReflect()); err != nil {
		return nil, err
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(c)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(append([]byte("graphnest-semantic-v2\x00"), data...))
	return h[:], nil
}

func canonicalExtensions(m protoreflect.Message) error {
	if e, ok := m.Interface().(*graphv2.Extension); ok {
		data, err := canonicalJSON(e.Json)
		if err != nil {
			return err
		}
		e.Json = data
		return nil
	}
	var err error
	m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if f.Kind() != protoreflect.MessageKind {
			return true
		}
		if f.IsList() {
			list := v.List()
			if f.Message().FullName() == "graphnest.graph.v2.Extension" {
				items := make([]*graphv2.Extension, list.Len())
				for i := range items {
					items[i] = list.Get(i).Message().Interface().(*graphv2.Extension)
				}
				slices.SortFunc(items, func(a, b *graphv2.Extension) int { return strings.Compare(a.Namespace, b.Namespace) })
				for i, item := range items {
					list.Set(i, protoreflect.ValueOfMessage(item.ProtoReflect()))
				}
			}
			for i := 0; i < list.Len(); i++ {
				if err = canonicalExtensions(list.Get(i).Message()); err != nil {
					return false
				}
			}
		} else {
			err = canonicalExtensions(v.Message())
		}
		return err == nil
	})
	return err
}

func validExtensions(extensions []*graphv2.Extension) bool {
	seen := make(map[string]bool, len(extensions))
	for _, e := range extensions {
		if e == nil || seen[e.Namespace] || !strings.Contains(e.Namespace, ".") || strings.HasPrefix(e.Namespace, ".") || strings.HasSuffix(e.Namespace, ".") {
			return false
		}
		for _, r := range e.Namespace {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
				return false
			}
		}
		seen[e.Namespace] = true
		if _, err := canonicalJSON(e.Json); err != nil {
			return false
		}
	}
	return true
}

// Decode tokens with a depth/token budget, rejecting duplicate keys before
// building a JSON tree. UseNumber preserves integers above float64 precision.
func canonicalJSON(data []byte) ([]byte, error) {
	if !utf8.Valid(data) {
		return nil, ErrInvalidArtifact
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	budget := 32768
	v, err := jsonValue(d, 0, &budget)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrInvalidArtifact
	}
	return json.Marshal(v)
}

func jsonValue(d *json.Decoder, depth int, budget *int) (any, error) {
	*budget--
	if depth > 32 || *budget < 0 {
		return nil, ErrInvalidArtifact
	}
	t, err := d.Token()
	if err != nil {
		return nil, ErrInvalidArtifact
	}
	switch v := t.(type) {
	case json.Number:
		return canonicalNumber(string(v))
	case json.Delim:
		switch v {
		case '{':
			m := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, ErrInvalidArtifact
				}
				s, ok := key.(string)
				if !ok {
					return nil, ErrInvalidArtifact
				}
				if _, exists := m[s]; exists {
					return nil, ErrInvalidArtifact
				}
				item, err := jsonValue(d, depth+1, budget)
				if err != nil {
					return nil, err
				}
				m[s] = item
			}
			if end, err := d.Token(); err != nil || end != json.Delim('}') {
				return nil, ErrInvalidArtifact
			}
			return m, nil
		case '[':
			items := []any{}
			for d.More() {
				item, err := jsonValue(d, depth+1, budget)
				if err != nil {
					return nil, err
				}
				items = append(items, item)
			}
			if end, err := d.Token(); err != nil || end != json.Delim(']') {
				return nil, ErrInvalidArtifact
			}
			return items, nil
		}
		return nil, ErrInvalidArtifact
	default:
		return v, nil
	}
}

// Exact decimal normalization without float rounding or exponent-sized buffers.
func canonicalNumber(s string) (json.Number, error) {
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign = "-"
		s = s[1:]
	}
	exponent := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		var err error
		exponent, err = strconv.Atoi(s[i+1:])
		if err != nil || exponent < -1_000_000 || exponent > 1_000_000 {
			return "", ErrInvalidArtifact
		}
		s = s[:i]
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		exponent -= len(s) - i - 1
		s = s[:i] + s[i+1:]
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0", nil
	}
	trimmed := strings.TrimRight(s, "0")
	exponent += len(s) - len(trimmed)
	s = trimmed
	if exponent < -1_000_000 || exponent > 1_000_000 {
		return "", ErrInvalidArtifact
	}
	if exponent != 0 {
		s += "e" + strconv.Itoa(exponent)
	}
	return json.Number(sign + s), nil
}
