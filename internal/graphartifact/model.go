package graphartifact

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
)

const (
	DefaultMaxNodes           = 500_000
	DefaultMaxEdges           = 2_000_000
	HardMaxNodes              = 2_000_000
	HardMaxEdges              = 10_000_000
	DefaultMaxPathBytes       = 4_096
	DefaultMaxIdentifierBytes = 16_384
)

type Limits struct{ MaxNodes, MaxEdges, MaxPathBytes, MaxIdentifierBytes int }
