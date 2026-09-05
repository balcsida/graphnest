package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/oauthas"
	"github.com/balcsida/graphnest/internal/observability"
	"github.com/balcsida/graphnest/internal/sso/browserflow"
)

func TestAuthRuntimeAcceptsCanonicalBrowserOrigins(t *testing.T) {
	settings, endpoints, httpClient := authRuntimeSettings(t)
	settings.SSO.MCPOAuth.Enabled = true
	settings.SSO.SessionIdle = time.Hour
	settings.SSO.SessionTTL = 24 * time.Hour
	for _, test := range []struct{ publicURL, origin, wrongPort string }{
		{"https://graphnest.example/", "https://graphnest.example", "https://graphnest.example:8443"},
		{"https://GraphNest.Example/", "https://graphnest.example", "https://graphnest.example:8443"},
		{"https://graphnest.example:443/", "https://graphnest.example", "https://graphnest.example:8443"},
		{"https://GraphNest.Example:443/", "https://graphnest.example", "https://graphnest.example:8443"},
		{"https://GraphNest.Example:8443/", "https://graphnest.example:8443", "https://graphnest.example"},
		{"https://[2001:DB8::1]:443/", "https://[2001:db8::1]", "https://[2001:db8::1]:8443"},
		{"https://[2001:DB8::1]:8443/", "https://[2001:db8::1]:8443", "https://[2001:db8::1]"},
		{"https://graphnest.example:0443/", "https://graphnest.example", "https://graphnest.example:8443"},
		{"https://graphnest.example:08443/", "https://graphnest.example:8443", "https://graphnest.example"},
		{"https://[2001:0DB8:0000:0000:0000:0000:0000:0001]/", "https://[2001:db8::1]", "https://[2001:db8::1]:8443"},
		{"https://[0:0:0:0:0:ffff:c000:201]/", "https://[::ffff:c000:201]", "https://[::ffff:c000:201]:8443"},
		{"https://[::ffff:0.0.0.1]:08443/", "https://[::ffff:0:1]:8443", "https://[::ffff:0:1]"},
		{"https://bücher.example/", "https://xn--bcher-kva.example", "https://xn--bcher-kva.example:8443"},
	} {
		t.Run(test.publicURL, func(t *testing.T) {
			configured := settings
			configured.SSO.PublicURL, _ = url.Parse(test.publicURL)
			configuredURL := configured.SSO.PublicURL.String()
			raw := make([]byte, 32)
			token := base64.RawURLEncoding.EncodeToString(raw)
			store := originSessionStore{
				oauthCapableStore: oauthCapableStore{client: authn.OAuthClient{ID: "gnc_origin"}},
				hash:              sha256.Sum256(raw),
			}
			runtime, err := newAuthRuntime(t.Context(), configured, store, nil, observability.New(), endpoints, httpClient)
			if err != nil {
				t.Fatal(err)
			}
			if configured.SSO.PublicURL.String() != configuredURL {
				t.Fatalf("public URL changed: %s", configured.SSO.PublicURL)
			}
			for _, provider := range runtime.providers {
				flow := provider.(*browserflow.Provider)
				authorization, err := url.Parse(flow.Client.AuthorizationURL("state", "nonce", strings.Repeat("v", 43)))
				if err != nil {
					t.Fatal(err)
				}
				wantCallback := strings.TrimSuffix(configuredURL, "/") + flow.Spec.CallbackPath
				if authorization.Query().Get("redirect_uri") != wantCallback {
					t.Fatalf("%s callback=%q want=%q", provider.Metadata().ID, authorization.Query().Get("redirect_uri"), wantCallback)
				}
			}
			handler := newAPIHandler(configured, observability.New(), runtime.requestAuth, nil, nil, nil, nil, nil, nil, nil, nil, nil, runtime.providers, runtime.sessions, nil, nil, runtime.mcpOAuth)
			metadata := httptest.NewRecorder()
			handler.ServeHTTP(metadata, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil))
			var document map[string]any
			if err := json.Unmarshal(metadata.Body.Bytes(), &document); err != nil {
				t.Fatal(err)
			}
			if document["issuer"] != test.origin {
				t.Errorf("MCP issuer=%q want=%q", document["issuer"], test.origin)
			}
			requestID := hex.EncodeToString(store.hash[:])
			for _, origin := range []string{test.origin, test.wrongPort, "https://other.example", ""} {
				for _, path := range []string{"/oauth/authorize", "/auth/logout"} {
					request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(url.Values{"request_id": {requestID}, "decision": {"deny"}}.Encode()))
					request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					request.Header.Set("Origin", origin)
					request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: token})
					request.AddCookie(&http.Cookie{Name: oauthas.RequestCookie + "_" + requestID, Value: token})
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, request)
					want := http.StatusNoContent
					if path == "/oauth/authorize" {
						want = http.StatusSeeOther
					}
					if origin != test.origin {
						want = http.StatusUnauthorized
						if path == "/oauth/authorize" {
							want = http.StatusBadRequest
						}
					}
					if response.Code != want {
						t.Errorf("%s Origin=%q: status=%d want=%d body=%s", path, origin, response.Code, want, response.Body)
					}
				}
			}
		})
	}
}

type originSessionStore struct {
	oauthCapableStore
	hash [32]byte
}

func (store originSessionStore) SessionPrincipal(_ context.Context, hash [32]byte, _, _ time.Time) (authn.Principal, error) {
	if hash != store.hash {
		return authn.Principal{}, authn.ErrUnauthenticated
	}
	return authn.Principal{Subject: "11", Method: authn.ProviderOIDC}, nil
}

func (store originSessionStore) RevokeSessionAudited(_ context.Context, hash [32]byte) error {
	if hash != store.hash {
		return authn.ErrUnauthenticated
	}
	return nil
}

func (store originSessionStore) OAuthAuthorizationRequest(_ context.Context, id [32]byte, phase string, now time.Time) (authn.OAuthAuthorizationRequest, error) {
	if id != store.hash || phase != "pending" {
		return authn.OAuthAuthorizationRequest{}, authn.ErrUnauthenticated
	}
	return authn.OAuthAuthorizationRequest{ID: id, Phase: phase, ClientID: store.client.ID, RedirectURI: "http://127.0.0.1/callback", CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}, nil
}

func (originSessionStore) DeleteOAuthAuthorizationRequest(context.Context, [32]byte) error {
	return nil
}
