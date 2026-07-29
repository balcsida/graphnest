package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/account"
	"github.com/grepnest/grepnest/internal/authn"
)

type accountStoreStub struct {
	authn.APITokenStore
	listed   []authn.APITokenMetadata
	user, id int64
}

func (*accountStoreStub) CreateAPIToken(context.Context, authn.APITokenRecord) (int64, error) {
	return 3, nil
}
func (s *accountStoreStub) ListAPITokens(context.Context, int64) ([]authn.APITokenMetadata, error) {
	return s.listed, nil
}
func (s *accountStoreStub) RevokeAPIToken(_ context.Context, user, id int64) error {
	s.user, s.id = user, id
	return nil
}

func TestAccountTokenRoutesRevealPlaintextOnlyAtCreation(t *testing.T) {
	// Break caught: account token endpoint leaking a reusable plaintext token in metadata.
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	store := &accountStoreStub{listed: []authn.APITokenMetadata{{ID: 3, Prefix: "gnp_visible", CreatedAt: now}}}
	service := &account.Service{Manager: authn.TokenManager{Store: store, Rand: strings.NewReader(strings.Repeat("x", 32))}}
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
