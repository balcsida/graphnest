package webui

import (
	"os/exec"
	"testing"
)

func requireNode(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not available; skipping optional JavaScript contract")
	}
	return path
}
