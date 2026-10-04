package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

const contentSecurityPolicy = "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; " +
	"connect-src 'self'; img-src 'self' data:; font-src 'self'; script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'"

var htmlRoutes = []string{
	"GET /{$}", "GET /index.html", "GET /repositories", "GET /admin", "GET /admin/",
	"GET /account", "GET /account/", "GET /supply-chain", "GET /supply-chain/",
}

func embeddedBuild() fs.FS {
	build, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	return build
}

// Built reports whether the web console build is embedded in this binary.
func Built() bool {
	return built(embeddedBuild())
}

// Register mounts the console routes. There is deliberately no catch-all
// pattern: it would turn the 404 of unknown non-GET paths into a 405.
func Register(mux *http.ServeMux) {
	register(mux, embeddedBuild())
}

func built(build fs.FS) bool {
	info, err := fs.Stat(build, "index.html")
	return err == nil && !info.IsDir()
}

func register(mux *http.ServeMux, build fs.FS) {
	for _, route := range htmlRoutes {
		mux.HandleFunc(route, func(writer http.ResponseWriter, request *http.Request) {
			serveIndex(writer, build)
		})
	}
	mux.HandleFunc("GET /assets/", func(writer http.ResponseWriter, request *http.Request) {
		serveFile(writer, request, build, "assets/"+strings.TrimPrefix(request.URL.Path, "/assets/"), "public, max-age=31536000, immutable")
	})
	mux.HandleFunc("GET /favicon.svg", func(writer http.ResponseWriter, request *http.Request) {
		serveFile(writer, request, build, "favicon.svg", "no-store")
	})
}

func securityHeaders(writer http.ResponseWriter) {
	header := writer.Header()
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=(), usb=()")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}

func serveIndex(writer http.ResponseWriter, build fs.FS) {
	securityHeaders(writer)
	body, err := fs.ReadFile(build, "index.html")
	if err != nil {
		writer.Header().Set("Cache-Control", "no-store")
		http.Error(writer, "web console not built; run make ui", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = writer.Write(body)
}

func serveFile(writer http.ResponseWriter, request *http.Request, build fs.FS, name, cacheControl string) {
	securityHeaders(writer)
	if !fs.ValidPath(name) {
		http.NotFound(writer, request)
		return
	}
	file, err := build.Open(name)
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	seeker, ok := file.(interface {
		Read([]byte) (int, error)
		Seek(int64, int) (int64, error)
	})
	if err != nil || info.IsDir() || !ok {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Cache-Control", cacheControl)
	http.ServeContent(writer, request, info.Name(), info.ModTime(), seeker)
}
