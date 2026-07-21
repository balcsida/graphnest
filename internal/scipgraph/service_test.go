package scipgraph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/pkg/api"
	"github.com/scip-code/scip/bindings/go/scip"
)

var (
	serviceSHA        = strings.Repeat("a", 40)
	userPrincipal     = authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
	adminPrincipal    = authn.Principal{Administrator: true, InstallationID: 10, RepositoryIDs: []int64{101}}
	serviceRepository = repository.Repository{ID: 1, GitHubID: 101, IndexedSHA: serviceSHA}
)

func TestUploadRequiresScopedAdministratorAndCurrentCommit(t *testing.T) {
	store := &fakeStore{repositories: map[int64]repository.Repository{101: serviceRepository}}
	service := Service{Store: store}
	data := marshalIndex(t, &scip.Index{Metadata: &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: "test"}}})

	if err := service.Upload(t.Context(), userPrincipal, 101, serviceSHA, data); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ordinary Upload() error = %v", err)
	}
	if err := service.Upload(t.Context(), adminPrincipal, 102, serviceSHA, data); !errors.Is(err, errUnauthorizedRepository) {
		t.Fatalf("unscoped administrator Upload() error = %v", err)
	}
	if err := service.Upload(t.Context(), adminPrincipal, 101, strings.Repeat("b", 40), data); !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("stale Upload() error = %v", err)
	}
	if err := service.Upload(t.Context(), adminPrincipal, 101, serviceSHA, []byte("bad")); !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("invalid Upload() error = %v", err)
	}
	if err := service.Upload(t.Context(), adminPrincipal, 101, serviceSHA, data); err != nil {
		t.Fatal(err)
	}
	if store.replacedRepositoryID != 1 || store.replacedCommit != serviceSHA {
		t.Fatalf("ReplaceSCIP() = repository %d commit %q", store.replacedRepositoryID, store.replacedCommit)
	}
}

func TestUploadMapsStaleReplacementOnly(t *testing.T) {
	backendError := errors.New("backend unavailable")
	data := marshalIndex(t, &scip.Index{Metadata: &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: "test"}}})
	for _, test := range []struct {
		name     string
		storeErr error
		wantErr  error
	}{
		{name: "indexed SHA changed", storeErr: ErrStaleIndex, wantErr: ErrNotIndexed},
		{name: "backend failure", storeErr: backendError, wantErr: backendError},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{repositories: map[int64]repository.Repository{101: serviceRepository}, replaceErr: test.storeErr}
			err := (&Service{Store: store}).Upload(t.Context(), adminPrincipal, 101, serviceSHA, data)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Upload() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestNavigateValidatesRequestAndUsesZeroBasedStorageLine(t *testing.T) {
	store := &fakeStore{repositories: map[int64]repository.Repository{101: serviceRepository}, origin: StoredOccurrence{RepositoryID: 1, Commit: serviceSHA}}
	service := Service{Store: store, MaxResults: 7}
	request := api.SCIPNavigationRequest{RepositoryID: 101, Path: "a.go", Line: 3, Character: 4, Operation: "definitions"}

	if _, err := service.Navigate(t.Context(), userPrincipal, request); err != nil {
		t.Fatal(err)
	}
	if store.occurrenceRepositoryID != 1 || store.occurrenceCommit != serviceSHA || store.occurrenceLine != 2 || store.occurrenceCharacter != 4 || store.locationsMax != 7 {
		t.Fatalf("storage request = %#v", store)
	}

	for _, invalid := range []api.SCIPNavigationRequest{
		{RepositoryID: 101, Path: "a.go", Line: 0, Operation: "definitions"},
		{RepositoryID: 101, Path: "../a.go", Line: 1, Operation: "definitions"},
		{RepositoryID: 101, Path: "a.go", Line: 1, Character: -1, Operation: "definitions"},
		{RepositoryID: 101, Path: "a.go", Line: 1, Operation: "unknown"},
	} {
		if _, err := service.Navigate(t.Context(), userPrincipal, invalid); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("Navigate(%#v) error = %v", invalid, err)
		}
	}
}

