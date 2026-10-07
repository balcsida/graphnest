package graphimport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Git is what freshness verification needs from a repository.
type Git interface {
	// Archive streams the tree of commit as a tar archive (git archive --format=tar).
	Archive(ctx context.Context, repo, commit string) (io.ReadCloser, error)
	// Ignored evaluates gitignore patterns against relative paths and returns the ignored ones.
	Ignored(ctx context.Context, patterns string, ignoreCase bool, paths []string) ([]string, error)
}

// ExecGit implements Git with the git command-line tool.
type ExecGit struct{}

type archiveStream struct {
	io.ReadCloser
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stderr *bytes.Buffer
	eof    bool
}

func (a *archiveStream) Read(p []byte) (int, error) {
	n, err := a.ReadCloser.Read(p)
	if err == io.EOF {
		a.eof = true
	}
	return n, err
}

// Close waits for git. Closing before the end of the stream stops git and reports no error.
func (a *archiveStream) Close() error {
	if !a.eof {
		a.cancel()
	}
	err := a.cmd.Wait()
	a.cancel()
	if err != nil && a.eof {
		return fmt.Errorf("git archive: %w: %s", err, strings.TrimSpace(a.stderr.String()))
	}
	return nil
}

// Archive runs git -C repo archive --format=tar commit and streams its stdout.
func (ExecGit) Archive(ctx context.Context, repo, commit string) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "archive", "--format=tar", commit)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("git archive: %w", err)
	}
	return &archiveStream{ReadCloser: stdout, cmd: cmd, cancel: cancel, stderr: stderr}, nil
}

// Ignored writes patterns to a .gitignore in a temporary repository and asks git check-ignore
// which of paths they ignore. Neither the user's nor the system's git configuration applies.
func (ExecGit) Ignored(ctx context.Context, patterns string, ignoreCase bool, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	dir, err := os.MkdirTemp("", "graphnest-ignore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := runGit(ctx, env, dir, nil, "init", "-q", "--template="); err != nil {
		return nil, fmt.Errorf("git init: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(patterns), 0o600); err != nil {
		return nil, err
	}
	stdin := strings.Join(paths, "\x00") + "\x00"
	out, err := runGit(ctx, env, dir, strings.NewReader(stdin), "-c", "core.ignorecase="+strconv.FormatBool(ignoreCase), "-c", "core.excludesFile=/dev/null", "check-ignore", "-z", "--stdin", "--no-index")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil, nil // nothing ignored
	}
	if err != nil {
		return nil, fmt.Errorf("git check-ignore: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var ignored []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			ignored = append(ignored, p)
		}
	}
	return ignored, nil
}

// runGit returns stdout, or stderr when the command fails.
func runGit(ctx context.Context, env []string, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stderr.Bytes(), err
	}
	return stdout.Bytes(), nil
}
