package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphimport"
)

const (
	fixture = "../../test/fixtures/codegraph/reference.db"
	sha     = "0123456789abcdef0123456789abcdef01234567"
	token   = "s3cret-token"
	source  = "../../test/fixtures/codegraph/source"
)

// fakeRepository serves an in-memory archive (or an error) and delegates ignore evaluation to git.
type fakeRepository struct {
	archive    []byte
	archiveErr error
}

func (r fakeRepository) Archive(context.Context, string, string) (io.ReadCloser, error) {
	if r.archiveErr != nil {
		return nil, r.archiveErr
	}
	return io.NopCloser(bytes.NewReader(r.archive)), nil
}

func (fakeRepository) Ignored(ctx context.Context, patterns string, ignoreCase bool, paths []string) ([]string, error) {
	return graphimport.ExecGit{}.Ignored(ctx, patterns, ignoreCase, paths)
}

// sourceTree reads every file of the fixture source tree.
func sourceTree() map[string][]byte {
	tree := map[string][]byte{}
	err := filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		rel, _ := filepath.Rel(source, path)
		tree[filepath.ToSlash(rel)] = data
		return err
	})
	if err != nil {
		panic(err)
	}
	return tree
}

// tarOf builds a tar stream with directory entries followed by the files.
func tarOf(tree map[string][]byte) []byte {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	write := func(h *tar.Header, data []byte) {
		if err := w.WriteHeader(h); err != nil {
			panic(err)
		}
		if _, err := w.Write(data); err != nil {
			panic(err)
		}
	}
	dirs := map[string]bool{}
	names := slices.Sorted(maps.Keys(tree))
	for _, name := range names {
		for dir := filepath.Dir(name); dir != "."; dir = filepath.Dir(dir) {
			dirs[dir+"/"] = true
		}
	}
	for _, dir := range slices.Sorted(maps.Keys(dirs)) {
		write(&tar.Header{Typeflag: tar.TypeDir, Name: dir, Mode: 0o755}, nil)
	}
	for _, name := range names {
		write(&tar.Header{Typeflag: tar.TypeReg, Name: name, Size: int64(len(tree[name])), Mode: 0o644}, tree[name])
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// testEnv serves the unmodified fixture source tree as the commit.
func testEnv(vars map[string]string) Environment {
	return testEnvWith(vars, fakeRepository{archive: tarOf(sourceTree())})
}

func testEnvWith(vars map[string]string, repository fakeRepository) Environment {
	return Environment{
		Repository: repository,
		Getenv:     func(k string) string { return vars[k] },
		ReadFile:   os.ReadFile,
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
	if report.Output != nil || report.Freshness.Status != "fresh" || report.Freshness.Compared != 13 || len(report.Freshness.Modified)+len(report.Freshness.NotInCommit)+len(report.Freshness.NotIndexed)+len(report.Freshness.Unverified) != 0 || report.Diagnostics.Dropped == nil || len(report.Diagnostics.Dropped) != 0 || report.Diagnostics.UnresolvedReferences != 6 {
		t.Fatalf("%+v %+v", report.Freshness, report.Diagnostics)
	}
	if report.Index.Producer.Name != "codegraph" || report.Index.Producer.Version == "" || report.Index.SchemaVersion != 9 || len(report.NodeKinds) == 0 || len(report.EdgeKinds) == 0 {
		t.Fatalf("%+v", report.Index)
	}
	if !strings.Contains(stdout, `"dropped": []`) {
		t.Fatal("dropped must serialise as an empty array")
	}
	for _, list := range []string{"modified", "not_in_commit", "not_indexed", "unverified"} {
		if !strings.Contains(stdout, `"`+list+`": []`) {
			t.Fatalf("%s must serialise as an empty array", list)
		}
	}
	if !strings.Contains(stdout, `"output": null`) {
		t.Fatal("a dry run reports output null")
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
		{"no dry run", failingGit, []string{"--index", fixture, "--commit", sha}, 2, "publication need --repository-id"},
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
	var out struct{ Version, Go, SQLite string }
	if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &out) != nil || out.Version == "" || !strings.HasPrefix(out.Go, "go") || out.SQLite == "" {
		t.Fatalf("code %d stdout %q stderr %q", code, stdout, stderr)
	}
	Version = "1.2.3"
	defer func() { Version = "" }()
	if _, stdout, _ = run(t, testEnv(nil), "version"); !strings.Contains(stdout, `"version": "1.2.3"`) {
		t.Fatalf("stdout %q", stdout)
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

func TestGraphStatusUnauthorizedHint(t *testing.T) {
	_, vars := statusServer(t, false)
	vars["GRAPHNEST_TOKEN"] = "wrong"
	code, _, stderr := run(t, testEnv(vars), "graph", "status", "--repository-id", "9")
	if code != 1 || !strings.Contains(stderr, "check the token or run graphnest login") || strings.Contains(stderr, "wrong") {
		t.Fatalf("code %d stderr %q", code, stderr)
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

func importArgs(index string, extra ...string) []string {
	return append([]string{"graph", "import", "codegraph", "--index", index, "--commit", sha, "--repository-id", "42"}, extra...)
}

func decodeReport(t *testing.T, stdout string) importReport {
	t.Helper()
	var report importReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("%v: %q", err, stdout)
	}
	return report
}

func TestOutputWritesArtifact(t *testing.T) {
	out := filepath.Join(t.TempDir(), "graph.pb")
	code, stdout, stderr := run(t, testEnv(nil), importArgs(fixture, "--output", out)...)
	if code != 0 || stderr != "" {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	report := decodeReport(t, stdout)
	if report.DryRun || report.Published || report.Output == nil || report.Output.Path != out || report.Output.Bytes != len(data) || report.Artifact.Bytes != len(data) {
		t.Fatalf("%+v len %d", report, len(data))
	}
	artifact, err := graphartifact.ParseV2(data, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Repository != "42" || artifact.Commit != sha || len(artifact.ContentHash) != 32 || hex.EncodeToString(artifact.ContentHash) != report.Artifact.ContentHash || len(artifact.Nodes) != 68 || len(artifact.Files) != 13 {
		t.Fatalf("artifact %s %s %d nodes %d files", artifact.Repository, artifact.Commit, len(artifact.Nodes), len(artifact.Files))
	}
}

func TestOutputRefusals(t *testing.T) {
	tree := sourceTree()
	partial := filepath.Join(t.TempDir(), "partial.db")
	data, _ := os.ReadFile(fixture)
	if err := os.WriteFile(partial, data, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", partial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`update project_metadata set value='partial' where key='index_state'`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	repo := t.TempDir()
	inData := filepath.Join(repo, ".codegraph", "graph.pb")
	if err = os.MkdirAll(filepath.Dir(inData), 0o755); err != nil {
		t.Fatal(err)
	}
	changed := maps.Clone(tree)
	changed["core.ts"] = append([]byte("x"), tree["core.ts"][1:]...)
	missing := maps.Clone(tree)
	delete(missing, "Model.java")
	extra := maps.Clone(tree)
	extra["extra.ts"] = []byte("export const extra = 1\n")

	for _, tc := range []struct {
		name       string
		index      string
		repository fakeRepository
		repo, out  string
		wantStdout string
		wantStderr string
	}{
		{"modified", fixture, fakeRepository{archive: tarOf(changed)}, "", "", `"modified": [` + "\n" + `      "core.ts"`, "modified"},
		{"not in commit", fixture, fakeRepository{archive: tarOf(missing)}, "", "", `"Model.java"`, "not in the commit"},
		{"not indexed", fixture, fakeRepository{archive: tarOf(extra)}, "", "", `"extra.ts"`, "missing from the index"},
		{"unverifiable", fixture, fakeRepository{archiveErr: errors.New("boom")}, "", "", `"status": "unverifiable"`, "boom"},
		{"partial index", partial, fakeRepository{archive: tarOf(tree)}, "", "", `"status": "fresh"`, "index_state=partial"},
		{"data directory", fixture, fakeRepository{archive: tarOf(tree)}, repo, inData, `"status": "fresh"`, "CodeGraph data directory"},
	} {
		out := tc.out
		if out == "" {
			out = filepath.Join(t.TempDir(), "graph.pb")
		}
		args := importArgs(tc.index, "--output", out)
		if tc.repo != "" {
			args = append(args, "--repo", tc.repo)
		}
		code, stdout, stderr := run(t, testEnvWith(nil, tc.repository), args...)
		if code != 1 || !strings.Contains(stdout, tc.wantStdout) || !strings.Contains(stderr, tc.wantStderr) {
			t.Errorf("%s: code %d stdout %q stderr %q", tc.name, code, stdout, stderr)
		}
		if _, err := os.Stat(out); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: output exists (%v)", tc.name, err)
		}
		if report := decodeReport(t, stdout); report.Output != nil || report.Published {
			t.Errorf("%s: %+v", tc.name, report)
		}
	}
}

func TestDryRunUnverifiableFreshness(t *testing.T) {
	env := testEnvWith(nil, fakeRepository{archiveErr: errors.New("boom")})
	code, stdout, stderr := run(t, env, importArgs(fixture, "--dry-run")...)
	report := decodeReport(t, stdout)
	if code != 0 || stderr != "" || report.Freshness.Status != "unverifiable" || report.Freshness.Detail != "boom" || report.Freshness.Commit != sha || !strings.Contains(stdout, `"modified": []`) {
		t.Fatalf("code %d stderr %q %+v", code, stderr, report.Freshness)
	}
}

func TestOutputUsageErrors(t *testing.T) {
	out := filepath.Join(t.TempDir(), "graph.pb")
	for name, args := range map[string][]string{
		"both flags":    importArgs(fixture, "--dry-run", "--output", out),
		"no repository": {"graph", "import", "codegraph", "--index", fixture, "--commit", sha, "--output", out},
	} {
		code, stdout, stderr := run(t, testEnv(nil), args...)
		if code != 2 || stdout != "" || stderr == "" {
			t.Errorf("%s: code %d stdout %q stderr %q", name, code, stdout, stderr)
		}
		if _, err := os.Stat(out); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: output exists", name)
		}
	}
}
