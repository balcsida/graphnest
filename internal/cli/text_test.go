package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTextDryRun(t *testing.T) {
	code, stdout, stderr := run(t, testEnv(nil), importArgs(fixture, "--dry-run", "--format", "text")...)
	if code != 0 || stderr != "" {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	for _, want := range []string{"Index\n", "producer: codegraph ", "schema version: 9", "Repository\n  commit: " + sha, "status: fresh", "Counts\n  nodes: 68\n  edges: 93\n  files: 13",
		"node kinds:\n", "Diagnostics\n  unresolved references: 6", "dropped facts: none", "Artifact\n", "content hash: ", "Next step: write the artifact with --output FILE"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "{") || strings.Contains(stdout, "written to") {
		t.Errorf("not a plain text report:\n%s", stdout)
	}
}

func TestTextStaleOutputRefusal(t *testing.T) {
	tree := sourceTree()
	for i := 0; i < 25; i++ {
		tree["extra"+string(rune('a'+i))+".ts"] = []byte("export const x = 1\n")
	}
	tree["core.ts"] = append([]byte("x"), tree["core.ts"][1:]...)
	out := filepath.Join(t.TempDir(), "graph.pb")
	code, stdout, stderr := run(t, testEnvWith(nil, fakeRepository{archive: tarOf(tree)}), importArgs(fixture, "--output", out, "--format", "text")...)
	if code != 1 || !strings.Contains(stderr, "not fresh") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	for _, want := range []string{"status: stale", "modified (1):\n    core.ts", "not indexed (25):", "    ... and 5 more\n", "Next step: the index is not fresh (status=stale)"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in\n%s", want, stdout)
		}
	}
	if strings.Count(stdout, "    extra") != maxListed {
		t.Errorf("want %d listed entries:\n%s", maxListed, stdout)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("output was written")
	}
}

func TestTextUpload(t *testing.T) {
	path, _ := artifactFile(t)
	s := newPublishServer(t, sha, activeSeven)
	s.uploads = []http.HandlerFunc{respondResult(8, false)}
	code, stdout, stderr := run(t, s.env(fakeRepository{}), "graph", "upload", path, "--repository-id", "42", "--replace-producer", "--format", "text")
	if code != 0 || stderr != "" {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	for _, want := range []string{"Artifact\n  path: " + path, "Repository\n  repository id: 42\n  commit: " + sha, "Server\n  indexed commit: " + sha, "active generation before: 7 (codegraph 1, commit " + sha,
		"Publication\n  generation: 8\n  replaced generation: 7", "attempts: 1", "Next step: nothing; the generation is published."} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, token) {
		t.Error("token in output")
	}
}

func TestFormatUsageError(t *testing.T) {
	path, _ := artifactFile(t)
	for _, args := range [][]string{importArgs(fixture, "--dry-run", "--format", "yaml"), {"graph", "upload", path, "--repository-id", "42", "--format", "yaml"}} {
		if code, stdout, stderr := run(t, testEnv(nil), args...); code != 2 || stdout != "" || !strings.Contains(stderr, "--format") {
			t.Errorf("%v: code %d stdout %q stderr %q", args, code, stdout, stderr)
		}
	}
}
