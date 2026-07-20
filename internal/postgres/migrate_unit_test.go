package postgres

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestMigrationDescriptorsRejectDuplicateVersions(t *testing.T) {
	entries, err := fs.ReadDir(fstest.MapFS{
		"001_first.sql":  {},
		"001_second.sql": {},
	}, ".")
	if err != nil {
		t.Fatal(err)
	}
	_, err = migrationDescriptors(entries)
	if err == nil || !strings.Contains(err.Error(), `duplicate migration version 1: "001_first.sql" and "001_second.sql"`) {
		t.Fatalf("error=%v", err)
	}
}