func TestNavigateAuthorizesEveryLocationAndConvertsLines(t *testing.T) {
	principal := userPrincipal
	principal.RepositoryNames = []string{"acme/one"}
	store := &fakeStore{
		repositories: map[int64]repository.Repository{101: serviceRepository},
		origin:       StoredOccurrence{RepositoryID: 1, Commit: serviceSHA},
		locations: []Location{
			{RepositoryID: 101, RepositoryName: "acme/one", Commit: serviceSHA, Path: "allowed.go", StartLine: 2, EndLine: 3, Approximate: true},
			{RepositoryID: 102, RepositoryName: "acme/two", Commit: serviceSHA, Path: "forbidden.go"},
			{RepositoryID: 101, RepositoryName: "acme/one", Commit: strings.Repeat("b", 40), Path: "stale.go"},
		},
		locationsTruncated: true,
	}
	service := Service{Store: store, MaxResults: 100}

	got, err := service.Navigate(t.Context(), principal, api.SCIPNavigationRequest{RepositoryID: 101, Path: "a.go", Line: 3, Character: 4, Operation: "definitions"})
	if err != nil || len(got.Locations) != 1 || got.Locations[0].RepositoryID != 101 || got.Locations[0].StartLine != 3 || got.Locations[0].EndLine != 4 || !got.Locations[0].Approximate || !got.Truncated {
		t.Fatalf("Navigate() = %#v, %v", got, err)
	}
	if len(store.authorizationCalls) != 4 {
		t.Fatalf("AuthorizedRepository() calls = %#v", store.authorizationCalls)
	}
	for index, call := range store.authorizationCalls {
		if call.installationID != principal.InstallationID || len(call.repositoryIDs) != 1 || call.repositoryIDs[0] != 101 {
			t.Fatalf("AuthorizedRepository() call = %#v", call)
		}
		if want := []int64{101, 101, 102, 101}[index]; call.repositoryID != want {
			t.Fatalf("AuthorizedRepository() call %d repository = %d, want %d", index, call.repositoryID, want)
		}
	}
	if store.locationsPrincipal.InstallationID != principal.InstallationID || len(store.locationsPrincipal.RepositoryIDs) != 1 || store.locationsPrincipal.RepositoryIDs[0] != 101 || len(store.locationsPrincipal.RepositoryNames) != 1 || store.locationsPrincipal.RepositoryNames[0] != "acme/one" {
		t.Fatalf("Locations() principal = %#v", store.locationsPrincipal)
	}
}

