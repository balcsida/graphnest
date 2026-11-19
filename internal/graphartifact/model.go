package graphartifact

import "math"

type Artifact struct {
	SchemaVersion uint32
	Analyzer      Analyzer
	RepositoryID  int64
	Commit        string
	ContentHash   []byte
	Nodes         []Node
	Edges         []Edge
}

type Analyzer struct{ Name, Version string }

type Node struct {
	UID, Path, Language, SymbolKind, QualifiedName, Signature, SCIPSymbol string
	Kind                                                                  NodeKind
	Range                                                                 Range
}

type Edge struct {
	SourceUID, TargetUID, Path, ResolutionReason string
	Kind                                         EdgeKind
	Range                                        Range
	Confidence                                   float32
}

type Range struct{ StartLine, StartCharacter, EndLine, EndCharacter int32 }

type NodeKind uint8

const (
	NodeUnspecified NodeKind = iota
	NodeRepository
	NodeFile
	NodeSymbol
)

type EdgeKind uint8

const (
	EdgeUnspecified EdgeKind = iota
	EdgeContains
	EdgeImports
	EdgeReferences
	EdgeCalls
	EdgeExtends
	EdgeImplements
	EdgeExports
	EdgeTypeOf
	EdgeReturns
	EdgeInstantiates
	EdgeOverrides
	EdgeDecorates
	EdgeNavigates
)

const (
	DefaultMaxNodes           = 500_000
	DefaultMaxEdges           = 2_000_000
	DefaultMaxFiles           = 100_000
	DefaultMaxUnresolved      = DefaultMaxEdges
	HardMaxNodes              = 2_000_000
	HardMaxEdges              = 10_000_000
	DefaultMaxPathBytes       = 4_096
	DefaultMaxIdentifierBytes = 16_384
)

// The v2 artifact byte budget charges every string and 16 bytes per field; a
// SCIP-derived edge costs about 1 KiB of it. A deployment can raise the budget
// of SCIP-derived generations to the hard maximum; a published artifact is
// bounded by its upload size long before. min keeps the constant an int on
// 32-bit builds.
const (
	DefaultMaxArtifactBytes = 128 << 20
	HardMaxArtifactBytes    = min(4<<30, math.MaxInt)
)

type Limits struct {
	MaxNodes, MaxEdges, MaxPathBytes, MaxIdentifierBytes int
	// Additional v2 bounds, applied before protobuf or extension JSON decoding.
	MaxArtifactBytes, MaxFiles, MaxUnresolved, MaxDiagnostics int
	MaxMetadataBytes, MaxExtensionBytes, MaxCollectionItems   int
}
