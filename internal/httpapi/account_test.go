package httpapi

import (
	"context"
	"encoding/base64"
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

type grantAccountStore struct {
	accountStoreStub
	grants  []authn.OAuthGrantMetadata
	revoked []int64
}

func (s *grantAccountStore) ListOAuthGrants(_ context.Context, _ int64, afterID int64, limit int) ([]authn.OAuthGrantMetadata, bool, error) {
	var grants []authn.OAuthGrantMetadata
	for _, grant := range s.grants {
		if grant.ID > afterID {
			grants = append(grants, grant)
		}
	}
	if len(grants) > limit {
		return grants[:limit], true, nil
	}
	return grants, false, nil
}
func (s *grantAccountStore) RevokeUserOAuthGrantAudited(_ context.Context, _ int64, grantID int64, _ audit.Event) error {
	s.revoked = append(s.revoked, grantID)
	return nil
}

func TestAccountOAuthGrantRoutes(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store := &grantAccountStore{grants: []authn.OAuthGrantMetadata{{ID: 7, ClientName: "OpenCode", CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour)}}}
	service := &account.Service{Manager: authn.TokenManager{Store: store}}
	mux := http.NewServeMux()
	RegisterAccount(mux, authn.RequestAuthenticator{Bearer: authn.NewStatic(map[string]authn.Principal{
		"user":   {Subject: "11", Method: "oauth"},
		"gno_ag": {Subject: "11", Method: authn.ProviderOAuthToken},
	})}, service, 1024, 4096)
	call := func(method, path, token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}
	response := call(http.MethodGet, "/v1/account/oauth-grants", "user")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"client_name":"OpenCode"`) || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call(http.MethodGet, "/v1/account/oauth-grants", "gno_ag"); response.Code != http.StatusForbidden {
		t.Fatalf("access token listing status=%d", response.Code)
	}
	if response := call(http.MethodDelete, "/v1/account/oauth-grants/7", "user"); response.Code != http.StatusNoContent || len(store.revoked) != 1 || store.revoked[0] != 7 {
		t.Fatalf("revoke status=%d revoked=%v", response.Code, store.revoked)
	}
	for _, path := range []string{"/v1/account/oauth-grants/0", "/v1/account/oauth-grants/x", "/v1/account/oauth-grants/7/extra"} {
		if response := call(http.MethodDelete, path, "user"); response.Code != http.StatusBadRequest {
			t.Errorf("%s: status=%d", path, response.Code)
		}
	}
	if response := call(http.MethodPost, "/v1/account/oauth-grants", "user"); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d", response.Code)
	}
}

func TestAccountOAuthGrantListPaginatesWithinWireBudget(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	grants := []authn.OAuthGrantMetadata{
		{ID: 7, ClientName: "Client One", CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour)},
		{ID: 8, ClientName: "Client Two", CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour)},
	}
	cursor := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"id":7}`))
	expected, err := json.Marshal(struct {
		Grants     []account.Grant `json:"grants"`
		Truncated  bool            `json:"truncated"`
		NextCursor string          `json:"next_cursor"`
	}{
		Grants:    []account.Grant{{ID: 7, ClientName: "Client One", CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour)}},
		Truncated: true, NextCursor: cursor,
	})
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	service := &account.Service{Manager: authn.TokenManager{Store: &grantAccountStore{grants: grants}}}
	RegisterAccount(mux, authn.RequestAuthenticator{Bearer: authn.NewStatic(map[string]authn.Principal{
		"user": {Subject: "11", Method: "oauth"},
	})}, service, 1024, int64(len(expected)+1))

	first := accountGrantRequest(t, mux, "/v1/account/oauth-grants")
	if first.Code != http.StatusOK || first.Body.String() != string(expected)+"\n" {
		t.Fatalf("first status=%d bytes=%d body=%s", first.Code, first.Body.Len(), first.Body.String())
	}
	second := accountGrantRequest(t, mux, "/v1/account/oauth-grants?cursor="+cursor)
	var page struct {
		Grants     []account.Grant `json:"grants"`
		Truncated  bool            `json:"truncated"`
		NextCursor string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if second.Code != http.StatusOK || len(page.Grants) != 1 || page.Grants[0].ID != 8 || page.Truncated || page.NextCursor != "" {
		t.Fatalf("second status=%d page=%+v body=%s", second.Code, page, second.Body.String())
	}
}

func TestAccountOAuthGrantListRejectsInvalidCursors(t *testing.T) {
	mux := http.NewServeMux()
	service := &account.Service{Manager: authn.TokenManager{Store: &grantAccountStore{}}}
	RegisterAccount(mux, authn.RequestAuthenticator{Bearer: authn.NewStatic(map[string]authn.Principal{
		"user": {Subject: "11", Method: "oauth"},
	})}, service, 1024, 4096)
	encode := func(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
	for name, cursor := range map[string]string{
		"empty":               "",
		"not base64":          "not-base64",
		"unsupported version": encode(`{"v":2,"id":7}`),
		"missing id":          encode(`{"v":1}`),
		"zero id":             encode(`{"v":1,"id":0}`),
		"unknown field":       encode(`{"v":1,"id":7,"extra":true}`),
		"second JSON value":   encode(`{"v":1,"id":7} {}`),
	} {
		t.Run(name, func(t *testing.T) {
			response := accountGrantRequest(t, mux, "/v1/account/oauth-grants?cursor="+cursor)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestAccountOAuthGrantListRejectsImpossibleWireBudgets(t *testing.T) {
	principal := authn.RequestAuthenticator{Bearer: authn.NewStatic(map[string]authn.Principal{
		"user": {Subject: "11", Method: "oauth"},
	})}
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	t.Run("first grant cannot fit", func(t *testing.T) {
		mux := http.NewServeMux()
		service := &account.Service{Manager: authn.TokenManager{Store: &grantAccountStore{grants: []authn.OAuthGrantMetadata{{
			ID: 7, ClientName: strings.Repeat("x", 512), CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour),
		}}}}}
		const budget = int64(160)
		RegisterAccount(mux, principal, service, 1024, budget)
		request := httptest.NewRequest(http.MethodGet, "/v1/account/oauth-grants", nil)
		request.Header.Set("Authorization", "Bearer user")
		response := httptest.NewRecorder()
		response.Header().Set("X-Request-ID", "request-42")
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusInternalServerError || response.Body.Len() == 0 || int64(response.Body.Len()) > budget || !json.Valid(response.Body.Bytes()) || !strings.Contains(response.Body.String(), `"request_id":"request-42"`) {
			t.Fatalf("status=%d bytes=%d body=%q", response.Code, response.Body.Len(), response.Body.String())
		}
	})

	t.Run("empty envelope cannot fit", func(t *testing.T) {
		mux := http.NewServeMux()
		service := &account.Service{Manager: authn.TokenManager{Store: &grantAccountStore{}}}
		budget := int64(len(`{"grants":[],"truncated":false}`))
		RegisterAccount(mux, principal, service, 1024, budget)
		response := accountGrantRequest(t, mux, "/v1/account/oauth-grants")
		if response.Code != http.StatusInternalServerError || response.Body.Len() != 0 {
			t.Fatalf("status=%d bytes=%d body=%q", response.Code, response.Body.Len(), response.Body.String())
		}
	})
}

func accountGrantRequest(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer user")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}
