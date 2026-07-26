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
		if len(body) >= 48<<10 || !bytes.Contains(body, []byte(`data-grepnest-app`)) {
			t.Fatalf("document bytes=%d shell=%t", len(body), bytes.Contains(body, []byte(`data-grepnest-app`)))
		}
		policy := response.Header().Get("Content-Security-Policy")
		if !strings.Contains(policy, "script-src 'sha256-") || !strings.Contains(policy, "style-src 'sha256-") || strings.Contains(policy, "unsafe-inline") {
			t.Fatalf("CSP=%q", policy)
		}
		for _, want := range []string{
			`id="provider-options"`, `id="token-form"`, `id="sign-out"`,
			`id="search-form"`, `id="repository-picker"`, `id="status"`,
			"/v1/auth/config", "/v1/auth/session", "/auth/logout",
			"sessionStorage", "credentials:\"same-origin\"",
			"prefers-reduced-motion: reduce",
			"const SIGN_OUT_FAILURE=",
			"return response.ok",
			"if(!await logout()){showAuth(SIGN_OUT_FAILURE);return}",
			"if(mode===\"session\"&&!await logout()){showError(new Error(SIGN_OUT_FAILURE));return}",
			"if(!config.token_login)clearToken()",
			"if(config.token_login&&remembered)",
		} {
			if !bytes.Contains(body, []byte(want)) {
				t.Fatalf("%s missing %q", path, want)
			}
		}
		for _, forbidden := range []string{
			"localStorage", "innerHTML", "outerHTML", "insertAdjacentHTML",
			"document.write", "eval(", "createElement(\"script\")",
		} {
			if bytes.Contains(body, []byte(forbidden)) {
				t.Fatalf("%s contains forbidden %q", path, forbidden)
			}
		}
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
		{http.MethodPost, "/", http.StatusMethodNotAllowed},
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
