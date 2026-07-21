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
