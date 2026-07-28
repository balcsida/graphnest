package ladybug

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestViewAllowsConcurrentReaders(t *testing.T) {
	db := testDatabase(t, Options{ReadConnections: 2})
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan error, 2)
	for range 2 {
		go func() {
			done <- db.View(t.Context(), func(*Session) error {
				entered <- struct{}{}
				<-release
				return nil
			})
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("readers did not run concurrently")
		}
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestUpdateSerializesWriters(t *testing.T) {
	db := testDatabase(t, Options{})
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan error, 2)
	var active atomic.Int32
	var maximum atomic.Int32
	for range 2 {
		go func() {
			done <- db.Update(t.Context(), func(*Session) error {
				current := active.Add(1)
				if current > maximum.Load() {
					maximum.Store(current)
				}
				entered <- struct{}{}
				<-release
				active.Add(-1)
				return nil
			})
		}()
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	select {
	case <-entered:
		t.Fatal("writers ran concurrently")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if got := maximum.Load(); got != 1 {
		t.Fatalf("maximum concurrent writers = %d, want 1", got)
	}
}

func TestUpdateRollsBackCallbackFailure(t *testing.T) {
	db := testDatabase(t, Options{})
	sentinel := errors.New("stop")
	err := db.Update(t.Context(), func(session *Session) error {
		if _, err := session.Execute(t.Context(), `CREATE (:File {uid: "rolled-back", repository_id: 1, path: "bad"})`, nil, QueryLimits{}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Update() error = %v, want %v", err, sentinel)
	}
	err = db.View(t.Context(), func(session *Session) error {
		result, err := session.Execute(t.Context(), `MATCH (f:File {uid: "rolled-back"}) RETURN count(f)`, nil, QueryLimits{})
		if err != nil {
			return err
		}
		if got := result.Rows[0][0]; got != int64(0) {
			t.Fatalf("count = %v, want 0", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestViewRejectsWrites(t *testing.T) {
	db := testDatabase(t, Options{})
	err := db.View(t.Context(), func(session *Session) error {
		_, err := session.Execute(t.Context(), `CREATE (:Symbol {uid: "bad"})`, nil, QueryLimits{MaxRows: 1, MaxBytes: 1024})
		return err
	})
	if err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	db := testDatabase(t, Options{})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseWaitsForInterruptedExecution(t *testing.T) {
	db := testDatabase(t, Options{InterruptGrace: time.Nanosecond})
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(time.Millisecond, cancel)
	_ = db.View(t.Context(), func(session *Session) error {
		_, err := session.Execute(ctx, slowQuery, nil, QueryLimits{})
		return err
	})
	closed := make(chan error, 1)
	go func() { closed <- db.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close() returned while native execution remained active: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not finish after interrupted execution stopped")
	}
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if db, err := Open(Options{}); err == nil {
		db.Close()
		t.Fatal("empty path unexpectedly opened")
	}
}

func TestOptionsApplyReaderDefaultsAndCap(t *testing.T) {
	for _, test := range []struct {
		name string
		in   int
		want int
	}{
		{name: "default", want: 8},
		{name: "cap", in: 33, want: 32},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := normalizeOptions(Options{ReadConnections: test.in})
			if options.ReadConnections != test.want {
				t.Fatalf("ReadConnections = %d, want %d", options.ReadConnections, test.want)
			}
		})
	}
}

func testDatabase(t *testing.T, options Options) *Database {
	t.Helper()
	options.Path = filepath.Join(t.TempDir(), "graph")
	db, err := Open(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.Update(t.Context(), func(session *Session) error {
		return EnsureSchema(t.Context(), session.connection)
	}); err != nil {
		t.Fatal(err)
	}
	return db
}
