package ladybug

import (
	"path/filepath"
	"testing"

	lbug "github.com/LadybugDB/go-ladybug"
)

func TestEnsureSchemaIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph")
	database, err := lbug.OpenDatabase(path, lbug.DefaultSystemConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	connection, err := lbug.OpenConnection(database)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := EnsureSchema(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSchema(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"Repository", "File", "Symbol", "CONTAINS", "IMPORTS", "REFERENCES", "CALLS", "EXTENDS", "IMPLEMENTS"} {
		if !tableExists(t, connection, table) {
			t.Fatalf("missing %s", table)
		}
	}
}

func tableExists(t *testing.T, connection *lbug.Connection, table string) bool {
	t.Helper()
	result, err := connection.Query("CALL show_tables() RETURN *")
	if err != nil {
		t.Fatal(err)
	}
	defer result.Close()
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			t.Fatal(err)
		}
		values, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(values) > 1 && values[1] == table {
			return true
		}
	}
	return false
}
