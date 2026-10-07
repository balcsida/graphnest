package graphimport

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
)

const fixturePath = "../../test/fixtures/codegraph/reference.db"

func hashFile(t *testing.T, path string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

// copyFixture returns a writable copy of the fixture in a temp dir.
func copyFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "codegraph.db")
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func execCopy(t *testing.T, path string, statements ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range statements {
		if _, err = db.Exec(s); err != nil {
			t.Fatal(s, err)
		}
	}
}

func TestReadFixture(t *testing.T) {
	s, err := Read(context.Background(), fixturePath, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if s.SchemaVersion != 9 || len(s.Nodes) != 68 || len(s.Edges) != 93 || len(s.Files) != 13 || len(s.Unresolved) != 6 || len(s.Metadata) != 5 {
		t.Fatalf("counts: v%d %d/%d/%d/%d/%d", s.SchemaVersion, len(s.Nodes), len(s.Edges), len(s.Files), len(s.Unresolved), len(s.Metadata))
	}
	if s.Nodes[0].ID != "file:Model.java" || s.Edges[0].ID != 1 || s.Edges[0].Kind != "contains" || s.Files[0].Path != "Model.java" || s.Metadata[0].Key != "index_state" {
		t.Fatalf("order: %+v %+v %+v %+v", s.Nodes[0], s.Edges[0], s.Files[0], s.Metadata[0])
	}
}

func TestReadDoesNotMutate(t *testing.T) {
	before := hashFile(t, fixturePath)
	if _, err := Read(context.Background(), fixturePath, graphartifact.Limits{}); err != nil {
		t.Fatal(err)
	}
	if hashFile(t, fixturePath) != before {
		t.Fatal("fixture changed")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(fixturePath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists: %v", suffix, err)
		}
	}
}

func TestReadIsRepeatable(t *testing.T) {
	a, err := Read(context.Background(), fixturePath, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Read(context.Background(), fixturePath, graphartifact.Limits{})
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("snapshots differ (%v)", err)
	}
}

func TestReadRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	missing := filepath.Join(dir, "missing.db")
	if _, err := Read(ctx, missing, graphartifact.Limits{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Read created the missing file")
	}

	random := filepath.Join(dir, "random.db")
	if err := os.WriteFile(random, []byte(strings.Repeat("not a database, just text. ", 400)), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.db")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other.db")
	execCopy(t, other, `create table t (x)`)
	for _, path := range []string{random, empty, other} {
		if _, err := Read(ctx, path, graphartifact.Limits{}); !errors.Is(err, ErrNotCodeGraph) {
			t.Fatalf("%s: %v", filepath.Base(path), err)
		}
	}

	newer := copyFixture(t)
	execCopy(t, newer, `insert into schema_versions (version, applied_at) values (12, 0)`)
	if _, err := Read(ctx, newer, graphartifact.Limits{}); !errors.Is(err, ErrUnsupportedSchema) || !strings.Contains(err.Error(), "12") || !strings.Contains(err.Error(), "update") {
		t.Fatalf("newer: %v", err)
	}
	older := copyFixture(t)
	execCopy(t, older, `delete from schema_versions`, `insert into schema_versions (version, applied_at) values (8, 0)`)
	if _, err := Read(ctx, older, graphartifact.Limits{}); !errors.Is(err, ErrUnsupportedSchema) || !strings.Contains(err.Error(), "re-index") {
		t.Fatalf("older: %v", err)
	}

	dropped := copyFixture(t)
	execCopy(t, dropped, `drop index idx_files_generated`, `alter table files drop column generated`)
	if _, err := Read(ctx, dropped, graphartifact.Limits{}); !errors.Is(err, ErrNotCodeGraph) || !strings.Contains(err.Error(), "generated") {
		t.Fatalf("dropped column: %v", err)
	}
}

func TestReadLimitsAndCancellation(t *testing.T) {
	if _, err := Read(context.Background(), fixturePath, graphartifact.Limits{MaxNodes: 10}); !errors.Is(err, ErrIndexTooLarge) || !strings.Contains(err.Error(), "nodes") || !strings.Contains(err.Error(), "68") {
		t.Fatalf("limit: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Read(ctx, fixturePath, graphartifact.Limits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

// TestWALWriterHelper is the writer process for TestReadWithLiveWALWriter; it
// does nothing unless GRAPHNEST_TEST_WAL_WRITER names the database copy.
func TestWALWriterHelper(t *testing.T) {
	path := os.Getenv("GRAPHNEST_TEST_WAL_WRITER")
	if path == "" {
		return
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, s := range []string{`pragma journal_mode=wal`, `pragma wal_autocheckpoint=0`, extraNode("committed")} {
		if _, err = db.Exec(s); err != nil {
			t.Fatal(s, err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(extraNode("uncommitted")); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("ready\n")
	_, _ = os.Stdin.Read(make([]byte, 1)) // blocks until the parent closes stdin or kills us
	_ = tx.Rollback()
}

func extraNode(id string) string {
	return `insert into nodes (id, kind, name, qualified_name, file_path, language, start_line, end_line, start_column, end_column, updated_at) values ('` + id + `', 'function', 'x', 'x', 'x.go', 'go', 1, 1, 0, 1, 0)`
}

func TestReadWithLiveWALWriter(t *testing.T) {
	if os.Getenv("GRAPHNEST_TEST_WAL_WRITER") != "" {
		t.Skip("helper process")
	}
	path := copyFixture(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestWALWriterHelper$")
	cmd.Env = append(os.Environ(), "GRAPHNEST_TEST_WAL_WRITER="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close() })
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("writer not ready: %q %v", line, err)
	}

	wal := path + "-wal"
	walBefore, dbBefore := hashFile(t, wal), hashFile(t, path)
	walSize := fileSize(t, wal)
	if walSize == 0 {
		t.Fatal("writer produced an empty WAL")
	}
	assertRead := func(stage string) {
		s, err := Read(context.Background(), path, graphartifact.Limits{})
		if err != nil {
			t.Fatal(stage, err)
		}
		if len(s.Nodes) != 69 || s.Nodes[68].ID != "committed" {
			t.Fatalf("%s: %d nodes, want 69 including only the committed row", stage, len(s.Nodes))
		}
		if hashFile(t, path) != dbBefore || hashFile(t, wal) != walBefore {
			t.Fatalf("%s: db or WAL bytes changed", stage)
		}
		if fileSize(t, wal) != walSize {
			t.Fatalf("%s: WAL size %d, was %d", stage, fileSize(t, wal), walSize)
		}
	}
	assertRead("live writer")

	if err = cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	assertRead("after SIGKILL")
	if _, err = os.Stat(path + "-shm"); err != nil {
		t.Fatal(err)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}
