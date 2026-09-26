package githubapp

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDependencySBOMReducesSPDXAndRefreshes401(t *testing.T) {
	var tokenRequests, sbomRequests int
	var client *Client
	payload := `{"sbom":{"SPDXID":"SPDXRef-DOCUMENT","documentDescribes":["SPDXRef-root"],"packages":[{"SPDXID":"SPDXRef-root","name":"ignored","externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:golang/example.com/acme/app@v1"},{"referenceType":"cpe23Type","referenceLocator":"ignored"}]},{"SPDXID":"SPDXRef-dep","externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:npm/acme@1.0.0"}]}],"relationships":[{"spdxElementId":"SPDXRef-root","relationshipType":"DEPENDS_ON","relatedSpdxElement":"SPDXRef-dep"},{"spdxElementId":"SPDXRef-root","relationshipType":"CONTAINS","relatedSpdxElement":"SPDXRef-dep"}]}}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v3/app/installations/10/access_tokens":
			tokenRequests++
			fmt.Fprintf(w, `{"token":"token-%d","expires_at":"2026-07-18T13:00:00Z"}`, tokenRequests)
		case "/api/v3/repos/acme/repo/dependency-graph/sbom":
			sbomRequests++
			assertRequest(t, r, http.MethodGet, fmt.Sprintf("Bearer token-%d", sbomRequests))
			if sbomRequests == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, payload)
		default:
			t.Fatalf("unexpected path %q", r.URL.EscapedPath())
		}
	}))
	defer server.Close()
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	client = testClient(t, server, &now, int64(len(payload)))

	got, available, err := client.DependencySBOM(t.Context(), 10, "acme", "repo")
	want := SBOM{
		DocumentSPDXID:    "SPDXRef-DOCUMENT",
		DocumentDescribes: []string{"SPDXRef-root"},
		Packages:          []SBOMPackage{{SPDXID: "SPDXRef-root", PURLs: []string{"pkg:golang/example.com/acme/app@v1"}}, {SPDXID: "SPDXRef-dep", PURLs: []string{"pkg:npm/acme@1.0.0"}}},
		Relationships:     []SBOMRelationship{{SPDXElementID: "SPDXRef-root", Type: "DEPENDS_ON", RelatedSPDXElement: "SPDXRef-dep"}, {SPDXElementID: "SPDXRef-root", Type: "CONTAINS", RelatedSPDXElement: "SPDXRef-dep"}},
	}
	if err != nil || !available || !reflect.DeepEqual(got, want) {
		t.Fatalf("DependencySBOM() = %#v, %v, %v", got, available, err)
	}
	if tokenRequests != 2 || sbomRequests != 2 {
		t.Fatalf("requests = token %d, sbom %d", tokenRequests, sbomRequests)
	}
}

func TestDependencySBOMUnavailable(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := dependencyClient(t, status, `{}`)
			_, available, err := client.DependencySBOM(t.Context(), 10, "acme", "repo")
			if err != nil || available {
				t.Fatalf("available=%v err=%v", available, err)
			}
		})
	}
}

func TestDependencySBOMPreservesOtherStatusError(t *testing.T) {
	client := dependencyClient(t, http.StatusInternalServerError, `{}`)
	_, available, err := client.DependencySBOM(t.Context(), 10, "acme", "repo")
	var statusError HTTPStatusError
	if available || !errors.As(err, &statusError) || statusError.StatusCode != http.StatusInternalServerError {
		t.Fatalf("available=%v error=%v", available, err)
	}
}

func TestDependencySBOMBoundsResponse(t *testing.T) {
	client := dependencyClient(t, http.StatusOK, strings.Repeat("x", 1025))
	_, _, err := client.DependencySBOM(t.Context(), 10, "acme", "repo")
	if err == nil || !strings.Contains(err.Error(), "response too large") {
		t.Fatalf("error = %v", err)
	}
}

func dependencyClient(t *testing.T, status int, body string) *Client {
	t.Helper()
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.EscapedPath(), "/access_tokens") {
			fmt.Fprint(w, `{"token":"token","expires_at":"2026-07-18T13:00:00Z"}`)
			return
		}
		if r.URL.EscapedPath() != "/api/v3/repos/acme/repo/dependency-graph/sbom" {
			t.Errorf("path = %q", r.URL.EscapedPath())
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return testClient(t, server, &now, 1024)
}

func TestDependencySBOMDocumentPreservesBytesVerbatim(t *testing.T) {
	// The member is deliberately non-canonical (odd spacing, key order, unknown
	// members, unicode escape) so re-encoding would be detectable.
	member := "{\"spdxVersion\" : \"SPDX-2.3\",\"unknownFutureField\":{\"a\":[1,2 ,3]},\"name\":\"acme\\u002Fwidgets\",\"packages\":[]}"
	envelope := "{\n  \"sbom\": " + member + "\n}\n"
	var client *Client
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.EscapedPath(), "/access_tokens") {
			fmt.Fprint(w, `{"token":"token","expires_at":"2026-07-18T13:00:00Z"}`)
			return
		}
		assertRequest(t, r, http.MethodGet, "Bearer token")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		fmt.Fprint(w, envelope)
	}))
	defer server.Close()
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	client = testClient(t, server, &now, 1<<20)
	got, err := client.DependencySBOMDocument(t.Context(), 10, "acme", "repo", 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Body) != member {
		t.Fatalf("body = %q, want verbatim member %q", got.Body, member)
	}
	if got.Status != http.StatusOK || got.MediaType != "application/json; charset=utf-8" || !got.FetchedAt.Equal(now) || got.RateLimit.Remaining != 4999 || got.RateLimit.Limited {
		t.Fatalf("document = %+v", got)
	}
}

func TestDependencySBOMDocumentClassifiesFailures(t *testing.T) {
	cases := map[string]struct {
		status      int
		headers     map[string]string
		rateLimited bool
		retryAfter  time.Duration
	}{
		"forbidden":               {status: http.StatusForbidden},
		"not found":               {status: http.StatusNotFound},
		"rate limited 403":        {status: http.StatusForbidden, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1784376000"}, rateLimited: true},
		"too many requests":       {status: http.StatusTooManyRequests, headers: map[string]string{"Retry-After": "120"}, rateLimited: true, retryAfter: 2 * time.Minute},
		"retry after date":        {status: http.StatusServiceUnavailable, headers: map[string]string{"Retry-After": "Sat, 18 Jul 2026 12:05:00 GMT"}, rateLimited: true, retryAfter: 5 * time.Minute},
		"retry after bounded":     {status: http.StatusTooManyRequests, headers: map[string]string{"Retry-After": "999999999"}, rateLimited: true, retryAfter: 24 * time.Hour},
		"retry after in the past": {status: http.StatusBadGateway, headers: map[string]string{"Retry-After": "Sat, 18 Jul 2026 11:00:00 GMT"}},
		"server error":            {status: http.StatusInternalServerError},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.EscapedPath(), "/access_tokens") {
					fmt.Fprint(w, `{"token":"token","expires_at":"2026-07-18T13:00:00Z"}`)
					return
				}
				for key, value := range test.headers {
					w.Header().Set(key, value)
				}
				w.WriteHeader(test.status)
				fmt.Fprint(w, `{"message":"secret detail that must not be retained"}`)
			}))
			defer server.Close()
			client := testClient(t, server, &now, 1<<20)
			got, err := client.DependencySBOMDocument(t.Context(), 10, "acme", "repo", 0)
			var sbomError SBOMError
			if !errors.As(err, &sbomError) {
				t.Fatalf("error = %v, want SBOMError", err)
			}
			if sbomError.Status != test.status || got.Status != test.status || len(got.Body) != 0 {
				t.Fatalf("document = %+v, error = %+v", got, sbomError)
			}
			if sbomError.IsRateLimited() != test.rateLimited || sbomError.RetryAfter != test.retryAfter {
				t.Fatalf("rate limited = %v retry after = %v, want %v %v", sbomError.IsRateLimited(), sbomError.RetryAfter, test.rateLimited, test.retryAfter)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error retains response body: %v", err)
			}
		})
	}
}

func TestDependencySBOMDocumentRejectsOversizedAndMalformed(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	for name, test := range map[string]struct {
		body string
		max  int64
		want error
	}{
		"too large":     {body: `{"sbom":{"spdxVersion":"SPDX-2.3"}}`, max: 10, want: ErrSBOMTooLarge},
		"not an object": {body: `{"sbom":[1]}`, max: 1024, want: ErrSBOMMalformed},
		"missing sbom":  {body: `{"other":{}}`, max: 1024, want: ErrSBOMMalformed},
		"trailing":      {body: `{"sbom":{}} {}`, max: 1024, want: ErrSBOMMalformed},
		"not json":      {body: `<html>`, max: 1024, want: ErrSBOMMalformed},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.EscapedPath(), "/access_tokens") {
					fmt.Fprint(w, `{"token":"token","expires_at":"2026-07-18T13:00:00Z"}`)
					return
				}
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			client := testClient(t, server, &now, 1<<20)
			_, err := client.DependencySBOMDocument(t.Context(), 10, "acme", "repo", test.max)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}
