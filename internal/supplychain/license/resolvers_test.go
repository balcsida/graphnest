package license

import (
	"compress/gzip"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/supplychain/spdxexpr"
)

// registry is a TLS fake registry whose handler decides per path. Requests
// are counted so "no outbound traffic" assertions are exact.
type registry struct {
	server  *httptest.Server
	handler func(http.ResponseWriter, *http.Request)
	calls   atomic.Int32
	last    atomic.Pointer[http.Request]
}

func newRegistry(t *testing.T, handler func(http.ResponseWriter, *http.Request)) *registry {
	t.Helper()
	r := &registry{handler: handler}
	r.server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		r.calls.Add(1)
		r.last.Store(request.Clone(context.Background()))
		r.handler(writer, request)
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *registry) route(t *testing.T, ecosystem, basePath string) Route {
	t.Helper()
	base, err := url.Parse(r.server.URL + basePath)
	if err != nil {
		t.Fatal(err)
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: r.server.Certificate().Raw})
	// httptest listens on 127.0.0.1, so tests opt into private hosts; the
	// private-host denial is tested separately.
	return Route{Name: ecosystem + ":test", Ecosystem: ecosystem, BaseURL: base, CAPEM: certificate, AllowPrivateHosts: true, Timeout: 5 * time.Second, MaxResponseBytes: 64 << 10}
}

const npmLeftPad = `{"name":"@scope/left-pad","version":"1.3.0","license":"MIT","dist":{"integrity":"sha512-abc","shasum":"deadbeef"}}`

