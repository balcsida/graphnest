package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/account"
	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/repository"
)

type failingAuthorizer struct{ err error }

func (a failingAuthorizer) AuthorizedRepository(context.Context, authn.Principal, int64) (repository.Repository, error) {
	return repository.Repository{}, a.err
}

type accountStoreStub struct {
	authn.APITokenStore
	listed   []authn.APITokenMetadata
	user, id int64
}

func TestAccountTokenRouteReturnsUnavailableForAuthorizerOutage(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	service := &account.Service{Manager: authn.TokenManager{Store: &accountStoreStub{}, Now: func() time.Time { return now }, Rand: strings.NewReader(strings.Repeat("x", 32))}, Authorizer: failingAuthorizer{errors.New("database unavailable")}}
	mux := http.NewServeMux()
	RegisterAccount(mux, authn.RequestAuthenticator{Bearer: authn.NewStatic(map[string]authn.Principal{"user": {Subject: "11", Method: "oidc", RepositoryIDs: []int64{101}}})}, service, 1024, 4096)
	request := httptest.NewRequest(http.MethodPost, "/v1/account/api-tokens", strings.NewReader(`{"expires_at":"2026-08-29T00:00:00Z","repository_ids":[101]}`))
	request.Header.Set("Authorization", "Bearer user")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", response.Code)
	}
}

func (*accountStoreStub) CreateAPIToken(context.Context, authn.APITokenRecord) (int64, error) {
	return 3, nil
}
func (store *accountStoreStub) CreateAPITokenAudited(ctx context.Context, record authn.APITokenRecord, _ audit.Event) (int64, error) {
	return store.CreateAPIToken(ctx, record)
}
func (s *accountStoreStub) ListAPITokens(context.Context, int64) ([]authn.APITokenMetadata, error) {
	return s.listed, nil
}
func (s *accountStoreStub) RevokeAPIToken(_ context.Context, user, id int64) error {
	s.user, s.id = user, id
	return nil
}
func (store *accountStoreStub) RevokeAPITokenAudited(ctx context.Context, user, id int64, _ audit.Event) error {
	return store.RevokeAPIToken(ctx, user, id)
}

