//go:build unix

package secretstage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyStagesBoundedPrivateSecret(t *testing.T) {
	source := writeSource(t, []byte("token"), 0o440)
	destination := filepath.Join(t.TempDir(), "secret")
	if err := Copy(source, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "token" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	info, err := os.Lstat(destination)
	if err != nil || info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
}

func TestCopyStagesProjectedSecret(t *testing.T) {
	root, source := projectedSecret(t, "one", []byte("projected-token"))
	destination := filepath.Join(t.TempDir(), "secret")
	if err := Copy(source, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "projected-token" {
		t.Fatalf("root=%s data=%q err=%v", root, data, err)
	}
}

func TestCopyKeepsResolvedVersionAcrossDataSwap(t *testing.T) {
	root, source := projectedSecret(t, "one", []byte("first"))
	writeProjectedVersion(t, root, "two", []byte("second"))
	destination := filepath.Join(t.TempDir(), "secret")
	ops := systemOperations()
	open := ops.open
	swapped := false
	ops.open = func(path string) (stageFile, error) {
		if !swapped {
			swapped = true
			next := filepath.Join(root, "..data-next")
			mustSymlink(t, "..version-two", next)
			if err := os.Rename(next, filepath.Join(root, "..data")); err != nil {
				t.Fatal(err)
			}
		}
		return open(path)
	}
	if err := copyWith(source, destination, ops); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "first" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestCopyRejectsProjectedSecretEscapes(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string, string) string
	}{
		{name: "absolute key", setup: func(t *testing.T, root, outside string) string {
			source := filepath.Join(root, "secret")
			mustSymlink(t, outside, source)
			return source
		}},
		{name: "relative escape", setup: func(t *testing.T, root, _ string) string {
			source := filepath.Join(root, "secret")
			mustSymlink(t, "../outside", source)
			return source
		}},
		{name: "escaping chain", setup: func(t *testing.T, root, _ string) string {
			mustSymlink(t, "first", filepath.Join(root, "secret"))
			mustSymlink(t, "../outside", filepath.Join(root, "first"))
			return filepath.Join(root, "secret")
		}},
		{name: "escaping directory component", setup: func(t *testing.T, root, outside string) string {
			outsideDir := filepath.Dir(outside)
			if err := os.WriteFile(filepath.Join(outsideDir, "key"), []byte("outside"), 0o440); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, outsideDir, filepath.Join(root, "nested"))
			mustSymlink(t, "nested/key", filepath.Join(root, "secret"))
			return filepath.Join(root, "secret")
		}},
		{name: "escaping data link", setup: func(t *testing.T, root, outside string) string {
			outsideDir := filepath.Dir(outside)
			if err := os.WriteFile(filepath.Join(outsideDir, "secret"), []byte("outside"), 0o440); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, outsideDir, filepath.Join(root, "..data"))
			mustSymlink(t, "..data/secret", filepath.Join(root, "secret"))
			return filepath.Join(root, "secret")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("outside"), 0o440); err != nil {
				t.Fatal(err)
			}
			source := test.setup(t, root, outside)
			if err := Copy(source, filepath.Join(t.TempDir(), "secret")); !errors.Is(err, ErrInvalidSource) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestCopyRejectsRacedProjectedTarget(t *testing.T) {
	root, source := projectedSecret(t, "one", []byte("first"))
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o440); err != nil {
		t.Fatal(err)
	}
	ops := systemOperations()
	open := ops.open
	ops.open = func(path string) (stageFile, error) {
		old := path + "-old"
		if err := os.Rename(path, old); err != nil {
			t.Fatal(err)
		}
		mustSymlink(t, outside, path)
		return open(path)
	}
	err := copyWith(source, filepath.Join(t.TempDir(), "secret"), ops)
	if !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("root=%s err=%v", root, err)
	}
}

