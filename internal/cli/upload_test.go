package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/pkg/api"
)

type recordedRequest struct {
	Method, Path string
	Query        url.Values
	Header       http.Header
	BodyLength   int
	Body         []byte
}

// publishServer scripts the three endpoints a publication touches and records every request.
type publishServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
	summary  api.RepositorySummary
	status   api.GraphStatus
	uploads  []http.HandlerFunc // one per POST, in order
}

func newPublishServer(t *testing.T, indexed string, active *api.GraphActiveGeneration) *publishServer {
	t.Helper()
	s := &publishServer{
		summary: api.RepositorySummary{ID: 42, IndexedSHA: indexed},
		status:  api.GraphStatus{RepositoryID: 42, Publication: &api.GraphPublication{Permitted: true, ActiveGeneration: active}},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, recordedRequest{r.Method, r.URL.Path, r.URL.Query(), r.Header.Clone(), len(body), body})
		posts := 0
		for _, q := range s.requests {
			if q.Method == http.MethodPost {
				posts++
			}
		}
		summary, status, uploads := s.summary, s.status, s.uploads
		s.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/repositories/42":
			json.NewEncoder(w).Encode(summary)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/graph/repositories/42/status":
			json.NewEncoder(w).Encode(status)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/graph/uploads" && posts <= len(uploads):
			uploads[posts-1](w, r)
		default:
			t.Errorf("unscripted request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unscripted", http.StatusTeapot)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *publishServer) posts() (posts []recordedRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.requests {
		if r.Method == http.MethodPost {
			posts = append(posts, r)
		}
	}
	return posts
}

func (s *publishServer) env(repository fakeRepository) Environment {
	env := testEnvWith(map[string]string{"GRAPHNEST_SERVER_URL": s.URL, "GRAPHNEST_TOKEN": token}, repository)
	env.Sleep = func(context.Context, time.Duration) error { return nil }
	return env
}

func respondResult(generation int64, deduplicated bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.GraphPublicationResult{RepositoryID: 42, Commit: sha, Generation: generation, ReplacedGeneration: 7, ContentHash: "ab", Deduplicated: deduplicated})
	}
}

func respondError(status int, code string, retryable bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": "scripted", "retryable": retryable}})
	}
}

func dropConnection(w http.ResponseWriter, r *http.Request) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		conn.Close()
	}
}

var activeSeven = &api.GraphActiveGeneration{ID: 7, Commit: sha, SchemaVersion: 2, Source: api.GraphSourceExternal, Producer: "codegraph", ProducerVersion: "1", ContentHash: "aa"}

// artifactFile writes the fixture's artifact for repository 42 and returns its path and bytes.
func artifactFile(t *testing.T) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "graph.pb")
	if code, _, stderr := run(t, testEnv(nil), importArgs(fixture, "--output", path)...); code != 0 {
		t.Fatalf("code %d %s", code, stderr)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, data
}

func decodeUpload(t *testing.T, stdout string) uploadReport {
	t.Helper()
	var report uploadReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("%v: %q", err, stdout)
	}
	return report
}

