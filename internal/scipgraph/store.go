package scipgraph

import "errors"

var (
	ErrOccurrenceNotFound = errors.New("SCIP occurrence not found")
	ErrStaleIndex         = errors.New("stale SCIP index")
)

type StoredOccurrence struct {
	UploadID, RepositoryID                           int64
	Commit, Path, Symbol                             string
	StartLine, StartCharacter, EndLine, EndCharacter int32
	PositionEncoding                                 int32
	Roles                                            int32
	Local                                            bool
}

type Location struct {
	RepositoryID                                     int64
	RepositoryName, Commit, Path, Symbol             string
	StartLine, StartCharacter, EndLine, EndCharacter int32
	PositionEncoding                                 int32
	Roles                                            int32
	Approximate                                      bool
}