func TestCopyRejectsUnsafeInputs(t *testing.T) {
	tests := []struct {
		name  string
		data  []byte
		mode  os.FileMode
		setup func(*testing.T, string, string)
	}{
		{name: "empty", mode: 0o440},
		{name: "oversized", data: make([]byte, maxSecretBytes+1), mode: 0o440},
		{name: "writable source", data: []byte("token"), mode: 0o660},
		{name: "absolute source symlink", data: []byte("token"), mode: 0o440, setup: func(t *testing.T, source, _ string) {
			target := source + "-target"
			if err := os.Rename(source, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, source); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "existing destination", data: []byte("token"), mode: 0o440, setup: func(t *testing.T, _, destination string) {
			if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "destination symlink", data: []byte("token"), mode: 0o440, setup: func(t *testing.T, _, destination string) {
			if err := os.Symlink(destination+"-target", destination); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := writeSource(t, test.data, test.mode)
			destination := filepath.Join(t.TempDir(), "secret")
			if test.setup != nil {
				test.setup(t, source, destination)
			}
			if err := Copy(source, destination); err == nil {
				t.Fatal("unsafe stage succeeded")
			}
		})
	}
}

func TestCopyCleansPartialFilesAndSanitizesErrors(t *testing.T) {
	for _, failure := range []string{"source close", "write", "sync", "destination close", "publish", "directory sync", "directory close"} {
		t.Run(failure, func(t *testing.T) {
			source := writeSource(t, []byte("super-secret"), 0o440)
			directory := t.TempDir()
			destination := filepath.Join(directory, "secret")
			ops := systemOperations()
			injectFailure(t, &ops, failure)
			err := copyWith(source, destination, ops)
			if err == nil || strings.Contains(err.Error(), source) ||
				strings.Contains(err.Error(), destination) || strings.Contains(err.Error(), "super-secret") {
				t.Fatalf("unsanitized error: %v", err)
			}
			matches, globErr := filepath.Glob(filepath.Join(directory, ".secret.tmp-*"))
			if globErr != nil || len(matches) != 0 {
				t.Fatalf("partial files=%v err=%v", matches, globErr)
			}
			if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("partial destination remains: %v", statErr)
			}
		})
	}
}

type failingFile struct {
	*os.File
	writeErr, syncErr, closeErr error
}

func (file *failingFile) Write(data []byte) (int, error) {
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	return file.File.Write(data)
}

func (file *failingFile) Sync() error {
	if file.syncErr != nil {
		return file.syncErr
	}
	return file.File.Sync()
}

func (file *failingFile) Close() error {
	if file.closeErr != nil {
		_ = file.File.Close()
		return file.closeErr
	}
	return file.File.Close()
}

func injectFailure(t *testing.T, ops *operations, failure string) {
	t.Helper()
	boom := errors.New("sensitive backend detail")
	switch failure {
	case "source close":
		open := ops.open
		ops.open = func(path string) (stageFile, error) {
			file, err := open(path)
			if err != nil {
				return nil, err
			}
			return &failingFile{File: file.(*os.File), closeErr: boom}, nil
		}
	case "write", "sync", "destination close":
		createTemp := ops.createTemp
		ops.createTemp = func(dir, pattern string) (stageFile, error) {
			file, err := createTemp(dir, pattern)
			if err != nil {
				return nil, err
			}
			wrapper := &failingFile{File: file.(*os.File)}
			if failure == "write" {
				wrapper.writeErr = boom
			} else if failure == "sync" {
				wrapper.syncErr = boom
			} else {
				wrapper.closeErr = boom
			}
			return wrapper, nil
		}
	case "publish":
		ops.publish = func(_, _ string) error { return boom }
	case "directory sync", "directory close":
		openDir := ops.openDir
		ops.openDir = func(path string) (stageFile, error) {
			file, err := openDir(path)
			if err != nil {
				return nil, err
			}
			wrapper := &failingFile{File: file.(*os.File)}
			if failure == "directory sync" {
				wrapper.syncErr = boom
			} else {
				wrapper.closeErr = boom
			}
			return wrapper, nil
		}
	}
}

func writeSource(t *testing.T, data []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func projectedSecret(t *testing.T, version string, data []byte) (string, string) {
	t.Helper()
	root := t.TempDir()
	writeProjectedVersion(t, root, version, data)
	mustSymlink(t, "..version-"+version, filepath.Join(root, "..data"))
	mustSymlink(t, "..data/secret", filepath.Join(root, "secret"))
	return root, filepath.Join(root, "secret")
}

func writeProjectedVersion(t *testing.T, root, version string, data []byte) {
	t.Helper()
	directory := filepath.Join(root, "..version-"+version)
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "secret")
	if err := os.WriteFile(path, data, 0o440); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o440); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
