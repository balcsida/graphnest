//go:build linux

package agentskills

import "golang.org/x/sys/unix"

func atomicSwap(left, right string) error {
	return unix.Renameat2(unix.AT_FDCWD, left, unix.AT_FDCWD, right, unix.RENAME_EXCHANGE)
}
