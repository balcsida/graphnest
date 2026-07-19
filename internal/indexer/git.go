//go:build unix

package indexer

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/grepnest/grepnest/internal/repository"
)

var ErrTargetMissing = errors.New("target commit unavailable")

type Git struct {
	Binary, BaseURL, AskPass, CABundle, MirrorsDir, WorktreesDir string
	Runner                                                       Runner
}

func (git *Git) Prepare(ctx context.Context, repo repository.Repository, job postgres.IndexJob, token string) (string, string, error) {
	if git == nil || git.Binary == "" || git.AskPass == "" || token == "" || git.MirrorsDir == "" || git.WorktreesDir == "" || git.Runner.MaxOutput <= 0 || git.Runner.KillGrace <= 0 || repo.ID <= 0 || repo.ZoektID == 0 || repo.Name == "" || repo.WebURL == "" || job.ID <= 0 || job.RepositoryID != repo.ID || !validSHA(job.TargetSHA) || repo.Branch == "" {
		return "", "", errors.New("invalid Git repository job")
	}
	remote, err := git.remoteURL(repo.Name)
	if err != nil {
		return "", "", err
	}
	mirrorBase, err := numericPath(git.MirrorsDir, strconv.FormatInt(repo.ID, 10))
	if err != nil {
		return "", "", err
	}
	mirror := mirrorBase + ".git"
	worktree, err := numericPath(git.WorktreesDir, strconv.FormatInt(repo.ID, 10), strconv.FormatInt(job.ID, 10))
	if err != nil {
		return "", "", err
	}
	if err := ensureDirectory(git.MirrorsDir); err != nil {
		return "", "", err
	}
	if err := ensureDirectory(filepath.Dir(worktree)); err != nil {
		return "", "", err
	}
	environment := git.environment(token)
	if info, err := os.Lstat(mirror); errors.Is(err, os.ErrNotExist) {
		if err := git.run(ctx, environment, "init", "--bare", mirror); err != nil {
			return "", "", err
		}
	} else if err != nil {
		return "", "", err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("mirror must be a real directory")
	}
	ref := "refs/heads/" + repo.Branch
	refspec := "+" + ref + ":" + ref
	for _, arguments := range [][]string{
		{"check-ref-format", ref},
		{"--git-dir", mirror, "config", "remote.origin.url", remote},
		{"--git-dir", mirror, "config", "--replace-all", "remote.origin.fetch", refspec},
		{"--git-dir", mirror, "config", "remote.origin.tagOpt", "--no-tags"},
		{"--git-dir", mirror, "config", "zoekt.repoid", strconv.FormatUint(uint64(repo.ZoektID), 10)},
		{"--git-dir", mirror, "config", "zoekt.name", repo.Name},
		{"--git-dir", mirror, "config", "zoekt.web-url", repo.WebURL},
		{"--git-dir", mirror, "config", "zoekt.web-url-type", "github"},
		{"--git-dir", mirror, "fetch", "--no-tags", "--prune", "origin"},
	} {
		if err := git.run(ctx, environment, arguments...); err != nil {
			return "", "", err
		}
	}
	if err := git.run(ctx, environment, "--git-dir", mirror, "cat-file", "-e", job.TargetSHA+"^{commit}"); err != nil {
		return "", "", errors.Join(ErrTargetMissing, err)
	}
	if _, err := os.Lstat(worktree); err == nil {
		return "", "", errors.New("worktree already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	if err := git.run(ctx, environment, "--git-dir", mirror, "worktree", "add", "--detach", worktree, job.TargetSHA); err != nil {
		return "", "", err
	}
	return mirror, worktree, nil
}

func (git *Git) Cleanup(ctx context.Context, repositoryID, jobID int64) error {
	worktree, err := numericPath(git.WorktreesDir, strconv.FormatInt(repositoryID, 10), strconv.FormatInt(jobID, 10))
	if err != nil {
		return err
	}
	if err := realDirectory(filepath.Dir(worktree)); err == nil {
		if err := os.RemoveAll(worktree); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	mirrorBase, err := numericPath(git.MirrorsDir, strconv.FormatInt(repositoryID, 10))
	if err != nil {
		return err
	}
	mirror := mirrorBase + ".git"
	if err := realDirectory(mirror); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return git.run(ctx, git.environment(""), "--git-dir", mirror, "worktree", "prune")
}

func (git *Git) Prune(ctx context.Context, active map[int64]struct{}) error {
	repositories, err := os.ReadDir(git.WorktreesDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, repositoryEntry := range repositories {
		repositoryID, ok := numericID(repositoryEntry.Name())
		if !ok || !repositoryEntry.IsDir() {
			continue
		}
		repositoryPath, _ := numericPath(git.WorktreesDir, repositoryEntry.Name())
		jobs, err := os.ReadDir(repositoryPath)
		if err != nil {
			return err
		}
		for _, jobEntry := range jobs {
			jobID, ok := numericID(jobEntry.Name())
			if !ok || !jobEntry.IsDir() {
				continue
			}
			if _, keep := active[jobID]; keep {
				continue
			}
			if err := git.Cleanup(ctx, repositoryID, jobID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (git *Git) run(ctx context.Context, environment []string, arguments ...string) error {
	if git.Binary == "" {
		return errors.New("Git binary is required")
	}
	return git.Runner.Run(ctx, git.Binary, arguments, environment, "")
}

func (git *Git) environment(token string) []string {
	values := [][2]string{
		{"credential.helper", ""},
		{"core.askPass", git.AskPass},
		{"http.followRedirects", "false"},
		{"protocol.file.allow", "never"},
		{"core.hooksPath", "/dev/null"},
		{"filter.lfs.smudge", ""},
		{"filter.lfs.required", "false"},
		{"submodule.recurse", "false"},
	}
	if git.CABundle != "" {
		values = append(values, [2]string{"http.sslCAInfo", git.CABundle})
	}
	environment := []string{
		"LANG=C", "LC_ALL=C", "PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.TempDir(),
		"GIT_ASKPASS=" + git.AskPass, "GIT_TERMINAL_PROMPT=0", "GREPNEST_GIT_TOKEN=" + token,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_COUNT=" + strconv.Itoa(len(values)),
	}
	for index, value := range values {
		environment = append(environment,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", index, value[0]),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", index, value[1]),
		)
	}
	return environment
}

func (git *Git) remoteURL(name string) (string, error) {
	owner, repositoryName, ok := strings.Cut(name, "/")
	if !ok || strings.Contains(repositoryName, "/") || !validOwner(owner) || !validRepositoryName(repositoryName) {
		return "", errors.New("invalid GitHub repository name")
	}
	base, err := url.Parse(git.BaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", errors.New("invalid Git base URL")
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + "/" + owner + "/" + repositoryName + ".git"
	base.RawPath = ""
	return base.String(), nil
}

func numericPath(root string, ids ...string) (string, error) {
	if root == "" {
		return "", errors.New("path root is required")
	}
	for _, id := range ids {
		if _, ok := numericID(id); !ok {
			return "", errors.New("path component must be a positive numeric ID")
		}
	}
	return filepath.Join(append([]string{root}, ids...)...), nil
}

func ensureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return realDirectory(path)
}

func realDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("path must be a real directory")
	}
	return nil
}

func numericID(value string) (int64, bool) {
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

func validSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validOwner(value string) bool {
	if len(value) == 0 || len(value) > 39 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') && character != '-' {
			return false
		}
	}
	return true
}

func validRepositoryName(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') && !strings.ContainsRune("._-", character) {
			return false
		}
	}
	return true
}