func TestNPMResolverExactVersion(t *testing.T) {
	cases := map[string]struct {
		body      string
		status    int
		outcome   Outcome
		kind      RawKind
		parse     spdxexpr.Status
		normal    string
		raw       string
		wantURL   string
		wantFile  string
		wantPath  string
		wantMsgIn string
	}{
		"plain expression":    {body: npmLeftPad, status: 200, outcome: OutcomeResolved, kind: RawExpression, parse: spdxexpr.StatusParsed, normal: "MIT", raw: "MIT", wantPath: "/registry/@scope%2Fleft-pad/1.3.0"},
		"compound expression": {body: `{"version":"1.3.0","license":"(MIT OR Apache-2.0)"}`, status: 200, outcome: OutcomeResolved, kind: RawExpression, parse: spdxexpr.StatusParsed, normal: "MIT OR Apache-2.0", raw: "(MIT OR Apache-2.0)"},
		"unlicensed":          {body: `{"version":"1.3.0","license":"UNLICENSED"}`, status: 200, outcome: OutcomeResolved, kind: RawExpression, parse: spdxexpr.StatusUnlicensed, raw: "UNLICENSED"},
		"see license in":      {body: `{"version":"1.3.0","license":"SEE LICENSE IN LICENSE.md"}`, status: 200, outcome: OutcomeResolved, kind: RawLicenseFile, parse: NotApplicable, raw: "SEE LICENSE IN LICENSE.md", wantFile: "LICENSE.md", wantMsgIn: "reference to a file"},
		"legacy object":       {body: `{"version":"1.3.0","license":{"type":"BSD-3-Clause","url":"https://example.invalid/LICENSE"}}`, status: 200, outcome: OutcomeResolved, kind: RawLegacyObject, parse: spdxexpr.StatusInvalid, raw: "BSD-3-Clause (https://example.invalid/LICENSE)"},
		"legacy array":        {body: `{"version":"1.3.0","licenses":[{"type":"MIT","url":"https://a"},{"type":"Apache-2.0","url":"https://b"}]}`, status: 200, outcome: OutcomeResolved, kind: RawLegacyObject, parse: spdxexpr.StatusInvalid, raw: "MIT (https://a), Apache-2.0 (https://b)"},
		"array of names":      {body: `{"version":"1.3.0","licenses":["MIT","Apache-2.0"]}`, status: 200, outcome: OutcomeResolved, kind: RawLegacyObject, parse: spdxexpr.StatusInvalid, raw: "MIT, Apache-2.0"},
		"unknown identifier":  {body: `{"version":"1.3.0","license":"Custom-Corp-1.0"}`, status: 200, outcome: OutcomeResolved, kind: RawExpression, parse: spdxexpr.StatusUnknownTerms, normal: "Custom-Corp-1.0", raw: "Custom-Corp-1.0"},
		"free text":           {body: `{"version":"1.3.0","license":"Copyright Acme, all rights reserved"}`, status: 200, outcome: OutcomeResolved, kind: RawExpression, parse: spdxexpr.StatusInvalid, raw: "Copyright Acme, all rights reserved"},
		"missing license":     {body: `{"version":"1.3.0","name":"x"}`, status: 200, outcome: OutcomeNoLicenseMetadata, kind: RawMissing, parse: NotApplicable},
		"empty license":       {body: `{"version":"1.3.0","license":""}`, status: 200, outcome: OutcomeNoLicenseMetadata, kind: RawMissing, parse: NotApplicable},
		"version mismatch":    {body: `{"version":"1.4.0","license":"MIT"}`, status: 200, outcome: OutcomeRejected, kind: RawMissing, parse: NotApplicable, wantMsgIn: "different version"},
		"not found":           {body: `{"error":"Not found"}`, status: 404, outcome: OutcomeNotFound, kind: RawMissing, parse: NotApplicable},
		"forbidden":           {body: `{}`, status: 403, outcome: OutcomeUnavailable, kind: RawMissing, parse: NotApplicable},
		"server error":        {body: `{}`, status: 503, outcome: OutcomeUnavailable, kind: RawMissing, parse: NotApplicable},
		"malformed":           {body: `<html>`, status: 200, outcome: OutcomeMalformed, kind: RawMissing, parse: NotApplicable},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) {
				writer.WriteHeader(test.status)
				fmt.Fprint(writer, test.body)
			})
			resolver, err := NewNPMResolver(r.route(t, "npm", "/registry/"))
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := resolver.Resolve(t.Context(), Coordinates{Ecosystem: "npm", Namespace: "@scope", Name: "left-pad", Version: "1.3.0"})
			if err != nil {
				t.Fatal(err)
			}
			if evidence.Outcome != test.outcome || evidence.RawKind != test.kind || evidence.ParseStatus != test.parse || evidence.NormalizedExpression != test.normal || evidence.RawValue != test.raw {
				t.Fatalf("evidence = outcome %s kind %s parse %s normalized %q raw %q message %q", evidence.Outcome, evidence.RawKind, evidence.ParseStatus, evidence.NormalizedExpression, evidence.RawValue, evidence.Message)
			}
			if evidence.Source != SourceRegistryNPM || evidence.Route != "npm:test" || evidence.ResolverVersion != ResolverVersion || evidence.LicenseListVersion != spdxexpr.ListVersion || evidence.Coordinates.Version != "1.3.0" {
				t.Fatalf("provenance = %+v", evidence)
			}
			if test.wantFile != "" && evidence.LicenseFileName != test.wantFile {
				t.Fatalf("license file = %q", evidence.LicenseFileName)
			}
			if test.wantMsgIn != "" && !strings.Contains(evidence.Message, test.wantMsgIn) {
				t.Fatalf("message = %q", evidence.Message)
			}
			if test.wantPath != "" && r.last.Load().URL.EscapedPath() != test.wantPath {
				t.Fatalf("path = %q, want %q", r.last.Load().URL.EscapedPath(), test.wantPath)
			}
			negativeOutcome := evidence.Outcome != OutcomeResolved
			if negativeOutcome != (evidence.ExpiresAt != nil) {
				t.Fatalf("negative results must expire, positive must not: outcome %s expires %v", evidence.Outcome, evidence.ExpiresAt)
			}
			if evidence.Outcome == OutcomeResolved && len(evidence.ContentSHA256) != 32 {
				t.Fatalf("resolved evidence must hash its content: %+v", evidence.ContentSHA256)
			}
			if r.last.Load().Header.Get("Accept") != "application/json" || r.last.Load().Header.Get("User-Agent") != "GraphNest" {
				t.Fatalf("headers = %v", r.last.Load().Header)
			}
		})
	}
}

func TestNPMResolverNeverUsesDistTags(t *testing.T) {
	r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, "latest") || strings.Count(strings.Trim(request.URL.Path, "/"), "/") < 1 {
			t.Errorf("resolver asked for a dist-tag or packument: %s", request.URL.Path)
		}
		fmt.Fprint(writer, npmLeftPad)
	})
	resolver, _ := NewNPMResolver(r.route(t, "npm", "/"))
	if _, err := resolver.Resolve(t.Context(), Coordinates{Ecosystem: "npm", Namespace: "@scope", Name: "left-pad", Version: "1.3.0"}); err != nil {
		t.Fatal(err)
	}
	if got := r.last.Load().URL.EscapedPath(); got != "/@scope%2Fleft-pad/1.3.0" {
		t.Fatalf("path = %q", got)
	}
	// A missing version is rejected before any request is made.
	before := r.calls.Load()
	evidence, _ := resolver.Resolve(t.Context(), Coordinates{Ecosystem: "npm", Name: "left-pad"})
	if evidence.Outcome != OutcomeRejected || r.calls.Load() != before {
		t.Fatalf("versionless lookup made a request: %+v calls=%d", evidence, r.calls.Load()-before)
	}
}

