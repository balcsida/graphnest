package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWrapHTTPRecordsRequests(t *testing.T) {
	metrics := New()
	handler := metrics.WrapHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(recorder.Body.String(), "grepnest_http_requests_total") {
		t.Fatalf("metrics = %s", recorder.Body.String())
	}
}

func TestWrapHTTPRecordsFirstStatus(t *testing.T) {
	metrics := New()
	handler := metrics.WrapHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing", nil))

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(recorder.Body.String(), "path=\"/missing\",status=\"404\"") {
		t.Fatalf("metrics = %s", recorder.Body.String())
	}
}
