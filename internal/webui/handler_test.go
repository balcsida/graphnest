package webui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterServesBoundedConsoleAtExactPaths(t *testing.T) {
	for _, path := range []string{"/", "/index.html"} {
		mux := http.NewServeMux()
		Register(mux)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))

		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d", path, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Fatalf("content type=%q", got)
		}
		for name, want := range map[string]string{
			"Cache-Control":          "no-store",
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "no-referrer",
			"X-Frame-Options":        "DENY",
		} {
			if got := response.Header().Get(name); got != want {
				t.Fatalf("%s=%q, want %q", name, got, want)
			}
		}
		body := response.Body.Bytes()
		if len(body) >= 48<<10 || !bytes.Contains(body, []byte(`data-graphnest-app`)) {
			t.Fatalf("document bytes=%d shell=%t", len(body), bytes.Contains(body, []byte(`data-graphnest-app`)))
		}
		policy := response.Header().Get("Content-Security-Policy")
		if !strings.Contains(policy, "script-src 'sha256-") || !strings.Contains(policy, "style-src 'sha256-") || strings.Contains(policy, "unsafe-inline") {
			t.Fatalf("CSP=%q", policy)
		}
		for _, want := range []string{
			`id="token-form"`, `id="search-form"`, `id="repository-picker"`,
			`id="status"`, `id="file-view"`, `id="navigation-panel"`,
			"prefers-reduced-motion: reduce",
		} {
			if !bytes.Contains(body, []byte(want)) {
				t.Fatalf("%s missing %q", path, want)
			}
		}
		for _, forbidden := range []string{"localStorage", "innerHTML", "outerHTML", "insertAdjacentHTML"} {
			if bytes.Contains(body, []byte(forbidden)) {
				t.Fatalf("%s contains forbidden %q", path, forbidden)
			}
		}
		if bytes.Contains(body, []byte(`id="local-auth"`)) {
			t.Fatal("default console exposes local authentication")
		}
	}
}

func TestRegisterWithBreakGlassServesOIDCFirstRecovery(t *testing.T) {
	mux := http.NewServeMux()
	RegisterWithBreakGlass(mux, true)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	body := response.Body.String()
	for _, marker := range []string{
		`id="provider-options"`, `id="local-auth"`, `<details`, `Administrator recovery`,
		`autocomplete="username"`, `autocomplete="current-password"`, `autocomplete="new-password"`,
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("missing %q", marker)
		}
	}
	if strings.Index(body, `id="provider-options"`) > strings.Index(body, `id="local-auth"`) {
		t.Fatal("administrator recovery appears before SSO")
	}
	for _, forbidden := range []string{"localStorage", "sessionStorage", "innerHTML"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("contains forbidden %q", forbidden)
		}
	}
	if len(body) >= 52<<10 {
		t.Fatalf("document bytes=%d", len(body))
	}
}

func TestRegisterServesAdminWithConsoleSecurityHeaders(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
	policy := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "script-src 'sha256-") || strings.Contains(policy, "unsafe-inline") {
		t.Fatalf("CSP=%q", policy)
	}
}

func TestRegisterRestrictsConsoleRoutes(t *testing.T) {
	for _, test := range []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/missing", http.StatusNotFound},
		{http.MethodGet, "/index.html/", http.StatusNotFound},
		{http.MethodGet, "/admin/", http.StatusNotFound},
		{http.MethodPost, "/", http.StatusMethodNotAllowed},
		{http.MethodPost, "/admin", http.StatusMethodNotAllowed},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			mux := http.NewServeMux()
			Register(mux)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			if response.Code != test.want {
				t.Fatalf("status=%d, want %d", response.Code, test.want)
			}
		})
	}
}
