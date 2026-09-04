package oauthas

import (
	"net/http"
	"net/http/httptest"
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
