package graphtransport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/graphprotocol"
)

type fakeEngine struct {
	contextFn func(context.Context, graphprotocol.ContextRequest) (graphprotocol.ContextResponse, error)
	impactFn  func(context.Context, graphprotocol.ImpactRequest) (graphprotocol.ImpactResponse, error)
	traceFn   func(context.Context, graphprotocol.TraceRequest) (graphprotocol.TraceResponse, error)
	cypherFn  func(context.Context, graphprotocol.CypherRequest) (graphprotocol.CypherResponse, error)
}

func (engine fakeEngine) Context(ctx context.Context, request graphprotocol.ContextRequest) (graphprotocol.ContextResponse, error) {
	if engine.contextFn != nil {
		return engine.contextFn(ctx, request)
	}
	return graphprotocol.ContextResponse{Status: graphprotocol.StatusFound}, nil
}

func (engine fakeEngine) Impact(ctx context.Context, request graphprotocol.ImpactRequest) (graphprotocol.ImpactResponse, error) {
	if engine.impactFn != nil {
		return engine.impactFn(ctx, request)
	}
	return graphprotocol.ImpactResponse{Status: graphprotocol.StatusOK}, nil
}

func (engine fakeEngine) Trace(ctx context.Context, request graphprotocol.TraceRequest) (graphprotocol.TraceResponse, error) {
	if engine.traceFn != nil {
		return engine.traceFn(ctx, request)
	}
	return graphprotocol.TraceResponse{Status: graphprotocol.StatusNoPath}, nil
}

func (engine fakeEngine) Cypher(ctx context.Context, request graphprotocol.CypherRequest) (graphprotocol.CypherResponse, error) {
	if engine.cypherFn != nil {
		return engine.cypherFn(ctx, request)
	}
	return graphprotocol.CypherResponse{Columns: []string{"value"}, Rows: [][]any{{"safe"}}}, nil
}

func testLimits() Limits {
	return Limits{MaxRequestBytes: 512, MaxResponseBytes: 512, RequestTimeout: 50 * time.Millisecond}
}

func request(handler http.Handler, method, path, secret, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestHandlerRejectsInvalidConfiguration(t *testing.T) {
	for _, secret := range [][]byte{nil, {}, []byte("has space"), []byte("bad\nsecret")} {
		if handler := NewHandler(secret, fakeEngine{}, testLimits()); handler == nil {
			t.Fatal("NewHandler returned nil")
		} else if got := request(handler, http.MethodPost, "/internal/v1/graph/context", "", "application/json", `{}`); got.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d", got.Code)
		}
	}
}