func TestRouteNamespaceRestrictionBlocksRequests(t *testing.T) {
	r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, `{"version":"1.0.0","license":"MIT"}`)
	})
	route := r.route(t, "npm", "/")
	route.AllowedNamespaces = []string{"@acme"}
	resolver, _ := NewNPMResolver(route)
	evidence, _ := resolver.Resolve(t.Context(), Coordinates{Ecosystem: "npm", Namespace: "@scope", Name: "left-pad", Version: "1.3.0"})
	if evidence.Outcome != OutcomeRejected || r.calls.Load() != 0 {
		t.Fatalf("out-of-scope package reached the registry: %+v calls=%d", evidence, r.calls.Load())
	}
	evidence, _ = resolver.Resolve(t.Context(), Coordinates{Ecosystem: "npm", Namespace: "@acme", Name: "widget", Version: "1.0.0"})
	if evidence.Outcome != OutcomeResolved || r.calls.Load() != 1 {
		t.Fatalf("in-scope package = %+v calls=%d", evidence, r.calls.Load())
	}
}

func TestFetcherRejectsHostileRegistries(t *testing.T) {
	t.Run("cross-origin redirect", func(t *testing.T) {
		other := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) { fmt.Fprint(writer, npmLeftPad) })
		r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, other.server.URL+request.URL.Path, http.StatusFound)
		})
		resolver, _ := NewNPMResolver(r.route(t, "npm", "/"))
		evidence, _ := resolver.Resolve(t.Context(), Coordinates{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0"})
		if evidence.Outcome != OutcomeRejected || other.calls.Load() != 0 {
			t.Fatalf("redirect followed off-route: %+v other calls=%d", evidence, other.calls.Load())
		}
	})
	t.Run("redirect outside base path", func(t *testing.T) {
		r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) {
			if strings.HasPrefix(request.URL.Path, "/registry/") {
				http.Redirect(writer, request, "/admin/secret", http.StatusFound)
				return
			}
			fmt.Fprint(writer, npmLeftPad)
		})
		resolver, _ := NewNPMResolver(r.route(t, "npm", "/registry/"))
		evidence, _ := resolver.Resolve(t.Context(), Coordinates{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0"})
		if evidence.Outcome != OutcomeRejected || r.calls.Load() != 1 {
			t.Fatalf("redirect escaped the base path: %+v calls=%d", evidence, r.calls.Load())
		}
	})
	t.Run("same-origin redirect within base is followed", func(t *testing.T) {
		r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) {
			if strings.HasSuffix(request.URL.Path, "/1.3.0") {
				http.Redirect(writer, request, "/registry/left-pad/1.3.0/", http.StatusMovedPermanently)
				return
			}
			fmt.Fprint(writer, `{"version":"1.3.0","license":"ISC"}`)
		})
		resolver, _ := NewNPMResolver(r.route(t, "npm", "/registry/"))
		evidence, _ := resolver.Resolve(t.Context(), Coordinates{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0"})
		if evidence.Outcome != OutcomeResolved || evidence.NormalizedExpression != "ISC" || r.calls.Load() != 2 {
			t.Fatalf("same-origin redirect = %+v calls=%d", evidence, r.calls.Load())
		}
	})
	t.Run("oversized body", func(t *testing.T) {
		r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) {
			fmt.Fprint(writer, `{"version":"1.3.0","license":"`+strings.Repeat("M", 70<<10)+`"}`)
		})
		resolver, _ := NewNPMResolver(r.route(t, "npm", "/"))
		evidence, _ := resolver.Resolve(t.Context(), Coordinates{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0"})
		if evidence.Outcome != OutcomeTooLarge {
			t.Fatalf("oversized = %+v", evidence)
		}
	})
	t.Run("decompression bomb is bounded", func(t *testing.T) {
		r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Encoding", "gzip")
			compressor := gzip.NewWriter(writer)
			fmt.Fprint(compressor, `{"version":"1.3.0","license":"`+strings.Repeat("A", 1<<20)+`"}`)
			compressor.Close()
		})
		resolver, _ := NewNPMResolver(r.route(t, "npm", "/"))
		evidence, _ := resolver.Resolve(t.Context(), Coordinates{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0"})
		if evidence.Outcome != OutcomeTooLarge {
			t.Fatalf("bomb = %+v", evidence)
		}
	})
	t.Run("private address denied by default", func(t *testing.T) {
		r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) { fmt.Fprint(writer, npmLeftPad) })
		route := r.route(t, "npm", "/")
		route.AllowPrivateHosts = false
		resolver, _ := NewNPMResolver(route)
		evidence, _ := resolver.Resolve(t.Context(), Coordinates{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0"})
		if evidence.Outcome != OutcomeRejected || r.calls.Load() != 0 {
			t.Fatalf("loopback registry reached without AllowPrivateHosts: %+v calls=%d", evidence, r.calls.Load())
		}
	})
	t.Run("untrusted certificate", func(t *testing.T) {
		r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) { fmt.Fprint(writer, npmLeftPad) })
		route := r.route(t, "npm", "/")
		route.CAPEM = nil
		resolver, _ := NewNPMResolver(route)
		evidence, _ := resolver.Resolve(t.Context(), Coordinates{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0"})
		if evidence.Outcome != OutcomeUnavailable {
			t.Fatalf("untrusted TLS = %+v", evidence)
		}
	})
	t.Run("hostile names cannot traverse", func(t *testing.T) {
		r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) { fmt.Fprint(writer, npmLeftPad) })
		resolver, _ := NewNPMResolver(r.route(t, "npm", "/registry/"))
		for _, name := range []string{"..", "a/b", "a?b", "a#b", ".", "a\\b"} {
			evidence, _ := resolver.Resolve(t.Context(), Coordinates{Ecosystem: "npm", Name: name, Version: "1.0.0"})
			if evidence.Outcome != OutcomeRejected {
				t.Fatalf("name %q was requested: %+v", name, evidence)
			}
		}
		if r.calls.Load() != 0 {
			t.Fatalf("hostile names produced %d requests", r.calls.Load())
		}
	})
}

