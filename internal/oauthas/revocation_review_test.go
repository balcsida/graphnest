package oauthas

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
)

func TestRevocationAuditsOnlyNewlyRevokedGrants(t *testing.T) {
	for _, prefix := range []string{AccessTokenPrefix, RefreshTokenPrefix} {
		t.Run(prefix, func(t *testing.T) {
			h := newHarness(t)
			token, hash, err := newSecret(strings.NewReader(strings.Repeat("a", 32)), prefix)
			if err != nil {
				t.Fatal(err)
			}
			grant := authn.OAuthGrant{ClientID: "gnc_owner"}
			if prefix == AccessTokenPrefix {
				grant.AccessHash = hash
			} else {
				grant.RefreshHash = hash
			}
			if _, err := h.store.CreateOAuthGrant(t.Context(), grant); err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				name, token, client string
				wantAudits          int
			}{
				{"unknown", prefix + strings.Repeat("A", 43), grant.ClientID, 0},
				{"wrong client", token, "gnc_other", 0},
				{"new revocation", token, grant.ClientID, 1},
				{"duplicate", token, grant.ClientID, 1},
			} {
				response := h.do(revocationRequest(test.token, test.client))
				if response.Code != http.StatusOK || len(h.audit.events) != test.wantAudits {
					t.Errorf("%s: status=%d audits=%d, want 200 and %d", test.name, response.Code, len(h.audit.events), test.wantAudits)
				}
			}
			if len(h.audit.events) == 0 {
				t.Fatal("revocation did not record an audit")
			}
			if event := h.audit.events[len(h.audit.events)-1]; event.Operation != OperationGrantRevoked || event.Outcome != "success" {
				t.Fatalf("revocation event=%+v", event)
			}
		})
	}
}

func TestRevocationLimiterFailsClosedBeforeRevoking(t *testing.T) {
	for _, test := range []struct {
		name    string
		limiter authn.OAuthRequestLimiter
		status  int
	}{
		{"exhausted", denyAll{}, http.StatusTooManyRequests},
		{"missing", nil, http.StatusServiceUnavailable},
		{"unavailable", failedRevocationLimiter{}, http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			token := AccessTokenPrefix + strings.Repeat("A", 43)
			hash, _ := hashSecret(token, AccessTokenPrefix)
			id, err := h.store.CreateOAuthGrant(t.Context(), authn.OAuthGrant{ClientID: "gnc_owner", AccessHash: hash})
			if err != nil {
				t.Fatal(err)
			}
			h.server.Limiter = test.limiter
			response := h.do(revocationRequest(token, "gnc_owner"))
			if response.Code != test.status || h.store.grants[id].RevokedAt != nil || len(h.audit.events) != 0 {
				t.Fatalf("status=%d revoked=%v audits=%d", response.Code, h.store.grants[id].RevokedAt, len(h.audit.events))
			}
		})
	}
}

func TestRevocationStorageFailureDoesNotAuditSuccess(t *testing.T) {
	h := newHarness(t)
	h.server.Store = failedRevocationStore{OAuthStore: h.store}
	response := h.do(revocationRequest(AccessTokenPrefix+strings.Repeat("A", 43), "gnc_owner"))
	if response.Code != http.StatusServiceUnavailable || len(h.audit.events) != 0 {
		t.Fatalf("status=%d audits=%d", response.Code, len(h.audit.events))
	}
}

func revocationRequest(token, client string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/oauth/revoke", strings.NewReader(url.Values{"token": {token}, "client_id": {client}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

type failedRevocationLimiter struct{}

func (failedRevocationLimiter) AllowOAuthRequest(context.Context, string, string, time.Time) (bool, error) {
	return false, errors.New("limiter unavailable")
}

type failedRevocationStore struct{ authn.OAuthStore }

func (failedRevocationStore) RevokeOAuthGrantByToken(context.Context, [32]byte, string) (bool, error) {
	return false, errors.New("storage unavailable")
}
