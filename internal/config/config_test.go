package config

import (
	"errors"
	"reflect"
	"testing"
)

func TestLoadKeepsStaticConfiguration(t *testing.T) {
	setValidEnvironment(t)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.DatabaseURL != "" || got.GitHub.AppID != 0 {
		t.Fatalf("durable configuration = %#v", got)
	}
}

func TestLoadReadsDurableConfiguration(t *testing.T) {
	setValidEnvironment(t)
	setDurableEnvironment(t)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.DatabaseURL != "postgres://grepnest:secret@db/grepnest" || got.GitHub.WebURL != "https://ghe.example.com" || got.GitHub.APIURL != "https://ghe.example.com/api/v3" || got.GitHub.UploadURL != "https://ghe.example.com/uploads" || got.GitHub.GitURL != "https://ghe.example.com" || got.GitHub.AppID != 123 || got.GitHub.PrivateKeyFile != "/run/secrets/key.pem" || got.GitHub.WebhookSecretFile != "/run/secrets/webhook" || got.GitHub.CAFile != "/run/secrets/ca.pem" || got.GitHub.APIVersion != "2022-11-28" || got.UserInstallationID != 10 || !reflect.DeepEqual(got.UserRepositoryIDs, []int64{101, 102}) || got.AdminInstallationID != 10 || !reflect.DeepEqual(got.AdminRepositoryIDs, []int64{101, 102, 103}) || got.Indexer != (Indexer{}) {
		t.Fatalf("configuration = %#v", got)
	}
}

func TestLoadDurableServerDoesNotRequireStaticOrIndexerConfiguration(t *testing.T) {
	setValidEnvironment(t)
	setDurableEnvironment(t)
	for _, name := range []string{
		"GREPNEST_REPOSITORIES_FILE", "GREPNEST_USER_REPOSITORIES", "GREPNEST_ADMIN_REPOSITORIES",
		"GREPNEST_DATA_DIR", "GREPNEST_INDEX_DIR", "GREPNEST_GIT_PATH",
		"GREPNEST_ZOEKT_GIT_INDEX", "GREPNEST_WORKER_ID", "GREPNEST_MIN_FREE_BYTES",
	} {
		t.Setenv(name, "")
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.DatabaseURL == "" || got.RepositoriesFile != "" || got.Indexer != (Indexer{}) {
		t.Fatalf("configuration = %#v", got)
	}
}

func TestLoadRejectsUnsafeDurableConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, env, value string
	}{
		{"HTTP GitHub API", "GREPNEST_GITHUB_API_URL", "http://ghe.example.com/api/v3"},
		{"missing private key path", "GREPNEST_GITHUB_PRIVATE_KEY_FILE", ""},
		{"missing webhook secret path", "GREPNEST_GITHUB_WEBHOOK_SECRET_FILE", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			setValidEnvironment(t)
			setDurableEnvironment(t)
			t.Setenv(test.env, test.value)
			if _, err := Load(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}
}

func TestLoadIndexerRejectsZeroFreeSpaceFloor(t *testing.T) {
	setDurableEnvironment(t)
	t.Setenv("GREPNEST_MIN_FREE_BYTES", "0")
	if _, err := LoadIndexer(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("LoadIndexer() error = %v", err)
	}
}

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

func setDurableEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("GREPNEST_DATABASE_URL", "postgres://grepnest:secret@db/grepnest")
	t.Setenv("GREPNEST_GITHUB_WEB_URL", "https://ghe.example.com")
	t.Setenv("GREPNEST_GITHUB_API_URL", "https://ghe.example.com/api/v3")
	t.Setenv("GREPNEST_GITHUB_UPLOAD_URL", "https://ghe.example.com/uploads")
	t.Setenv("GREPNEST_GITHUB_GIT_URL", "https://ghe.example.com")
	t.Setenv("GREPNEST_GITHUB_APP_ID", "123")
	t.Setenv("GREPNEST_GITHUB_PRIVATE_KEY_FILE", "/run/secrets/key.pem")
	t.Setenv("GREPNEST_GITHUB_WEBHOOK_SECRET_FILE", "/run/secrets/webhook")
	t.Setenv("GREPNEST_GITHUB_CA_FILE", "/run/secrets/ca.pem")
	t.Setenv("GREPNEST_USER_INSTALLATION_ID", "10")
	t.Setenv("GREPNEST_USER_REPOSITORY_IDS", "101,102")
	t.Setenv("GREPNEST_ADMIN_INSTALLATION_ID", "10")
	t.Setenv("GREPNEST_ADMIN_REPOSITORY_IDS", "101,102,103")
	t.Setenv("GREPNEST_DATA_DIR", "/var/lib/grepnest/data")
	t.Setenv("GREPNEST_INDEX_DIR", "/var/lib/grepnest/index")
	t.Setenv("GREPNEST_GIT_PATH", "/usr/bin/git")
	t.Setenv("GREPNEST_ZOEKT_GIT_INDEX", "/usr/local/bin/zoekt-git-index")
	t.Setenv("GREPNEST_WORKER_ID", "worker-1")
	t.Setenv("GREPNEST_MIN_FREE_BYTES", "1073741824")
}