func TestNavigateMapsOnlyMissingOccurrence(t *testing.T) {
	backendError := errors.New("backend unavailable")
	for _, test := range []struct {
		name     string
		storeErr error
		wantErr  error
	}{
		{name: "missing occurrence", storeErr: ErrOccurrenceNotFound, wantErr: ErrNotIndexed},
		{name: "canceled", storeErr: context.Canceled, wantErr: context.Canceled},
		{name: "deadline", storeErr: context.DeadlineExceeded, wantErr: context.DeadlineExceeded},
		{name: "backend failure", storeErr: backendError, wantErr: backendError},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{repositories: map[int64]repository.Repository{101: serviceRepository}, occurrenceErr: test.storeErr}
			_, err := (&Service{Store: store}).Navigate(t.Context(), userPrincipal, api.SCIPNavigationRequest{RepositoryID: 101, Path: "a.go", Line: 1, Operation: "definitions"})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Navigate() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestNavigateReportsNotIndexed(t *testing.T) {
	store := &fakeStore{repositories: map[int64]repository.Repository{101: {ID: 1, GitHubID: 101}}}
	if _, err := (&Service{Store: store}).Navigate(t.Context(), userPrincipal, api.SCIPNavigationRequest{RepositoryID: 101, Path: "a.go", Line: 1, Operation: "references"}); !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("Navigate() error = %v", err)
	}
}

func TestSetDependenciesRequiresScopedAdministratorAndMapsPURLs(t *testing.T) {
	store := &fakeStore{repositories: map[int64]repository.Repository{101: serviceRepository}}
	service := Service{Store: store}
	purls := api.RepositoryPackages{Provides: []string{"pkg:golang/example.com/acme/app@v1"}, DependsOn: []string{"pkg:npm/acme@1.0.0"}}

	if err := service.SetDependencies(t.Context(), userPrincipal, 101, purls); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ordinary SetDependencies() error = %v", err)
	}
	if err := service.SetDependencies(t.Context(), adminPrincipal, 102, purls); !errors.Is(err, errUnauthorizedRepository) {
		t.Fatalf("unscoped administrator SetDependencies() error = %v", err)
	}
	if err := service.SetDependencies(t.Context(), adminPrincipal, 101, api.RepositoryPackages{Provides: []string{"bad"}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid SetDependencies() error = %v", err)
	}
	if err := service.SetDependencies(t.Context(), adminPrincipal, 101, purls); err != nil {
		t.Fatal(err)
	}
	if store.packagesRepositoryID != 1 || store.packagesSource != "manual" || len(store.packages) != 2 || store.packages[0].Relation != "provides" || store.packages[1].Relation != "depends_on" {
		t.Fatalf("ReplacePackages() = repository %d source %q mappings %#v", store.packagesRepositoryID, store.packagesSource, store.packages)
	}
}

var errUnauthorizedRepository = errors.New("unauthorized repository")

type fakeStore struct {
	repositories                                      map[int64]repository.Repository
	origin                                            StoredOccurrence
	locations                                         []Location
	locationsTruncated                                bool
	replacedRepositoryID                              int64
	replacedCommit                                    string
	occurrenceRepositoryID                            int64
	occurrenceCommit                                  string
	occurrenceLine, occurrenceCharacter, locationsMax int
	packagesRepositoryID                              int64
	packagesSource                                    string
	packages                                          []PackageMapping
	replaceErr, occurrenceErr                         error
	authorizationCalls                                []authorizationCall
	locationsPrincipal                                authn.Principal
}

type authorizationCall struct {
	installationID int64
	repositoryIDs  []int64
	repositoryID   int64
}

func (store *fakeStore) AuthorizedRepository(_ context.Context, installationID int64, repositoryIDs []int64, repositoryID int64) (repository.Repository, error) {
	store.authorizationCalls = append(store.authorizationCalls, authorizationCall{installationID, append([]int64(nil), repositoryIDs...), repositoryID})
	item, ok := store.repositories[repositoryID]
	if !ok {
		return repository.Repository{}, errUnauthorizedRepository
	}
	return item, nil
}

func (store *fakeStore) ReplaceSCIP(_ context.Context, repositoryID int64, commit string, _ Upload) error {
	store.replacedRepositoryID, store.replacedCommit = repositoryID, commit
	return store.replaceErr
}

func (store *fakeStore) OccurrenceAt(_ context.Context, repositoryID int64, commit, _ string, line, character int) (StoredOccurrence, error) {
	store.occurrenceRepositoryID, store.occurrenceCommit = repositoryID, commit
	store.occurrenceLine, store.occurrenceCharacter = line, character
	return store.origin, store.occurrenceErr
}

func (store *fakeStore) Locations(_ context.Context, principal authn.Principal, _ StoredOccurrence, _ string, max int) ([]Location, bool, error) {
	store.locationsMax = max
	store.locationsPrincipal = principal
	return store.locations, store.locationsTruncated, nil
}

func (store *fakeStore) ReplacePackages(_ context.Context, repositoryID int64, source string, packages []PackageMapping) error {
	store.packagesRepositoryID, store.packagesSource = repositoryID, source
	store.packages = packages
	return nil
}