func TestHandlerRejectsMalformedBearerValues(t *testing.T) {
	handler := NewHandler([]byte("abc+/=="), fakeEngine{}, testLimits())
	body := `{"scope":{"repositories":[{"id":1,"commit":"0123456789abcdef0123456789abcdef01234567"}]},"uid":"A"}`
	tests := []struct {
		name   string
		values []string
	}{
		{"duplicate", []string{"Bearer abc+/==", "Bearer abc+/=="}},
		{"wrong scheme", []string{"Basic abc+/=="}},
		{"space", []string{"Bearer abc +/=="}},
		{"control", []string{"Bearer abc\t+/=="}},
		{"empty", []string{"Bearer "}},
		{"padding in middle", []string{"Bearer abc=+/"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/internal/v1/graph/context", strings.NewReader(body))
			req.Header["Authorization"] = test.values
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestHandlerCapsEveryErrorResponse(t *testing.T) {
	limits := testLimits()
	limits.MaxResponseBytes = 1
	valid := `{"scope":{"repositories":[{"id":1,"commit":"0123456789abcdef0123456789abcdef01234567"}]},"uid":"A"}`
	engineError := fakeEngine{contextFn: func(context.Context, graphprotocol.ContextRequest) (graphprotocol.ContextResponse, error) {
		return graphprotocol.ContextResponse{}, errors.New("native statement secret=value")
	}}
	tests := []struct {
		name, method, path, secret, contentType, body string
		engine                                        graphprotocol.QueryEngine
		status                                        int
	}{
		{"auth", "POST", "/internal/v1/graph/context", "wrong", "application/json", valid, fakeEngine{}, 401},
		{"method", "GET", "/internal/v1/graph/context", "right", "application/json", valid, fakeEngine{}, 405},
		{"content type", "POST", "/internal/v1/graph/context", "right", "text/plain", valid, fakeEngine{}, 415},
		{"decode", "POST", "/internal/v1/graph/context", "right", "application/json", `{`, fakeEngine{}, 400},
		{"engine", "POST", "/internal/v1/graph/context", "right", "application/json", valid, engineError, 503},
		{"unknown", "POST", "/unknown", "right", "application/json", valid, fakeEngine{}, 404},
		{"health method", "POST", "/healthz", "", "", "", fakeEngine{}, 405},
		{"health success", "GET", "/healthz", "", "", "", fakeEngine{}, 200},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := request(NewHandler([]byte("right"), test.engine, limits), test.method, test.path, test.secret, test.contentType, test.body)
			if got.Code != test.status || got.Body.Len() > 1 {
				t.Fatalf("status=%d bytes=%d body=%q", got.Code, got.Body.Len(), got.Body.String())
			}
		})
	}
}

func TestHandlerStrictBoundary(t *testing.T) {
	valid := `{"scope":{"repositories":[{"id":1,"commit":"0123456789abcdef0123456789abcdef01234567"}]},"uid":"A"}`
	tests := []struct {
		name, method, path, secret, contentType, body string
		status                                        int
	}{
		{"wrong secret", "POST", "/internal/v1/graph/context", "wrong", "application/json", valid, 401},
		{"missing secret", "POST", "/internal/v1/graph/context", "", "application/json", valid, 401},
		{"wrong method", "GET", "/internal/v1/graph/context", "right", "application/json", valid, 405},
		{"wrong content type", "POST", "/internal/v1/graph/context", "right", "application/json; charset=utf-8", valid, 415},
		{"unknown field", "POST", "/internal/v1/graph/context", "right", "application/json", `{"extra":true}`, 400},
		{"multiple values", "POST", "/internal/v1/graph/context", "right", "application/json", valid + valid, 400},
		{"trailing value", "POST", "/internal/v1/graph/context", "right", "application/json", valid + `x`, 400},
		{"empty scope", "POST", "/internal/v1/graph/context", "right", "application/json", `{"scope":{"repositories":[]},"uid":"A"}`, 400},
		{"bad commit", "POST", "/internal/v1/graph/context", "right", "application/json", `{"scope":{"repositories":[{"id":1,"commit":"ABC"}]},"uid":"A"}`, 400},
		{"oversized", "POST", "/internal/v1/graph/context", "right", "application/json", strings.Repeat("x", 513), 413},
		{"unknown route", "POST", "/internal/v1/graph/nope", "right", "application/json", `{}`, 404},
	}
	handler := NewHandler([]byte("right"), fakeEngine{}, testLimits())
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := request(handler, test.method, test.path, test.secret, test.contentType, test.body)
			if got.Code != test.status {
				t.Fatalf("status=%d body=%s", got.Code, got.Body.String())
			}
		})
	}
}

func TestHandlerHealthDoesNotDependOnAuthorization(t *testing.T) {
	handler := NewHandler([]byte("right"), fakeEngine{}, testLimits())
	for _, path := range []string{"/healthz", "/readyz"} {
		for _, secret := range []string{"", "wrong", "right"} {
			got := request(handler, http.MethodGet, path, secret, "", "")
			if got.Code != http.StatusOK || got.Body.String() != "ok\n" {
				t.Fatalf("%s secret=%q status=%d body=%q", path, secret, got.Code, got.Body.String())
			}
		}
	}
}

