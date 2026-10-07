package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/graphimport"
)

const (
	fixture = "../../test/fixtures/codegraph/reference.db"
	sha     = "0123456789abcdef0123456789abcdef01234567"
	token   = "s3cret-token"
)

func testEnv(vars map[string]string) Environment {
	return Environment{
		Getenv:   func(k string) string { return vars[k] },
		ReadFile: os.ReadFile,
		Git: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("git must not run")
		},
		Now: time.Now,
	}
}

func run(t *testing.T, env Environment, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return runCtx(t, context.Background(), env, args...)
}

func runCtx(t *testing.T, ctx context.Context, env Environment, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(ctx, args, env, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestDryRunReport(t *testing.T) {
	code, stdout, stderr := run(t, testEnv(nil), "graph", "import", "codegraph", "--dry-run", "--index", fixture, "--commit", sha, "--repository-id", "42")
	if code != 0 || stderr != "" {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	if !strings.HasSuffix(stdout, "}\n") || !strings.Contains(stdout, "\n  \"command\"") {
		t.Fatalf("stdout is not one indented document: %q", stdout)
	}
	var report importReport
	decoder := json.NewDecoder(strings.NewReader(stdout))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		t.Fatal(err)
	}
	if report.Command != "graph import codegraph" || !report.DryRun || report.Published || report.RepositoryID != 42 || report.Commit != sha {
		t.Fatalf("%+v", report)
	}
	if report.Counts != (countsReport{Nodes: 68, Edges: 93, Files: 13, Unresolved: 6, Metadata: 5}) {
		t.Fatalf("counts %+v", report.Counts)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(report.Artifact.ContentHash) || report.Artifact.Bytes == 0 {
		t.Fatalf("artifact %+v", report.Artifact)
	}
	if report.Freshness.Status != "unverified" || report.Diagnostics.Dropped == nil || len(report.Diagnostics.Dropped) != 0 || report.Diagnostics.UnresolvedReferences != 6 {
		t.Fatalf("%+v %+v", report.Freshness, report.Diagnostics)
	}
	if report.Index.Producer.Name != "codegraph" || report.Index.Producer.Version == "" || report.Index.SchemaVersion != 9 || len(report.NodeKinds) == 0 || len(report.EdgeKinds) == 0 {
		t.Fatalf("%+v", report.Index)
	}
	if !strings.Contains(stdout, `"dropped": []`) {
		t.Fatal("dropped must serialise as an empty array")
	}
	_, again, _ := run(t, testEnv(nil), "graph", "import", "codegraph", "--dry-run", "--index", fixture, "--commit", sha, "--repository-id", "42")
	if again != stdout {
		t.Fatal("dry run is not deterministic")
	}
}

func TestDryRunOmitsRepositoryIDWhenUnset(t *testing.T) {
	code, stdout, _ := run(t, testEnv(nil), "graph", "import", "codegraph", "--dry-run", "--index", fixture, "--commit", sha)
	if code != 0 || strings.Contains(stdout, "repository_id") {
		t.Fatalf("code %d stdout %s", code, stdout)
	}
}

func TestDryRunRepoUsesGitAndCodeGraphDir(t *testing.T) {
	repo := t.TempDir()
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(repo, ".cg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repo, ".cg", "codegraph.db"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	env := testEnv(map[string]string{"CODEGRAPH_DIR": ".cg"})
	var gitDir string
	env.Git = func(_ context.Context, dir string, args ...string) ([]byte, error) {
		gitDir = dir
		if strings.Join(args, " ") != "rev-parse HEAD" {
			t.Errorf("git args %v", args)
		}
		return []byte(sha + "\n"), nil
	}
	code, stdout, stderr := run(t, env, "graph", "import", "codegraph", "--dry-run", "--repo", repo)
	if code != 0 || gitDir != repo || !strings.Contains(stdout, sha) || !strings.Contains(stdout, filepath.Join(repo, ".cg", "codegraph.db")) {
		t.Fatalf("code %d git dir %q stdout %s stderr %s", code, gitDir, stdout, stderr)
	}
}

func TestDryRunFailures(t *testing.T) {
	notDB := filepath.Join(t.TempDir(), "plain.db")
	if err := os.WriteFile(notDB, []byte("this is not a database, just text padding padding padding"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "absent.db")
	failingGit := testEnv(nil)
	for _, tc := range []struct {
		name string
		env  Environment
		args []string
		code int
		want string
	}{
		{"git failure", failingGit, []string{"--index", fixture}, 1, "--commit"},
		{"malformed commit", failingGit, []string{"--index", fixture, "--commit", "ABC"}, 1, "commit must be 40 lowercase hexadecimal"},
		{"missing index", failingGit, []string{"--index", missing, "--commit", sha}, 1, missing},
		{"not codegraph", failingGit, []string{"--index", notDB, "--commit", sha}, 1, graphimport.ErrNotCodeGraph.Error()},
		{"no dry run", failingGit, []string{"--index", fixture, "--commit", sha}, 2, "use --dry-run"},
		{"unknown flag", failingGit, []string{"--dry-run", "--bogus"}, 2, "bogus"},
		{"bad repository id", failingGit, []string{"--dry-run", "--repository-id", "0", "--index", fixture, "--commit", sha}, 2, "--repository-id"},
	} {
		args := append([]string{"graph", "import", "codegraph"}, tc.args...)
		if tc.name != "no dry run" && tc.name != "unknown flag" && tc.name != "bad repository id" {
			args = append(args, "--dry-run")
		}
		code, stdout, stderr := run(t, tc.env, args...)
		if code != tc.code || stdout != "" || !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: code %d stdout %q stderr %q", tc.name, code, stdout, stderr)
		}
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	data, _ := os.ReadFile(fixture)
	index := filepath.Join(dir, "codegraph.db")
	if err := os.WriteFile(index, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := run(t, testEnv(nil), "graph", "import", "codegraph", "--dry-run", "--index", index, "--commit", sha); code != 0 {
		t.Fatal(stderr)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("dry run left files behind: %v", entries)
	}
	after, _ := os.ReadFile(index)
	if !bytes.Equal(after, data) {
		t.Fatal("index modified")
	}
}

func TestVersion(t *testing.T) {
	code, stdout, stderr := run(t, testEnv(nil), "version")
	var out struct{ Version string }
	if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &out) != nil || out.Version == "" {
		t.Fatalf("code %d stdout %q stderr %q", code, stdout, stderr)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}, {"graph"}, {"graph", "bogus"}, {"version", "extra"}} {
		if code, stdout, stderr := run(t, testEnv(nil), args...); code != 2 || stdout != "" || !strings.Contains(stderr, "usage: graphnest") {
			t.Errorf("%v: code %d stdout %q stderr %q", args, code, stdout, stderr)
		}
	}
	for _, args := range [][]string{{"-h"}, {"graph", "-h"}, {"version", "-h"}, {"graph", "status", "-h"}, {"graph", "import", "codegraph", "-h"}} {
		if code, stdout, stderr := run(t, testEnv(nil), args...); code != 0 || stdout != "" || !strings.Contains(stderr, "usage: graphnest") {
			t.Errorf("%v: code %d stdout %q stderr %q", args, code, stdout, stderr)
		}
	}
}

func statusServer(t *testing.T, notFound bool) (*httptest.Server, map[string]string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if notFound {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":"not_found","message":"repository not found","retryable":true}}`))
			return
		}
		switch r.URL.Path {
		case "/v1/repositories/9":
			w.Write([]byte(`{"id":9,"name":"o/n","status":"indexed"}`))
		case "/v1/graph/repositories/9/status":
			w.Write([]byte(`{"repository_id":9,"state":"current"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, map[string]string{"GRAPHNEST_SERVER_URL": server.URL, "GRAPHNEST_TOKEN": token}
}

func TestGraphStatus(t *testing.T) {
	_, vars := statusServer(t, false)
	code, stdout, stderr := run(t, testEnv(vars), "graph", "status", "--repository-id", "9")
	if code != 0 || stderr != "" {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	var out struct {
		Repository struct {
			ID   int64
			Name string
		}
		Graph struct {
			RepositoryID int64 `json:"repository_id"`
			State        string
		}
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil || out.Repository.ID != 9 || out.Repository.Name != "o/n" || out.Graph.RepositoryID != 9 || out.Graph.State != "current" {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestGraphStatusServerError(t *testing.T) {
	_, vars := statusServer(t, true)
	code, stdout, stderr := run(t, testEnv(vars), "graph", "status", "--repository-id", "9")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "not_found") || !strings.Contains(stderr, "retryable") || strings.Contains(stderr, token) {
		t.Fatalf("code %d stdout %q stderr %q", code, stdout, stderr)
	}
}

func TestGraphStatusConfiguration(t *testing.T) {
	code, _, stderr := run(t, testEnv(map[string]string{"GRAPHNEST_TOKEN": token}), "graph", "status", "--repository-id", "9")
	if code != 1 || !strings.Contains(stderr, "GRAPHNEST_SERVER_URL") || strings.Contains(stderr, token) {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	if code, _, _ = run(t, testEnv(nil), "graph", "status"); code != 2 {
		t.Fatalf("missing --repository-id: code %d", code)
	}
}

func TestCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, stdout, stderr := runCtx(t, ctx, testEnv(nil), "graph", "import", "codegraph", "--dry-run", "--index", fixture, "--commit", sha)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "context canceled") {
		t.Fatalf("import: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	_, vars := statusServer(t, false)
	code, stdout, stderr = runCtx(t, ctx, testEnv(vars), "graph", "status", "--repository-id", "9")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "context canceled") {
		t.Fatalf("status: code %d stdout %q stderr %q", code, stdout, stderr)
	}
}
