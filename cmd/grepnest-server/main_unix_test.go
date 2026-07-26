//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSharedBoundedReaderRetainsNonRegularCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- os.WriteFile(path, []byte("secret"), 0o600)
	}()
	got, err := readBoundedFile(path, 64)
	if err != nil || string(got) != "secret" {
		t.Fatalf("secret=%q err=%v", got, err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
}
