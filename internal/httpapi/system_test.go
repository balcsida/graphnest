package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type failingChecker struct{}

func (failingChecker) Health(context.Context) error { return errors.New("unavailable") }

func TestRegisterSystemHealthDoesNotCallDependencies(t *testing.T) {
	mux := http.NewServeMux()
	RegisterSystem(mux, failingChecker{}, http.NotFoundHandler())

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ok\n" {
		t.Fatalf("healthz = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestRegisterSystemReturnsUnavailableWhenNotReady(t *testing.T) {
	mux := http.NewServeMux()
	RegisterSystem(mux, failingChecker{}, http.NotFoundHandler())

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable || recorder.Body.String() != "{\"error\":\"unavailable\"}\n" {
		t.Fatalf("readyz = %d %q", recorder.Code, recorder.Body.String())
	}
}
