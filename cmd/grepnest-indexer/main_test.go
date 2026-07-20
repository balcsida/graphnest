//go:build unix

package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestAskPass(t *testing.T) {
	const origin = "https://ghe.example"
	for _, test := range []struct{ prompt, token, origin, want string }{
		{"Username for 'https://ghe.example': ", "secret", origin, "x-access-token\n"},
		{"Password for 'https://x-access-token@ghe.example': ", "secret", origin, "secret\n"},
		{"Password for 'https://attacker.example': ", "secret", origin, ""},
		{"Password supplied by repository", "secret", origin, ""},
		{"Username for 'https://ghe.example': ", "secret", "http://ghe.example", ""},
		{"Username for 'https://ghe.example': ", "secret", "https://user@ghe.example", ""},
		{"Username for 'https://ghe.example': ", "secret", "https://ghe.example/path", ""},
		{"other", "secret", origin, ""},
		{"Password for 'https://x-access-token@ghe.example': ", "", origin, ""},
	} {
		if got := askPass(test.prompt, test.token, test.origin); got != test.want {
			t.Fatalf("askPass(%q) = %q, want %q", test.prompt, got, test.want)
		}
	}
}

func TestRunAskPassFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		env  map[string]string
		want string
		code int
	}{
		{name: "username", args: []string{"Username for 'https://ghe.example': "}, env: map[string]string{askPassModeEnv: "1", gitTokenEnv: "secret", askPassOriginEnv: "https://ghe.example"}, want: "x-access-token\n"},
		{name: "password", args: []string{"Password for 'https://x-access-token@ghe.example': "}, env: map[string]string{askPassModeEnv: "1", gitTokenEnv: "secret", askPassOriginEnv: "https://ghe.example"}, want: "secret\n"},
		{name: "wrong mode", args: []string{"Password for 'https://x-access-token@ghe.example': "}, env: map[string]string{askPassModeEnv: "true", gitTokenEnv: "secret", askPassOriginEnv: "https://ghe.example"}, code: 1},
		{name: "mismatched host", args: []string{"Password for 'https://x-access-token@attacker.example': "}, env: map[string]string{askPassModeEnv: "1", gitTokenEnv: "secret", askPassOriginEnv: "https://ghe.example"}, code: 1},
		{name: "unknown prompt", args: []string{"Other"}, env: map[string]string{askPassModeEnv: "1", gitTokenEnv: "secret", askPassOriginEnv: "https://ghe.example"}, code: 1},
		{name: "missing origin", args: []string{"Password for 'https://x-access-token@ghe.example': "}, env: map[string]string{askPassModeEnv: "1", gitTokenEnv: "secret"}, code: 1},
		{name: "extra argument", args: []string{"Password:", "extra"}, env: map[string]string{askPassModeEnv: "1", gitTokenEnv: "secret", askPassOriginEnv: "https://ghe.example"}, code: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output strings.Builder
			code := runAskPass(test.args, func(name string) string { return test.env[name] }, &output)
			if code != test.code || output.String() != test.want {
				t.Fatalf("code=%d output=%q", code, output.String())
			}
		})
	}
}

func TestRuntimeInitializesBeforeOneWorkerAndClosesLast(t *testing.T) {
	var events []string
	add := func(event string) func(context.Context) error {
		return func(context.Context) error { events = append(events, event); return nil }
	}
	runtime := indexRuntime{
		ping: add("ping"), migrate: add("migrate"), upsertNode: add("upsert primary"),
		reapExpired: add("reap"), pruneHistory: add("prune history"), runWorker: add("prune worktrees and run worker"),
		close: func() { events = append(events, "close") },
	}
	if err := runtime.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := "ping,migrate,upsert primary,reap,prune history,prune worktrees and run worker,close"
	if got := strings.Join(events, ","); got != want {
		t.Fatalf("events=%q want=%q", got, want)
	}
}

func TestRuntimeWaitsForCancelledWorkerBeforeClosing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	var mu sync.Mutex
	closed := false
	runtime := indexRuntime{
		ping:         func(context.Context) error { return nil },
		migrate:      func(context.Context) error { return nil },
		upsertNode:   func(context.Context) error { return nil },
		reapExpired:  func(context.Context) error { return nil },
		pruneHistory: func(context.Context) error { return nil },
		runWorker: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			mu.Lock()
			defer mu.Unlock()
			if closed {
				t.Error("resources closed before worker stopped")
			}
			return ctx.Err()
		},
		close: func() { mu.Lock(); closed = true; mu.Unlock() },
	}
	done := make(chan error, 1)
	go func() { done <- runtime.run(ctx) }()
	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !closed {
		t.Fatal("resources were not closed")
	}
}

func TestRuntimeClosesAfterInitializationFailure(t *testing.T) {
	closed := false
	want := errors.New("migrate")
	runtime := indexRuntime{ping: func(context.Context) error { return nil }, migrate: func(context.Context) error { return want }, close: func() { closed = true }}
	if err := runtime.run(t.Context()); !errors.Is(err, want) || !closed {
		t.Fatalf("error=%v closed=%v", err, closed)
	}
}

func TestRuntimeSurfacesCleanupFailureJoinedWithCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cleanupErr := errors.New("cleanup failed")
	runtime := indexRuntime{
		ping: func(context.Context) error { return nil }, migrate: func(context.Context) error { return nil },
		upsertNode: func(context.Context) error { return nil }, reapExpired: func(context.Context) error { return nil },
		pruneHistory: func(context.Context) error { return nil },
		runWorker:    func(context.Context) error { return errors.Join(context.Canceled, cleanupErr) },
	}
	if err := runtime.run(ctx); !errors.Is(err, cleanupErr) {
		t.Fatalf("error=%v", err)
	}
}
