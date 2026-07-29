//go:build unix

package indexer

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/grepnest/grepnest/internal/repository"
)

const gitTargetSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestGitNumericPathsStayContained(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name string
		ids  []string
		ok   bool
	}{
		{name: "numeric", ids: []string{"12", "34"}, ok: true},
		{name: "zero", ids: []string{"0"}},
		{name: "leading zero", ids: []string{"01"}},
		{name: "non numeric", ids: []string{"job"}},
		{name: "traversal", ids: []string{"..", "34"}},
		{name: "slash", ids: []string{"12/../../tmp"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := numericPath(root, test.ids...)
			if test.ok {
				want := filepath.Join(append([]string{root}, test.ids...)...)
				if err != nil || got != want {
					t.Fatalf("path=%q want=%q err=%v", got, want, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted path %q", got)
			}
		})
	}
}

func TestGitPrepareCommitFetchesOnlyTargetBranch(t *testing.T) {
	git, repo, job, promptsFile, serverURL, _, _ := gitPrepareFixture(t)
	mirror, worktree, err := git.PrepareCommit(t.Context(), repo, job.ID, job.TargetSHA, "token-that-must-not-persist")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(git.WorktreesDir, "7", "11"); worktree != want {
		t.Fatalf("worktree=%q want=%q", worktree, want)
	}
	if got := strings.TrimSpace(runGit(t, "", "--git-dir", mirror, "config", "--get", "remote.origin.url")); got != serverURL+"/acme/repo.git" || strings.Contains(got, "token") {
		t.Fatalf("remote=%q", got)
	}
	if got := strings.Fields(runGit(t, "", "--git-dir", mirror, "config", "--get-all", "remote.origin.fetch")); !slices.Equal(got, []string{"+refs/heads/main:refs/heads/main"}) {
		t.Fatalf("fetch refspec=%v", got)
	}
	for key, want := range map[string]string{"zoekt.repoid": "17", "zoekt.name": "acme/repo", "zoekt.web-url": "https://ghe.example/acme/repo", "zoekt.web-url-type": "github"} {
		if got := strings.TrimSpace(runGit(t, "", "--git-dir", mirror, "config", "--get", key)); got != want {
			t.Fatalf("%s=%q want=%q", key, got, want)
		}
	}
	if command := exec.Command(gitBinary(t), "--git-dir", mirror, "show-ref", "--verify", "refs/tags/not-fetched"); command.Run() == nil {
		t.Fatal("tag was fetched")
	}
	if got := strings.TrimSpace(runGit(t, worktree, "rev-parse", "HEAD")); got != job.TargetSHA {
		t.Fatalf("worktree HEAD=%q want=%q", got, job.TargetSHA)
	}
	prompts, err := os.ReadFile(promptsFile)
	if err != nil {
		t.Fatal(err)
	}
	promptOrigin, err := credentialOrigin(serverURL + "/acme/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	wantPrompts := "Username for '" + promptOrigin + "': \nPassword for '" + strings.Replace(promptOrigin, "https://", "https://x-access-token@", 1) + "': \n"
	if string(prompts) != wantPrompts {
		t.Fatalf("prompts=%q want=%q", prompts, wantPrompts)
	}
	if command := exec.Command(gitBinary(t), "-C", worktree, "symbolic-ref", "-q", "HEAD"); command.Run() == nil {
		t.Fatal("worktree HEAD is attached")
	}

	missing := job
	missing.ID++
	missing.TargetSHA = gitTargetSHA
	_, _, err = git.Prepare(t.Context(), repo, missing, "token-that-must-not-persist")
	if !errors.Is(err, ErrTargetMissing) || strings.Contains(fmt.Sprint(err), "secret") {
		t.Fatalf("missing target err=%v", err)
	}
}

func TestGitPreparePreservesSuccessfulExactCheckout(t *testing.T) {
	git, repo, job, _, _, _, _ := gitPrepareFixture(t)
	mirror, worktree, err := git.Prepare(t.Context(), repo, job, "token-that-must-not-persist")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(git.MirrorsDir, "7.git"); mirror != want {
		t.Fatalf("mirror=%q want=%q", mirror, want)
	}
	if want := filepath.Join(git.WorktreesDir, "7", "11"); worktree != want {
		t.Fatalf("worktree=%q want=%q", worktree, want)
	}
	if got := strings.TrimSpace(runGit(t, worktree, "rev-parse", "HEAD")); got != job.TargetSHA {
		t.Fatalf("worktree HEAD=%q want=%q", got, job.TargetSHA)
	}
}

func TestGitPrepareFetchesExactSHAAfterBranchRewrite(t *testing.T) {
	git, repo, job, _, _, source, origin := gitPrepareFixture(t)
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("rewritten\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "commit", "-m", "rewrite")
	runGit(t, source, "push", "--force", "origin", "main")
	runGit(t, "", "--git-dir", origin, "config", "uploadpack.allowAnySHA1InWant", "true")

	_, worktree, err := git.PrepareCommit(t.Context(), repo, job.ID, job.TargetSHA, "token-that-must-not-persist")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGit(t, worktree, "rev-parse", "HEAD")); got != job.TargetSHA {
		t.Fatalf("worktree HEAD=%q want=%q", got, job.TargetSHA)
	}
}

func TestMirrorLockSerializesUsers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "7.lock")
	unlock, err := lockMirror(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	var wait sync.WaitGroup
	wait.Add(1)
	var secondErr error
	go func() {
		defer wait.Done()
		_, secondErr = lockMirror(ctx, path)
	}()
	wait.Wait()
	if !errors.Is(secondErr, context.DeadlineExceeded) {
		t.Fatalf("lockMirror() error = %v", secondErr)
	}
}

func gitPrepareFixture(t *testing.T) (Git, repository.Repository, postgres.IndexJob, string, string, string, string) {
	t.Helper()
	requireGit(t)
	projectRoot := t.TempDir()
	origin := filepath.Join(projectRoot, "acme", "repo.git")
	if err := os.MkdirAll(filepath.Dir(origin), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, "", "init", "--bare", origin)
	source := t.TempDir()
	runGit(t, source, "init", "-b", "main")
	runGit(t, source, "config", "user.name", "Test")
	runGit(t, source, "config", "user.email", "test@example.com")
	runGit(t, source, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "commit", "-m", "target")
	target := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD"))
	runGit(t, source, "tag", "not-fetched")
	runGit(t, source, "remote", "add", "origin", origin)
	runGit(t, source, "push", "origin", "main", "not-fetched")

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		if !ok {
			writer.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if username != "x-access-token" || password != "token-that-must-not-persist" {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		gitHTTPBackend(projectRoot).ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)
	certificate := server.Certificate()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := x509.ParseCertificate(certificate.Raw); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	promptsFile := filepath.Join(directory, "prompts")
	askPass := filepath.Join(directory, "askpass")
	askPassScript := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> '" + promptsFile + "'\ncase \"$1\" in\nUsername*) printf '%s\\n' x-access-token;;\nPassword*) printf '%s\\n' \"$GREPNEST_GIT_TOKEN\";;\n*) exit 1;;\nesac\n"
	if err := os.WriteFile(askPass, []byte(askPassScript), 0o700); err != nil {
		t.Fatal(err)
	}
	git := Git{
		Binary:       gitBinary(t),
		BaseURL:      server.URL,
		AskPass:      askPass,
		CABundle:     caFile,
		MirrorsDir:   filepath.Join(directory, "mirrors"),
		WorktreesDir: filepath.Join(directory, "worktrees"),
		Runner:       Runner{MaxOutput: 64 << 10, KillGrace: 100 * time.Millisecond},
	}
	repo := repository.Repository{ID: 7, ZoektID: 17, Name: "acme/repo", Branch: "main", WebURL: "https://ghe.example/acme/repo"}
	job := postgres.IndexJob{ID: 11, RepositoryID: 7, TargetSHA: target}
	return git, repo, job, promptsFile, server.URL, source, origin
}

func TestGitPruneRemovesOnlyInactiveNumericWorktrees(t *testing.T) {
	root := t.TempDir()
	git := Git{Binary: gitBinary(t), MirrorsDir: filepath.Join(root, "mirrors"), WorktreesDir: filepath.Join(root, "worktrees"), Runner: Runner{MaxOutput: 1024, KillGrace: time.Millisecond}}
	for _, path := range []string{"7/11", "7/12", "7/not-a-job"} {
		if err := os.MkdirAll(filepath.Join(git.WorktreesDir, path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := git.Prune(t.Context(), map[int64]struct{}{11: {}}); err != nil {
		t.Fatal(err)
	}
	for path, exists := range map[string]bool{"7/11": true, "7/12": false, "7/not-a-job": true} {
		_, err := os.Stat(filepath.Join(git.WorktreesDir, path))
		if (err == nil) != exists {
			t.Fatalf("path=%s exists=%v err=%v", path, exists, err)
		}
	}
}

func TestGitRejectsSymlinkedMirror(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "external.git")
	runGit(t, "", "init", "--bare", external)
	mirrors := filepath.Join(root, "mirrors")
	if err := os.MkdirAll(mirrors, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(mirrors, "7.git")); err != nil {
		t.Fatal(err)
	}
	git := Git{Binary: gitBinary(t), BaseURL: "https://127.0.0.1:1", AskPass: "/usr/bin/false", MirrorsDir: mirrors, WorktreesDir: filepath.Join(root, "worktrees"), Runner: Runner{MaxOutput: 1024, KillGrace: time.Millisecond}}
	repo := repository.Repository{ID: 7, Name: "acme/repo", Branch: "main"}
	job := postgres.IndexJob{ID: 11, RepositoryID: 7, TargetSHA: gitTargetSHA}
	if _, _, err := git.Prepare(t.Context(), repo, job, "secret"); err == nil {
		t.Fatal("symlinked mirror was accepted")
	}
	command := exec.Command(gitBinary(t), "--git-dir", external, "config", "--get", "remote.origin.url")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("external repository was modified: %s", output)
	}
}

func TestGitCleanupRejectsSymlinkedRepositoryRoot(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "external")
	if err := os.MkdirAll(filepath.Join(external, "11"), 0o700); err != nil {
		t.Fatal(err)
	}
	worktrees := filepath.Join(root, "worktrees")
	if err := os.MkdirAll(worktrees, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(worktrees, "7")); err != nil {
		t.Fatal(err)
	}
	git := Git{MirrorsDir: filepath.Join(root, "mirrors"), WorktreesDir: worktrees}
	if err := git.Cleanup(t.Context(), 7, 11); err == nil {
		t.Fatal("symlinked repository root was accepted")
	}
	if _, err := os.Stat(filepath.Join(external, "11")); err != nil {
		t.Fatalf("external worktree was removed: %v", err)
	}
}

func TestGitRejectsMissingCredentialInputsBeforeDiskWrites(t *testing.T) {
	for _, test := range []struct{ name, askPass, token string }{
		{name: "askpass", token: "secret"},
		{name: "token", askPass: "/usr/bin/false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			git := Git{Binary: gitBinary(t), BaseURL: "https://ghe.example", AskPass: test.askPass, MirrorsDir: filepath.Join(root, "mirrors"), WorktreesDir: filepath.Join(root, "worktrees"), Runner: Runner{MaxOutput: 1024, KillGrace: time.Millisecond}}
			repo := repository.Repository{ID: 7, Name: "acme/repo", Branch: "main"}
			job := postgres.IndexJob{ID: 11, RepositoryID: 7, TargetSHA: gitTargetSHA}
			if _, _, err := git.Prepare(t.Context(), repo, job, test.token); err == nil {
				t.Fatal("missing credential input was accepted")
			}
			if _, err := os.Stat(git.MirrorsDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("disk was modified: %v", err)
			}
		})
	}
}

func TestGitEnvironmentEnablesFixedAskPassOnlyWithToken(t *testing.T) {
	git := Git{AskPass: "/proc/self/exe"}
	withToken := strings.Join(git.environment("secret", "https://ghe.example"), "\n")
	if !strings.Contains(withToken, "GREPNEST_ASKPASS_MODE=1") || !strings.Contains(withToken, "GREPNEST_GIT_TOKEN=secret") || !strings.Contains(withToken, "GREPNEST_ASKPASS_ORIGIN=https://ghe.example") {
		t.Fatalf("askpass environment = %q", withToken)
	}
	withoutToken := strings.Join(git.environment("", ""), "\n")
	if strings.Contains(withoutToken, "ASKPASS") || strings.Contains(withoutToken, "askPass") || strings.Contains(withoutToken, "GREPNEST_GIT_TOKEN") {
		t.Fatalf("cleanup environment contains credentials = %q", withoutToken)
	}
}

func TestGitCredentialOriginUsesValidatedHTTPSRemote(t *testing.T) {
	if got, err := credentialOrigin("https://ghe.example:8443/acme/repo.git"); err != nil || got != "https://ghe.example:8443" {
		t.Fatalf("origin=%q error=%v", got, err)
	}
	for _, remote := range []string{"http://ghe.example/acme/repo.git", "https://user@ghe.example/acme/repo.git", "not-a-url"} {
		if _, err := credentialOrigin(remote); err == nil {
			t.Fatalf("accepted remote %q", remote)
		}
	}
}

func TestGitCommandUsesFixedDeadline(t *testing.T) {
	git := Git{
		Binary: "/bin/sh", CommandTimeout: 10 * time.Millisecond,
		Runner: Runner{MaxOutput: 1024, KillGrace: time.Millisecond},
	}
	started := time.Now()
	err := git.run(t.Context(), []string{"PATH=/usr/bin:/bin"}, "-c", "/bin/sleep 5")
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("error=%v duration=%s", err, time.Since(started))
	}
}

