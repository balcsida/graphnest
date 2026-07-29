//go:build darwin || linux

package graphscan

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func openSourceRoot(path string) (*sourceRoot, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	return &sourceRoot{file: os.NewFile(uintptr(fd), path)}, nil
}

func openSourceFile(root *sourceRoot, path string) (*os.File, error) {
	parts := strings.Split(path, "/")
	fd := int(root.file.Fd())
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(fd, part, flags, 0)
		if i > 0 {
			_ = unix.Close(fd)
		}
		if err != nil {
			return nil, err
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}
