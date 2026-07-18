package config

import (
	"errors"
	"testing"
)

func TestLoadRejectsMissingToken(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("GREPNEST_USER_TOKEN", "")

	_, err := Load()
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadClampsDefaults(t *testing.T) {
	setValidEnvironment(t)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Limits.MaxResults != 100 || got.Limits.MaxContextLines != 20 || got.Limits.MaxResponseBytes != 256<<10 {
		t.Fatalf("limits = %#v", got.Limits)
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("GREPNEST_LISTEN_ADDRESS", "127.0.0.1:8080")
	t.Setenv("GREPNEST_ZOEKT_URL", "http://127.0.0.1:6070")
	t.Setenv("GREPNEST_REPOSITORIES_FILE", "repositories.json")
	t.Setenv("GREPNEST_USER_TOKEN", "user-token")
	t.Setenv("GREPNEST_ADMIN_TOKEN", "admin-token")
	t.Setenv("GREPNEST_USER_REPOSITORIES", "acme/one, acme/two")
	t.Setenv("GREPNEST_ADMIN_REPOSITORIES", "acme/one,acme/two,acme/three")
}
