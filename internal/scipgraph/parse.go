// Package scipgraph parses SCIP indexes into storage rows.
package scipgraph

import (
	"errors"
	"path"
	"strings"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

var ErrInvalidIndex = errors.New("invalid SCIP index")

const (
	maxDocuments     = 100_000
	maxOccurrences   = 2_000_000
	maxRelationships = 2_000_000
	maxSymbols       = 500_000
	maxDiagnostics   = 2_000_000
	maxPathBytes     = 4_096
	maxSymbolBytes   = 8_192
)

type Upload struct {
	ProjectRoot, IndexerName, IndexerVersion string
	Occurrences                              []Occurrence
	Relationships                            []Relationship
}

type Occurrence struct {
	Path, Symbol                                     string
	StartLine, StartCharacter, EndLine, EndCharacter int32
	PositionEncoding                                 int32
	Roles                                            int32
	Local                                            bool
}

type Relationship struct {
	Path                                                  string
	Source, Target                                        string
	Definition, Reference, Implementation, TypeDefinition bool
}

func Parse(data []byte) (Upload, error) {
	if !validWire(data) {
		return Upload{}, ErrInvalidIndex
	}
	var index scip.Index
	if err := proto.Unmarshal(data, &index); err != nil || index.Metadata == nil || index.Metadata.ToolInfo == nil {
		return Upload{}, ErrInvalidIndex
	}

	upload := Upload{
		ProjectRoot:    index.Metadata.ProjectRoot,
		IndexerName:    index.Metadata.ToolInfo.Name,
		IndexerVersion: index.Metadata.ToolInfo.Version,
	}
	paths := make(map[string]struct{}, len(index.Documents))
	for _, document := range index.Documents {
		if !validDocument(document, paths) {
			return Upload{}, ErrInvalidIndex
		}
		paths[document.RelativePath] = struct{}{}

		for _, occurrence := range document.Occurrences {
			sourceRange, ok := occurrence.SourceRange()
			if !ok || sourceRange.Validate() != nil || !validSymbol(occurrence.Symbol) {
				return Upload{}, ErrInvalidIndex
			}
			upload.Occurrences = append(upload.Occurrences, Occurrence{
				Path: document.RelativePath, Symbol: occurrence.Symbol,
				StartLine: sourceRange.Start.Line, StartCharacter: sourceRange.Start.Character,
				EndLine: sourceRange.End.Line, EndCharacter: sourceRange.End.Character,
				PositionEncoding: int32(document.PositionEncoding),
				Roles:            occurrence.SymbolRoles, Local: scip.IsLocalSymbol(occurrence.Symbol),
			})
		}

		for _, symbol := range document.Symbols {
			if !validSymbol(symbol.Symbol) {
				return Upload{}, ErrInvalidIndex
			}
			for _, relationship := range symbol.Relationships {
				if !validSymbol(relationship.Symbol) {
					return Upload{}, ErrInvalidIndex
				}
				upload.Relationships = append(upload.Relationships, Relationship{
					Path: document.RelativePath, Source: symbol.Symbol, Target: relationship.Symbol,
					Definition: relationship.IsDefinition, Reference: relationship.IsReference,
					Implementation: relationship.IsImplementation, TypeDefinition: relationship.IsTypeDefinition,
				})
			}
		}
	}
	return upload, nil
}

type wireCounts struct {
	documents, occurrences, relationships, symbols, diagnostics int
}

type wireLimits struct {
	documents, occurrences, relationships, symbols, diagnostics int
	pathBytes, symbolBytes                                      int
}

const (
	wireNone byte = iota
	wireIndex
	wireDocument
	wireOccurrence
	wireSymbolInformation
	wireRelationship
	wireSignature
	wirePath
	wireSymbol
	wireOpaque
)

func validWire(data []byte) bool {
	return validWireLimits(data, wireLimits{
		documents: maxDocuments, occurrences: maxOccurrences, relationships: maxRelationships,
		symbols: maxSymbols, diagnostics: maxDiagnostics, pathBytes: maxPathBytes, symbolBytes: maxSymbolBytes,
	})
}

func validWireLimits(data []byte, limits wireLimits) bool {
	return scanWireLimits(data, wireIndex, &wireCounts{}, limits)
}

func scanWireLimits(data []byte, message byte, counts *wireCounts, limits wireLimits) bool {
	for len(data) > 0 {
		number, wireType, n := protowire.ConsumeTag(data)
		if n < 0 {
			return false
		}
		data = data[n:]

		child := wireNone
		switch message {
		case wireIndex:
			if number == 2 {
				counts.documents++
				child = wireDocument
			} else if number == 3 {
				counts.symbols++
				child = wireSymbolInformation
			}
		case wireDocument:
			if number == 1 {
				child = wirePath
			} else if number == 2 {
				counts.occurrences++
				child = wireOccurrence
			} else if number == 3 {
				counts.symbols++
				child = wireSymbolInformation
			}
		case wireOccurrence:
			if number == 2 {
				child = wireSymbol
			} else if number == 6 {
				counts.diagnostics++
				child = wireOpaque
			}
		case wireSymbolInformation:
			if number == 1 || number == 8 {
				child = wireSymbol
			} else if number == 4 {
				counts.relationships++
				child = wireRelationship
			} else if number == 7 {
				child = wireSignature
			}
		case wireRelationship:
			if number == 1 {
				child = wireSymbol
			}
		case wireSignature:
			if number == 2 {
				counts.occurrences++
				child = wireOccurrence
			}
		}
		if counts.documents > limits.documents || counts.occurrences > limits.occurrences || counts.relationships > limits.relationships ||
			counts.symbols > limits.symbols || counts.diagnostics > limits.diagnostics {
			return false
		}
		if child != wireNone {
			if wireType != protowire.BytesType {
				return false
			}
			value, n := protowire.ConsumeBytes(data)
			if n < 0 || child == wirePath && len(value) > limits.pathBytes || child == wireSymbol && len(value) > limits.symbolBytes ||
				child < wirePath && !scanWireLimits(value, child, counts, limits) {
				return false
			}
			data = data[n:]
			continue
		}
		n = protowire.ConsumeFieldValue(number, wireType, data)
		if n < 0 {
			return false
		}
		data = data[n:]
	}
	return true
}

func validDocument(document *scip.Document, paths map[string]struct{}) bool {
	if document == nil {
		return false
	}
	switch document.PositionEncoding {
	case scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart,
		scip.PositionEncoding_UTF16CodeUnitOffsetFromLineStart,
		scip.PositionEncoding_UTF32CodeUnitOffsetFromLineStart:
	default:
		return false
	}
	clean := path.Clean(document.RelativePath)
	if document.RelativePath == "" || clean != document.RelativePath || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || strings.Contains(document.RelativePath, "\\") {
		return false
	}
	_, exists := paths[document.RelativePath]
	return !exists
}

func validSymbol(symbol string) bool {
	_, err := scip.ParseSymbol(symbol)
	return err == nil
}
