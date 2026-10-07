package graphimport

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Server binaries must never link SQLite or the CodeGraph importer.
func TestServerBinariesDoNotLinkSQLite(t *testing.T) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(strings.TrimSpace(string(out)))
	for _, command := range []string{"graphnest-server", "graphnest-indexer", "graphnest-mcp", "graphnest-migrate", "graphnest-admin"} {
		cmd := exec.Command("go", "list", "-deps", "./cmd/"+command)
		cmd.Dir = root
		deps, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list -deps ./cmd/%s: %v", command, err)
		}
		for _, dep := range strings.Fields(string(deps)) {
			if strings.HasPrefix(dep, "modernc.org/") || dep == "github.com/balcsida/graphnest/internal/graphimport" {
				t.Errorf("%s links %s", command, dep)
			}
		}
	}
}
