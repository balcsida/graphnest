package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testBuild() fstest.MapFS {
	return fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><title>console</title>")},
		"favicon.svg":      {Data: []byte("<svg/>")},
		"assets/app-1.js":  {Data: []byte("console.log(1)")},
		"assets/sub/a.css": {Data: []byte("body{}")},
	}
}

func testMux(build fstest.MapFS) *http.ServeMux {
	mux := http.NewServeMux()
	register(mux, build)
	return mux
}

func serve(mux *http.ServeMux, method, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(method, path, nil))
	return response
}

func requireSecurityHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	for name, want := range map[string]string{
		"Cross-Origin-Opener-Policy": "same-origin",
		"Permissions-Policy":         "camera=(), geolocation=(), microphone=(), payment=(), usb=()",
		"Referrer-Policy":            "no-referrer",
		"X-Content-Type-Options":     "nosniff",
		"X-Frame-Options":            "DENY",
	} {
		if got := response.Header().Get(name); got != want {
			t.Fatalf("%s=%q want %q", name, got, want)
		}
	}
}

func TestHTMLRoutesServeIndexWithPolicy(t *testing.T) {
	mux := testMux(testBuild())
	for _, path := range []string{"/", "/index.html", "/repositories", "/admin", "/admin/", "/account", "/account/", "/supply-chain", "/supply-chain/", "/admin/jobs", "/supply-chain/licenses"} {
		response := serve(mux, http.MethodGet, path)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "<title>console</title>") {
			t.Fatalf("%s status=%d body=%q", path, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s cache-control=%q", path, got)
		}
		if got := response.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
			t.Fatalf("%s csp=%q", path, got)
		}
		if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Fatalf("%s content-type=%q", path, got)
		}
		requireSecurityHeaders(t, response)
	}
}

func TestContentSecurityPolicyIsExact(t *testing.T) {
	want := "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'"
	if contentSecurityPolicy != want {
		t.Fatalf("csp=%q", contentSecurityPolicy)
	}
}

func TestAssetsAreImmutableAndNeverListed(t *testing.T) {
	mux := testMux(testBuild())
	for path, body := range map[string]string{"/assets/app-1.js": "console.log(1)", "/assets/sub/a.css": "body{}"} {
		response := serve(mux, http.MethodGet, path)
		if response.Code != http.StatusOK || response.Body.String() != body {
			t.Fatalf("%s status=%d body=%q", path, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Fatalf("%s cache-control=%q", path, got)
		}
		requireSecurityHeaders(t, response)
	}
	for _, path := range []string{"/assets/", "/assets", "/assets/sub", "/assets/sub/", "/assets/missing.js", "/assets/../index.html"} {
		response := serve(mux, http.MethodGet, path)
		if response.Code == http.StatusOK {
			t.Fatalf("%s status=%d body=%q", path, response.Code, response.Body.String())
		}
	}
}

func TestFaviconIsNoStore(t *testing.T) {
	response := serve(testMux(testBuild()), http.MethodGet, "/favicon.svg")
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache-control=%q", response.Code, response.Header().Get("Cache-Control"))
	}
	requireSecurityHeaders(t, response)
}

func TestUnknownPathsAndMethodsAreNotFound(t *testing.T) {
	mux := testMux(testBuild())
	for _, path := range []string{"/missing", "/index.html/", "/repositories/x", "/dist/index.html"} {
		if response := serve(mux, http.MethodGet, path); response.Code != http.StatusNotFound {
			t.Fatalf("GET %s status=%d", path, response.Code)
		}
	}
	for _, path := range []string{"/auth/local", "/missing", "/assets/app-1.js"} {
		if response := serve(mux, http.MethodPost, path); response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s status=%d", path, response.Code)
		}
	}
	if response := serve(mux, http.MethodPost, "/auth/local"); response.Code != http.StatusNotFound {
		t.Fatalf("POST /auth/local status=%d, a catch-all would make it 405", response.Code)
	}
}

func TestMissingBuildAnswers503(t *testing.T) {
	build := fstest.MapFS{}
	if built(build) {
		t.Fatal("empty build reported as built")
	}
	mux := testMux(build)
	for _, path := range []string{"/", "/admin", "/supply-chain/"} {
		response := serve(mux, http.MethodGet, path)
		if response.Code != http.StatusServiceUnavailable || strings.TrimSpace(response.Body.String()) != "web console not built; run make ui" {
			t.Fatalf("%s status=%d body=%q", path, response.Code, response.Body.String())
		}
		requireSecurityHeaders(t, response)
	}
	if response := serve(mux, http.MethodGet, "/assets/app-1.js"); response.Code != http.StatusNotFound {
		t.Fatalf("asset status=%d", response.Code)
	}
}

func TestBuiltDetectsIndex(t *testing.T) {
	if !built(testBuild()) {
		t.Fatal("build with index.html reported as missing")
	}
}