func TestCredentialsStayOnTheRoute(t *testing.T) {
	r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret-token" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(writer, npmLeftPad)
	})
	route := r.route(t, "npm", "/")
	route.BearerToken = "secret-token"
	resolver, _ := NewNPMResolver(route)
	evidence, _ := resolver.Resolve(t.Context(), Coordinates{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0"})
	if evidence.Outcome != OutcomeResolved {
		t.Fatalf("bearer route = %+v", evidence)
	}
	if strings.Contains(fmt.Sprintf("%+v", evidence), "secret-token") {
		t.Fatal("evidence leaks the route credential")
	}
	basic := r.route(t, "npm", "/")
	basic.BasicUser, basic.BasicPassword = "user", "pw"
	resolver, _ = NewNPMResolver(basic)
	evidence, _ = resolver.Resolve(t.Context(), Coordinates{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0"})
	if evidence.Outcome != OutcomeUnavailable || evidence.HTTPStatus == nil || *evidence.HTTPStatus != 401 {
		t.Fatalf("basic auth refused = %+v", evidence)
	}
}

const nuspecExpression = `<?xml version="1.0" encoding="utf-8"?><package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata><id>Newtonsoft.Json</id><version>13.0.3</version><license type="expression">MIT</license><licenseUrl>https://licenses.nuget.org/MIT</licenseUrl></metadata></package>`

func TestNuGetResolverPreservesLicenseKinds(t *testing.T) {
	cases := map[string]struct {
		body    string
		status  int
		outcome Outcome
		kind    RawKind
		parse   spdxexpr.Status
		normal  string
		file    string
		url     string
	}{
		"expression":       {body: nuspecExpression, status: 200, outcome: OutcomeResolved, kind: RawExpression, parse: spdxexpr.StatusParsed, normal: "MIT", url: "https://licenses.nuget.org/MIT"},
		"compound":         {body: strings.Replace(nuspecExpression, `<license type="expression">MIT</license>`, `<license type="expression" version="1.0.0">Apache-2.0 OR MIT</license>`, 1), status: 200, outcome: OutcomeResolved, kind: RawExpression, parse: spdxexpr.StatusParsed, normal: "Apache-2.0 OR MIT", url: "https://licenses.nuget.org/MIT"},
		"file":             {body: strings.Replace(nuspecExpression, `<license type="expression">MIT</license>`, `<license type="file">LICENSE.txt</license>`, 1), status: 200, outcome: OutcomeResolved, kind: RawLicenseFile, parse: NotApplicable, file: "LICENSE.txt", url: "https://licenses.nuget.org/MIT"},
		"legacy url only":  {body: strings.Replace(nuspecExpression, `<license type="expression">MIT</license>`, ``, 1), status: 200, outcome: OutcomeResolved, kind: RawLicenseURL, parse: NotApplicable, url: "https://licenses.nuget.org/MIT"},
		"nothing declared": {body: `<package><metadata><id>Newtonsoft.Json</id><version>13.0.3</version></metadata></package>`, status: 200, outcome: OutcomeNoLicenseMetadata, kind: RawMissing, parse: NotApplicable},
		"wrong version":    {body: strings.Replace(nuspecExpression, "<version>13.0.3</version>", "<version>13.0.4</version>", 1), status: 200, outcome: OutcomeRejected, kind: RawMissing, parse: NotApplicable},
		"wrong id":         {body: strings.Replace(nuspecExpression, "<id>Newtonsoft.Json</id>", "<id>Evil.Json</id>", 1), status: 200, outcome: OutcomeRejected, kind: RawMissing, parse: NotApplicable},
		"not found":        {body: ``, status: 404, outcome: OutcomeNotFound, kind: RawMissing, parse: NotApplicable},
		"malformed":        {body: `<package><metadata>`, status: 200, outcome: OutcomeMalformed, kind: RawMissing, parse: NotApplicable},
		"external entity":  {body: `<?xml version="1.0"?><!DOCTYPE p [<!ENTITY x SYSTEM "file:///etc/passwd">]><package><metadata><id>Newtonsoft.Json</id><version>13.0.3</version><license type="expression">&x;</license></metadata></package>`, status: 200, outcome: OutcomeMalformed, kind: RawMissing, parse: NotApplicable},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) {
				writer.WriteHeader(test.status)
				fmt.Fprint(writer, test.body)
			})
			resolver, err := NewNuGetResolver(r.route(t, "nuget", "/v3-flatcontainer/"))
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := resolver.Resolve(t.Context(), Coordinates{Ecosystem: "nuget", Name: "Newtonsoft.Json", Version: "13.0.3"})
			if err != nil {
				t.Fatal(err)
			}
			if evidence.Outcome != test.outcome || evidence.RawKind != test.kind || evidence.ParseStatus != test.parse || evidence.NormalizedExpression != test.normal || evidence.LicenseFileName != test.file || evidence.LicenseURL != test.url {
				t.Fatalf("evidence = outcome %s kind %s parse %s normalized %q file %q url %q message %q", evidence.Outcome, evidence.RawKind, evidence.ParseStatus, evidence.NormalizedExpression, evidence.LicenseFileName, evidence.LicenseURL, evidence.Message)
			}
			if got := r.last.Load().URL.EscapedPath(); got != "/v3-flatcontainer/newtonsoft.json/13.0.3/newtonsoft.json.nuspec" {
				t.Fatalf("path = %q", got)
			}
			if evidence.Source != SourceRegistryNuGet || evidence.Route != "nuget:test" {
				t.Fatalf("provenance = %+v", evidence)
			}
		})
	}
}

