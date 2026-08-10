package repository

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/pkg/api"
)

func TestServiceList(t *testing.T) {
	indexedAt := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	store := &serviceStore{repositories: []Repository{{
		ID: 1, GitHubID: 101, Name: "acme/one", Branch: "main", DesiredSHA: strings.Repeat("b", 40),
		IndexedSHA: strings.Repeat("a", 40), WebURL: "https://github.com/acme/one", Status: "ready", SearchNode: "node-a", LastIndexedAt: &indexedAt,
	}}}
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}, RepositoryNames: []string{"acme/one"}}

	got, err := (&Service{Store: store}).List(t.Context(), principal)
	if err != nil {
		t.Fatal(err)
	}
	want := []api.RepositorySummary{{
		ID: 101, GitHubID: 101, Name: "acme/one", Branch: "main", DesiredSHA: strings.Repeat("b", 40),
		IndexedSHA: strings.Repeat("a", 40), WebURL: "https://github.com/acme/one", Status: "ready", SearchNode: "node-a", LastIndexedAt: &indexedAt, SCIPStatus: api.SCIPStatusUnknown,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if store.installationID != 10 || !reflect.DeepEqual(store.repositoryIDs, []int64{101}) || !reflect.DeepEqual(store.names, []string{"acme/one"}) {
		t.Fatalf("authorization = %#v", store)
	}
}

func TestServiceListReturnsIDReusableByStatus(t *testing.T) {
	repository := Repository{ID: 1, GitHubID: 101, Name: "acme/one", SearchNode: "node-a"}
	store := &serviceStore{repositories: []Repository{repository}, repository: repository}
	service := &Service{Store: store}
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}

	repositories, err := service.List(t.Context(), principal)
	if err != nil || len(repositories) != 1 {
		t.Fatalf("repositories = %#v, err = %v", repositories, err)
	}
	status, err := service.Status(t.Context(), principal, repositories[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if store.authorizedID != repository.GitHubID || status.ID != repository.GitHubID {
		t.Fatalf("list ID = %d, status input ID = %d, status ID = %d, want GitHub ID %d", repositories[0].ID, store.authorizedID, status.ID, repository.GitHubID)
	}
}

func TestServiceStatus(t *testing.T) {
	store := &serviceStore{repository: Repository{ID: 1, GitHubID: 101, Name: "acme/one", SearchNode: "node-a"}}
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}

	got, err := (&Service{Store: store}).Status(t.Context(), principal, 101)
	if err != nil || got.ID != 101 || got.GitHubID != 101 || got.SearchNode != "node-a" {
		t.Fatalf("got %#v, err %v", got, err)
	}
	if store.authorizedID != 101 || store.installationID != 10 || !reflect.DeepEqual(store.repositoryIDs, []int64{101}) {
		t.Fatalf("authorization = %#v", store)
	}
}

func TestServiceAdministratorUsesGlobalAuthorization(t *testing.T) {
	sha := strings.Repeat("a", 40)
	repository := Repository{ID: 2, InstallationID: 20, GitHubID: 201, Name: "other/two", IndexedSHA: sha, SearchNode: "node-a"}
	store := &serviceStore{globalRepositories: []Repository{repository}, globalRepository: repository}
	reader := &contentReader{content: githubapp.Content{Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte("one")), SHA: "blob", Size: 3}}
	service := &Service{Store: store, GitHub: reader}
	principal := authn.Principal{Administrator: true, InstallationID: 10, RepositoryIDs: []int64{101}}

	list, err := service.List(t.Context(), principal)
	if err != nil || len(list) != 1 || list[0].GitHubID != 201 {
		t.Fatalf("list = %#v, err = %v", list, err)
	}
	status, err := service.Status(t.Context(), principal, 201)
	if err != nil || status.GitHubID != 201 {
		t.Fatalf("status = %#v, err = %v", status, err)
	}
	if _, err := service.ReadFile(t.Context(), principal, api.ReadFileRequest{RepositoryID: 201, Path: "file"}); err != nil {
		t.Fatal(err)
	}
	if store.globalLists != 1 || store.globalLookups != 3 || store.authorizedCalls != 0 {
		t.Fatalf("global lists=%d lookups=%d scoped lookups=%d", store.globalLists, store.globalLookups, store.authorizedCalls)
	}
}

