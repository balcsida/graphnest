package oauthas

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
)

func TestAuthorizationRejectsForcedPasswordRotation(t *testing.T) {
	h := newHarness(t)
	h.server.Sessions = staticSessions{principal: authn.Principal{Subject: "11", Method: authn.ProviderLocal, ForceRotation: true}}
	request := httptest.NewRequest(http.MethodGet, ResumePath, nil)
	request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: sessionTokenValue})
	if _, ok := h.server.sessionPrincipal(request); ok {
		t.Fatal("mandatory password rotation must prevent OAuth consent")
	}
}

func TestAuthorizationUsesConfiguredLogin(t *testing.T) {
	for _, githubSync := range []bool{false, true} {
		h := newHarness(t)
		h.server.LoginPath = "/auth/oidc/corp/login"
		h.server.GitHubLoginPath = "/auth/oauth/github/login"
		if !githubSync {
			h.server.GitHub = nil
		}
		client := h.registerClient(t, "http://127.0.0.1:5000/cb")
		_, challenge := pkce()
		request := httptest.NewRequest(http.MethodGet, authorizeURL(client, "http://127.0.0.1:5000/cb", challenge), nil)
		if githubSync {
			// Existing GraphNest sessions must still obtain a fresh GitHub token.
			request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: sessionTokenValue})
		}
		response := h.do(request)
		requestCookie := cookieNamed(response, RequestCookie)
		resume := ResumePath + "?request_id=" + strings.TrimPrefix(requestCookie.Name, RequestCookie+"_")
		want := h.server.LoginPath
		if githubSync {
			want = h.server.GitHubLoginPath
		}
		want += "?return_to=" + url.QueryEscape(resume)
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != want {
			t.Errorf("GitHub sync=%v status=%d location=%q want=%q", githubSync, response.Code, response.Header().Get("Location"), want)
		}
	}
}
