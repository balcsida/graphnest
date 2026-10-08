package graphingest

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/jackc/pgx/v5"
)

func TestPublishRequiresGrantOrAdministratorBeforeParsing(t *testing.T) {
	store := &fakeStore{repository: readyRepository(101, testCommit)}
	service := Service{Store: store}
	reader := readerPrincipal("42", 101)
	if _, err := service.Publish(t.Context(), reader, 101, testCommit, []byte("bad"), Publication{}); !errors.Is(err, ErrForbidden) || store.grantCalls != 1 {
		t.Fatalf("read access published: err=%v grantCalls=%d", err, store.grantCalls)
	}
	if err := service.ValidatePublication(t.Context(), authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}, 101, testCommit); !errors.Is(err, ErrForbidden) {
		t.Fatalf("anonymous subject err=%v", err)
	}
	store.granted = map[string]bool{"42": true}
	result, err := service.Publish(t.Context(), reader, 101, testCommit, validV2Bytes(t, "101"), Publication{ExpectedGeneration: 7, ReplaceProducer: true})
	if err != nil {
		t.Fatal(err)
	}
	want := postgres.GraphPublication{Publisher: "api_token:42", ExpectedActiveID: 7, AllowProviderChange: true}
	if store.replacedRepositoryID != 1 || store.publication.Publisher != want.Publisher || store.publication.ExpectedActiveID != 7 || !store.publication.AllowProviderChange {
		t.Fatalf("repository=%d publication=%#v", store.replacedRepositoryID, store.publication)
	}
	hash, err := graphartifact.SemanticHashV2(store.replacedV2, graphartifact.Limits{})
	if err != nil || result != (api.GraphPublicationResult{RepositoryID: 101, Commit: testCommit, Generation: 9, ReplacedGeneration: 7, ContentHash: hex.EncodeToString(hash)}) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	store.granted, store.grantCalls = nil, 0
	if _, err := service.Publish(t.Context(), adminPrincipal(101), 101, testCommit, validV2Bytes(t, "101"), Publication{}); err != nil || store.grantCalls != 0 {
		t.Fatalf("administrator err=%v grantCalls=%d", err, store.grantCalls)
	}
}

func TestPublishRechecksCredentialAndAuthorityAfterParse(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*fakeStore) context.Context
		want  error
	}{
		{"revoked credential", func(*fakeStore) context.Context {
			return authn.WithFreshPrincipal(t.Context(), func(context.Context) (authn.Principal, error) { return authn.Principal{}, pgx.ErrNoRows })
		}, ErrUnauthenticated},
		{"revoked grant", func(store *fakeStore) context.Context {
			store.afterGrant = func() { store.granted = nil }
			return t.Context()
		}, ErrForbidden},
		{"lost repository grant", func(store *fakeStore) context.Context {
			store.afterAuthorize = func() { store.authorizeErr = pgx.ErrNoRows }
			return t.Context()
		}, pgx.ErrNoRows},
		{"advanced commit", func(store *fakeStore) context.Context {
			store.afterAuthorize = func() { store.repository.IndexedSHA = strings.Repeat("b", 40) }
			return t.Context()
		}, ErrNotIndexed},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{repository: readyRepository(101, testCommit), granted: map[string]bool{"42": true}}
			ctx := test.setup(store)
			_, err := (&Service{Store: store}).Publish(ctx, readerPrincipal("42", 101), 101, testCommit, validV2Bytes(t, "101"), Publication{})
			if !errors.Is(err, test.want) || store.replacedV2 != nil {
				t.Fatalf("err=%v replaced=%v", err, store.replacedV2 != nil)
			}
		})
	}
}

func TestPublishRejectsArtifactForAnotherTarget(t *testing.T) {
	for name, data := range map[string][]byte{
		"repository": validV2Bytes(t, "102"),
		"not v2":     validArtifactBytes(t, 101),
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{repository: readyRepository(101, testCommit)}
			if _, err := (&Service{Store: store}).Publish(t.Context(), adminPrincipal(101), 101, testCommit, data, Publication{}); !errors.Is(err, ErrInvalidArtifact) || store.replacedV2 != nil {
				t.Fatalf("err=%v", err)
			}
		})
	}
	store := &fakeStore{repository: readyRepository(101, strings.Repeat("b", 40))}
	if _, err := (&Service{Store: store}).Publish(t.Context(), adminPrincipal(101), 101, strings.Repeat("b", 40), validV2Bytes(t, "101"), Publication{}); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("commit mismatch err=%v", err)
	}
}

