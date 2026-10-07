package graphartifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/scipgraph"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// SCIPProducer names the v2 generations GraphNest derives from SCIP uploads.
// A generation from any other producer is never replaced by a SCIP upload.
const SCIPProducer = "scip"

// scipProducerVersion changes whenever the conversion rules change, so entity
// identities and semantic hashes of SCIP-derived generations change with them.
const scipProducerVersion = "1"

// ErrGraphTooLarge reports a SCIP index whose derived graph exceeds the v2
// generation limits (entity and edge counts or the artifact byte budget); the
// SCIP navigation data itself stays usable.
var ErrGraphTooLarge = errors.New("scip graph exceeds generation limits")

// maxDocumentationBytes mirrors the validator's per-field ceiling for
// documentation; longer producer text is clipped rather than rejected.
const maxDocumentationBytes = 256 << 10

// SCIPCapabilities lists the relationship kinds a SCIP-derived generation can
// contain. SCIP records references, not call expressions, so calls are absent.
func SCIPCapabilities() []string {
	return []string{"contains", "imports", "references", "implements", "type_of"}
}

// FromSCIPV2 derives a v2 generation from a parsed SCIP index for the public
// repository identity and its indexed commit. Each non-local symbol becomes an
// entity at its definition; each reference occurrence becomes a references
// edge from the innermost definition whose enclosing range holds it, or from
// the file when the producer gave no enclosing range. SCIP carries no file
// contents or hashes, so the artifact has no File facts. limits bounds the
// derived generation; zero values use the defaults.
func FromSCIPV2(repository, commit string, upload scipgraph.Upload, limits Limits) (*graphv2.Artifact, error) {
	limits, ok := normalizedV2Limits(limits)
	if !ok {
		return nil, ErrInvalidArtifact
	}
	c := &scipConverter{
		nodes: map[string]*graphv2.Node{}, edges: map[string]*graphv2.Edge{},
		languages: map[string]string{}, info: map[string]scipgraph.SymbolInformation{},
	}
	for _, document := range upload.Documents {
		c.languages[document.Path] = document.Language
		c.encodings = append(c.encodings, scip.PositionEncoding(document.PositionEncoding).String())
	}
	for _, symbol := range upload.Symbols {
		if _, ok := c.info[symbol.Symbol]; !ok {
			c.info[symbol.Symbol] = symbol
		}
	}
	for _, document := range upload.Documents {
		c.file(document.Path)
	}
	for _, occurrence := range upload.Occurrences {
		c.file(occurrence.Path)
		if occurrence.Local {
			continue
		}
		node := c.symbol(occurrence.Symbol, c.languages[occurrence.Path])
		if occurrence.Definition() && node.Path == nil {
			node.Path = proto.String(occurrence.Path)
			node.Location = location(occurrence.Path, occurrence.StartLine, occurrence.StartCharacter, occurrence.EndLine, occurrence.EndCharacter)
			node.Language = c.languages[occurrence.Path]
			c.edge(edgeID("contains", fileOccurrence(occurrence.Path), occurrence.Symbol), graphv2.EdgeKind_EDGE_KIND_CONTAINS, fileOccurrence(occurrence.Path), occurrence.Symbol, &graphv2.Location{Path: proto.String(occurrence.Path)}, "scip_definition")
		}
	}
	definitions := upload.Definitions()
	for _, occurrence := range upload.Occurrences {
		if occurrence.Local || occurrence.Definition() {
			continue
		}
		id := edgeID("ref", fmt.Sprintf("%s:%d:%d-%d:%d", occurrence.Path, occurrence.StartLine, occurrence.StartCharacter, occurrence.EndLine, occurrence.EndCharacter), occurrence.Symbol)
		where := location(occurrence.Path, occurrence.StartLine, occurrence.StartCharacter, occurrence.EndLine, occurrence.EndCharacter)
		if c.nodes[occurrence.Symbol].Kind == "module" || c.nodes[occurrence.Symbol].Kind == "namespace" {
			// Package qualifiers repeat on every use; one import per file is the fact.
			c.edge(edgeID("imports", fileOccurrence(occurrence.Path), occurrence.Symbol), graphv2.EdgeKind_EDGE_KIND_IMPORTS, fileOccurrence(occurrence.Path), occurrence.Symbol, &graphv2.Location{Path: proto.String(occurrence.Path)}, "scip_document")
			continue
		}
		if definition, ok := definitions.Enclosing(occurrence); ok {
			c.edge(id, graphv2.EdgeKind_EDGE_KIND_REFERENCES, definition.Symbol, occurrence.Symbol, where, "scip_enclosing_range")
		} else {
			c.edge(id, graphv2.EdgeKind_EDGE_KIND_REFERENCES, fileOccurrence(occurrence.Path), occurrence.Symbol, where, "scip_document")
		}
	}
	for _, relationship := range upload.Relationships {
		if scip.IsLocalSymbol(relationship.Source) || scip.IsLocalSymbol(relationship.Target) {
			continue
		}
		language := c.languages[relationship.Path]
		c.symbol(relationship.Source, language)
		c.symbol(relationship.Target, language)
		for _, kind := range []struct {
			enabled bool
			kind    graphv2.EdgeKind
			name    string
		}{{relationship.Implementation, graphv2.EdgeKind_EDGE_KIND_IMPLEMENTS, "implements"}, {relationship.TypeDefinition, graphv2.EdgeKind_EDGE_KIND_TYPE_OF, "type_of"}, {relationship.Reference, graphv2.EdgeKind_EDGE_KIND_REFERENCES, "references"}} {
			if kind.enabled {
				c.edge(edgeID(kind.name, relationship.Source, relationship.Target), kind.kind, relationship.Source, relationship.Target, &graphv2.Location{Path: proto.String(relationship.Path)}, "scip_relationship")
			}
		}
	}
	for occurrence, node := range c.nodes {
		if node.Kind == "file" || node.Path == nil {
			continue
		}
		if parent, ok := c.parent(occurrence); ok {
			c.edge(edgeID("contains", parent, occurrence), graphv2.EdgeKind_EDGE_KIND_CONTAINS, parent, occurrence, &graphv2.Location{Path: node.Path}, "scip_descriptor")
		}
	}
	if len(c.nodes) > limits.MaxNodes || len(c.edges) > limits.MaxEdges {
		return nil, ErrGraphTooLarge
	}
	evidence, err := json.Marshal(map[string]any{"position_encodings": distinct(c.encodings), "file_facts": "absent"})
	if err != nil {
		return nil, err
	}
	artifact := &graphv2.Artifact{
		SchemaVersion: 2, Repository: repository, Commit: commit,
		Producer:   &graphv2.Producer{Name: SCIPProducer, Version: scipProducerVersion, Configuration: strings.TrimSpace(upload.IndexerName + " " + upload.IndexerVersion)},
		Metadata:   []*graphv2.MetadataEntry{{Key: "scip.indexer", Value: upload.IndexerName}, {Key: "scip.indexer_version", Value: upload.IndexerVersion}, {Key: "scip.project_root", Value: upload.ProjectRoot}},
		Extensions: []*graphv2.Extension{{Namespace: "graphnest.scip", Json: evidence}},
	}
	for _, node := range c.nodes {
		artifact.Nodes = append(artifact.Nodes, node)
	}
	for _, edge := range c.edges {
		artifact.Edges = append(artifact.Edges, edge)
	}
	slices.SortFunc(artifact.Nodes, func(a, b *graphv2.Node) int { return strings.Compare(a.Occurrence, b.Occurrence) })
	slices.SortFunc(artifact.Edges, func(a, b *graphv2.Edge) int { return strings.Compare(a.Occurrence, b.Occurrence) })
	// The validator's byte budget decides whether a large index fits; report
	// that as too large, not as an invalid artifact.
	if budget := (v2Budget{limits: limits}); !budget.message(artifact.ProtoReflect()) && budget.size > limits.MaxArtifactBytes {
		return nil, ErrGraphTooLarge
	}
	if err := ValidateV2(artifact, limits); err != nil {
		return nil, err
	}
	if artifact.ContentHash, err = semanticHashV2(artifact); err != nil {
		return nil, err
	}
	return artifact, nil
}

