//go:build !darwin && !linux

package agentskills

import "errors"

func atomicSwap(_, _ string) error {
	return errors.New("atomic agent skill replacement is unsupported on this platform")
}
