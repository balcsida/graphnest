package graphservice

import (
	"context"
	"errors"
	"testing"

	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
)

type classificationBackend struct {
	*inspectionBackend
	files graphprotocol.FileClassificationResponse
	count graphprotocol.GeneratedFileCountResponse
	after func()
}

func (b *classificationBackend) FileClassifications(_ context.Context, request graphprotocol.FileClassificationRequest) (graphprotocol.FileClassificationResponse, error) {
	b.scopes = append(b.scopes, request.Scope)
	if b.after != nil {
		b.after()
	}
	return b.files, nil
}

func (b *classificationBackend) GeneratedFileCount(_ context.Context, request graphprotocol.GeneratedFileCountRequest) (graphprotocol.GeneratedFileCountResponse, error) {
	b.scopes = append(b.scopes, request.Scope)
	if b.after != nil {
		b.after()
	}
	return b.count, nil
}

func TestFileClassificationServiceAuthority(t *testing.T) {
	service, base, store := inspectionFixture()
	repositories := append([]repository.Repository(nil), store.repositories...)
	persisted := true
	backend := &classificationBackend{inspectionBackend: base}
	backend.files = graphprotocol.FileClassificationResponse{
		Files:       []graphprotocol.FileClassification{{Path: "banner.ts", Present: true, PersistedGenerated: &persisted, Generated: true}},
		Generations: []graphprotocol.Generation{base.generation},
	}
	backend.count = graphprotocol.GeneratedFileCountResponse{GeneratedFiles: 1, TotalFiles: 2, Generations: []graphprotocol.Generation{base.generation}}
	service.Backend = backend
	request := FileClassificationsRequest{Repo: api.GraphRepositorySelector{ID: 101}, Paths: []string{"banner.ts"}}
	got, err := service.FileClassifications(t.Context(), principalFor(101), request)
	if err != nil || len(got.Files) != 1 || got.Files[0].Path != "banner.ts" || len(backend.scopes) != 1 || len(backend.scopes[0].Repositories) != 1 {
		t.Fatalf("files=%+v scopes=%+v err=%v", got, backend.scopes, err)
	}
	count, err := service.GeneratedFileCount(t.Context(), principalFor(101), GeneratedFileCountRequest{Repo: request.Repo})
	if err != nil || count.GeneratedFiles != 1 || count.TotalFiles != 2 {
		t.Fatalf("count=%+v err=%v", count, err)
	}
	backend.after = func() { store.repositories = nil }
	got, err = service.FileClassifications(t.Context(), principalFor(101), request)
	if err == nil || len(got.Files) != 0 {
		t.Fatalf("revoked result=%+v err=%v", got, err)
	}
	backend.after = nil
	store.repositories = repositories
	base.changed = true
	count, err = service.GeneratedFileCount(t.Context(), principalFor(101), GeneratedFileCountRequest{Repo: request.Repo})
	if !errors.Is(err, graphquery.ErrGenerationChanged) || len(count.Generations) != 0 {
		t.Fatalf("changed count=%+v err=%v", count, err)
	}
}

func TestFileClassificationServiceRejectsBounds(t *testing.T) {
	service, base, _ := inspectionFixture()
	service.Backend = &classificationBackend{inspectionBackend: base}
	paths := make([]string, 65)
	for i := range paths {
		paths[i] = "file.ts"
	}
	for _, candidate := range [][]string{paths, {"../secret"}, {"bad\x00path"}} {
		got, err := service.FileClassifications(t.Context(), principalFor(101), FileClassificationsRequest{Repo: api.GraphRepositorySelector{ID: 101}, Paths: candidate})
		if !errors.Is(err, ErrInvalidRequest) || len(got.Files) != 0 || len(base.scopes) != 0 {
			t.Fatalf("paths=%d result=%+v err=%v", len(candidate), got, err)
		}
	}
}
