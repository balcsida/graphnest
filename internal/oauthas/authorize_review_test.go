package oauthas

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
)

type authorizationStore struct {
	*memoryStore
	lookups    int
	insertions int
}

func (store *authorizationStore) OAuthClient(ctx context.Context, id string, now time.Time) (authn.OAuthClient, error) {
	store.lookups++
	return store.memoryStore.OAuthClient(ctx, id, now)
}

func (store *authorizationStore) CreateOAuthAuthorizationRequest(ctx context.Context, request authn.OAuthAuthorizationRequest) error {
	store.insertions++
	return store.memoryStore.CreateOAuthAuthorizationRequest(ctx, request)
}

type authorizationLimiter struct {
	allowed  bool
	err      error
	calls    int
	endpoint string
}

func (limiter *authorizationLimiter) AllowOAuthRequest(_ context.Context, _, endpoint string, _ time.Time) (bool, error) {
	limiter.calls++
	limiter.endpoint = endpoint
	return limiter.allowed, limiter.err
}

func TestAuthorizationRateLimitPrecedesClientLookup(t *testing.T) {
	for _, test := range []struct {
		name       string
		limiter    *authorizationLimiter
		wantStatus int
	}{
		{name: "denied", limiter: &authorizationLimiter{}, wantStatus: http.StatusTooManyRequests},
		{name: "missing", wantStatus: http.StatusServiceUnavailable},
		{name: "failed", limiter: &authorizationLimiter{err: errors.New("database unavailable")}, wantStatus: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newHarness(t)
			clientID := harness.registerClient(t, "http://127.0.0.1:5000/cb")
			store := &authorizationStore{memoryStore: harness.store}
			harness.server.Store = store
			if test.limiter == nil {
				harness.server.Limiter = nil
			} else {
				harness.server.Limiter = test.limiter
			}
			_, challenge := pkce()
			request := httptest.NewRequest(http.MethodGet, authorizeURL(clientID, "http://127.0.0.1:5000/cb", challenge), nil)
			request.RemoteAddr = "192.0.2.1:12345"

			response := harness.do(request)

			if response.Code != test.wantStatus {
				t.Fatalf("status=%d, want %d", response.Code, test.wantStatus)
			}
			if store.lookups != 0 || store.insertions != 0 {
				t.Fatalf("client lookups=%d authorization insertions=%d, want zero", store.lookups, store.insertions)
			}
			if test.limiter != nil && (test.limiter.calls != 1 || test.limiter.endpoint != "/oauth/authorize") {
				t.Fatalf("limiter calls=%d endpoint=%q", test.limiter.calls, test.limiter.endpoint)
			}
		})
	}
}
