package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/authz"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/search"
	"github.com/grepnest/grepnest/pkg/api"
)

func TestSearchHTTP(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		contentType string
		body        string
		token       string
		backendErr  error
		maxBytes    int64
		status      int
		backendCall bool
		code        string
		message     string
		retryable   bool
	}{
		{"valid search", http.MethodPost, "application/json", `{"query":"needle","repositories":["acme/one"]}`, "secret", nil, 1024, http.StatusOK, true, "", "", false},
		{"missing bearer", http.MethodPost, "application/json", `{"query":"needle"}`, "", nil, 1024, http.StatusUnauthorized, false, "unauthenticated", "authentication required", false},
		{"forbidden repository", http.MethodPost, "application/json", `{"query":"needle","repositories":["acme/two"]}`, "secret", nil, 1024, http.StatusOK, false, "", "", false},
		{"wrong content type", http.MethodPost, "application/json; charset=utf-8", `{"query":"needle"}`, "secret", nil, 1024, http.StatusUnsupportedMediaType, false, "invalid_request", "request is invalid", false},
		{"unknown JSON field", http.MethodPost, "application/json", `{"query":"needle","extra":true}`, "secret", nil, 1024, http.StatusBadRequest, false, "invalid_request", "request is invalid", false},
		{"oversized body", http.MethodPost, "application/json", `{"query":"needle"}`, "secret", nil, 8, http.StatusRequestEntityTooLarge, false, "invalid_request", "request is invalid", false},
		{"trailing data exceeds body limit", http.MethodPost, "application/json", `{"query":"needle"}x`, "secret", nil, 18, http.StatusRequestEntityTooLarge, false, "invalid_request", "request is invalid", false},
		{"wrong method", http.MethodGet, "application/json", `{"query":"needle"}`, "secret", nil, 1024, http.StatusMethodNotAllowed, false, "invalid_request", "request is invalid", false},
		{"backend timeout", http.MethodPost, "application/json", `{"query":"needle"}`, "secret", context.DeadlineExceeded, 1024, http.StatusGatewayTimeout, true, "timeout", "search timed out", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := &stubBackend{err: test.backendErr}
			handler := testHandler(t, backend, test.maxBytes)
			request := httptest.NewRequest(test.method, "/v1/search", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.status, response.Body.String())
			}
			if backend.called != test.backendCall {
				t.Fatalf("backend called = %v, want %v", backend.called, test.backendCall)
			}
			if test.status == http.StatusOK {
				var got api.SearchResponse
				if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
					t.Fatal(err)
				}
				return
			}
			assertSafeError(t, response.Body.String(), test.token, test.code, test.message, test.retryable)
		})
	}
}

func TestSearchResponseRespectsWireBudgetIncludingNewline(t *testing.T) {
	backend := &stubBackend{response: api.SearchResponse{Matches: []api.SearchMatch{{Path: "main.go", SHA: strings.Repeat("a", 40), Branches: []string{"main"}, ZoektID: 7, LineNumber: 1, Preview: "needle"}}}}
	registry, err := repository.NewStatic([]repository.Repository{{ID: 1, ZoektID: 7, Name: "acme/one", Branch: "main", IndexedSHA: strings.Repeat("a", 40)}})
	if err != nil {
		t.Fatal(err)
	}
	principal := authn.Principal{Subject: "user", RepositoryNames: []string{"acme/one"}}
	probe := search.NewService(backend, authz.NewStatic(registry), search.Limits{MaxResults: 100, MaxResponseBytes: 256 << 10})
	response, err := probe.Search(t.Context(), principal, api.SearchRequest{Query: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	service := search.NewService(backend, authz.NewStatic(registry), search.Limits{MaxResults: 100, MaxResponseBytes: int64(len(payload))})
	mux := http.NewServeMux()
	RegisterSearch(mux, requestAuthenticator(authn.NewStatic(map[string]authn.Principal{"secret": principal})), service, 1024, int64(len(payload)))

	request := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"query":"needle"}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError || recorder.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q, want bodyless wire-budget failure", recorder.Code, recorder.Body.String())
	}
}

func TestBearerAuthenticationAttachesPrincipal(t *testing.T) {
	authenticator := authn.NewStatic(map[string]authn.Principal{"secret": {Subject: "user"}})
	var got authn.Principal
	handler := AuthenticateBearer(authenticator, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		got = PrincipalFromContext(request.Context())
	}))
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Authorization", "Bearer secret")

	handler.ServeHTTP(httptest.NewRecorder(), request)

	if got.Subject != "user" {
		t.Fatalf("principal = %#v", got)
	}
}

func TestBearerAuthenticationRejectsAnythingButOneCredential(t *testing.T) {
	authenticator := authn.NewStatic(map[string]authn.Principal{"secret": {Subject: "user"}})
	for _, values := range [][]string{nil, {"Basic secret"}, {"Bearer"}, {"Bearer secret extra"}, {"Bearer secret", "Bearer secret"}} {
		t.Run(strings.Join(values, ";"), func(t *testing.T) {
			called := false
			handler := AuthenticateBearer(authenticator, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			request := httptest.NewRequest(http.MethodPost, "/", nil)
			for _, value := range values {
				request.Header.Add("Authorization", value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized || called {
				t.Fatalf("status=%d called=%v", response.Code, called)
			}
			assertSafeError(t, response.Body.String(), "secret", "unauthenticated", "authentication required", false)
		})
	}
}

func testHandler(t *testing.T, backend *stubBackend, maxBytes int64) http.Handler {
	t.Helper()
	registry, err := repository.NewStatic([]repository.Repository{{ID: 1, ZoektID: 7, Name: "acme/one"}, {ID: 2, ZoektID: 8, Name: "acme/two"}})
	if err != nil {
		t.Fatal(err)
	}
	service := search.NewService(backend, authz.NewStatic(registry), search.Limits{MaxResults: 100})
	mux := http.NewServeMux()
	RegisterSearch(mux, requestAuthenticator(authn.NewStatic(map[string]authn.Principal{"secret": {Subject: "user", RepositoryNames: []string{"acme/one"}}})), service, maxBytes, 256<<10)
	return mux
}

func assertSafeError(t *testing.T, body, secret, code, message string, retryable bool) {
	t.Helper()
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("invalid error JSON: %v; body=%q", err, body)
	}
	if envelope.Error.Code != code || envelope.Error.Message != message || envelope.Error.RequestID == "" || envelope.Error.Retryable != retryable {
		t.Fatalf("unsafe error envelope: %s", body)
	}
	if secret != "" && strings.Contains(body, secret) {
		t.Fatalf("error echoed credential: %s", body)
	}
}

type stubBackend struct {
	called   bool
	err      error
	response api.SearchResponse
}

func (backend *stubBackend) Search(context.Context, search.BackendRequest) (api.SearchResponse, error) {
	backend.called = true
	return backend.response, backend.err
}

func (backend *stubBackend) Health(context.Context) error { return errors.New("unused") }
