package webui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
)

//go:embed index.html admin.html
var assets embed.FS

var breakGlassDocument = mustReadDocument()
var document = withoutMarked(withoutMarked(breakGlassDocument, "<!-- break-glass -->", "<!-- /break-glass -->"), "/* break-glass */", "/* /break-glass */")
var contentSecurityPolicy = policyFor(document)
var adminDocument = mustRead("admin.html")
var adminContentSecurityPolicy = policyFor(adminDocument)

func Register(mux *http.ServeMux) {
	RegisterWithBreakGlass(mux, false)
}

func RegisterWithBreakGlass(mux *http.ServeMux, breakGlass bool) {
	index := document
	policy := contentSecurityPolicy
	if breakGlass {
		index = breakGlassDocument
		policy = policyFor(index)
	}
	mux.Handle("GET /{$}", handler(index, policy))
	mux.Handle("GET /index.html", handler(index, policy))
	mux.Handle("GET /admin", handler(adminDocument, adminContentSecurityPolicy))
}

func withoutMarked(document []byte, opening, closing string) []byte {
	found := false
	for {
		start := bytes.Index(document, []byte(opening))
		if start < 0 {
			break
		}
		end := bytes.Index(document[start+len(opening):], []byte(closing))
		if end < 0 {
			panic("embedded index.html has unmatched break-glass markers")
		}
		end += start + len(opening)
		document = bytes.Join([][]byte{document[:start], document[end+len(closing):]}, nil)
		found = true
	}
	if !found {
		panic("embedded index.html must contain break-glass markers")
	}
	return document
}

func handler(body []byte, policy string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Security-Policy", policy)
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		writer.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=(), usb=()")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		_, _ = writer.Write(body)
	})
}

func mustReadDocument() []byte {
	return mustRead("index.html")
}

func mustRead(name string) []byte {
	document, err := assets.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("read embedded %s: %v", name, err))
	}
	return document
}

func policyFor(document []byte) string {
	styleHash := base64.StdEncoding.EncodeToString(sha256Bytes(inlineContent(document, "style")))
	scriptHash := base64.StdEncoding.EncodeToString(sha256Bytes(inlineContent(document, "script")))
	return fmt.Sprintf("default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; connect-src 'self'; script-src 'sha256-%s'; style-src 'sha256-%s'", scriptHash, styleHash)
}

func sha256Bytes(content []byte) []byte {
	hash := sha256.Sum256(content)
	return hash[:]
}

func inlineContent(document []byte, tag string) []byte {
	open, close := "<"+tag+">", "</"+tag+">"
	start := strings.Index(string(document), open)
	if start < 0 || strings.Count(string(document), open) != 1 || strings.Count(string(document), close) != 1 {
		panic("embedded index.html must contain one " + tag + " element")
	}
	start += len(open)
	end := strings.Index(string(document[start:]), close)
	if end < 0 {
		panic("embedded index.html has an unclosed " + tag + " element")
	}
	return document[start : start+end]
}
