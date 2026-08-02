package ladybug

import (
	"context"
	"errors"
	"math/rand/v2"
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

func TestUpdateRollsBackCanceledCallback(t *testing.T) {
	db := testDatabase(t, Options{})
	ctx, cancel := context.WithCancel(t.Context())
	err := db.Update(ctx, func(session *Session) error {
		if _, err := session.Execute(t.Context(), `CREATE (:File {uid: "canceled", repository_id: 1, path: "bad"})`, nil, QueryLimits{}); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Update() error = %v, want context.Canceled", err)
	}
	assertUIDCount(t, db, "canceled", 0)
}

func TestUpdateRollsBackExpiredCallback(t *testing.T) {
	db := testDatabase(t, Options{})
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	err := db.Update(ctx, func(session *Session) error {
		if _, err := session.Execute(t.Context(), `CREATE (:File {uid: "expired", repository_id: 1, path: "bad"})`, nil, QueryLimits{}); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Update() error = %v, want context.DeadlineExceeded", err)
	}
	assertUIDCount(t, db, "expired", 0)
}

func TestViewRollsBackPanicBeforeRecyclingReader(t *testing.T) {
	db := testDatabase(t, Options{ReadConnections: 1})
	assertPanic(t, func() {
		_ = db.View(t.Context(), func(*Session) error {
			panic("reader panic")
		})
	})
	if err := db.View(t.Context(), func(session *Session) error {
		_, err := session.Execute(t.Context(), `RETURN 1`, nil, QueryLimits{})
		return err
	}); err != nil {
		t.Fatalf("View() after panic = %v", err)
	}
}

func TestUpdateRollsBackPanicBeforeReleasingWriter(t *testing.T) {
	db := testDatabase(t, Options{})
	assertPanic(t, func() {
		_ = db.Update(t.Context(), func(session *Session) error {
			if _, err := session.Execute(t.Context(), `CREATE (:File {uid: "panicked", repository_id: 1, path: "bad"})`, nil, QueryLimits{}); err != nil {
				t.Fatal(err)
			}
			panic("writer panic")
		})
	})
	if err := db.Update(t.Context(), func(session *Session) error {
		_, err := session.Execute(t.Context(), `CREATE (:File {uid: "healthy", repository_id: 1, path: "ok"})`, nil, QueryLimits{})
		return err
	}); err != nil {
		t.Fatalf("Update() after panic = %v", err)
	}
	assertUIDCount(t, db, "panicked", 0)
	assertUIDCount(t, db, "healthy", 1)
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

func TestSessionRejectsExecuteAfterCallbackReturns(t *testing.T) {
	db := testDatabase(t, Options{})
	var escaped *Session
	if err := db.View(t.Context(), func(session *Session) error {
		escaped = session
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := escaped.Execute(t.Context(), `RETURN 1`, nil, QueryLimits{}); err == nil {
		t.Fatal("escaped session executed after callback returned")
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

func TestUpdateRollsBackBeginRacedByCancellation(t *testing.T) {
	// BEGIN can land natively while the cancellation that raced it is what gets
	// reported. Returning that connection to the pool with its transaction still
	// open makes the next writer fail with "already has an active transaction".
	db := testDatabase(t, Options{})
	for i := range 200 {
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(time.Duration(rand.N(300_000)), cancel)
		_ = db.Update(ctx, func(session *Session) error {
			_, err := session.Execute(ctx, `RETURN 1`, nil, QueryLimits{})
			return err
		})
		cancel()
		if err := db.Update(t.Context(), func(session *Session) error {
			_, err := session.Execute(t.Context(), `RETURN 1`, nil, QueryLimits{})
			return err
		}); err != nil {
			t.Fatalf("iteration %d: Update() after canceled update = %v", i, err)
		}
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
	if err := db.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	return db
}

func assertPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("callback panic was swallowed")
		}
	}()
	fn()
}

func assertUIDCount(t *testing.T, db *Database, uid string, want int64) {
	t.Helper()
	err := db.View(t.Context(), func(session *Session) error {
		result, err := session.Execute(t.Context(), `MATCH (f:File {uid: $uid}) RETURN count(f)`, map[string]any{"uid": uid}, QueryLimits{})
		if err != nil {
			return err
		}
		if got := result.Rows[0][0]; got != want {
			t.Fatalf("count = %v, want %d", got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