func TestServiceReadFile(t *testing.T) {
	sha := strings.Repeat("a", 40)
	repository := Repository{ID: 1, InstallationID: 10, GitHubID: 101, Name: "acme/one", IndexedSHA: sha, SearchNode: "node-a"}
	store := &serviceStore{repository: repository}
	reader := &contentReader{content: githubapp.Content{
		Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte("one\ntwo\nthree\nfour")), SHA: "blob", Size: 18,
	}}
	service := &Service{Store: store, GitHub: reader, MaxFileBytes: 1024, MaxLines: 2}
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}

	got, err := service.ReadFile(t.Context(), principal, api.ReadFileRequest{RepositoryID: 101, Path: "dir/file.go", StartLine: 2, EndLine: 4})
	if err != nil {
		t.Fatal(err)
	}
	want := api.ReadFileResponse{RepositoryID: 101, Path: "dir/file.go", IndexedSHA: sha, BlobSHA: "blob", Content: "two\nthree", StartLine: 2, EndLine: 3, Truncated: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if store.authorizedCalls != 2 {
		t.Fatalf("authorization calls = %d", store.authorizedCalls)
	}
	wireBytes := int64(base64.StdEncoding.EncodedLen(1024)) + githubEnvelopeBytes
	if reader.calls != 1 || reader.installationID != 10 || reader.owner != "acme" || reader.name != "one" || reader.path != "dir/file.go" || reader.ref != sha || reader.maxBytes != wireBytes {
		t.Fatalf("contents call = %#v", reader)
	}
}

func TestServiceReadFileAllowsNearLimitContentThroughGitHubClient(t *testing.T) {
	data := []byte(strings.Repeat("a", 800<<10))
	sha := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/app/installations/10/access_tokens":
			_ = json.NewEncoder(writer).Encode(map[string]any{"token": "token", "expires_at": time.Now().Add(time.Hour)})
		case "/repos/acme/one/contents/large.txt":
			_ = json.NewEncoder(writer).Encode(githubapp.Content{Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString(data), SHA: "blob", Size: int64(len(data))})
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := githubapp.NewSigner(1, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	client := githubapp.NewClient(githubapp.Endpoints{API: endpoint}, server.Client(), signer, "2022-11-28", 2<<20, time.Now)
	store := &serviceStore{repository: Repository{ID: 1, InstallationID: 10, GitHubID: 101, Name: "acme/one", IndexedSHA: sha}}

	got, err := (&Service{Store: store, GitHub: client}).ReadFile(t.Context(), authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}, api.ReadFileRequest{RepositoryID: 101, Path: "large.txt"})
	if err != nil || got.Content != string(data) {
		t.Fatalf("content bytes = %d, err = %v", len(got.Content), err)
	}
}

