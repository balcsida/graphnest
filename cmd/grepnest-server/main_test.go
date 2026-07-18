package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/grepnest/grepnest/internal/config"
)

func TestNewHandlerRegistersSystemRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repositories.json")
	if err := os.WriteFile(path, []byte(`[{"id":1,"zoekt_id":7,"name":"acme/one"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(config.Config{
		ZoektURL: "http://zoekt.invalid", RepositoriesFile: path,
		UserToken: "user", AdminToken: "admin",
		Limits: config.Limits{MaxRequestBytes: 1024, MaxResponseBytes: 1024, MaxResults: 100, MaxContextLines: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK || response.Body.String() != "ok\n" {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestNewHandlerProtectsMCPRoute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repositories.json")
	if err := os.WriteFile(path, []byte(`[{"id":1,"zoekt_id":7,"name":"acme/one"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(config.Config{
		ZoektURL: "http://zoekt.invalid", RepositoriesFile: path,
		UserToken: "user", AdminToken: "admin",
		Limits: config.Limits{MaxRequestBytes: 1024, MaxResponseBytes: 1024, MaxResults: 100, MaxContextLines: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}
