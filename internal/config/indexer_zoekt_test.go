package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadIndexerUsesZoektIndexAndLegacyAlias(t *testing.T) {
	for _, test := range []struct {
		name, current, legacy, want string
		legacyUsed                  bool
	}{
		{"current", "/usr/local/bin/zoekt-index", "", "/usr/local/bin/zoekt-index", false},
		{"legacy alias", "", "/usr/local/bin/zoekt-git-index", "/usr/local/bin/zoekt-index", true},
		{"current wins", "/new/zoekt-index", "/old/zoekt-git-index", "/new/zoekt-index", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			setDurableEnvironment(t)
			t.Setenv("GRAPHNEST_ZOEKT_URL", "http://127.0.0.1:6070")
			t.Setenv("GRAPHNEST_ZOEKT_INDEX", test.current)
			t.Setenv("GRAPHNEST_ZOEKT_GIT_INDEX", test.legacy)
			got, err := LoadIndexer()
			if err != nil {
				t.Fatal(err)
			}
			if got.ZoektIndex != test.want || got.ZoektGitIndex != test.legacy || got.ZoektGitIndexDeprecated != test.legacyUsed {
				t.Fatalf("indexer = %#v", got)
			}
		})
	}
}

func TestLoadIndexerRejectsLegacyNonstandardZoektGitIndex(t *testing.T) {
	setDurableEnvironment(t)
	t.Setenv("GRAPHNEST_ZOEKT_URL", "http://127.0.0.1:6070")
	t.Setenv("GRAPHNEST_ZOEKT_INDEX", "")
	t.Setenv("GRAPHNEST_ZOEKT_GIT_INDEX", filepath.Join(t.TempDir(), "custom-indexer"))

	_, err := LoadIndexer()
	if err == nil || !strings.Contains(err.Error(), "GRAPHNEST_ZOEKT_INDEX") {
		t.Fatalf("LoadIndexer error = %v", err)
	}
}