func TestAccountTokenRoutesRevealPlaintextOnlyAtCreation(t *testing.T) {
	// Break caught: account token endpoint leaking a reusable plaintext token in metadata.
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	store := &accountStoreStub{listed: []authn.APITokenMetadata{{ID: 3, Prefix: "gnp_visible", CreatedAt: now}}}
	service := &account.Service{Manager: authn.TokenManager{Store: store, Now: func() time.Time { return now }, Rand: strings.NewReader(strings.Repeat("x", 32))}}
	mux := http.NewServeMux()
	RegisterAccount(mux, authn.RequestAuthenticator{Bearer: authn.NewStatic(map[string]authn.Principal{"user": {Subject: "11", Method: "oidc", RepositoryIDs: []int64{101}}})}, service, 1024, 4096)

	post := httptest.NewRequest(http.MethodPost, "/v1/account/api-tokens", strings.NewReader(`{"expires_at":"2026-08-29T00:00:00Z","repository_ids":[101]}`))
	post.Header.Set("Authorization", "Bearer user")
	post.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	mux.ServeHTTP(created, post)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"token":"gnp_`) || !strings.Contains(created.Body.String(), `"expires_at":"2026-08-29T00:00:00Z"`) {
		t.Fatalf("created=%d body=%q", created.Code, created.Body.String())
	}

	list := httptest.NewRequest(http.MethodGet, "/v1/account/api-tokens", nil)
	list.Header.Set("Authorization", "Bearer user")
	listed := httptest.NewRecorder()
	mux.ServeHTTP(listed, list)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), `"token"`) || strings.Contains(listed.Body.String(), "token_hash") {
		t.Fatalf("listed=%d body=%q", listed.Code, listed.Body.String())
	}
	var response struct {
		Tokens []account.Token `json:"tokens"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &response); err != nil || len(response.Tokens) != 1 {
		t.Fatalf("response=%q err=%v", listed.Body.String(), err)
	}

	remove := httptest.NewRequest(http.MethodDelete, "/v1/account/api-tokens/3", nil)
	remove.Header.Set("Authorization", "Bearer user")
	deleted := httptest.NewRecorder()
	mux.ServeHTTP(deleted, remove)
	if deleted.Code != http.StatusNoContent || store.user != 11 || store.id != 3 {
		t.Fatalf("deleted=%d user=%d id=%d", deleted.Code, store.user, store.id)
	}
}

func TestAccountTokenRouteRejectsNonUTCAndDuplicateRepositoryIDs(t *testing.T) {
	// Break caught: accepting ambiguous expiry or duplicate token repository ceilings.
	mux := http.NewServeMux()
	RegisterAccount(mux, authn.RequestAuthenticator{Bearer: authn.NewStatic(map[string]authn.Principal{"user": {Subject: "11", Method: "oidc", RepositoryIDs: []int64{101}}})}, &account.Service{}, 1024, 4096)
	request := httptest.NewRequest(http.MethodPost, "/v1/account/api-tokens", strings.NewReader(`{"expires_at":"2026-08-29T01:00:00+01:00","repository_ids":[101,101]}`))
	request.Header.Set("Authorization", "Bearer user")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestAccountTokenRouteRequiresFutureExpiry(t *testing.T) {
	// Break caught: HTTP accepts a past or equal expiry despite the service clock.
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for _, expiry := range []string{"2026-07-31T23:59:59Z", "2026-08-01T00:00:00Z"} {
		store := &accountStoreStub{}
		service := &account.Service{Manager: authn.TokenManager{Store: store, Now: func() time.Time { return now }, Rand: strings.NewReader(strings.Repeat("x", 32))}}
		mux := http.NewServeMux()
		RegisterAccount(mux, authn.RequestAuthenticator{Bearer: authn.NewStatic(map[string]authn.Principal{"user": {Subject: "11", Method: "oidc", RepositoryIDs: []int64{101}}})}, service, 1024, 4096)
		request := httptest.NewRequest(http.MethodPost, "/v1/account/api-tokens", strings.NewReader(`{"expires_at":"`+expiry+`","repository_ids":[101]}`))
		request.Header.Set("Authorization", "Bearer user")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("expiry=%s status=%d", expiry, response.Code)
		}
	}
}

func TestAccountTokenRouteAllowsOmittedOptionalControls(t *testing.T) {
	store := &accountStoreStub{}
	service := &account.Service{Manager: authn.TokenManager{Store: store, Rand: strings.NewReader(strings.Repeat("x", 32))}}
	mux := http.NewServeMux()
	RegisterAccount(mux, authn.RequestAuthenticator{Bearer: authn.NewStatic(map[string]authn.Principal{"user": {Subject: "11", Method: "oidc"}})}, service, 1024, 4096)
	request := httptest.NewRequest(http.MethodPost, "/v1/account/api-tokens", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer user")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), "expires_at") ||
		strings.Contains(response.Body.String(), "repository_ids") {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestAdminDelegatedTokenRouteMintsNarrowedToken(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	service := &account.Service{Manager: authn.TokenManager{Store: &accountStoreStub{}, Now: func() time.Time { return now }, Rand: strings.NewReader(strings.Repeat("x", 32))}}
	mux := http.NewServeMux()
	principals := map[string]authn.Principal{
		"admin-token": {Subject: "11", Method: "api_token", Administrator: true, RepositoryIDs: []int64{101, 102}},
		"user-token":  {Subject: "12", Method: "api_token", RepositoryIDs: []int64{101}},
		"admin-oidc":  {Subject: "11", Method: "oidc", Administrator: true, RepositoryIDs: []int64{101}},
	}
	RegisterAccount(mux, authn.RequestAuthenticator{Bearer: authn.NewStatic(principals)}, service, 1024, 4096)
	call := func(token, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/admin/api-tokens", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}

	response := call("admin-token", `{"expires_at":"2026-08-01T00:15:00Z","repository_ids":[101]}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var created struct {
		Token         string  `json:"token"`
		RepositoryIDs []int64 `json:"repository_ids"`
		ExpiresAt     string  `json:"expires_at"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, "gnp_") || len(created.RepositoryIDs) != 1 || created.RepositoryIDs[0] != 101 || created.ExpiresAt != "2026-08-01T00:15:00Z" {
		t.Fatalf("created=%+v", created)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q: a secret-bearing response must not be cached", response.Header().Get("Cache-Control"))
	}

	for name, tc := range map[string]struct {
		token, body string
		want        int
	}{
		"ordinary token":        {"user-token", `{"expires_at":"2026-08-01T00:15:00Z","repository_ids":[101]}`, http.StatusForbidden},
		"interactive admin":     {"admin-oidc", `{"expires_at":"2026-08-01T00:15:00Z","repository_ids":[101]}`, http.StatusForbidden},
		"outside ceiling":       {"admin-token", `{"expires_at":"2026-08-01T00:15:00Z","repository_ids":[999]}`, http.StatusForbidden},
		"missing expiry":        {"admin-token", `{"repository_ids":[101]}`, http.StatusBadRequest},
		"expiry beyond an hour": {"admin-token", `{"expires_at":"2026-08-01T01:00:01Z","repository_ids":[101]}`, http.StatusBadRequest},
		"empty ceiling":         {"admin-token", `{"expires_at":"2026-08-01T00:15:00Z","repository_ids":[]}`, http.StatusBadRequest},
		"unknown field":         {"admin-token", `{"expires_at":"2026-08-01T00:15:00Z","repository_ids":[101],"admin":true}`, http.StatusBadRequest},
	} {
		if response := call(tc.token, tc.body); response.Code != tc.want {
			t.Errorf("%s: status=%d want=%d body=%s", name, response.Code, tc.want, response.Body.String())
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/admin/api-tokens", nil)
	request.Header.Set("Authorization", "Bearer admin-token")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status=%d", response.Code)
	}
}
