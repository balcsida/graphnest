package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInstallationTokenCachesSortedRestrictionsAndRefreshes(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/api/v3/app/installations/42/access_tokens" {
			t.Errorf("request = %s %s", r.Method, r.URL.EscapedPath())
		}
		assertHeaders(t, r, "Bearer ")
		var body struct {
			RepositoryIDs []int64 `json:"repository_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !reflect.DeepEqual(body.RepositoryIDs, []int64{2, 7}) {
			t.Errorf("repository_ids = %v", body.RepositoryIDs)
		}
		fmt.Fprintf(w, `{"token":"opaque-token-%d","expires_at":%q}`, requests, now.Add(10*time.Minute).Format(time.RFC3339))
	}))
	defer server.Close()
	client := testClient(t, server, &now, 1024)

	first, err := client.InstallationToken(context.Background(), 42, []int64{7, 2})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.InstallationToken(context.Background(), 42, []int64{2, 7})
	if err != nil {
		t.Fatal(err)
	}
	if first.Value != "opaque-token-1" || second != first || len(first.Value) != 14 || requests != 1 {
		t.Fatalf("tokens = %#v %#v, requests = %d", first, second, requests)
	}
	now = now.Add(9 * time.Minute)
	if _, err := client.InstallationToken(context.Background(), 42, []int64{2, 7}); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestClientPaginatesRetries401AndEscapesSegments(t *testing.T) {
	var server *httptest.Server
	var repositoryRequests int
	var tokenRequests int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertHeaders(t, r, "")
		switch r.URL.EscapedPath() {
		case "/api/v3/app/installations":
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `[{"id":2,"account":{"login":"two","type":"Organization"},"status":"active"}]`)
				return
			}
			w.Header().Set("Link", "<"+server.URL+"/api/v3/app/installations?page=2>; rel=\"next\"")
			fmt.Fprint(w, `[{"id":1,"account":{"login":"one","type":"User"},"status":"active"}]`)
		case "/api/v3/app/installations/9/access_tokens":
			tokenRequests++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if _, ok := body["repository_ids"]; ok {
				t.Errorf("unexpected repository_ids: %v", body)
			}
			fmt.Fprintf(w, `{"token":"installation-secret-%d","expires_at":"2026-07-18T13:00:00Z"}`, tokenRequests)
		case "/api/v3/installation/repositories":
			repositoryRequests++
			if repositoryRequests == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer installation-secret-2" {
				t.Errorf("authorization = %q", got)
			}
			fmt.Fprint(w, `{"repositories":[{"id":22,"full_name":"o/r","owner":{"login":"o"},"name":"r","clone_url":"https://example/r.git","html_url":"https://example/r","default_branch":"main","private":true}]}`)
		case "/api/v3/repos/space%20owner/repo%2Fname/branches/main%2Fbranch":
			fmt.Fprint(w, `{"commit":{"sha":"abc123"}}`)
		case "/api/v3/repos/space%20owner/repo%2Fname/contents/dir/file%20name":
			if got := r.URL.Query().Get("ref"); got != "refs/heads/main & exact" {
				t.Errorf("ref = %q", got)
			}
			fmt.Fprint(w, `{"type":"file","encoding":"base64","content":"YQ==","sha":"blob","size":1}`)
		default:
			t.Errorf("unexpected path %q", r.URL.EscapedPath())
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server = httptest.NewTLSServer(handler)
	defer server.Close()
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	client := testClient(t, server, &now, 4096)

	installations, err := client.Installations(context.Background())
	if err != nil || len(installations) != 2 || installations[1].AccountLogin != "two" {
		t.Fatalf("installations = %#v, err = %v", installations, err)
	}
	repositories, err := client.InstallationRepositories(context.Background(), 9)
	if err != nil || len(repositories) != 1 || repositories[0].ID != 22 || repositories[0].InstallationID != 9 {
		t.Fatalf("repositories = %#v, err = %v", repositories, err)
	}
	if repositoryRequests != 2 || tokenRequests != 2 {
		t.Fatalf("repository requests = %d, token requests = %d", repositoryRequests, tokenRequests)
	}
	sha, err := client.DefaultBranchSHA(context.Background(), 9, "space owner", "repo/name", "main/branch")
	if err != nil || sha != "abc123" {
		t.Fatalf("sha = %q, err = %v", sha, err)
	}
	content, err := client.ReadContents(context.Background(), 9, "space owner", "repo/name", "dir/file name", "refs/heads/main & exact", 256)
	if err != nil || content.SHA != "blob" {
		t.Fatalf("content = %#v, err = %v", content, err)
	}
}

func TestClientBoundsResponsesAndKeepsErrorsSafe(t *testing.T) {
	secret := "installation-secret"
	bodySecret := "private-response-body"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			fmt.Fprintf(w, `{"token":%q,"expires_at":"2026-07-18T13:00:00Z"}`, secret)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, bodySecret+strings.Repeat("x", 100))
	}))
	defer server.Close()
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	client := testClient(t, server, &now, 32)
	_, err := client.DefaultBranchSHA(context.Background(), 9, "o", "r", "main")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), bodySecret) {
		t.Fatalf("unsafe error = %q", err)
	}

	large := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", 34))
	}))
	defer large.Close()
	client = testClient(t, large, &now, 32)
	_, err = client.Installations(context.Background())
	if err == nil || !strings.Contains(err.Error(), "response too large") {
		t.Fatalf("error = %v", err)
	}

	trailing := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[] []`)
	}))
	defer trailing.Close()
	client = testClient(t, trailing, &now, 32)
	_, err = client.Installations(context.Background())
	if err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("error = %v", err)
	}
}

func testClient(t *testing.T, server *httptest.Server, now *time.Time, maxBytes int64) *Client {
	t.Helper()
	endpoint, err := url.Parse(server.URL + "/api/v3")
	if err != nil {
		t.Fatal(err)
	}
	httpClient, err := NewHTTPClient(pemCertificate(t, server.Certificate()), Endpoints{Web: endpoint, API: endpoint, Upload: endpoint, Git: endpoint}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner(1, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	return NewClient(Endpoints{Web: endpoint, API: endpoint, Upload: endpoint, Git: endpoint}, httpClient, signer, "2022-11-28", maxBytes, func() time.Time { return *now })
}

func assertHeaders(t *testing.T, r *http.Request, authPrefix string) {
	t.Helper()
	if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
		t.Errorf("Accept = %q", got)
	}
	if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
		t.Errorf("version = %q", got)
	}
	if authPrefix != "" && !strings.HasPrefix(r.Header.Get("Authorization"), authPrefix) {
		t.Errorf("authorization = %q", r.Header.Get("Authorization"))
	}
}
