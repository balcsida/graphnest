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

func TestCompatibilityDistinguishesMissingCurrentAndMismatch(t *testing.T) {
	db, err := Open(Options{Path: filepath.Join(t.TempDir(), "graph")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	compatible, err := db.Compatible(t.Context())
	if err != nil || compatible {
		t.Fatalf("missing marker compatible=%v err=%v", compatible, err)
	}
	if err := db.WriteCompatibility(t.Context()); err != nil {
		t.Fatal(err)
	}
	compatible, err = db.Compatible(t.Context())
	if err != nil || !compatible {
		t.Fatalf("current marker compatible=%v err=%v", compatible, err)
	}
	if err := db.Update(t.Context(), func(session *Session) error {
		_, err := session.Execute(t.Context(), `MATCH (m:GraphMetadata) SET m.native_version = $version`,
			map[string]any{"version": "0.17.0"}, QueryLimits{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	compatible, err = db.Compatible(t.Context())
	if err != nil || compatible {
		t.Fatalf("native mismatch compatible=%v err=%v", compatible, err)
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