func TestPublishMapsReplacementOutcomes(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{"changed generation", postgres.ErrGraphPrecondition, ErrConflict},
		{"different producer", postgres.ErrGraphProviderConflict, ErrProducerConflict},
		{"invalid", graphartifact.ErrInvalidArtifact, ErrInvalidArtifact},
		{"repository disabled", pgx.ErrNoRows, ErrNotIndexed},
		{"repository unavailable", postgres.ErrGraphRepositoryUnavailable, ErrNotIndexed},
		{"backend", errors.New("database password"), ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{repository: readyRepository(101, testCommit), replaceV2Err: test.err}
			_, err := (&Service{Store: store}).Publish(t.Context(), adminPrincipal(101), 101, testCommit, validV2Bytes(t, "101"), Publication{})
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "password") {
				t.Fatalf("err=%v", err)
			}
		})
	}
	store := &fakeStore{repository: readyRepository(101, testCommit), replacementV2: postgres.GraphReplacement{Upload: postgres.GraphUpload{ID: 4}, Applied: true, Deduplicated: true}}
	result, err := (&Service{Store: store}).Publish(t.Context(), adminPrincipal(101), 101, testCommit, validV2Bytes(t, "101"), Publication{})
	if err != nil || !result.Deduplicated || result.Generation != 4 || result.ReplacedGeneration != 0 {
		t.Fatalf("dedupe result=%#v err=%v", result, err)
	}
}

func TestStatusReportsPublicationPreflight(t *testing.T) {
	active := &api.GraphActiveGeneration{ID: 3, Commit: testCommit, SchemaVersion: 2, Source: api.GraphSourceExternal, Producer: "codegraph", ProducerVersion: "0.7.0", ContentHash: strings.Repeat("ab", 32)}
	for _, test := range []struct {
		name      string
		principal authn.Principal
		granted   map[string]bool
		permitted bool
	}{
		{"reader", readerPrincipal("42", 101), nil, false},
		{"grantee", readerPrincipal("42", 101), map[string]bool{"42": true}, true},
		{"administrator", adminPrincipal(101), nil, true},
		{"OAuth grantee without graph:write", oauthPrincipal("42", 101, ""), map[string]bool{"42": true}, false},
		{"OAuth grantee with graph:write", oauthPrincipal("42", 101, "graph:write"), map[string]bool{"42": true}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{repository: readyRepository(101, testCommit), status: api.GraphStatus{RepositoryID: 101, Commit: testCommit, State: api.GraphStatePending}, active: active, granted: test.granted}
			got, err := (&Service{Store: store, MaxUploadBytes: 1 << 20}).Status(t.Context(), test.principal, 101)
			want := api.GraphPublication{UploadArtifactVersions: []int{1, 2}, MaxUploadBytes: 1 << 20, Permitted: test.permitted, ActiveGeneration: active}
			if err != nil || got.Publication == nil || got.Publication.Permitted != want.Permitted || got.Publication.MaxUploadBytes != want.MaxUploadBytes ||
				len(got.Publication.UploadArtifactVersions) != 2 || got.Publication.ActiveGeneration != active {
				t.Fatalf("publication=%#v err=%v", got.Publication, err)
			}
		})
	}
	store := &fakeStore{repository: readyRepository(101, testCommit), activeErr: errors.New("database password")}
	if _, err := (&Service{Store: store}).Status(t.Context(), adminPrincipal(101), 101); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "password") {
		t.Fatalf("active generation error=%v", err)
	}
}

func oauthPrincipal(subject string, repositoryID int64, scope string) authn.Principal {
	principal := readerPrincipal(subject, repositoryID)
	principal.Method, principal.Scope = authn.ProviderOAuthToken, scope
	return principal
}

func TestOAuthPublicationNeedsGraphWriteScope(t *testing.T) {
	store := &fakeStore{repository: readyRepository(101, testCommit), granted: map[string]bool{"42": true}}
	service := &Service{Store: store}
	if _, err := service.Publish(t.Context(), oauthPrincipal("42", 101, ""), 101, testCommit, validV2Bytes(t, "101"), Publication{}); !errors.Is(err, ErrForbidden) || store.grantCalls != 0 {
		t.Fatalf("unscoped OAuth published: err=%v grantCalls=%d", err, store.grantCalls)
	}
	if _, err := service.Publish(t.Context(), oauthPrincipal("42", 101, "graph:write"), 101, testCommit, validV2Bytes(t, "101"), Publication{}); err != nil || store.publication.Publisher != "oauth_token:42" {
		t.Fatalf("scoped OAuth publish: err=%v publisher=%q", err, store.publication.Publisher)
	}
}

func readerPrincipal(subject string, repositoryID int64) authn.Principal {
	return authn.Principal{Subject: subject, Method: "api_token", InstallationID: 10, RepositoryIDs: []int64{repositoryID}}
}

func validV2Bytes(t *testing.T, repository string) []byte {
	t.Helper()
	data, err := graphartifact.MarshalV2(&graphv2.Artifact{SchemaVersion: 2, Repository: repository, Commit: testCommit, Producer: &graphv2.Producer{Name: "codegraph", Version: "0.7.0", Configuration: "portable"},
		Nodes: []*graphv2.Node{{SourceId: "a", Occurrence: "declaration:1", Kind: "function"}, {SourceId: "b", Occurrence: "declaration:2", Kind: "class"}}}, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return data
}
