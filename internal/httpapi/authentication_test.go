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

// A delegation-only token is an administrator with an empty ceiling, which is
// exactly the shape several admin read paths treat as "all repositories". The
// middleware must therefore refuse it on every route but the one it exists
// for, so no handler has to remember the special case.
func TestAuthenticateRequestConfinesDelegationOnlyTokenToDelegationRoute(t *testing.T) {
	broker := authn.Principal{Subject: "3", Method: "api_token", Administrator: true, DelegationOnly: true}
	reached := 0
	handler := AuthenticateRequest(requestAuthenticator(httpSession{principal: broker}), http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		reached++
		writer.WriteHeader(http.StatusNoContent)
	}))
	call := func(method, path string) int {
		request := httptest.NewRequest(method, path, nil)
		request.Header.Set("Authorization", "Bearer broker")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	if code := call(http.MethodPost, "/v1/admin/api-tokens"); code != http.StatusNoContent || reached != 1 {
		t.Fatalf("delegation route status=%d reached=%d", code, reached)
	}
	for _, path := range []string{"/v1/admin/overview", "/v1/admin/jobs", "/v1/repositories", "/v1/search", "/v1/account/api-tokens", "/v1/scip/uploads"} {
		if code := call(http.MethodGet, path); code != http.StatusForbidden {
			t.Errorf("%s: status=%d want=403", path, code)
		}
	}
	if code := call(http.MethodGet, "/v1/admin/api-tokens"); code != http.StatusForbidden {
		t.Errorf("wrong method on delegation route: status=%d want=403", code)
	}
	if reached != 1 {
		t.Fatalf("handler reached %d times; delegation-only principal leaked past the gate", reached)
	}
}

func TestAuthenticateBearerConfinesDelegationOnlyToken(t *testing.T) {
	broker := authn.Principal{Subject: "3", Method: "api_token", Administrator: true, DelegationOnly: true}
	handler := AuthenticateBearer(httpSession{principal: broker}, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Fatal("delegation-only principal reached a bearer-only route")
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/graph/uploads", nil)
	request.Header.Set("Authorization", "Bearer broker")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d want=403", response.Code)
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

func TestAuthenticateBearerWithChallengeAdvertisesOAuthDiscovery(t *testing.T) {
	challenge := func(writer http.ResponseWriter, invalidToken bool) {
		value := `Bearer resource_metadata="https://gn.example/.well-known/oauth-protected-resource"`
		if invalidToken {
			value = `Bearer error="invalid_token", resource_metadata="https://gn.example/.well-known/oauth-protected-resource"`
		}
		writer.Header().Set("WWW-Authenticate", value)
	}
	handler := AuthenticateBearerWithChallenge(authn.NewStatic(map[string]authn.Principal{"good": {Subject: "s"}}), challenge, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	cases := map[string]struct {
		authorization string
		wantStatus    int
		wantHeader    string
	}{
		"no credential":   {"", http.StatusUnauthorized, `Bearer resource_metadata="https://gn.example/.well-known/oauth-protected-resource"`},
		"bad credential":  {"Bearer nope", http.StatusUnauthorized, `Bearer error="invalid_token", resource_metadata="https://gn.example/.well-known/oauth-protected-resource"`},
		"good credential": {"Bearer good", http.StatusNoContent, ""},
	}
	for name, tc := range cases {
		request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if tc.authorization != "" {
			request.Header.Set("Authorization", tc.authorization)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.wantStatus || response.Header().Get("WWW-Authenticate") != tc.wantHeader {
			t.Errorf("%s: status=%d WWW-Authenticate=%q", name, response.Code, response.Header().Get("WWW-Authenticate"))
		}
	}
	plain := AuthenticateBearer(authn.NewStatic(nil), http.NotFoundHandler())
	response := httptest.NewRecorder()
	plain.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if response.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("plain bearer authentication must not advertise OAuth")
	}
}
