//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/enrichment"
)

func TestRunEnrichmentScansProvidedSnapshot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	var output bytes.Buffer
	if err := runEnrichment(t.Context(), []string{"--protocol=1", "--root=" + root, "--repository-id=4", "--commit=" + sha}, &output); err != nil {
		t.Fatal(err)
	}
	var response enrichment.Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || response.Version != enrichment.ProtocolVersion ||
		response.Artifact.RepositoryID != 4 || response.Artifact.Commit != sha || len(response.Artifact.Nodes) == 0 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestRunEnrichmentRejectsOutputPathAndStaleSHA(t *testing.T) {
	for _, args := range [][]string{
		{"--protocol=1", "--root=/snapshot", "--repository-id=4", "--commit=" + strings.Repeat("A", 40)},
		{"--protocol=1", "--root=/snapshot", "--repository-id=4", "--commit=" + strings.Repeat("a", 40), "--output=/tmp/artifact"},
	} {
		if err := runEnrichment(t.Context(), args, &bytes.Buffer{}); err == nil {
			t.Fatalf("runEnrichment(%q) succeeded", args)
		}
	}
}
