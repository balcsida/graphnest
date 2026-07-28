//go:build unix

package secretstage

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const maxSecretBytes = 4 << 10

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
	open       func(string) (stageFile, error)
	createTemp func(string, string) (stageFile, error)
	publish    func(string, string) error
	remove     func(string) error
	openDir    func(string) (stageFile, error)
}

func systemOperations() operations {
	return operations{
		lstat: os.Lstat,
		open: func(path string) (stageFile, error) {
			return os.Open(path)
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
	sourceInfo, err := ops.lstat(source)
	if err != nil || !safeSource(sourceInfo) {
		return ErrInvalidSource
	}
	if _, err = ops.lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return ErrInvalidDestination
	}
	directory := filepath.Dir(destination)
	directoryInfo, err := ops.lstat(directory)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidDestination
	}

	sourceFile, err := ops.open(source)
	if err != nil {
		return ErrInvalidSource
	}
	openedInfo, statErr := sourceFile.Stat()
	data, readErr := io.ReadAll(io.LimitReader(sourceFile, maxSecretBytes+1))
	closeErr := sourceFile.Close()
	if statErr != nil || !safeSource(openedInfo) || !os.SameFile(sourceInfo, openedInfo) ||
		readErr != nil || closeErr != nil || len(data) == 0 || len(data) > maxSecretBytes {
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

func safeSource(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0o022 == 0
}
