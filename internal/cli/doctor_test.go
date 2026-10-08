package cli

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/client"
)

// doctorRepo copies the fixture index to <repo>/.codegraph/codegraph.db, optionally changing project_metadata.
func doctorRepo(t *testing.T, metadata map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, ".codegraph", "codegraph.db")
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if len(metadata) > 0 {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		for key, value := range metadata {
			if _, err = db.Exec(`update project_metadata set value=? where key=?`, value, key); err != nil {
				t.Fatal(err)
			}
		}
		db.Close()
	}
	return repo
}

// fakeGit answers git --version and git rev-parse --verify HEAD^{commit}.
func fakeGit(version, head error) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "--version":
			return []byte("git version 2.50.0\n"), version
		case "rev-parse --verify HEAD^{commit}":
			return []byte(sha + "\n"), head
		}
		return nil, errors.New("unexpected git call " + strings.Join(args, " "))
	}
}

func doctorEnv(vars map[string]string, repository fakeRepository, git func(context.Context, string, ...string) ([]byte, error)) Environment {
	env := testEnvWith(vars, repository)
	env.Git = git
	return env
}

func runDoctorChecks(t *testing.T, env Environment, wantCode int, args ...string) map[string]doctorCheck {
	t.Helper()
	code, stdout, stderr := run(t, env, append([]string{"doctor"}, args...)...)
	if code != wantCode {
		t.Fatalf("code %d, want %d; stdout %s stderr %q", code, wantCode, stdout, stderr)
	}
	var report doctorReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("%v: %q", err, stdout)
	}
	if report.Command != "doctor" || report.OK != (wantCode == 0) {
		t.Fatalf("%+v", report)
	}
	names := []string{"git", "repository", "index", "producer-rules", "index-state", "freshness", "server"}
	var got []string
	checks := map[string]doctorCheck{}
	for _, c := range report.Checks {
		got = append(got, c.Name)
		checks[c.Name] = c
	}
	if !slices.Equal(got, names) {
		t.Fatalf("checks %v", got)
	}
	if strings.Contains(stdout, token) || strings.Contains(stderr, token) {
		t.Fatal("the token appears in the output")
	}
	return checks
}

func wantStatuses(t *testing.T, checks map[string]doctorCheck, want map[string]string) {
	t.Helper()
	for name, status := range want {
		if checks[name].Status != status {
			t.Errorf("%s: status %q (%s), want %q", name, checks[name].Status, checks[name].Detail, status)
		}
	}
}

