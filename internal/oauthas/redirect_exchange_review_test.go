package oauthas

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
)

func TestCodeExchangeRequiresExactAuthorizationRedirect(t *testing.T) {
	tests := []struct {
		name       string
		registered string
		authorized string
		exchanged  string
		wantStatus int
	}{
		{"exact ephemeral loopback port", "http://127.0.0.1:4000/cb", "http://127.0.0.1:5000/cb", "http://127.0.0.1:5000/cb", http.StatusOK},
		{"exact HTTPS URI", "https://ide.example.com/cb?window=1", "https://ide.example.com/cb?window=1", "https://ide.example.com/cb?window=1", http.StatusOK},
		{"omitted", "http://127.0.0.1:5000/cb", "http://127.0.0.1:5000/cb", "", http.StatusBadRequest},
		{"different loopback port", "http://127.0.0.1:4000/cb", "http://127.0.0.1:5000/cb", "http://127.0.0.1:6000/cb", http.StatusBadRequest},
		{"different path", "http://127.0.0.1:5000/cb", "http://127.0.0.1:5000/cb", "http://127.0.0.1:5000/other", http.StatusBadRequest},
		{"different query", "http://127.0.0.1:5000/cb?window=1", "http://127.0.0.1:5000/cb?window=1", "http://127.0.0.1:5000/cb?window=2", http.StatusBadRequest},
		{"different byte spelling", "http://127.0.0.1:5000/%63b", "http://127.0.0.1:5000/%63b", "http://127.0.0.1:5000/cb", http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.server.GitHub = nil
			clientID := h.registerClient(t, test.registered)
			verifier, challenge := pkce()
			code := exchangeReviewCode(t, h, clientID, test.authorized, challenge)
			form := url.Values{
				"grant_type":    {"authorization_code"},
				"code":          {code},
				"client_id":     {clientID},
				"code_verifier": {verifier},
			}
			if test.exchanged != "" {
				form.Set("redirect_uri", test.exchanged)
			}
			response, body := h.exchange(t, form)
			if response.Code != test.wantStatus {
				t.Fatalf("exchange status=%d body=%v want=%d", response.Code, body, test.wantStatus)
			}
			if test.wantStatus == http.StatusOK {
				if access, _ := body["access_token"].(string); !strings.HasPrefix(access, AccessTokenPrefix) || len(h.store.grants) != 1 {
					t.Fatalf("exchange body=%v grants=%d", body, len(h.store.grants))
				}
			} else if body["error"] != "invalid_grant" || len(h.store.grants) != 0 {
				t.Fatalf("exchange body=%v grants=%d", body, len(h.store.grants))
			}
		})
	}
}

func exchangeReviewCode(t *testing.T, h *harness, clientID, redirect, challenge string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, authorizeURL(clientID, redirect, challenge), nil)
	request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: sessionTokenValue})
	response := h.do(request)
	requestCookie := cookieNamed(response, RequestCookie)
	if response.Code != http.StatusOK || requestCookie == nil {
		t.Fatalf("authorize status=%d body=%s cookie=%v", response.Code, response.Body.String(), requestCookie)
	}
	form := url.Values{"request_id": {requestCookie.Value}, "decision": {"allow"}}
	request = httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", origin)
	request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: sessionTokenValue})
	request.AddCookie(requestCookie)
	response = h.do(request)
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil || response.Code != http.StatusSeeOther || !strings.HasPrefix(location.Query().Get("code"), CodePrefix) {
		t.Fatalf("consent status=%d location=%q err=%v", response.Code, response.Header().Get("Location"), err)
	}
	return location.Query().Get("code")
}