func TestServiceReadFileRejectsBeforeGitHub(t *testing.T) {
	sha := strings.Repeat("a", 40)
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
	tests := []struct {
		name       string
		repository Repository
		request    api.ReadFileRequest
		want       error
	}{
		{"not indexed", Repository{GitHubID: 101, Name: "acme/one"}, api.ReadFileRequest{RepositoryID: 101, Path: "file"}, ErrNotIndexed},
		{"empty path", Repository{GitHubID: 101, Name: "acme/one", IndexedSHA: sha}, api.ReadFileRequest{RepositoryID: 101}, ErrInvalidPath},
		{"absolute path", Repository{GitHubID: 101, Name: "acme/one", IndexedSHA: sha}, api.ReadFileRequest{RepositoryID: 101, Path: "/file"}, ErrInvalidPath},
		{"backslash", Repository{GitHubID: 101, Name: "acme/one", IndexedSHA: sha}, api.ReadFileRequest{RepositoryID: 101, Path: `dir\file`}, ErrInvalidPath},
		{"NUL", Repository{GitHubID: 101, Name: "acme/one", IndexedSHA: sha}, api.ReadFileRequest{RepositoryID: 101, Path: "dir\x00file"}, ErrInvalidPath},
		{"dot", Repository{GitHubID: 101, Name: "acme/one", IndexedSHA: sha}, api.ReadFileRequest{RepositoryID: 101, Path: "."}, ErrInvalidPath},
		{"traversal", Repository{GitHubID: 101, Name: "acme/one", IndexedSHA: sha}, api.ReadFileRequest{RepositoryID: 101, Path: "dir/../file"}, ErrInvalidPath},
		{"parent", Repository{GitHubID: 101, Name: "acme/one", IndexedSHA: sha}, api.ReadFileRequest{RepositoryID: 101, Path: "../file"}, ErrInvalidPath},
		{"empty segment", Repository{GitHubID: 101, Name: "acme/one", IndexedSHA: sha}, api.ReadFileRequest{RepositoryID: 101, Path: "dir//file"}, ErrInvalidPath},
		{"invalid range", Repository{GitHubID: 101, Name: "acme/one", IndexedSHA: sha}, api.ReadFileRequest{RepositoryID: 101, Path: "file", StartLine: 2, EndLine: 1}, ErrInvalidRange},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &contentReader{}
			_, err := (&Service{Store: &serviceStore{repository: test.repository}, GitHub: reader}).ReadFile(t.Context(), principal, test.request)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if reader.calls != 0 {
				t.Fatalf("GitHub called %d times", reader.calls)
			}
		})
	}

	authorizationError := errors.New("denied")
	reader := &contentReader{}
	_, err := (&Service{Store: &serviceStore{err: authorizationError}, GitHub: reader}).ReadFile(t.Context(), principal, api.ReadFileRequest{RepositoryID: 101, Path: "file"})
	if !errors.Is(err, authorizationError) || reader.calls != 0 {
		t.Fatalf("error = %v, GitHub calls = %d", err, reader.calls)
	}
}

func TestServiceReadFileRejectsUnsafeContent(t *testing.T) {
	sha := strings.Repeat("a", 40)
	repository := Repository{InstallationID: 10, GitHubID: 101, Name: "acme/one", IndexedSHA: sha}
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
	tests := []struct {
		name    string
		content githubapp.Content
		limit   int64
		want    error
	}{
		{"directory", githubapp.Content{Type: "dir", Encoding: "base64"}, 1024, ErrInvalidFile},
		{"encoding", githubapp.Content{Type: "file", Encoding: "utf-8"}, 1024, ErrInvalidFile},
		{"reported oversized", githubapp.Content{Type: "file", Encoding: "base64", Size: 1025}, 1024, ErrFileTooLarge},
		{"decoded oversized", githubapp.Content{Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString(make([]byte, 1025)), Size: 1}, 1024, ErrFileTooLarge},
		{"invalid base64", githubapp.Content{Type: "file", Encoding: "base64", Content: "***", Size: 1}, 1024, ErrInvalidFile},
		{"NUL content", githubapp.Content{Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte{'a', 0}), Size: 2}, 1024, ErrBinaryFile},
		{"invalid UTF-8", githubapp.Content{Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte{0xff}), Size: 1}, 1024, ErrBinaryFile},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.content.SHA = "blob"
			_, err := (&Service{Store: &serviceStore{repository: repository}, GitHub: &contentReader{content: test.content}, MaxFileBytes: test.limit}).ReadFile(t.Context(), principal, api.ReadFileRequest{RepositoryID: 101, Path: "file"})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestServiceReadFileRangesAndReauthorizes(t *testing.T) {
	sha := strings.Repeat("a", 40)
	repository := Repository{InstallationID: 10, GitHubID: 101, Name: "acme/one", IndexedSHA: sha}
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
	content := githubapp.Content{Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte("one\ntwo\nthree")), SHA: "blob", Size: 13}

	service := &Service{Store: &serviceStore{repository: repository}, GitHub: &contentReader{content: content}, MaxLines: 10}
	got, err := service.ReadFile(t.Context(), principal, api.ReadFileRequest{RepositoryID: 101, Path: "file", StartLine: 2, EndLine: 99})
	if err != nil || got.Content != "two\nthree" || got.StartLine != 2 || got.EndLine != 3 || got.Truncated {
		t.Fatalf("got %#v, err %v", got, err)
	}

	_, err = service.ReadFile(t.Context(), principal, api.ReadFileRequest{RepositoryID: 101, Path: "file", StartLine: 4})
	if !errors.Is(err, ErrLineOutOfRange) {
		t.Fatalf("error = %v", err)
	}

	denied := errors.New("disabled")
	store := &serviceStore{repository: repository, errors: []error{nil, denied}}
	_, err = (&Service{Store: store, GitHub: &contentReader{content: content}}).ReadFile(t.Context(), principal, api.ReadFileRequest{RepositoryID: 101, Path: "file"})
	if !errors.Is(err, denied) || store.authorizedCalls != 2 {
		t.Fatalf("error = %v, authorization calls = %d", err, store.authorizedCalls)
	}

	changed := repository
	changed.IndexedSHA = strings.Repeat("b", 40)
	store = &serviceStore{repositoriesByCall: []Repository{repository, changed}}
	_, err = (&Service{Store: store, GitHub: &contentReader{content: content}}).ReadFile(t.Context(), principal, api.ReadFileRequest{RepositoryID: 101, Path: "file"})
	if !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("changed indexed SHA error = %v", err)
	}

	store = &serviceStore{repository: repository}
	got, err = (&Service{Store: store, GitHub: &contentReader{content: content}, MaxLines: math.MaxInt}).ReadFile(t.Context(), principal, api.ReadFileRequest{RepositoryID: 101, Path: "file", StartLine: 2})
	if err != nil || got.Content != "two\nthree" || got.Truncated {
		t.Fatalf("large line limit got %#v, err %v", got, err)
	}
}

