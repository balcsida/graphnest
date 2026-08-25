package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
)

type fakeTTY struct {
	closed bool
}

func (*fakeTTY) Fd() uintptr      { return 7 }
func (tty *fakeTTY) Close() error { tty.closed = true; return nil }

func testCommand(apply func(context.Context, string, string, authn.PasswordCredential, audit.Event) error) commandRuntime {
	return commandRuntime{
		getenv:  func(string) string { return "postgres://user:secret@db/graphnest" },
		openTTY: func() (terminal, error) { return nil, errors.New("no tty") },
		hashPassword: func([]byte, io.Reader) (authn.PasswordCredential, error) {
			return authn.PasswordCredential{
				Salt: bytes.Repeat([]byte{7}, 16), Hash: bytes.Repeat([]byte{8}, 32),
				MemoryKiB: 65536, Iterations: 3, Parallelism: 2,
			}, nil
		},
		apply: apply,
	}
}

func TestCommandAcceptsOnlySetPasswordInvocation(t *testing.T) {
	valid := []string{"break-glass", "set-password", "recovery-admin"}
	invalid := [][]string{
		nil,
		{"break-glass"},
		{"break-glass", "set-password"},
		{"break-glass", "unknown", "recovery-admin"},
		{"unknown", "set-password", "recovery-admin"},
		{"break-glass", "set-password", "recovery-admin", "extra"},
		{"break-glass", "set-password", "--password=secret"},
		{"--verbose", "break-glass", "set-password"},
	}
	called := 0
	runtime := testCommand(func(context.Context, string, string, authn.PasswordCredential, audit.Event) error {
		called++
		return nil
	})
	for _, args := range invalid {
		var stdout, stderr bytes.Buffer
		if code := runtime.run(t.Context(), args, strings.NewReader("sixteen-byte-secret\nsixteen-byte-secret\n"), &stdout, &stderr); code == 0 {
			t.Fatalf("accepted arguments %q", args)
		}
		if stdout.Len() != 0 || strings.Contains(stderr.String(), "secret") {
			t.Fatalf("arguments %q stdout=%q stderr=%q", args, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runtime.run(t.Context(), valid, strings.NewReader("sixteen-byte-secret\nsixteen-byte-secret\n"), &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if called != 1 {
		t.Fatalf("apply calls=%d", called)
	}
}

func TestCommandPrefersTTYAndClearsBothPasswords(t *testing.T) {
	tty := &fakeTTY{}
	first := []byte("sixteen-byte-secret")
	second := []byte("sixteen-byte-secret")
	passwords := [][]byte{first, second}
	runtime := testCommand(func(context.Context, string, string, authn.PasswordCredential, audit.Event) error { return nil })
	runtime.openTTY = func() (terminal, error) { return tty, nil }
	runtime.isTerminal = func(int) bool { return true }
	runtime.readPassword = func(int) ([]byte, error) {
		password := passwords[0]
		passwords = passwords[1:]
		return password, nil
	}
	hashPassword := runtime.hashPassword
	runtime.hashPassword = func(password []byte, random io.Reader) (authn.PasswordCredential, error) {
		if !bytes.Equal(second, make([]byte, len(second))) {
			t.Fatalf("confirmation password remained live during hashing: %q", second)
		}
		return hashPassword(password, random)
	}
	var stdout, stderr bytes.Buffer
	if code := runtime.run(t.Context(), []string{"break-glass", "set-password", "recovery-admin"},
		strings.NewReader("stdin-must-not-be-read\n"), &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !tty.closed || !bytes.Equal(first, make([]byte, len(first))) || !bytes.Equal(second, make([]byte, len(second))) {
		t.Fatalf("closed=%v first=%q second=%q", tty.closed, first, second)
	}
	if strings.Contains(stdout.String()+stderr.String(), "sixteen-byte-secret") {
		t.Fatal("password appeared in output")
	}
}

func TestCommandFallsBackToExactlyTwoBoundedStdinLines(t *testing.T) {
	for _, input := range []string{
		"sixteen-byte-secret\n",
		"sixteen-byte-secret\nsixteen-byte-secret\nthird\n",
		strings.Repeat("x", 1025) + "\n" + strings.Repeat("x", 1025) + "\n",
	} {
		runtime := testCommand(func(context.Context, string, string, authn.PasswordCredential, audit.Event) error { return nil })
		var stdout, stderr bytes.Buffer
		if code := runtime.run(t.Context(), []string{"break-glass", "set-password", "recovery-admin"},
			strings.NewReader(input), &stdout, &stderr); code == 0 {
			t.Fatalf("accepted stdin with %d bytes", len(input))
		}
		if strings.Contains(stdout.String()+stderr.String(), strings.TrimSpace(input)) {
			t.Fatal("password input appeared in output")
		}
	}
}

func TestCommandRejectsInvalidPasswordWithoutApplying(t *testing.T) {
	inputs := []string{
		"\n\n",
		"fifteen-bytes!!\nfifteen-bytes!!\n",
		"sixteen-byte-secret\ndifferent-password\n",
		strings.Repeat("x", 1025) + "\n" + strings.Repeat("x", 1025) + "\n",
	}
	for _, input := range inputs {
		called := false
		runtime := testCommand(func(context.Context, string, string, authn.PasswordCredential, audit.Event) error {
			called = true
			return nil
		})
		var stdout, stderr bytes.Buffer
		if code := runtime.run(t.Context(), []string{"break-glass", "set-password", "recovery-admin"},
			strings.NewReader(input), &stdout, &stderr); code == 0 || called {
			t.Fatalf("input length=%d code=%d called=%v", len(input), code, called)
		}
		if strings.Contains(stdout.String()+stderr.String(), "different-password") {
			t.Fatal("password appeared in output")
		}
	}
}

func TestCommandSetsForcedRotationAndBoundedAuditEvent(t *testing.T) {
	var gotURL, gotUser string
	var gotCredential authn.PasswordCredential
	var gotEvent audit.Event
	runtime := testCommand(func(_ context.Context, databaseURL, userName string, credential authn.PasswordCredential, event audit.Event) error {
		gotURL, gotUser, gotCredential, gotEvent = databaseURL, userName, credential, event
		return nil
	})
	var stdout, stderr bytes.Buffer
	code := runtime.run(t.Context(), []string{"break-glass", "set-password", "recovery-admin"},
		strings.NewReader("sixteen-byte-secret\nsixteen-byte-secret\n"), &stdout, &stderr)
	if code != 0 || gotURL != "postgres://user:secret@db/graphnest" || gotUser != "recovery-admin" {
		t.Fatalf("code=%d URL=%q user=%q stderr=%q", code, gotURL, gotUser, stderr.String())
	}
	if !gotCredential.ForceRotation || gotEvent.ActorType != "operator" ||
		gotEvent.TargetType != "user" || gotEvent.TargetID != "recovery-admin" ||
		gotEvent.AuthenticationMethod != "operator" ||
		gotEvent.Operation != "break_glass_password_set" || gotEvent.Outcome != "success" {
		t.Fatalf("credential=%#v event=%#v", gotCredential, gotEvent)
	}
	if stdout.String() != "Break-glass password updated.\n" {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if !bytes.Equal(gotCredential.Salt, make([]byte, 16)) ||
		!bytes.Equal(gotCredential.Hash, make([]byte, 32)) {
		t.Fatalf("credential buffers not cleared: %#v", gotCredential)
	}
}

func TestCommandReportsSafeErrors(t *testing.T) {
	var gotCredential authn.PasswordCredential
	runtime := testCommand(func(_ context.Context, _, _ string, credential authn.PasswordCredential, _ audit.Event) error {
		gotCredential = credential
		return errors.New("postgres://user:secret@db/internal SQL secret")
	})
	var stdout, stderr bytes.Buffer
	code := runtime.run(t.Context(), []string{"break-glass", "set-password", "recovery-admin"},
		strings.NewReader("sixteen-byte-secret\nsixteen-byte-secret\n"), &stdout, &stderr)
	if code == 0 || stdout.Len() != 0 {
		t.Fatalf("code=%d stdout=%q", code, stdout.String())
	}
	for _, secret := range []string{"postgres://", "secret", "internal SQL"} {
		if strings.Contains(stderr.String(), secret) {
			t.Fatalf("stderr disclosed %q: %q", secret, stderr.String())
		}
	}
	if !bytes.Equal(gotCredential.Salt, make([]byte, 16)) ||
		!bytes.Equal(gotCredential.Hash, make([]byte, 32)) {
		t.Fatalf("credential buffers not cleared: %#v", gotCredential)
	}
}
