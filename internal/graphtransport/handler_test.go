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
}

func (engine fakeEngine) Context(ctx context.Context, request graphprotocol.ContextRequest) (graphprotocol.ContextResponse, error) {
	if engine.contextFn != nil {
		return engine.contextFn(ctx, request)
	}
	return graphprotocol.ContextResponse{Status: graphprotocol.StatusFound}, nil
}

func (fakeEngine) Impact(context.Context, graphprotocol.ImpactRequest) (graphprotocol.ImpactResponse, error) {
	return graphprotocol.ImpactResponse{Status: graphprotocol.StatusOK}, nil
}

func (fakeEngine) Trace(context.Context, graphprotocol.TraceRequest) (graphprotocol.TraceResponse, error) {
	return graphprotocol.TraceResponse{Status: graphprotocol.StatusNoPath}, nil
}

func (fakeEngine) Cypher(context.Context, graphprotocol.CypherRequest) (graphprotocol.CypherResponse, error) {
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
	for _, secret := range [][]byte{nil, {}} {
		if handler := NewHandler(secret, fakeEngine{}, testLimits()); handler == nil {
			t.Fatal("NewHandler returned nil")
		} else if got := request(handler, http.MethodPost, "/internal/v1/graph/context", "", "application/json", `{}`); got.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d", got.Code)
		}
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
