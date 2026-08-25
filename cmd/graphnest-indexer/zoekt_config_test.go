//go:build unix

package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/config"
)

func TestWarnsForLegacyZoektIndexerSetting(t *testing.T) {
	var output bytes.Buffer
	warnIgnoredGitSettings(slog.New(slog.NewJSONHandler(&output, nil)), config.Indexer{ZoektGitIndexDeprecated: true})
	if text := output.String(); !strings.Contains(text, "GRAPHNEST_ZOEKT_GIT_INDEX") || !strings.Contains(text, "GRAPHNEST_ZOEKT_INDEX") || !strings.Contains(text, "deprecated") {
		t.Fatalf("warning = %q", text)
	}
}
