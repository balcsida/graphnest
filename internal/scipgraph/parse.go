// Package scipgraph parses SCIP indexes into storage rows.
package scipgraph

import (
	"errors"
	"path"
	"strings"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

var ErrInvalidIndex = errors.New("invalid SCIP index")

type Upload struct {
	ProjectRoot, IndexerName, IndexerVersion string
	Occurrences                              []Occurrence
	Relationships                            []Relationship
}

type Occurrence struct {
	Path, Symbol                                     string
	StartLine, StartCharacter, EndLine, EndCharacter int32
	Roles                                            int32
	Local                                            bool
}

type Relationship struct {
	Source, Target                                        string
	Definition, Reference, Implementation, TypeDefinition bool
}

func Parse(data []byte) (Upload, error) {
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
				Roles: occurrence.SymbolRoles, Local: scip.IsLocalSymbol(occurrence.Symbol),
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
					Source: symbol.Symbol, Target: relationship.Symbol,
					Definition: relationship.IsDefinition, Reference: relationship.IsReference,
					Implementation: relationship.IsImplementation, TypeDefinition: relationship.IsTypeDefinition,
				})
			}
		}
	}
	return upload, nil
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