type scipConverter struct {
	nodes     map[string]*graphv2.Node
	edges     map[string]*graphv2.Edge
	languages map[string]string
	info      map[string]scipgraph.SymbolInformation
	encodings []string
}

func fileOccurrence(filePath string) string { return "file:" + filePath }

func (c *scipConverter) file(filePath string) {
	occurrence := fileOccurrence(filePath)
	if _, ok := c.nodes[occurrence]; ok {
		return
	}
	c.nodes[occurrence] = &graphv2.Node{
		SourceId: occurrence, Occurrence: occurrence, Kind: "file", Name: path.Base(filePath), QualifiedName: filePath,
		Path: proto.String(filePath), Language: c.languages[filePath], Location: &graphv2.Location{Path: proto.String(filePath)},
	}
}

func (c *scipConverter) symbol(symbol, language string) *graphv2.Node {
	if node, ok := c.nodes[symbol]; ok {
		return node
	}
	parsed, err := scip.ParseSymbol(symbol)
	info := c.info[symbol]
	name, qualified, kind := symbol, symbol, "symbol"
	if err == nil && len(parsed.Descriptors) > 0 {
		names := make([]string, 0, len(parsed.Descriptors))
		for _, descriptor := range parsed.Descriptors {
			if descriptor.Name != "" {
				names = append(names, descriptor.Name)
			}
		}
		if len(names) > 0 {
			name, qualified = names[len(names)-1], strings.Join(names, ".")
		}
		kind = scipKind(info.Kind, parsed.Descriptors)
	}
	if info.DisplayName != "" {
		name = clip(info.DisplayName, DefaultMaxIdentifierBytes)
	}
	node := &graphv2.Node{SourceId: symbol, Occurrence: symbol, Kind: kind, Name: name, QualifiedName: clip(qualified, DefaultMaxIdentifierBytes), Language: clip(language, DefaultMaxIdentifierBytes), ScipSymbol: proto.String(symbol)}
	if info.Documentation != "" {
		node.Documentation = proto.String(clip(info.Documentation, maxDocumentationBytes))
	}
	if info.Signature != "" {
		node.Signature = proto.String(clip(info.Signature, DefaultMaxIdentifierBytes))
	}
	if language == "go" && kind != "module" && kind != "namespace" {
		first, _ := utf8.DecodeRuneInString(name)
		node.IsExported = proto.Bool(unicode.IsUpper(first))
	}
	c.nodes[symbol] = node
	return node
}

