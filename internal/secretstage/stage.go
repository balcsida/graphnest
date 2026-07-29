//go:build unix

package secretstage

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxSecretBytes = 4 << 10
	maxSymlinks    = 32
)

var (
	ErrUsage              = errors.New("stage-secret requires source and destination paths")
	ErrInvalidSource      = errors.New("secret source is invalid")
	ErrInvalidDestination = errors.New("secret destination is invalid")
	ErrStageFailed        = errors.New("secret staging failed")
)

type stageFile interface {
	io.Reader
	io.Writer
	Stat() (os.FileInfo, error)
	Chmod(os.FileMode) error
	Sync() error
	Close() error
	Name() string
}

type operations struct {
	lstat      func(string) (os.FileInfo, error)
	open       func(*os.Root, string) (stageFile, error)
	createTemp func(string, string) (stageFile, error)
	publish    func(string, string) error
	remove     func(string) error
	openDir    func(string) (stageFile, error)
}

func systemOperations() operations {
	return operations{
		lstat: os.Lstat,
		open: func(root *os.Root, name string) (stageFile, error) {
			return root.Open(name)
		},
		createTemp: func(directory, pattern string) (stageFile, error) {
			return os.CreateTemp(directory, pattern)
		},
		publish: os.Link,
		remove:  os.Remove,
		openDir: func(path string) (stageFile, error) {
			return os.Open(path)
		},
	}
}

func Copy(source, destination string) error {
	return copyWith(source, destination, systemOperations())
}

func copyWith(source, destination string, ops operations) error {
	sourceRoot, sourceName, sourceInfo, err := resolveSource(source, ops)
	if err != nil {
		return ErrInvalidSource
	}
	if _, err = ops.lstat(destination); !errors.Is(err, os.ErrNotExist) {
		_ = sourceRoot.Close()
		return ErrInvalidDestination
	}
	directory := filepath.Dir(destination)
	directoryInfo, err := ops.lstat(directory)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		_ = sourceRoot.Close()
		return ErrInvalidDestination
	}

	sourceFile, err := ops.open(sourceRoot, sourceName)
	if err != nil {
		_ = sourceRoot.Close()
		return ErrInvalidSource
	}
	openedInfo, statErr := sourceFile.Stat()
	rootCloseErr := sourceRoot.Close()
	if statErr != nil || rootCloseErr != nil || !safeSource(openedInfo) ||
		!os.SameFile(sourceInfo, openedInfo) {
		_ = sourceFile.Close()
		return ErrInvalidSource
	}
	data, readErr := io.ReadAll(io.LimitReader(sourceFile, maxSecretBytes+1))
	closeErr := sourceFile.Close()
	if readErr != nil || closeErr != nil || len(data) == 0 || len(data) > maxSecretBytes {
		return ErrInvalidSource
	}

	temp, err := ops.createTemp(directory, "."+filepath.Base(destination)+".tmp-*")
	if err != nil {
		return ErrStageFailed
	}
	tempPath := temp.Name()
	published := false
	defer func() {
		_ = temp.Close()
		if !published {
			_ = ops.remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return ErrStageFailed
	}
	if _, err := io.Copy(temp, bytes.NewReader(data)); err != nil {
		return ErrStageFailed
	}
	if err := temp.Sync(); err != nil {
		return ErrStageFailed
	}
	if err := temp.Close(); err != nil {
		return ErrStageFailed
	}
	if err := ops.publish(tempPath, destination); err != nil {
		return ErrInvalidDestination
	}
	if err := ops.remove(tempPath); err != nil {
		_ = ops.remove(destination)
		return ErrStageFailed
	}
	published = true

	directoryFile, err := ops.openDir(directory)
	if err != nil {
		_ = ops.remove(destination)
		return ErrStageFailed
	}
	if err := directoryFile.Sync(); err != nil {
		_ = directoryFile.Close()
		_ = ops.remove(destination)
		return ErrStageFailed
	}
	if err := directoryFile.Close(); err != nil {
		_ = ops.remove(destination)
		return ErrStageFailed
	}
	return nil
}

func resolveSource(source string, ops operations) (*os.Root, string, os.FileInfo, error) {
	absolute, err := filepath.Abs(source)
	if err != nil {
		return nil, "", nil, ErrInvalidSource
	}
	root := filepath.Dir(filepath.Clean(absolute))
	rootInfo, err := ops.lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, "", nil, ErrInvalidSource
	}
	rootDirectory, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", nil, ErrInvalidSource
	}
	defer rootDirectory.Close()
	openedRootInfo, err := rootDirectory.Stat(".")
	if err != nil || !os.SameFile(rootInfo, openedRootInfo) {
		return nil, "", nil, ErrInvalidSource
	}
	relative := filepath.Base(absolute)
	for links := 0; links <= maxSymlinks; links++ {
		parts := strings.Split(relative, string(filepath.Separator))
		prefix := ""
		followed := false
		for index, part := range parts {
			if part == "" || part == "." || part == ".." {
				return nil, "", nil, ErrInvalidSource
			}
			candidate := filepath.Join(prefix, part)
			info, err := rootDirectory.Lstat(candidate)
			if err != nil {
				return nil, "", nil, ErrInvalidSource
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := rootDirectory.Readlink(candidate)
				if err != nil || target == "" || filepath.IsAbs(target) {
					return nil, "", nil, ErrInvalidSource
				}
				remaining := filepath.Join(parts[index+1:]...)
				relative = filepath.Clean(filepath.Join(prefix, target, remaining))
				if !insideRoot(relative) {
					return nil, "", nil, ErrInvalidSource
				}
				followed = true
				break
			}
			if index < len(parts)-1 {
				if !info.IsDir() {
					return nil, "", nil, ErrInvalidSource
				}
				prefix = filepath.Join(prefix, part)
				continue
			}
			if !safeSource(info) {
				return nil, "", nil, ErrInvalidSource
			}
			sourceRoot, err := rootDirectory.OpenRoot(filepath.Dir(candidate))
			if err != nil {
				return nil, "", nil, ErrInvalidSource
			}
			sourceName := filepath.Base(candidate)
			frozenInfo, err := sourceRoot.Lstat(sourceName)
			if err != nil || !safeSource(frozenInfo) || !os.SameFile(info, frozenInfo) {
				_ = sourceRoot.Close()
				return nil, "", nil, ErrInvalidSource
			}
			return sourceRoot, sourceName, frozenInfo, nil
		}
		if !followed {
			return nil, "", nil, ErrInvalidSource
		}
	}
	return nil, "", nil, ErrInvalidSource
}

func insideRoot(relative string) bool {
	return relative != "" && relative != "." && relative != ".." &&
		!filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func safeSource(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0o022 == 0 &&
		info.Size() > 0 && info.Size() <= maxSecretBytes
}
