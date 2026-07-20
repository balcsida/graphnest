package main

import (
	"context"
	"errors"
	"testing"
)

func TestLoadDatabaseURLRequiresOnlyPostgreSQL(t *testing.T) {
	environment := map[string]string{
		"GREPNEST_DATABASE_URL": "postgres://grepnest:secret@db/grepnest",
		"GREPNEST_USER_TOKEN":   "", "GREPNEST_ADMIN_TOKEN": "",
		"GREPNEST_GITHUB_WEBHOOK_SECRET_FILE": "",
	}
	got, err := loadDatabaseURL(func(name string) string { return environment[name] })
	if err != nil || got != environment["GREPNEST_DATABASE_URL"] {
		t.Fatalf("database URL=%q error=%v", got, err)
	}
}

func TestLoadDatabaseURLRejectsMissingOrUnsafeValue(t *testing.T) {
	for _, value := range []string{"", "http://db/grepnest", "postgres:///grepnest"} {
		if _, err := loadDatabaseURL(func(string) string { return value }); err == nil {
			t.Fatalf("accepted database URL %q", value)
		}
	}
}

func TestMigrationPingsMigratesAndCloses(t *testing.T) {
	var events []string
	runtime := migrationRuntime{
		ping:    func(context.Context) error { events = append(events, "ping"); return nil },
		migrate: func(context.Context) error { events = append(events, "migrate"); return nil },
		close:   func() { events = append(events, "close") },
	}
	if err := runtime.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := len(events); got != 3 || events[0] != "ping" || events[1] != "migrate" || events[2] != "close" {
		t.Fatalf("events=%v", events)
	}
}

func TestMigrationClosesAfterFailure(t *testing.T) {
	want := errors.New("migration failed")
	closed := false
	runtime := migrationRuntime{
		ping:    func(context.Context) error { return nil },
		migrate: func(context.Context) error { return want },
		close:   func() { closed = true },
	}
	if err := runtime.run(t.Context()); !errors.Is(err, want) || !closed {
		t.Fatalf("error=%v closed=%v", err, closed)
	}
}