// parent is the symbol one descriptor up, when that symbol is an entity.
func (c *scipConverter) parent(symbol string) (string, bool) {
	parsed, err := scip.ParseSymbol(symbol)
	if err != nil || len(parsed.Descriptors) < 2 {
		return "", false
	}
	parsed.Descriptors = parsed.Descriptors[:len(parsed.Descriptors)-1]
	parent := scip.VerboseSymbolFormatter.FormatSymbol(parsed)
	_, ok := c.nodes[parent]
	return parent, ok && parent != symbol
}

func (c *scipConverter) edge(occurrence string, kind graphv2.EdgeKind, source, target string, where *graphv2.Location, reason string) {
	if _, ok := c.edges[occurrence]; ok {
		return
	}
	c.edges[occurrence] = &graphv2.Edge{SourceId: occurrence, Occurrence: occurrence, Source: source, Target: target, Kind: kind, Location: where, Confidence: proto.Float64(1), Provenance: proto.String("scip"), ResolutionReason: proto.String(reason)}
}

// edgeID joins an edge's kind and endpoints into its occurrence identifier.
// Two long symbols can exceed the identifier limit; such identifiers keep the
// kind and name the endpoints by hash instead.
func edgeID(kind, source, target string) string {
	id := kind + " " + source + " " + target
	if len(id) <= DefaultMaxIdentifierBytes {
		return id
	}
	sum := sha256.Sum256([]byte(source + "\x00" + target))
	return kind + " sha256:" + hex.EncodeToString(sum[:])
}

// clip bounds producer text to limit bytes on a rune boundary.
func clip(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func location(filePath string, startLine, startCharacter, endLine, endCharacter int32) *graphv2.Location {
	return &graphv2.Location{Path: proto.String(filePath), Start: &graphv2.Position{Line: proto.Int32(startLine), Character: proto.Int32(startCharacter)}, End: &graphv2.Position{Line: proto.Int32(endLine), Character: proto.Int32(endCharacter)}}
}

// scipKind maps the producer's symbol kind onto the v2 registry, falling back
// to what the descriptor suffix encodes when the producer recorded none.
func scipKind(kind string, descriptors []*scip.Descriptor) string {
	switch kind {
	case "Class", "SingletonClass", "Object", "Contract", "Mixin", "Delegate", "Instance":
		return "class"
	case "Struct":
		return "struct"
	case "Interface":
		return "interface"
	case "Trait", "TypeClass":
		return "trait"
	case "Protocol":
		return "protocol"
	case "Union":
		return "union"
	case "Enum":
		return "enum"
	case "EnumMember":
		return "enum_member"
	case "Type", "TypeAlias", "AssociatedType", "TypeFamily", "DataFamily":
		return "type_alias"
	case "Function", "Macro", "Constructor", "Operator":
		return "function"
	case "Method", "AbstractMethod", "StaticMethod", "SingletonMethod", "MethodSpecification", "TraitMethod", "TypeClassMethod", "PureVirtualMethod", "ProtocolMethod", "MethodAlias", "Getter", "Setter", "Accessor":
		return "method"
	case "Property", "StaticProperty":
		return "property"
	case "Field", "StaticField", "StaticDataMember", "Attribute":
		return "field"
	case "Variable", "StaticVariable", "Value":
		return "variable"
	case "Constant":
		return "constant"
	case "Package", "PackageObject", "Module", "Library":
		return "module"
	case "Namespace":
		return "namespace"
	case "Parameter", "TypeParameter", "SelfParameter", "ThisParameter":
		return "parameter"
	case "File":
		return "file"
	}
	last := descriptors[len(descriptors)-1]
	inType := len(descriptors) > 1 && descriptors[len(descriptors)-2].Suffix == scip.Descriptor_Type
	switch last.Suffix {
	case scip.Descriptor_Namespace:
		return "module"
	case scip.Descriptor_Type:
		return "class"
	case scip.Descriptor_Term:
		if inType {
			return "field"
		}
		return "variable"
	case scip.Descriptor_Method:
		if inType {
			return "method"
		}
		return "function"
	case scip.Descriptor_Macro:
		return "function"
	case scip.Descriptor_TypeParameter, scip.Descriptor_Parameter:
		return "parameter"
	}
	return "symbol"
}

func distinct(values []string) []string {
	slices.Sort(values)
	return slices.Compact(values)
}
