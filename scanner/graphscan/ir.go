package graphscan

import "github.com/balcsida/graphnest/internal/graphartifact"

type Language string

const (
	Go         Language = "go"
	JavaScript Language = "javascript"
	TypeScript Language = "typescript"
	Java       Language = "java"
	Kotlin     Language = "kotlin"
	Rust       Language = "rust"
)

type Point struct{ Line, Column uint32 }
type Range struct{ Start, End Point }

type Declaration struct {
	LocalID, Path, Name, QualifiedName, Signature string
	Kind, ScopeID, Receiver, TypeName, SCIPSymbol string
	PointerReceiver                               bool
	Range                                         Range
}

type Import struct {
	Path, Target, Alias string
	Range               Range
}

type Reference struct {
	Path, FromLocalID, Name string
	Candidates              []string
	Range                   Range
	Call                    bool
}

type Heritage struct {
	Path, ChildLocalID string
	Candidates         []string
	Kind               graphartifact.EdgeKind
	Range              Range
}

type File struct {
	Path, Module string
	Language     Language
	Declarations []Declaration
	Imports      []Import
	References   []Reference
	Heritage     []Heritage
}
