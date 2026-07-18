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

func TestLoadEnforcesSafetyCaps(t *testing.T) {
	cases := []struct {
		name     string
		env      string
		boundary string
		over     string
	}{
		{"results", "GREPNEST_MAX_RESULTS", "100", "101"},
		{"context lines", "GREPNEST_MAX_CONTEXT_LINES", "20", "21"},
		{"timeout", "GREPNEST_MAX_TIMEOUT", "5s", "6s"},
		{"response bytes", "GREPNEST_MAX_RESPONSE_BYTES", "262144", "262145"},
		{"request bytes", "GREPNEST_MAX_REQUEST_BYTES", "65536", "65537"},
	}
	for _, test := range cases {
		t.Run(test.name+" boundary", func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(test.env, test.boundary)
			if _, err := Load(); err != nil {
				t.Fatalf("Load() error = %v", err)
			}
		})
		t.Run(test.name+" over cap", func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(test.env, test.over)
			if _, err := Load(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Load() error = %v", err)
			}
		})
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