func TestHandlerPropagatesTimeoutAndMapsSafeErrors(t *testing.T) {
	engine := fakeEngine{contextFn: func(ctx context.Context, _ graphprotocol.ContextRequest) (graphprotocol.ContextResponse, error) {
		<-ctx.Done()
		return graphprotocol.ContextResponse{}, errors.New("native failed: MATCH secret=$password")
	}}
	handler := NewHandler([]byte("right"), engine, Limits{
		MaxRequestBytes: 512, MaxResponseBytes: 512, ContextTimeout: time.Millisecond,
	})
	body := `{"scope":{"repositories":[{"id":1,"commit":"0123456789abcdef0123456789abcdef01234567"}]},"uid":"password"}`
	got := request(handler, "POST", "/internal/v1/graph/context", "right", "application/json", body)
	if got.Code != http.StatusServiceUnavailable || strings.Contains(got.Body.String(), "MATCH") || strings.Contains(got.Body.String(), "password") {
		t.Fatalf("status=%d body=%s", got.Code, got.Body.String())
	}
}

func TestHandlerMapsInvalidQueryAndCapsResponse(t *testing.T) {
	body := `{"scope":{"repositories":[{"id":1,"commit":"0123456789abcdef0123456789abcdef01234567"}]},"uid":"A"}`
	invalid := fakeEngine{contextFn: func(context.Context, graphprotocol.ContextRequest) (graphprotocol.ContextResponse, error) {
		return graphprotocol.ContextResponse{}, errors.New("invalid graph query")
	}}
	if got := request(NewHandler([]byte("right"), invalid, testLimits()), "POST", "/internal/v1/graph/context", "right", "application/json", body); got.Code != 400 {
		t.Fatalf("invalid status=%d", got.Code)
	}
	large := fakeEngine{contextFn: func(context.Context, graphprotocol.ContextRequest) (graphprotocol.ContextResponse, error) {
		return graphprotocol.ContextResponse{Status: strings.Repeat("x", 600)}, nil
	}}
	limits := testLimits()
	limits.MaxResponseBytes = 64
	if got := request(NewHandler([]byte("right"), large, limits), "POST", "/internal/v1/graph/context", "right", "application/json", body); got.Code != 503 || got.Body.Len() > 64 {
		t.Fatalf("large status=%d bytes=%d", got.Code, got.Body.Len())
	}
}

func TestHandlerDispatchesEveryQueryRoute(t *testing.T) {
	scope := `{"repositories":[{"id":1,"commit":"0123456789abcdef0123456789abcdef01234567"}]}`
	tests := []struct {
		name, path, body, want string
		engine                 fakeEngine
	}{
		{"impact", "/internal/v1/graph/impact", `{"scope":` + scope + `,"target_uid":"A"}`, `"status":"ok"`,
			fakeEngine{impactFn: func(_ context.Context, request graphprotocol.ImpactRequest) (graphprotocol.ImpactResponse, error) {
				if request.TargetUID != "A" {
					return graphprotocol.ImpactResponse{}, errors.New("wrong request")
				}
				return graphprotocol.ImpactResponse{Status: graphprotocol.StatusOK}, nil
			}}},
		{"trace", "/internal/v1/graph/trace", `{"scope":` + scope + `,"source_uid":"A","target_uid":"B"}`, `"status":"no_path"`,
			fakeEngine{traceFn: func(_ context.Context, request graphprotocol.TraceRequest) (graphprotocol.TraceResponse, error) {
				if request.SourceUID != "A" || request.TargetUID != "B" {
					return graphprotocol.TraceResponse{}, errors.New("wrong request")
				}
				return graphprotocol.TraceResponse{Status: graphprotocol.StatusNoPath}, nil
			}}},
		{"cypher", "/internal/v1/graph/cypher", `{"scope":` + scope + `,"admin":true,"statement":"RETURN $value","parameters":{"value":"safe"}}`, `"safe"`,
			fakeEngine{cypherFn: func(_ context.Context, request graphprotocol.CypherRequest) (graphprotocol.CypherResponse, error) {
				if !request.Admin || request.Statement != "RETURN $value" || request.Parameters["value"] != "safe" {
					return graphprotocol.CypherResponse{}, errors.New("wrong request")
				}
				return graphprotocol.CypherResponse{Columns: []string{"value"}, Rows: [][]any{{"safe"}}}, nil
			}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := request(NewHandler([]byte("right"), test.engine, testLimits()), "POST", test.path, "right", "application/json", test.body)
			if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), test.want) {
				t.Fatalf("status=%d body=%s", got.Code, got.Body.String())
			}
		})
	}
}

