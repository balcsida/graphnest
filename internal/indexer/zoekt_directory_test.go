//go:build unix

package indexer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/repository"
)

func TestZoektIndexWritesDirectoryMetadataOutsideSource(t *testing.T) {
	directory := t.TempDir()
	argumentsFile := filepath.Join(directory, "arguments")
	metadataFile := filepath.Join(directory, "metadata")
	metadataPathFile := filepath.Join(directory, "metadata-path")
	tempPathFile := filepath.Join(directory, "temp-path")
	environmentFile := filepath.Join(directory, "environment")
	binary := filepath.Join(directory, "zoekt-index")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > '" + argumentsFile + "'\n" +
		"while [ \"$#\" -gt 0 ]; do\n" +
		"  if [ \"$1\" = -meta ]; then printf '%s' \"$2\" > '" + metadataPathFile + "'; cp \"$2\" '" + metadataFile + "'; fi\n" +
		"  shift\n" +
		"done\n" +
		"printf '%s' \"$TMPDIR\" > '" + tempPathFile + "'\n" +
		"env > '" + environmentFile + "'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(directory, "snapshot")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	ambientTemp := filepath.Join(source, "ambient-temp")
	if err := os.Mkdir(ambientTemp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", ambientTemp)
	t.Setenv("GREPNEST_GIT_TOKEN", "must-not-leak")
	indexer := ZoektIndexer{Binary: binary, IndexDir: filepath.Join(directory, "index"), Runner: Runner{MaxOutput: 1024, KillGrace: time.Millisecond}}
	repo := repository.Repository{
		ID: 4, ZoektID: 7, Name: "acme/repo", Branch: "release/v3",
		DesiredSHA: "0123456789abcdef0123456789abcdef01234567",
		WebURL:     "https://ghe.example/acme/repo",
	}
	if err := indexer.Index(t.Context(), repo, source); err != nil {
		t.Fatal(err)
	}

	arguments, err := os.ReadFile(argumentsFile)
	if err != nil {
		t.Fatal(err)
	}
	gotArguments := strings.Fields(string(arguments))
	for _, want := range []string{"-index", indexer.IndexDir, "-meta", "-file_limit", "2097152", "-parallelism", "1", "-disable_ctags", source} {
		if !slices.Contains(gotArguments, want) {
			t.Fatalf("arguments %q do not contain %q", gotArguments, want)
		}
	}
	metadataPath, err := os.ReadFile(metadataPathFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(metadataPath), source+string(filepath.Separator)) {
		t.Fatalf("metadata written inside source: %q", metadataPath)
	}
	tempPath, err := os.ReadFile(tempPathFile)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(string(metadataPath)) != string(tempPath) {
		t.Fatalf("metadata path %q and TMPDIR %q use different directories", metadataPath, tempPath)
	}
	if filepath.Dir(string(tempPath)) != filepath.Dir(indexer.IndexDir) {
		t.Fatalf("TMPDIR %q is not adjacent to index directory %q", tempPath, indexer.IndexDir)
	}
	var metadata struct {
		ID       uint32
		Name     string
		URL      string
		Metadata map[string]string
		Branches []struct{ Name, Version string }
	}
	data, err := os.ReadFile(metadataFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.ID != repo.ZoektID || metadata.Name != repo.Name || metadata.URL != repo.WebURL || metadata.Metadata["grepnest_repository_id"] != "7" ||
		len(metadata.Branches) != 1 || metadata.Branches[0].Name != repo.Branch || metadata.Branches[0].Version != repo.DesiredSHA {
		t.Fatalf("metadata = %#v", metadata)
	}
	environment, err := os.ReadFile(environmentFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(environment), "must-not-leak") || strings.Contains(string(arguments), "must-not-leak") || strings.Contains(string(data), "must-not-leak") {
		t.Fatal("credential leaked to Zoekt process")
	}
	if _, err := os.Stat(string(metadataPath)); !os.IsNotExist(err) {
		t.Fatalf("metadata residue: %v", err)
	}
	if _, err := os.Stat(string(tempPath)); !os.IsNotExist(err) {
		t.Fatalf("temporary directory residue: %v", err)
	}
}