// hashTree digests every file name and content under dir.
func hashTree(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		h.Write([]byte(path))
		h.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestDoctorHealthyWritesNothing(t *testing.T) {
	repo := doctorRepo(t, nil)
	before := hashTree(t, repo)
	checks := runDoctorChecks(t, doctorEnv(nil, fakeRepository{archive: tarOf(sourceTree())}, fakeGit(nil, nil)), 0, "--repo", repo)
	wantStatuses(t, checks, map[string]string{"git": "ok", "repository": "ok", "index": "ok", "producer-rules": "ok", "index-state": "ok", "freshness": "ok", "server": "warn"})
	if !strings.Contains(checks["repository"].Detail, sha) || !strings.Contains(checks["index"].Detail, "schema 9") || !strings.Contains(checks["index"].Detail, "68 nodes") ||
		!strings.Contains(checks["server"].Detail, "GRAPHNEST_SERVER_URL is not set; server checks skipped") {
		t.Errorf("%+v", checks)
	}
	if hashTree(t, repo) != before {
		t.Fatal("doctor changed the repository directory")
	}
}

func TestDoctorFailures(t *testing.T) {
	good := fakeRepository{archive: tarOf(sourceTree())}
	garbage := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(garbage, []byte("this is not a database, just text padded to look big enough to open"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		repo  string
		args  []string
		git   func(context.Context, string, ...string) ([]byte, error)
		want  map[string]string
		check string
		text  string
	}{
		{"git missing", doctorRepo(t, nil), nil, fakeGit(errors.New("executable file not found"), nil), map[string]string{"git": "fail", "repository": "ok"}, "git", "install git"},
		{"not a repository", doctorRepo(t, nil), nil, fakeGit(nil, errors.New("fatal: not a git repository")), map[string]string{"repository": "fail", "freshness": "warn"}, "repository", "not a git work tree"},
		{"missing index", t.TempDir(), nil, fakeGit(nil, nil), map[string]string{"index": "fail", "producer-rules": "warn", "freshness": "warn"}, "index", "no such file"},
		{"not a CodeGraph index", doctorRepo(t, nil), []string{"--index", garbage}, fakeGit(nil, nil), map[string]string{"index": "fail", "freshness": "warn"}, "index", "not a CodeGraph index"},
		{"unknown producer", doctorRepo(t, map[string]string{"indexed_with_version": "9.9.9"}), nil, fakeGit(nil, nil), map[string]string{"index": "ok", "producer-rules": "fail", "freshness": "warn"}, "producer-rules", "cannot be verified for freshness"},
	} {
		args := append([]string{"--repo", tc.repo}, tc.args...)
		checks := runDoctorChecks(t, doctorEnv(nil, good, tc.git), 1, args...)
		wantStatuses(t, checks, tc.want)
		if !strings.Contains(checks[tc.check].Detail, tc.text) {
			t.Errorf("%s: %s detail %q lacks %q", tc.name, tc.check, checks[tc.check].Detail, tc.text)
		}
	}
}

func TestDoctorWarnings(t *testing.T) {
	tree := sourceTree()
	changed := map[string][]byte{}
	for name, data := range tree {
		changed[name] = data
	}
	changed["core.ts"] = append([]byte("x"), tree["core.ts"][1:]...)

	repo := doctorRepo(t, map[string]string{"index_state": "partial"})
	checks := runDoctorChecks(t, doctorEnv(nil, fakeRepository{archive: tarOf(tree)}, fakeGit(nil, nil)), 0, "--repo", repo)
	wantStatuses(t, checks, map[string]string{"index-state": "warn", "freshness": "ok"})
	if !strings.Contains(checks["index-state"].Detail, "index_state is partial") {
		t.Errorf("%+v", checks["index-state"])
	}

	repo = doctorRepo(t, nil)
	if err := os.WriteFile(filepath.Join(repo, ".codegraph", "codegraph.db-wal"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	checks = runDoctorChecks(t, doctorEnv(nil, fakeRepository{archive: tarOf(changed)}, fakeGit(nil, nil)), 0, "--repo", repo)
	wantStatuses(t, checks, map[string]string{"index-state": "warn", "freshness": "warn"})
	if !strings.Contains(checks["index-state"].Detail, "-wal") || !strings.Contains(checks["freshness"].Detail, "stale") || !strings.Contains(checks["freshness"].Detail, "modified 1") {
		t.Errorf("%+v %+v", checks["index-state"], checks["freshness"])
	}

	checks = runDoctorChecks(t, doctorEnv(nil, fakeRepository{archiveErr: errors.New("boom")}, fakeGit(nil, nil)), 0, "--repo", doctorRepo(t, nil))
	if checks["freshness"].Status != "warn" || !strings.Contains(checks["freshness"].Detail, "boom") {
		t.Errorf("%+v", checks["freshness"])
	}
}

func TestDoctorServer(t *testing.T) {
	good := fakeRepository{archive: tarOf(sourceTree())}
	repo := doctorRepo(t, nil)
	for _, tc := range []struct {
		name    string
		indexed string
		denied  bool
		args    []string
		want    string
		text    string
	}{
		{"ok", sha, false, []string{"--repository-id", "42"}, "ok", "indexed_sha " + sha},
		{"configuration only", sha, false, nil, "ok", "pass --repository-id"},
		{"indexed commit differs", "fedcba9876543210fedcba9876543210fedcba98", false, []string{"--repository-id", "42"}, "warn", "differs from HEAD"},
		{"publication denied", sha, true, []string{"--repository-id", "42"}, "warn", "PUT /v1/graph/publication-grants"},
	} {
		s := newPublishServer(t, tc.indexed, nil)
		s.status.Publication.Permitted = !tc.denied
		env := doctorEnv(map[string]string{"GRAPHNEST_SERVER_URL": s.URL, "GRAPHNEST_TOKEN": token}, good, fakeGit(nil, nil))
		checks := runDoctorChecks(t, env, 0, append([]string{"--repo", repo}, tc.args...)...)
		if checks["server"].Status != tc.want || !strings.Contains(checks["server"].Detail, tc.text) {
			t.Errorf("%s: %+v", tc.name, checks["server"])
		}
	}

	s := newPublishServer(t, sha, nil)
	env := doctorEnv(map[string]string{"GRAPHNEST_SERVER_URL": s.URL, "GRAPHNEST_TOKEN": token}, good, fakeGit(nil, nil))
	if checks := runDoctorChecks(t, env, 0, "--repo", repo, "--repository-id", "42"); !strings.Contains(checks["server"].Detail, "credentials: GRAPHNEST_TOKEN; ") {
		t.Errorf("%+v", checks["server"])
	}
	config := t.TempDir()
	login := client.Login{Server: s.URL, ClientID: "gnc", AccessToken: token, RefreshToken: "gnr", ExpiresAt: time.Now().Add(time.Hour), TokenEndpoint: s.URL + "/oauth/token"}
	if err := (client.Logins{Dir: filepath.Join(config, "graphnest", "credentials")}).Save(login); err != nil {
		t.Fatal(err)
	}
	env = doctorEnv(map[string]string{"GRAPHNEST_SERVER_URL": s.URL}, good, fakeGit(nil, nil))
	env.ConfigDir = func() (string, error) { return config, nil }
	if checks := runDoctorChecks(t, env, 0, "--repo", repo, "--repository-id", "42"); !strings.Contains(checks["server"].Detail, "credentials: stored login; ") {
		t.Errorf("%+v", checks["server"])
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondError(http.StatusForbidden, "forbidden", false)(w, r)
	}))
	t.Cleanup(failing.Close)
	checks := runDoctorChecks(t, doctorEnv(map[string]string{"GRAPHNEST_SERVER_URL": failing.URL, "GRAPHNEST_TOKEN": token}, good, fakeGit(nil, nil)), 1, "--repo", repo, "--repository-id", "42")
	if checks["server"].Status != "fail" || !strings.Contains(checks["server"].Detail, "code=forbidden") {
		t.Errorf("%+v", checks["server"])
	}

	checks = runDoctorChecks(t, doctorEnv(map[string]string{"GRAPHNEST_SERVER_URL": failing.URL}, good, fakeGit(nil, nil)), 1, "--repo", repo)
	if checks["server"].Status != "fail" || !strings.Contains(checks["server"].Detail, "GRAPHNEST_TOKEN") {
		t.Errorf("%+v", checks["server"])
	}
}

func TestDoctorUsageErrors(t *testing.T) {
	for name, args := range map[string][]string{
		"bad id":    {"doctor", "--repository-id", "0"},
		"timeout":   {"doctor", "--timeout", "0s"},
		"argument":  {"doctor", "extra"},
		"bad flag":  {"doctor", "--bogus"},
		"no format": {"doctor", "--format", "text"},
	} {
		if code, stdout, _ := run(t, testEnv(nil), args...); code != 2 || stdout != "" {
			t.Errorf("%s: code %d stdout %q", name, code, stdout)
		}
	}
}
