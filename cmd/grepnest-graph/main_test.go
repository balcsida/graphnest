//go:build unix

package main

import (
	"os"
	"testing"
)

func TestCommandHasNoEnvironmentSecretValue(t *testing.T) {
	if value, ok := os.LookupEnv("GREPNEST_GRAPH_INTERNAL_SECRET"); ok && value != "" {
		t.Fatal("test environment must not use an internal secret value")
	}
}
