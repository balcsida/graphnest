package graphservice

import (
	"context"
	"errors"
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/pkg/api"
)

type resolverStore struct {
	repositories []repository.Repository
	principal    authn.Principal
	err          error
}

func (store *resolverStore) GraphRepositories(_ context.Context, principal authn.Principal) ([]repository.Repository, error) {
	store.principal = principal
	return store.repositories, store.err
}

func TestResolveRepositoryUsesAuthorizedExactSnapshot(t *testing.T) {
	principal := authn.Principal{Subject: "user", InstallationID: 10, RepositoryIDs: []int64{101}}
	store := &resolverStore{repositories: []repository.Repository{
		{ID: 1, GitHubID: 101, Name: "Acme/One", Branch: "main", IndexedSHA: "abc"},
		{ID: 2, GitHubID: 102, Name: "acme/two", Branch: "trunk", IndexedSHA: "def"},
	}}
	for _, test := range []struct {
		name     string
		selector api.GraphRepositorySelector
		branch   string
		want     Snapshot
		wantErr  error
	}{
		{"public GitHub ID", api.GraphRepositorySelector{ID: 101}, "", Snapshot{ID: 1, GitHubID: 101, Name: "Acme/One", Branch: "main", Commit: "abc"}, nil},
		{"exact name", api.GraphRepositorySelector{Name: "acme/two"}, "trunk", Snapshot{ID: 2, GitHubID: 102, Name: "acme/two", Branch: "trunk", Commit: "def"}, nil},
		{"case folded name", api.GraphRepositorySelector{Name: "acme/one"}, "", Snapshot{}, ErrRepositoryNotFound},
		{"partial name", api.GraphRepositorySelector{Name: "two"}, "", Snapshot{}, ErrRepositoryNotFound},
		{"unknown ID", api.GraphRepositorySelector{ID: 999}, "", Snapshot{}, ErrRepositoryNotFound},
		{"other branch", api.GraphRepositorySelector{ID: 101}, "feature", Snapshot{}, ErrBranchNotIndexed},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveRepository(t.Context(), store, principal, test.selector, test.branch)
			if !errors.Is(err, test.wantErr) || got != test.want {
				t.Fatalf("ResolveRepository() = %#v, %v", got, err)
			}
		})
	}
	if store.principal.Subject != principal.Subject {
		t.Fatalf("principal = %#v", store.principal)
	}
}

func TestResolveRepositoryRequiresUnambiguousAuthorizedRepository(t *testing.T) {
	principal := authn.Principal{Subject: "user"}
	ready := repository.Repository{ID: 1, GitHubID: 101, Name: "acme/one", Branch: "main", IndexedSHA: "abc"}
	for _, test := range []struct {
		name         string
		repositories []repository.Repository
		want         Snapshot
		wantErr      error
	}{
		{"none", nil, Snapshot{}, ErrRepositoryNotFound},
		{"one", []repository.Repository{ready}, Snapshot{ID: 1, GitHubID: 101, Name: "acme/one", Branch: "main", Commit: "abc"}, nil},
		{"multiple", []repository.Repository{ready, {ID: 2, GitHubID: 102, Name: "acme/two", Branch: "main", IndexedSHA: "def"}}, Snapshot{}, ErrRepositoryRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveRepository(t.Context(), &resolverStore{repositories: test.repositories}, principal, api.GraphRepositorySelector{}, "")
			if !errors.Is(err, test.wantErr) || got != test.want {
				t.Fatalf("ResolveRepository() = %#v, %v", got, err)
			}
		})
	}
}

func TestResolveRepositoryRequiresIndexedSHA(t *testing.T) {
	store := &resolverStore{repositories: []repository.Repository{{ID: 1, GitHubID: 101, Name: "acme/one", Branch: "main"}}}
	_, err := ResolveRepository(t.Context(), store, authn.Principal{}, api.GraphRepositorySelector{ID: 101}, "")
	if !errors.Is(err, ErrGraphNotReady) {
		t.Fatalf("ResolveRepository() error = %v", err)
	}
}

func TestResolveRepositoryRejectsDuplicateAuthorizedName(t *testing.T) {
	store := &resolverStore{repositories: []repository.Repository{
		{ID: 1, GitHubID: 101, Name: "acme/one", Branch: "main", IndexedSHA: "abc"},
		{ID: 2, GitHubID: 201, Name: "acme/one", Branch: "main", IndexedSHA: "def"},
	}}
	_, err := ResolveRepository(t.Context(), store, authn.Principal{Administrator: true}, api.GraphRepositorySelector{Name: "acme/one"}, "")
	if !errors.Is(err, ErrRepositoryRequired) {
		t.Fatalf("ResolveRepository() error = %v", err)
	}
}

func TestResolveRepositoryRejectsInvalidProgrammaticSelector(t *testing.T) {
	store := &resolverStore{repositories: []repository.Repository{{ID: 1, GitHubID: 101, Name: "acme/one", Branch: "main", IndexedSHA: "abc"}}}
	for _, selector := range []api.GraphRepositorySelector{
		{ID: 101, Name: "acme/one"},
		{ID: -1},
		{ID: -1, Name: "acme/one"},
	} {
		if _, err := ResolveRepository(t.Context(), store, authn.Principal{}, selector, ""); !errors.Is(err, ErrInvalidRepositorySelector) {
			t.Fatalf("ResolveRepository(%#v) error = %v", selector, err)
		}
	}
}

func TestResolveRepositoryDoesNotLeakStoreErrorsAsRepositoryMatches(t *testing.T) {
	storeErr := errors.New("database unavailable")
	_, err := ResolveRepository(t.Context(), &resolverStore{err: storeErr}, authn.Principal{}, api.GraphRepositorySelector{ID: 101}, "")
	if !errors.Is(err, storeErr) {
		t.Fatalf("ResolveRepository() error = %v", err)
	}
}