func TestNuGetResolverRejectsWithoutVersion(t *testing.T) {
	r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) { fmt.Fprint(writer, nuspecExpression) })
	resolver, _ := NewNuGetResolver(r.route(t, "nuget", "/"))
	evidence, _ := resolver.Resolve(t.Context(), Coordinates{Ecosystem: "nuget", Name: "Newtonsoft.Json"})
	if evidence.Outcome != OutcomeRejected || r.calls.Load() != 0 {
		t.Fatalf("versionless = %+v calls=%d", evidence, r.calls.Load())
	}
}

func mavenPOM(group, artifact, version, parent, licenses, properties string) string {
	return `<?xml version="1.0"?><project xmlns="http://maven.apache.org/POM/4.0.0"><modelVersion>4.0.0</modelVersion>` + parent +
		`<groupId>` + group + `</groupId><artifactId>` + artifact + `</artifactId><version>` + version + `</version>` + properties + licenses + `</project>`
}

func mavenParent(group, artifact, version string) string {
	return `<parent><groupId>` + group + `</groupId><artifactId>` + artifact + `</artifactId><version>` + version + `</version></parent>`
}

func TestMavenResolverDeclarationsAndInheritance(t *testing.T) {
	poms := map[string]string{
		"/maven2/org/example/core/2.1.0/core-2.1.0.pom":               mavenPOM("org.example", "core", "2.1.0", "", `<licenses><license><name>The Apache Software License, Version 2.0</name><url>https://www.apache.org/licenses/LICENSE-2.0.txt</url></license></licenses>`, ""),
		"/maven2/org/example/child/1.0.0/child-1.0.0.pom":             mavenPOM("", "child", "", mavenParent("org.example", "parent", "3.0.0"), "", ""),
		"/maven2/org/example/parent/3.0.0/parent-3.0.0.pom":           mavenPOM("org.example", "parent", "3.0.0", mavenParent("org.example", "grandparent", "1.0.0"), "", ""),
		"/maven2/org/example/grandparent/1.0.0/grandparent-1.0.0.pom": mavenPOM("org.example", "grandparent", "1.0.0", "", `<licenses><license><name>${license.name}</name><url>${license.url}</url></license></licenses>`, `<properties><license.name>MIT License</license.name><license.url>https://opensource.org/licenses/MIT</license.url></properties>`),
		"/maven2/org/example/cyclic-a/1.0.0/cyclic-a-1.0.0.pom":       mavenPOM("org.example", "cyclic-a", "1.0.0", mavenParent("org.example", "cyclic-b", "1.0.0"), "", ""),
		"/maven2/org/example/cyclic-b/1.0.0/cyclic-b-1.0.0.pom":       mavenPOM("org.example", "cyclic-b", "1.0.0", mavenParent("org.example", "cyclic-a", "1.0.0"), "", ""),
		"/maven2/org/example/orphan/1.0.0/orphan-1.0.0.pom":           mavenPOM("org.example", "orphan", "1.0.0", mavenParent("org.example", "missing-parent", "9.9.9"), "", ""),
		"/maven2/org/example/dual/1.0.0/dual-1.0.0.pom":               mavenPOM("org.example", "dual", "1.0.0", "", `<licenses><license><name>MIT License</name></license><license><name>Apache License, Version 2.0</name></license></licenses>`, ""),
		"/maven2/org/example/urlonly/1.0.0/urlonly-1.0.0.pom":         mavenPOM("org.example", "urlonly", "1.0.0", "", `<licenses><license><url>https://example.invalid/LICENSE</url></license></licenses>`, ""),
		"/maven2/org/example/nolicense/1.0.0/nolicense-1.0.0.pom":     mavenPOM("org.example", "nolicense", "1.0.0", "", "", ""),
		"/maven2/org/example/customname/1.0.0/customname-1.0.0.pom":   mavenPOM("org.example", "customname", "1.0.0", "", `<licenses><license><name>Acme Internal License</name></license></licenses>`, ""),
		"/maven2/org/example/spdxname/1.0.0/spdxname-1.0.0.pom":       mavenPOM("org.example", "spdxname", "1.0.0", "", `<licenses><license><name>EPL-2.0 OR GPL-2.0-only WITH Classpath-exception-2.0</name></license></licenses>`, ""),
		"/maven2/org/example/unresolved/1.0.0/unresolved-1.0.0.pom":   mavenPOM("org.example", "unresolved", "1.0.0", mavenParent("${parent.group}", "parent", "3.0.0"), "", ""),
		"/maven2/org/example/foreign/1.0.0/foreign-1.0.0.pom":         mavenPOM("org.example", "foreign", "1.0.0", mavenParent("com.other", "parent", "1.0.0"), "", ""),
		"/maven2/org/example/selfref/1.0.0/selfref-1.0.0.pom":         mavenPOM("org.example", "selfref", "1.0.0", "", `<licenses><license><name>${a}</name></license></licenses>`, `<properties><a>${b}</a><b>${a}</b></properties>`),
	}
	var deep strings.Builder
	for level := 0; level <= maxParentDepth+1; level++ {
		_ = deep
		parent := ""
		if level < maxParentDepth+1 {
			parent = mavenParent("org.example", fmt.Sprintf("deep-%d", level+1), "1.0.0")
		}
		poms[fmt.Sprintf("/maven2/org/example/deep-%d/1.0.0/deep-%d-1.0.0.pom", level, level)] = mavenPOM("org.example", fmt.Sprintf("deep-%d", level), "1.0.0", parent, "", "")
	}
	r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) {
		body, ok := poms[request.URL.EscapedPath()]
		if !ok {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(writer, body)
	})
	route := r.route(t, "maven", "/maven2/")
	route.AllowedNamespaces = []string{"org.example"}
	resolver, err := NewMavenResolver(route)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		artifact string
		outcome  Outcome
		kind     RawKind
		parse    spdxexpr.Status
		normal   string
		raw      string
		url      string
		msgIn    string
		calls    int32
	}{
		"apache name normalized":    {artifact: "core", outcome: OutcomeResolved, kind: RawLicenseName, parse: spdxexpr.StatusParsed, normal: "Apache-2.0", raw: "The Apache Software License, Version 2.0", url: "https://www.apache.org/licenses/LICENSE-2.0.txt", calls: 1},
		"inherited through parents": {artifact: "child", outcome: OutcomeResolved, kind: RawLicenseName, parse: spdxexpr.StatusParsed, normal: "MIT", raw: "MIT License", url: "https://opensource.org/licenses/MIT", calls: 3},
		"cycle detected":            {artifact: "cyclic-a", outcome: OutcomeNoLicenseMetadata, kind: RawMissing, parse: NotApplicable, msgIn: "cyclic", calls: 3},
		"missing parent":            {artifact: "orphan", outcome: OutcomeNoLicenseMetadata, kind: RawMissing, parse: NotApplicable, msgIn: "could not be fetched", calls: 2},
		"depth bounded":             {artifact: "deep-0", outcome: OutcomeNoLicenseMetadata, kind: RawMissing, parse: NotApplicable, msgIn: "depth", calls: maxParentDepth + 1},
		"multiple licenses":         {artifact: "dual", outcome: OutcomeResolved, kind: RawLicenseName, parse: spdxexpr.StatusInvalid, normal: "", raw: "MIT License; Apache License, Version 2.0", msgIn: "no defined AND/OR", calls: 1},
		"url only":                  {artifact: "urlonly", outcome: OutcomeResolved, kind: RawLicenseURL, parse: NotApplicable, raw: "https://example.invalid/LICENSE", url: "https://example.invalid/LICENSE", msgIn: "not a concluded", calls: 1},
		"no license":                {artifact: "nolicense", outcome: OutcomeNoLicenseMetadata, kind: RawMissing, parse: NotApplicable, msgIn: "no <licenses>", calls: 1},
		"custom name kept":          {artifact: "customname", outcome: OutcomeResolved, kind: RawLicenseName, parse: spdxexpr.StatusInvalid, raw: "Acme Internal License", calls: 1},
		"spdx expression as name":   {artifact: "spdxname", outcome: OutcomeResolved, kind: RawLicenseName, parse: spdxexpr.StatusParsed, normal: "EPL-2.0 OR GPL-2.0-only WITH Classpath-exception-2.0", raw: "EPL-2.0 OR GPL-2.0-only WITH Classpath-exception-2.0", calls: 1},
		"unresolved property":       {artifact: "unresolved", outcome: OutcomeNoLicenseMetadata, kind: RawMissing, parse: NotApplicable, msgIn: "could not be resolved", calls: 1},
		"foreign parent group":      {artifact: "foreign", outcome: OutcomeNoLicenseMetadata, kind: RawMissing, parse: NotApplicable, msgIn: "outside the configured route", calls: 1},
		"self-referential property": {artifact: "selfref", outcome: OutcomeResolved, kind: RawLicenseName, parse: spdxexpr.StatusInvalid, raw: "${a}", calls: 1},
		"not found":                 {artifact: "absent", outcome: OutcomeNotFound, kind: RawMissing, parse: NotApplicable, calls: 1},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			r.calls.Store(0)
			evidence, err := resolver.Resolve(t.Context(), Coordinates{Ecosystem: "maven", Namespace: "org.example", Name: test.artifact, Version: "1.0.0"})
			if test.artifact == "core" {
				evidence, err = resolver.Resolve(t.Context(), Coordinates{Ecosystem: "maven", Namespace: "org.example", Name: "core", Version: "2.1.0"})
				r.calls.Store(1)
			}
			if err != nil {
				t.Fatal(err)
			}
			if evidence.Outcome != test.outcome || evidence.RawKind != test.kind || evidence.ParseStatus != test.parse || evidence.NormalizedExpression != test.normal || evidence.RawValue != test.raw || evidence.LicenseURL != test.url {
				t.Fatalf("evidence = outcome %s kind %s parse %s normalized %q raw %q url %q message %q", evidence.Outcome, evidence.RawKind, evidence.ParseStatus, evidence.NormalizedExpression, evidence.RawValue, evidence.LicenseURL, evidence.Message)
			}
			if test.msgIn != "" && !strings.Contains(evidence.Message, test.msgIn) {
				t.Fatalf("message = %q, want it to contain %q", evidence.Message, test.msgIn)
			}
			if r.calls.Load() != test.calls {
				t.Fatalf("registry calls = %d, want %d", r.calls.Load(), test.calls)
			}
			if evidence.Source != SourceRegistryMaven || evidence.Route != "maven:test" {
				t.Fatalf("provenance = %+v", evidence)
			}
		})
	}
}

