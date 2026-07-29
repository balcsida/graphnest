package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/observability"
)

type httpSession struct{ principal authn.Principal }

func requestAuthenticator(bearer authn.Authenticator) authn.RequestAuthenticator {
	return authn.RequestAuthenticator{Bearer: bearer}
}

func (s httpSession) Authenticate(context.Context, string) (authn.Principal, error) {
	return s.principal, nil
}

func TestAuthenticateRequestWritesGenericErrorAndAttachesPrincipalOnce(t *testing.T) {
	metrics := observability.New()
	handler := AuthenticateRequest(authn.RequestAuthenticator{Session: httpSession{principal: authn.Principal{Subject: "session"}}, Metrics: metrics}, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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
	metricsResponse := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{
		`grepnest_auth_events_total{event="session_auth",provider="session",result="success"} 1`,
		`grepnest_auth_events_total{event="session_auth",provider="unknown",result="invalid"} 1`,
	} {
		if !strings.Contains(metricsResponse.Body.String(), want) {
			t.Errorf("metrics missing %q:\n%s", want, metricsResponse.Body.String())
		}
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
