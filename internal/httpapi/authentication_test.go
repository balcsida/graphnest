package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
)

type httpSession struct{ principal authn.Principal }

func (s httpSession) Authenticate(context.Context, string) (authn.Principal, error) {
	return s.principal, nil
}

func TestAuthenticateRequestWritesGenericErrorAndAttachesPrincipalOnce(t *testing.T) {
	handler := AuthenticateRequest(authn.RequestAuthenticator{Session: httpSession{principal: authn.Principal{Subject: "session"}}}, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := PrincipalFromContext(request.Context()); got.Subject != "session" {
			t.Fatalf("principal = %#v", got)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: "session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), `"code":"unauthenticated"`) || !strings.Contains(response.Body.String(), `"message":"authentication required"`) {
		t.Fatalf("generic authentication response = %d %q", response.Code, response.Body.String())
	}
}

func TestAuthenticateBearerRejectsSessionCookie(t *testing.T) {
	handler := AuthenticateBearer(authn.NewStatic(map[string]authn.Principal{"bearer": {Subject: "bearer"}}), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler called")
	}))
	for _, name := range []string{"cookie only", "mixed"} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: "session"})
			if name == "mixed" {
				request.Header.Set("Authorization", "Bearer bearer")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d", response.Code)
			}
		})
	}
}
