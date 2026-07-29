//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/grepnest/grepnest/internal/secretstage"
)

func TestCommandHasNoEnvironmentSecretValue(t *testing.T) {
	if value, ok := os.LookupEnv("GREPNEST_GRAPH_INTERNAL_SECRET"); ok && value != "" {
		t.Fatal("test environment must not use an internal secret value")
	}
}

func TestStageSecretCommand(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	destination := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(source, []byte("token"), 0o440); err != nil {
		t.Fatal(err)
	}
	handled, err := runStageSecretCommand([]string{"stage-secret", source, destination})
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "token" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestStageSecretCommandRejectsMissingArguments(t *testing.T) {
	handled, err := runStageSecretCommand([]string{"stage-secret"})
	if !handled || !errors.Is(err, secretstage.ErrUsage) {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}