func gitHTTPBackend(projectRoot string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		command := exec.Command("git", "http-backend")
		command.Env = append(os.Environ(),
			"GIT_PROJECT_ROOT="+projectRoot,
			"GIT_HTTP_EXPORT_ALL=1",
			"PATH_INFO="+request.URL.Path,
			"QUERY_STRING="+request.URL.RawQuery,
			"REQUEST_METHOD="+request.Method,
			"CONTENT_TYPE="+request.Header.Get("Content-Type"),
			"CONTENT_LENGTH="+strconv.FormatInt(request.ContentLength, 10),
		)
		command.Stdin = request.Body
		output, err := command.Output()
		if err != nil {
			http.Error(writer, "git backend failed", http.StatusInternalServerError)
			return
		}
		head, body, ok := strings.Cut(string(output), "\r\n\r\n")
		if !ok {
			http.Error(writer, "invalid git backend response", http.StatusInternalServerError)
			return
		}
		status := http.StatusOK
		for line := range strings.SplitSeq(head, "\r\n") {
			name, value, found := strings.Cut(line, ":")
			if !found {
				continue
			}
			value = strings.TrimSpace(value)
			if strings.EqualFold(name, "Status") {
				status, _ = strconv.Atoi(strings.Fields(value)[0])
				continue
			}
			writer.Header().Add(name, value)
		}
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, body)
	}
}

func gitBinary(t *testing.T) string {
	t.Helper()
	binary, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}
	return binary
}

func requireGit(t *testing.T) { _ = gitBinary(t) }

func runGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command(gitBinary(t), arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}