func TestMavenResolverRejectsBeforeRequesting(t *testing.T) {
	r := newRegistry(t, func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, mavenPOM("g", "a", "1", "", "", ""))
	})
	route := r.route(t, "maven", "/maven2/")
	route.AllowedNamespaces = []string{"org.example"}
	resolver, _ := NewMavenResolver(route)
	for _, coordinates := range []Coordinates{
		{Ecosystem: "maven", Namespace: "org.example", Name: "core"},
		{Ecosystem: "maven", Name: "core", Version: "1.0"},
		{Ecosystem: "maven", Namespace: "com.other", Name: "core", Version: "1.0"},
		{Ecosystem: "maven", Namespace: "org.example", Name: "../etc", Version: "1.0"},
		{Ecosystem: "maven", Namespace: "org.example", Name: "core", Version: "1.0/../../x"},
	} {
		evidence, _ := resolver.Resolve(t.Context(), coordinates)
		if evidence.Outcome != OutcomeRejected {
			t.Fatalf("%+v = %+v", coordinates, evidence)
		}
	}
	if r.calls.Load() != 0 {
		t.Fatalf("rejected coordinates produced %d requests", r.calls.Load())
	}
}

func TestRoutesFromEnvDefaultsToNoRoutes(t *testing.T) {
	routes, err := RoutesFromEnv(func(string) string { return "" }, func(string) ([]byte, error) { return nil, errors.New("unexpected read") })
	if err != nil || len(routes) != 0 {
		t.Fatalf("routes = %v, %v", routes, err)
	}
	env := map[string]string{
		"GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_URL":             "https://npm.example/registry",
		"GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_TOKEN_FILE":      "/run/secrets/npm",
		"GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_NAMESPACES":      "@acme, @Internal",
		"GRAPHNEST_SUPPLY_CHAIN_REGISTRY_MAVEN_URL":           "https://maven.example/repository/public/",
		"GRAPHNEST_SUPPLY_CHAIN_REGISTRY_MAVEN_ALLOW_PRIVATE": "true",
		"GRAPHNEST_SUPPLY_CHAIN_REGISTRY_MAVEN_BASIC_FILE":    "/run/secrets/maven",
	}
	files := map[string]string{"/run/secrets/npm": "tok\n", "/run/secrets/maven": "deploy:pw"}
	routes, err = RoutesFromEnv(func(key string) string { return env[key] }, func(path string) ([]byte, error) { return []byte(files[path]), nil })
	if err != nil || len(routes) != 2 {
		t.Fatalf("routes = %+v, %v", routes, err)
	}
	npm, maven := routes[0], routes[1]
	if npm.Ecosystem != "npm" || npm.BaseURL.String() != "https://npm.example/registry/" || npm.BearerToken != "tok" || npm.AllowPrivateHosts || strings.Join(npm.AllowedNamespaces, ",") != "@acme,@internal" || npm.Name != "npm:npm.example" {
		t.Fatalf("npm route = %+v", npm)
	}
	if maven.Ecosystem != "maven" || !maven.AllowPrivateHosts || maven.BasicUser != "deploy" || maven.BasicPassword != "pw" || maven.BearerToken != "" {
		t.Fatalf("maven route = %+v", maven)
	}
	for name, bad := range map[string]map[string]string{
		"http":             {"GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_URL": "http://npm.example/"},
		"credentials":      {"GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_URL": "https://user:pw@npm.example/"},
		"query":            {"GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_URL": "https://npm.example/?x=1"},
		"orphan token":     {"GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NUGET_TOKEN_FILE": "/x"},
		"bad bool":         {"GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_URL": "https://npm.example/", "GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_ALLOW_PRIVATE": "yes"},
		"both credentials": {"GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_URL": "https://npm.example/", "GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_TOKEN_FILE": "/run/secrets/npm", "GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_BASIC_FILE": "/run/secrets/maven"},
		"empty token":      {"GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_URL": "https://npm.example/", "GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_TOKEN_FILE": "/run/secrets/empty"},
	} {
		if _, err := RoutesFromEnv(func(key string) string { return bad[key] }, func(path string) ([]byte, error) { return []byte(files[path]), nil }); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestPublicAddressPolicy(t *testing.T) {
	for address, public := range map[string]bool{
		"93.184.216.34": true, "2606:2800:220:1:248:1893:25c8:1946": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false, "169.254.169.254": false, "100.64.0.1": false,
		"0.0.0.0": false, "::1": false, "fe80::1": false, "fd00::1": false, "::ffff:10.0.0.1": false, "224.0.0.1": false, "198.18.0.1": false,
	} {
		parsed, err := parseAddress(address)
		if err != nil {
			t.Fatal(err)
		}
		if publicAddress(parsed) != public {
			t.Fatalf("publicAddress(%s) = %v, want %v", address, !public, public)
		}
	}
}

func TestEvidenceFingerprintChangesWithMaterialFields(t *testing.T) {
	base := Evidence{Source: SourceRegistryNPM, Route: "npm:test", Coordinates: Coordinates{Ecosystem: "npm", Name: "a", Version: "1"}, RawValue: "MIT", RawKind: RawExpression, ParseStatus: spdxexpr.StatusParsed, NormalizedExpression: "MIT", Outcome: OutcomeResolved}
	same := base
	same.FetchedAt = time.Now()
	same.Detail = map[string]any{"integrity": "x"}
	if string(base.Fingerprint()) != string(same.Fingerprint()) {
		t.Fatal("fetch time and detail must not change the fingerprint")
	}
	changed := base
	changed.RawValue, changed.NormalizedExpression = "ISC", "ISC"
	if string(base.Fingerprint()) == string(changed.Fingerprint()) {
		t.Fatal("a different license must change the fingerprint")
	}
	otherRoute := base
	otherRoute.Route = "npm:private"
	if string(base.Fingerprint()) == string(otherRoute.Fingerprint()) {
		t.Fatal("evidence from another route must not collide")
	}
}

func TestCertificateHelper(t *testing.T) {
	r := newRegistry(t, func(http.ResponseWriter, *http.Request) {})
	if _, err := x509.ParseCertificate(r.server.Certificate().Raw); err != nil {
		t.Fatal(err)
	}
}
