package graphscan

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestScanSkipsUnsafeAndUnsupportedFiles(t *testing.T) {
	root := t.TempDir()
	writeScanFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeScanFile(t, filepath.Join(root, "nested", ".git", "config"), "ignored")
	writeScanFile(t, filepath.Join(root, "submodule", ".git"), "gitdir: ../.git/modules/submodule\n")
	writeScanFile(t, filepath.Join(root, "binary.go"), "\x00not source")
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "leak.go")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "pipe.go"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Scan(t.Context(), scanRequest(root), map[string]Parser{".go": scanParser}, scanLimits())
	if err != nil || len(got.Nodes) != 2 || got.Nodes[0].Path != "main.go" {
		t.Fatalf("Scan() = %#v, %v", got, err)
	}
}

func TestScanEnforcesByteAndFileLimits(t *testing.T) {
	root := t.TempDir()
	writeScanFile(t, filepath.Join(root, "one.go"), "one")
	writeScanFile(t, filepath.Join(root, "two.go"), "two")
	for _, limits := range []Limits{
		{MaxFileBytes: 2, MaxTotalBytes: 10, MaxFiles: 2, MaxNodes: 10, MaxEdges: 10, ParseTimeout: time.Second},
		{MaxFileBytes: 10, MaxTotalBytes: 5, MaxFiles: 2, MaxNodes: 10, MaxEdges: 10, ParseTimeout: time.Second},
		{MaxFileBytes: 10, MaxTotalBytes: 10, MaxFiles: 1, MaxNodes: 10, MaxEdges: 10, ParseTimeout: time.Second},
	} {
		if _, err := Scan(t.Context(), scanRequest(root), map[string]Parser{".go": scanParser}, limits); err == nil {
			t.Fatal("Scan() error = nil, want limit error")
		}
	}
}

func TestScanEnforcesGraphAndParserLimits(t *testing.T) {
	root := t.TempDir()
	writeScanFile(t, filepath.Join(root, "main.go"), "package main")
	for _, test := range []struct {
		name    string
		parser  Parser
		limits  Limits
		context context.Context
	}{
		{"nodes", func(context.Context, string, []byte) (File, error) {
			return File{Declarations: []Declaration{{LocalID: "x", QualifiedName: "x", Kind: "Function"}}}, nil
		}, Limits{MaxFileBytes: 100, MaxTotalBytes: 100, MaxFiles: 1, MaxNodes: 2, MaxEdges: 10, ParseTimeout: time.Second}, t.Context()},
		{"edges", func(context.Context, string, []byte) (File, error) {
			return File{Imports: []Import{{Target: "x"}}}, nil
		}, Limits{MaxFileBytes: 100, MaxTotalBytes: 100, MaxFiles: 1, MaxNodes: 10, MaxEdges: 1, ParseTimeout: time.Second}, t.Context()},
		{"timeout", func(ctx context.Context, _ string, _ []byte) (File, error) { <-ctx.Done(); return File{}, ctx.Err() }, Limits{MaxFileBytes: 100, MaxTotalBytes: 100, MaxFiles: 1, MaxNodes: 10, MaxEdges: 10, ParseTimeout: time.Millisecond}, t.Context()},
		{"canceled", scanParser, scanLimits(), canceledContext()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Scan(test.context, scanRequest(root), map[string]Parser{".go": test.parser}, test.limits); err == nil {
				t.Fatal("Scan() error = nil")
			}
		})
	}
}

func TestScanRejectsParserThatReturnsAfterTimeout(t *testing.T) {
	root := t.TempDir()
	writeScanFile(t, filepath.Join(root, "main.go"), "package main")
	limits := scanLimits()
	limits.ParseTimeout = time.Millisecond
	_, err := Scan(t.Context(), scanRequest(root), map[string]Parser{".go": func(ctx context.Context, _ string, _ []byte) (File, error) {
		<-ctx.Done()
		return File{}, nil
	}}, limits)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Scan() error = %v, want deadline exceeded", err)
	}
}

func TestReadBoundedDoesNotFollowSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	writeScanFile(t, target, "secret")
	link := filepath.Join(root, "link.go")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readBounded(link, 100, 100); err == nil {
		t.Fatal("readBounded() error = nil, want symlink rejection")
	}
}

func TestScanRejectsMaxIntFileLimit(t *testing.T) {
	limits := scanLimits()
	limits.MaxFileBytes = math.MaxInt64
	if _, err := Scan(t.Context(), scanRequest(t.TempDir()), nil, limits); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Scan() error = %v, want invalid request", err)
	}
}

func TestScanUsesCleanRelativePathsDeterministically(t *testing.T) {
	root := t.TempDir()
	writeScanFile(t, filepath.Join(root, "z.go"), "z")
	writeScanFile(t, filepath.Join(root, "a.go"), "a")
	first, err := Scan(t.Context(), scanRequest(root), map[string]Parser{".go": scanParser}, scanLimits())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Scan(t.Context(), scanRequest(root), map[string]Parser{".go": scanParser}, scanLimits())
	if err != nil || !sameArtifact(first, second) || first.Nodes[0].Path != "a.go" {
		t.Fatalf("Scan() = %#v, %#v, %v", first, second, err)
	}
}

func scanRequest(root string) Request {
	return Request{RepositoryID: 101, Commit: strings.Repeat("a", 40), Root: root}
}
func scanLimits() Limits {
	return Limits{MaxFileBytes: 100, MaxTotalBytes: 1_000, MaxFiles: 10, MaxNodes: 100, MaxEdges: 100, ParseTimeout: time.Second}
}
func scanParser(_ context.Context, path string, _ []byte) (File, error) {
	return File{Path: path, Language: Go}, nil
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func writeScanFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
