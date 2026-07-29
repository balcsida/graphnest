//go:build darwin

package agentskills

import "golang.org/x/sys/unix"

func atomicSwap(left, right string) error {
	return unix.RenameatxNp(unix.AT_FDCWD, left, unix.AT_FDCWD, right, unix.RENAME_SWAP)
}
