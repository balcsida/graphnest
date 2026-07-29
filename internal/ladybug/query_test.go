package ladybug

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const slowQuery = `UNWIND range(1, 1000000) AS value RETURN sum(value)`

func TestExecuteTimesOut(t *testing.T) {
	db := testDatabase(t, Options{QueryTimeout: time.Millisecond, InterruptGrace: 100 * time.Millisecond})
	start := time.Now()
	err := db.View(t.Context(), func(session *Session) error {
		_, err := session.Execute(t.Context(), slowQuery, nil, QueryLimits{})
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute() error = %v, want context deadline exceeded", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("query timeout took too long")
	}
}

func TestExecuteInterruptsCanceledContext(t *testing.T) {
	db := testDatabase(t, Options{InterruptGrace: 100 * time.Millisecond})
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(time.Millisecond, cancel)
	start := time.Now()
	err := db.View(t.Context(), func(session *Session) error {
		_, err := session.Execute(ctx, slowQuery, nil, QueryLimits{})
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want context canceled", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("query cancellation took too long")
	}
}

func TestExecuteAppliesRowLimit(t *testing.T) {
	db := testDatabase(t, Options{})
	err := db.View(t.Context(), func(session *Session) error {
		result, err := session.Execute(t.Context(), `UNWIND range(1, 5) AS value RETURN value`, nil, QueryLimits{MaxRows: 2})
		if err != nil {
			return err
		}
		if len(result.Rows) != 2 || !result.Truncated {
			t.Fatalf("result = %#v, want two truncated rows", result)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExecuteAppliesEncodedByteLimit(t *testing.T) {
	db := testDatabase(t, Options{})
	err := db.View(t.Context(), func(session *Session) error {
		result, err := session.Execute(t.Context(), `UNWIND ["12345678", "abcdefgh"] AS value RETURN value`, nil, QueryLimits{MaxBytes: 65})
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if len(encoded) > 65 || !result.Truncated {
			t.Fatalf("encoded result is %d bytes, truncated=%v", len(encoded), result.Truncated)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExecuteRejectsMetadataOverByteLimit(t *testing.T) {
	db := testDatabase(t, Options{})
	alias := strings.Repeat("a", 100)
	err := db.View(t.Context(), func(session *Session) error {
		_, err := session.Execute(t.Context(), `MATCH (f:File) RETURN f.uid AS `+alias+` LIMIT 0`, nil, QueryLimits{MaxBytes: 80})
		return err
	})
	if err == nil {
		t.Fatal("oversized result metadata unexpectedly succeeded")
	}
}

func TestQueryLimitsApplyDefaultsAndCaps(t *testing.T) {
	for _, limits := range []QueryLimits{
		{},
		{MaxRows: defaultMaxRows + 1, MaxBytes: defaultMaxBytes + 1},
	} {
		got := normalizeLimits(limits)
		if got.MaxRows != 1000 || got.MaxBytes != 256<<10 {
			t.Fatalf("normalizeLimits(%+v) = %+v", limits, got)
		}
	}
}

func TestInterruptGraceMarksDatabaseUnhealthy(t *testing.T) {
	db := testDatabase(t, Options{InterruptGrace: time.Nanosecond})
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(time.Millisecond, cancel)
	err := db.View(t.Context(), func(session *Session) error {
		_, err := session.Execute(ctx, slowQuery, nil, QueryLimits{})
		return err
	})
	if err == nil {
		t.Fatal("query unexpectedly succeeded")
	}
	if err := db.Health(t.Context()); err == nil {
		t.Fatal("database remained healthy after interrupt grace")
	}
}
