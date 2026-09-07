package graphquery

import (
	"context"
	"errors"
	"testing"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
)

type fileTestStore struct {
	entityTestStore
	query FileQuery
	rows  []graphprotocol.IndexedFile
}

func (s *fileTestStore) QueryFiles(_ context.Context, q FileQuery) ([]graphprotocol.IndexedFile, error) {
	s.query = q
	return append([]graphprotocol.IndexedFile(nil), s.rows...), nil
}

func TestFilesPaginationAndGeneration(t *testing.T) {
	s := &fileTestStore{rows: []graphprotocol.IndexedFile{{RepositoryID: 1, Fact: &graphv2.File{Path: "A.ts"}}, {RepositoryID: 1, Fact: &graphv2.File{Path: "b.ts"}}}}
	service := &Service{Store: s}
	request := graphprotocol.FilesRequest{Scope: entityTestScope(), Limit: 1, Directory: "./src/", Glob: "**/*.ts"}
	got, err := service.IndexedFiles(t.Context(), request)
	if err != nil || len(got.Files) != 1 || got.NextCursor == "" || got.Files[0].RepositoryID != 101 || s.query.Directory != "src" {
		t.Fatalf("page=%+v query=%+v err=%v", got, s.query, err)
	}
	request.Cursor = got.NextCursor
	s.rows = s.rows[1:]
	if _, err := service.IndexedFiles(t.Context(), request); err != nil || s.query.Offset != 1 {
		t.Fatalf("second page=%+v err=%v", s.query, err)
	}
	s.changed = true
	if _, err := service.IndexedFiles(t.Context(), request); !errors.Is(err, ErrGenerationChanged) {
		t.Fatalf("stale cursor=%v", err)
	}
	if err := service.ValidateGenerations(t.Context(), entityTestScope(), got.Generations); !errors.Is(err, ErrGenerationChanged) {
		t.Fatalf("final readback=%v", err)
	}
}

func TestFilesRejectInvalidPathsAndHiddenLookahead(t *testing.T) {
	for _, directory := range []string{"../src", "/tmp", "src/../../escape", "bad\x00path"} {
		s := &fileTestStore{}
		if _, err := (&Service{Store: s}).IndexedFiles(t.Context(), graphprotocol.FilesRequest{Scope: entityTestScope(), Directory: directory}); !errors.Is(err, ErrInvalidRequest) || s.generationCalls != 0 {
			t.Fatalf("directory=%q err=%v", directory, err)
		}
	}
	s := &fileTestStore{rows: []graphprotocol.IndexedFile{{RepositoryID: 1, Fact: &graphv2.File{Path: "a"}}, {RepositoryID: 99, Fact: &graphv2.File{Path: "hidden"}}}}
	got, err := (&Service{Store: s}).IndexedFiles(t.Context(), graphprotocol.FilesRequest{Scope: entityTestScope(), Limit: 1})
	if !errors.Is(err, ErrGenerationChanged) || len(got.Files) > 0 || got.NextCursor != "" {
		t.Fatalf("hidden metadata=%+v err=%v", got, err)
	}
}

func TestInspectionEntityPageRejectsHiddenLookahead(t *testing.T) {
	s := &entityTestStore{lookup: func(context.Context, EntityQuery) ([]graphprotocol.Entity, error) {
		hidden := testEntity("hidden")
		hidden.RepositoryID = 99
		return []graphprotocol.Entity{testEntity("visible"), hidden}, nil
	}}
	got, err := (&Service{Store: s}).Entities(t.Context(), graphprotocol.EntitiesRequest{Scope: entityTestScope(), Limit: 1})
	if !errors.Is(err, ErrGenerationChanged) || got.NextCursor != "" || len(got.Entities) > 0 {
		t.Fatalf("hidden lookahead influenced inspection: %+v err=%v", got, err)
	}
}