func TestServiceReadFileAtRequiresExpectedSHA(t *testing.T) {
	sha := strings.Repeat("a", 40)
	service := &Service{Store: &serviceStore{repository: Repository{InstallationID: 10, GitHubID: 101, Name: "acme/one", IndexedSHA: sha}}, GitHub: &contentReader{content: githubapp.Content{Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte("one")), SHA: "blob", Size: 3}}}
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
	if _, err := service.ReadFileAt(t.Context(), principal, api.ReadFileRequest{RepositoryID: 101, Path: "file"}, strings.Repeat("b", 40)); !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("error = %v", err)
	}
}

type serviceStore struct {
	repositories       []Repository
	repository         Repository
	repositoriesByCall []Repository
	err                error
	errors             []error
	installationID     int64
	repositoryIDs      []int64
	names              []string
	authorizedID       int64
	authorizedCalls    int
	globalRepositories []Repository
	globalRepository   Repository
	globalLists        int
	globalLookups      int
}

func (store *serviceStore) AuthorizedRepositories(_ context.Context, installationID int64, repositoryIDs []int64, names []string) ([]Repository, error) {
	store.installationID = installationID
	store.repositoryIDs = append([]int64(nil), repositoryIDs...)
	store.names = append([]string(nil), names...)
	return store.repositories, store.err
}

func (store *serviceStore) AuthorizedRepository(_ context.Context, installationID int64, repositoryIDs []int64, repositoryID int64) (Repository, error) {
	store.installationID = installationID
	store.repositoryIDs = append([]int64(nil), repositoryIDs...)
	store.authorizedID = repositoryID
	call := store.authorizedCalls
	store.authorizedCalls++
	if call < len(store.errors) && store.errors[call] != nil {
		return Repository{}, store.errors[call]
	}
	if call < len(store.repositoriesByCall) {
		return store.repositoriesByCall[call], nil
	}
	return store.repository, store.err
}

func (store *serviceStore) AllAuthorizedRepositories(context.Context, []string) ([]Repository, error) {
	store.globalLists++
	return store.globalRepositories, store.err
}

func (store *serviceStore) AnyAuthorizedRepository(_ context.Context, repositoryID int64) (Repository, error) {
	store.globalLookups++
	if repositoryID != store.globalRepository.GitHubID {
		return Repository{}, errors.New("not found")
	}
	return store.globalRepository, store.err
}

type contentReader struct {
	content        githubapp.Content
	err            error
	calls          int
	installationID int64
	owner          string
	name           string
	path           string
	ref            string
	maxBytes       int64
}

func (reader *contentReader) ReadContents(_ context.Context, installationID int64, owner, name, path, ref string, maxBytes int64) (githubapp.Content, error) {
	reader.calls++
	reader.installationID = installationID
	reader.owner = owner
	reader.name = name
	reader.path = path
	reader.ref = ref
	reader.maxBytes = maxBytes
	return reader.content, reader.err
}
