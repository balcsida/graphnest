package oauthas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
)

type failedTokenStorage struct {
	authn.OAuthStore
	cancel context.CancelFunc
}

func (s failedTokenStorage) UpdateOAuthGrantGitHubToken(context.Context, int64, []byte) error {
	if s.cancel != nil {
		s.cancel()
	}
	return errors.New("storage unavailable")
}

func (s failedTokenStorage) RevokeOAuthGrant(ctx context.Context, id int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.OAuthStore.RevokeOAuthGrant(ctx, id)
}

func TestCodeExchangeRequiresGitHubTokenHandoff(t *testing.T) {
	for _, failure := range []string{"different replica", "sealing", "persistence", "canceled request"} {
		t.Run(failure, func(t *testing.T) {
			h := newHarness(t)
			client := h.registerClient(t, "http://127.0.0.1:5000/cb")
			verifier, challenge := pkce()
			response := h.runConsent(t, client, "http://127.0.0.1:5000/cb", challenge, "allow")
			location, _ := url.Parse(response.Header().Get("Location"))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch failure {
			case "different replica":
				h.server.GitHubTokens = NewProviderTokens(nil)
			case "sealing":
				// Two secrets succeed, then the sealing nonce read fails.
				h.server.Rand = bytes.NewReader(make([]byte, 64))
			case "persistence":
				h.server.Store = failedTokenStorage{OAuthStore: h.store}
			case "canceled request":
				h.server.Store = failedTokenStorage{OAuthStore: h.store, cancel: cancel}
			}
			form := url.Values{"grant_type": {"authorization_code"}, "code": {location.Query().Get("code")}, "client_id": {client}, "redirect_uri": {"http://127.0.0.1:5000/cb"}, "code_verifier": {verifier}}
			request := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode())).WithContext(ctx)
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response = h.do(request)
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code == http.StatusOK || body["access_token"] != nil || body["refresh_token"] != nil {
				t.Fatalf("issued tokens without durable provider credentials: status=%d body=%v", response.Code, body)
			}
			for _, grant := range h.store.grants {
				if grant.RevokedAt == nil {
					t.Fatal("failed handoff left an active grant")
				}
			}
		})
	}
}
