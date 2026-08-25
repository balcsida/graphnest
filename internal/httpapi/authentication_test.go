package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/observability"
)

type httpSession struct{ principal authn.Principal }

type contextAuthenticator struct{ ctx context.Context }

func (a *contextAuthenticator) Authenticate(ctx context.Context, _ string) (authn.Principal, error) {
	a.ctx = ctx
	return authn.Principal{Subject: "user"}, nil
}

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
		`graphnest_auth_events_total{event="session_auth",provider="session",result="success"} 1`,
		`graphnest_auth_events_total{event="session_auth",provider="unknown",result="invalid"} 1`,
	} {
		if !strings.Contains(metricsResponse.Body.String(), want) {
			t.Errorf("metrics missing %q:\n%s", want, metricsResponse.Body.String())
		}
	}
}

func TestAuthenticateRequestRejectsForcedRotationSession(t *testing.T) {
	handler := AuthenticateRequest(authn.RequestAuthenticator{Session: httpSession{principal: authn.Principal{
		Subject: "recovery-admin", Method: "local", Administrator: true, ForceRotation: true,
	}}}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler called")
	}))
	request := httptest.NewRequest(http.MethodGet, "/v1/repositories", nil)
	request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: "forced-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
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

func TestBearerMiddlewarePassesCanceledRequestContext(t *testing.T) {
	// Break caught: REST or MCP bearer authentication detaching from cancellation.
	for _, test := range []struct {
		name string
		wrap func(authn.Authenticator, http.Handler) http.Handler
	}{
		{"REST", func(authenticator authn.Authenticator, next http.Handler) http.Handler {
			return AuthenticateRequest(authn.RequestAuthenticator{Bearer: authenticator}, next)
		}},
		{"MCP", AuthenticateBearer},
	} {
		t.Run(test.name, func(t *testing.T) {
			authenticator := &contextAuthenticator{}
			handler := test.wrap(authenticator, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
			request.Header.Set("Authorization", "Bearer token")
			handler.ServeHTTP(httptest.NewRecorder(), request)
			if authenticator.ctx != ctx || authenticator.ctx.Err() != context.Canceled {
				t.Fatalf("authenticator context=%v err=%v", authenticator.ctx, authenticator.ctx.Err())
			}
		})
	}
}
