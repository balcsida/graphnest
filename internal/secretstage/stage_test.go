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
		{name: "source symlink", data: []byte("token"), mode: 0o440, setup: func(t *testing.T, source, _ string) {
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