func TestUploadPublishes(t *testing.T) {
	path, data := artifactFile(t)
	s := newPublishServer(t, sha, activeSeven)
	s.uploads = []http.HandlerFunc{respondResult(8, false)}
	code, stdout, stderr := run(t, s.env(fakeRepository{}), "graph", "upload", path, "--repository-id", "42")
	if code != 0 || stderr != "" {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	posts := s.posts()
	if len(posts) != 1 {
		t.Fatalf("posts=%d", len(posts))
	}
	post := posts[0]
	if post.Query.Get("repository_id") != "42" || post.Query.Get("commit") != sha || post.Query.Get("expected_generation") != "7" || post.Query.Has("replace_producer") {
		t.Fatalf("query=%v", post.Query)
	}
	if post.Header.Get("Content-Type") != api.GraphArtifactV2ContentType || post.Header.Get("Authorization") != "Bearer "+token || !bytes.Equal(post.Body, data) {
		t.Fatalf("headers=%v body=%d want %d", post.Header, post.BodyLength, len(data))
	}
	report := decodeUpload(t, stdout)
	artifact, err := graphartifact.ParseV2(data, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Command != "graph upload" || report.RepositoryID != 42 || report.Commit != sha || !report.Published ||
		report.Artifact.Path != path || report.Artifact.Bytes != len(data) || report.Artifact.Producer.Name != "codegraph" ||
		report.Artifact.Nodes != len(artifact.Nodes) || report.Artifact.Edges != len(artifact.Edges) || report.Artifact.Files != len(artifact.Files) || len(report.Artifact.ContentHash) != 64 {
		t.Fatalf("report=%+v", report)
	}
	if report.Server == nil || report.Server.IndexedSHA != sha || !report.Server.Permitted || report.Server.Before == nil || report.Server.Before.ID != 7 {
		t.Fatalf("server=%+v", report.Server)
	}
	if report.Publication == nil || report.Publication.ExpectedGeneration != 7 || report.Publication.ReplaceProducer || report.Publication.Attempts != 1 ||
		report.Publication.Result.Generation != 8 || report.Publication.Result.ReplacedGeneration != 7 {
		t.Fatalf("publication=%+v", report.Publication)
	}
	if strings.Contains(stdout, token) || strings.Contains(stderr, token) {
		t.Fatal("token leaked")
	}
}

func TestUploadWithoutActiveGenerationExpectsZero(t *testing.T) {
	path, _ := artifactFile(t)
	s := newPublishServer(t, sha, nil)
	s.uploads = []http.HandlerFunc{respondResult(1, false)}
	code, stdout, stderr := run(t, s.env(fakeRepository{}), "graph", "upload", "--repository-id", "42", "--expected-generation", "0", path)
	if code != 0 {
		t.Fatalf("code %d %s", code, stderr)
	}
	report := decodeUpload(t, stdout)
	if s.posts()[0].Query.Get("expected_generation") != "0" || report.Server.Before != nil || report.Publication.ExpectedGeneration != 0 {
		t.Fatalf("query=%v report=%+v", s.posts()[0].Query, report)
	}
}

func TestUploadReplaceProducer(t *testing.T) {
	path, _ := artifactFile(t)
	other := *activeSeven
	other.Producer = "other"
	s := newPublishServer(t, sha, &other)
	s.uploads = []http.HandlerFunc{respondResult(8, false)}
	code, stdout, stderr := run(t, s.env(fakeRepository{}), "graph", "upload", path, "--repository-id", "42", "--replace-producer")
	if code != 0 {
		t.Fatalf("code %d %s", code, stderr)
	}
	if s.posts()[0].Query.Get("replace_producer") != "true" || !decodeUpload(t, stdout).Publication.ReplaceProducer {
		t.Fatalf("query=%v", s.posts()[0].Query)
	}
}

func TestUploadRefusals(t *testing.T) {
	path, _ := artifactFile(t)
	other := *activeSeven
	other.Producer = "other"
	denied := func(s *publishServer) { s.status.Publication.Permitted = false }
	noPublication := func(s *publishServer) { s.status.Publication = nil }
	for _, tc := range []struct {
		name    string
		indexed string
		active  *api.GraphActiveGeneration
		tweak   func(*publishServer)
		args    []string
		want    string
	}{
		{"expected mismatch", sha, activeSeven, nil, []string{"--expected-generation", "3"}, "the active published generation is 7, not 3"},
		{"expected without generation", sha, nil, nil, []string{"--expected-generation", "5"}, "the active published generation is 0, not 5"},
		{"indexed sha mismatch", strings.Repeat("a", 40), activeSeven, nil, nil, "the server indexes " + strings.Repeat("a", 40) + " but the artifact is for " + sha},
		{"not permitted", sha, activeSeven, denied, nil, "this token may not publish to repository 42; an administrator can grant it with PUT /v1/graph/publication-grants"},
		{"no publication block", sha, activeSeven, noPublication, nil, "this token may not publish"},
		{"producer conflict", sha, &other, nil, nil, "the active generation was published by other; pass --replace-producer to replace it"},
		{"artifact for another repository", sha, activeSeven, nil, []string{"--repository-id", "43"}, "the artifact was created for repository 42, not 43"},
	} {
		s := newPublishServer(t, tc.indexed, tc.active)
		if tc.tweak != nil {
			tc.tweak(s)
		}
		args := append([]string{"graph", "upload", path, "--repository-id", "42"}, tc.args...)
		code, stdout, stderr := run(t, s.env(fakeRepository{}), args...)
		if code != 1 || stdout != "" || !strings.Contains(stderr, tc.want) || len(s.posts()) != 0 {
			t.Errorf("%s: code %d stdout %q stderr %q posts %d", tc.name, code, stdout, stderr, len(s.posts()))
		}
		if strings.Contains(stderr, token) {
			t.Errorf("%s: token leaked", tc.name)
		}
	}
}

func TestUploadRetriesServerError(t *testing.T) {
	path, _ := artifactFile(t)
	var slept []time.Duration
	s := newPublishServer(t, sha, activeSeven)
	s.uploads = []http.HandlerFunc{respondError(http.StatusServiceUnavailable, "unavailable", true), respondError(http.StatusBadGateway, "bad_gateway", false), respondResult(8, false)}
	env := s.env(fakeRepository{})
	env.Sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	code, stdout, stderr := run(t, env, "graph", "upload", path, "--repository-id", "42")
	if code != 0 {
		t.Fatalf("code %d %s", code, stderr)
	}
	if report := decodeUpload(t, stdout); report.Publication.Attempts != 3 || len(s.posts()) != 3 || len(slept) != 2 || slept[0] != time.Second || slept[1] != 2*time.Second {
		t.Fatalf("attempts=%d posts=%d slept=%v", report.Publication.Attempts, len(s.posts()), slept)
	}
}

func TestUploadRetriesDroppedConnection(t *testing.T) {
	path, _ := artifactFile(t)
	s := newPublishServer(t, sha, activeSeven)
	s.uploads = []http.HandlerFunc{dropConnection, respondResult(8, true)}
	code, stdout, stderr := run(t, s.env(fakeRepository{}), "graph", "upload", path, "--repository-id", "42")
	if code != 0 {
		t.Fatalf("code %d %s", code, stderr)
	}
	report := decodeUpload(t, stdout)
	if report.Publication.Attempts != 2 || !report.Publication.Result.Deduplicated || !report.Published {
		t.Fatalf("publication=%+v", report.Publication)
	}
}

func TestUploadGivesUpAfterThreeAttempts(t *testing.T) {
	path, _ := artifactFile(t)
	s := newPublishServer(t, sha, activeSeven)
	fail := respondError(http.StatusServiceUnavailable, "unavailable", true)
	s.uploads = []http.HandlerFunc{fail, fail, fail}
	code, stdout, stderr := run(t, s.env(fakeRepository{}), "graph", "upload", path, "--repository-id", "42")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "unavailable") || len(s.posts()) != 3 {
		t.Fatalf("code %d stdout %q stderr %q posts %d", code, stdout, stderr, len(s.posts()))
	}
}

func TestUploadErrorHints(t *testing.T) {
	path, _ := artifactFile(t)
	for _, tc := range []struct {
		status int
		code   string
		want   string
	}{
		{http.StatusConflict, "generation_conflict", "another publication or a new indexed commit landed since the preflight; read the status and retry"},
		{http.StatusConflict, "producer_conflict", "pass --replace-producer"},
		{http.StatusConflict, "not_indexed", "the indexed commit changed; run CodeGraph on the new commit and import again"},
		{http.StatusForbidden, "forbidden", "an administrator can grant it with PUT /v1/graph/publication-grants"},
		{http.StatusBadRequest, "invalid_request", "code=invalid_request"},
	} {
		s := newPublishServer(t, sha, activeSeven)
		s.uploads = []http.HandlerFunc{respondError(tc.status, tc.code, false)}
		code, stdout, stderr := run(t, s.env(fakeRepository{}), "graph", "upload", path, "--repository-id", "42")
		if code != 1 || stdout != "" || !strings.Contains(stderr, tc.want) || len(s.posts()) != 1 || strings.Contains(stderr, token) {
			t.Errorf("%s: code %d stdout %q stderr %q posts %d", tc.code, code, stdout, stderr, len(s.posts()))
		}
	}
}

func TestUploadUsageErrors(t *testing.T) {
	path, _ := artifactFile(t)
	for name, args := range map[string][]string{
		"no artifact":         {"graph", "upload", "--repository-id", "42"},
		"no repository":       {"graph", "upload", path},
		"two artifacts":       {"graph", "upload", path, path, "--repository-id", "42"},
		"negative generation": {"graph", "upload", path, "--repository-id", "42", "--expected-generation", "-1"},
	} {
		if code, stdout, _ := run(t, testEnv(nil), args...); code != 2 || stdout != "" {
			t.Errorf("%s: code %d stdout %q", name, code, stdout)
		}
	}
}

func TestImportPublishes(t *testing.T) {
	s := newPublishServer(t, sha, activeSeven)
	s.uploads = []http.HandlerFunc{respondResult(8, false)}
	code, stdout, stderr := run(t, s.env(fakeRepository{archive: tarOf(sourceTree())}), "graph", "import", "codegraph", "--index", fixture, "--commit", sha, "--repository-id", "42", "--replace-producer")
	if code != 0 || stderr != "" {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	report := decodeReport(t, stdout)
	posts := s.posts()
	if !report.Published || report.Publication == nil || report.Publication.Result.Generation != 8 || report.Publication.Attempts != 1 || !report.Publication.ReplaceProducer ||
		report.Server == nil || report.Server.Before == nil || report.Server.Before.ID != 7 || report.Output != nil || report.DryRun || len(posts) != 1 {
		t.Fatalf("report=%+v posts=%d", report, len(posts))
	}
	if posts[0].Query.Get("expected_generation") != "7" || posts[0].BodyLength != report.Artifact.Bytes {
		t.Fatalf("query=%v body=%d artifact=%d", posts[0].Query, posts[0].BodyLength, report.Artifact.Bytes)
	}
}

func TestImportDoesNotPublishStaleIndex(t *testing.T) {
	tree := sourceTree()
	tree["core.ts"] = append([]byte("x"), tree["core.ts"][1:]...)
	s := newPublishServer(t, sha, activeSeven)
	code, stdout, stderr := run(t, s.env(fakeRepository{archive: tarOf(tree)}), "graph", "import", "codegraph", "--index", fixture, "--commit", sha, "--repository-id", "42")
	if code != 1 || !strings.Contains(stderr, "not fresh") || len(s.posts()) != 0 || len(s.requests) != 0 {
		t.Fatalf("code %d stderr %q requests %d", code, stderr, len(s.requests))
	}
	if report := decodeReport(t, stdout); report.Published || report.Publication != nil || report.Server != nil || report.Freshness.Status == "fresh" {
		t.Fatalf("report=%+v", report)
	}
}

func TestImportPublishNeedsRepositoryID(t *testing.T) {
	code, stdout, stderr := run(t, testEnv(nil), "graph", "import", "codegraph", "--index", fixture, "--commit", sha)
	if code != 2 || stdout != "" || !strings.Contains(stderr, "--repository-id") {
		t.Fatalf("code %d stdout %q stderr %q", code, stdout, stderr)
	}
	code, _, _ = run(t, testEnv(nil), "graph", "import", "codegraph", "--dry-run", "--index", fixture, "--commit", sha, "--repository-id", "42", "--replace-producer")
	if code != 2 {
		t.Fatalf("--replace-producer with --dry-run: code %d", code)
	}
}
