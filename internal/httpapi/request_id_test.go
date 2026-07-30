package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grepnest/grepnest/internal/audit"
)

func TestRequestIDBoundaryGeneratesAndIgnoresIncomingHeader(t *testing.T) {
	var got string
	handler := RequestIDs(bytes.NewReader(bytes.Repeat([]byte{0xab}, 16)), http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		got = audit.RequestID(request.Context())
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Request-ID", "sentinel-client-request-id")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if got != "abababababababababababababababab" {
		t.Fatalf("request ID=%q", got)
	}
}

func TestRequestIDBoundaryReusesTrustedContext(t *testing.T) {
	var got string
	handler := RequestIDs(bytes.NewReader(nil), http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		got = audit.RequestID(request.Context())
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(audit.WithRequestID(request.Context(), "trusted-request"))
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if got != "trusted-request" {
		t.Fatalf("request ID=%q", got)
	}
}