func TestHandlerRejectsStrictPayloadsOnEveryQueryRoute(t *testing.T) {
	scope := `{"repositories":[{"id":1,"commit":"0123456789abcdef0123456789abcdef01234567"}]}`
	tests := []struct {
		name, path, body string
	}{
		{"impact unknown", "/internal/v1/graph/impact", `{"scope":` + scope + `,"extra":true}`},
		{"trace empty scope", "/internal/v1/graph/trace", `{"scope":{"repositories":[]},"source_uid":"A","target_uid":"B"}`},
		{"cypher invalid scope", "/internal/v1/graph/cypher", `{"scope":{"repositories":[{"id":1,"commit":"bad"}]},"admin":true,"statement":"RETURN 1"}`},
		{"cypher trailing", "/internal/v1/graph/cypher", `{"admin":true,"statement":"RETURN 1"} {}`},
	}
	handler := NewHandler([]byte("right"), fakeEngine{}, testLimits())
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := request(handler, "POST", test.path, "right", "application/json", test.body)
			if got.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", got.Code, got.Body.String())
			}
		})
	}
}

func TestHandlerMapsMissingCypherAdminWithoutLeakingParameters(t *testing.T) {
	engine := fakeEngine{cypherFn: func(_ context.Context, request graphprotocol.CypherRequest) (graphprotocol.CypherResponse, error) {
		if request.Admin {
			return graphprotocol.CypherResponse{}, errors.New("unexpected admin")
		}
		return graphprotocol.CypherResponse{}, errors.New("administrator required")
	}}
	body := `{"statement":"RETURN $password","parameters":{"password":"native-secret"}}`
	got := request(NewHandler([]byte("right"), engine, testLimits()), "POST", "/internal/v1/graph/cypher", "right", "application/json", body)
	if got.Code != http.StatusBadRequest || strings.Contains(got.Body.String(), "password") || strings.Contains(got.Body.String(), "native-secret") {
		t.Fatalf("status=%d body=%s", got.Code, got.Body.String())
	}
}

func TestHandlerTimesOutEveryQueryRouteWithoutLeakingCypher(t *testing.T) {
	wait := func(ctx context.Context) error {
		<-ctx.Done()
		return errors.New("native: RETURN $secret with value")
	}
	engine := fakeEngine{
		impactFn: func(ctx context.Context, _ graphprotocol.ImpactRequest) (graphprotocol.ImpactResponse, error) {
			return graphprotocol.ImpactResponse{}, wait(ctx)
		},
		traceFn: func(ctx context.Context, _ graphprotocol.TraceRequest) (graphprotocol.TraceResponse, error) {
			return graphprotocol.TraceResponse{}, wait(ctx)
		},
		cypherFn: func(ctx context.Context, _ graphprotocol.CypherRequest) (graphprotocol.CypherResponse, error) {
			return graphprotocol.CypherResponse{}, wait(ctx)
		},
	}
	limits := testLimits()
	limits.ImpactTimeout, limits.TraceTimeout, limits.CypherTimeout = time.Millisecond, time.Millisecond, time.Millisecond
	scope := `{"repositories":[{"id":1,"commit":"0123456789abcdef0123456789abcdef01234567"}]}`
	tests := []struct{ path, body string }{
		{"/internal/v1/graph/impact", `{"scope":` + scope + `,"target_uid":"A"}`},
		{"/internal/v1/graph/trace", `{"scope":` + scope + `,"source_uid":"A","target_uid":"B"}`},
		{"/internal/v1/graph/cypher", `{"admin":true,"statement":"RETURN $secret","parameters":{"secret":"value"}}`},
	}
	for _, test := range tests {
		got := request(NewHandler([]byte("right"), engine, limits), "POST", test.path, "right", "application/json", test.body)
		if got.Code != http.StatusServiceUnavailable || strings.Contains(got.Body.String(), "RETURN") || strings.Contains(got.Body.String(), "secret") || strings.Contains(got.Body.String(), "value") {
			t.Fatalf("%s status=%d body=%s", test.path, got.Code, got.Body.String())
		}
	}
}
