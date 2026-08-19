package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadGraphQueryOverrides(t *testing.T) {
	setValidEnvironment(t)
	setDurableEnvironment(t)
	for name, value := range map[string]string{
		"GREPNEST_GRAPH_DEFAULT_IMPACT_DEPTH": "2",
		"GREPNEST_GRAPH_MAX_IMPACT_DEPTH":     "7",
		"GREPNEST_GRAPH_DEFAULT_TRACE_DEPTH":  "4",
		"GREPNEST_GRAPH_MAX_TRACE_DEPTH":      "9",
		"GREPNEST_GRAPH_MAX_ROWS":             "321",
	} {
		t.Setenv(name, value)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Graph.QueryLimits.DefaultImpactDepth != 2 || got.Graph.QueryLimits.MaxDepth != 7 ||
		got.Graph.QueryLimits.DefaultTraceDepth != 4 || got.Graph.QueryLimits.MaxTraceDepth != 9 ||
		got.Graph.QueryLimits.MaxRows != 321 {
		t.Fatalf("query limits = %#v", got.Graph.QueryLimits)
	}
}

func TestLoadKeepsStaticConfiguration(t *testing.T) {
	setValidEnvironment(t)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.DatabaseURL != "" || got.GitHub.AppID != 0 || !reflect.DeepEqual(got.Graph, Graph{}) {
		t.Fatalf("durable configuration = %#v", got)
	}
}

func TestLoadSelectsZoektByDefaultAndGitHubExplicitly(t *testing.T) {
	setValidEnvironment(t)
	setDurableEnvironment(t)
	got, err := Load()
	if err != nil || got.SearchBackend != "zoekt" {
		t.Fatalf("configuration=%#v error=%v", got, err)
	}
	t.Setenv("GREPNEST_SEARCH_BACKEND", "github")
	got, err = Load()
	if err != nil || got.SearchBackend != "github" {
		t.Fatalf("configuration=%#v error=%v", got, err)
	}
	t.Setenv("GREPNEST_SEARCH_BACKEND", "other")
	if _, err := Load(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadReadsDurableConfiguration(t *testing.T) {
	setValidEnvironment(t)
	setDurableEnvironment(t)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// Break caught: a durable server still loading static bearer credentials.
	if got.DatabaseURL != "postgres://grepnest:secret@db/grepnest" || got.GitHub.WebURL != "https://ghe.example.com" || got.GitHub.APIURL != "https://ghe.example.com/api/v3" || got.GitHub.UploadURL != "https://ghe.example.com/uploads" || got.GitHub.GitURL != "https://ghe.example.com" || got.GitHub.AppID != 123 || got.GitHub.PrivateKeyFile != "/run/secrets/key.pem" || got.GitHub.WebhookSecretFile != "/run/secrets/webhook" || got.GitHub.CAFile != "/run/secrets/ca.pem" || got.GitHub.APIVersion != "2022-11-28" || got.UserToken != "" || got.AdminToken != "" || got.UserInstallationID != 0 || len(got.UserRepositoryIDs) != 0 || got.AdminInstallationID != 0 || len(got.AdminRepositoryIDs) != 0 || !reflect.DeepEqual(got.Indexer, Indexer{}) || got.Graph.MaxRequestBytes != 64<<10 || got.Graph.MaxResponseBytes != 256<<10 {
		t.Fatalf("configuration = %#v", got)
	}
}

func TestLoadRejectsInvalidServerGraphConfiguration(t *testing.T) {
	for _, test := range []struct{ name, env, value string }{
		{"request cap", "GREPNEST_GRAPH_MAX_REQUEST_BYTES", "65537"},
		{"response cap", "GREPNEST_GRAPH_MAX_RESPONSE_BYTES", "262145"},
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
	if got.DatabaseURL == "" || got.RepositoriesFile != "" || !reflect.DeepEqual(got.Indexer, Indexer{}) {
		t.Fatalf("configuration = %#v", got)
	}
}

func TestLoadSCIMConfiguration(t *testing.T) {
	setValidEnvironment(t)
	setDurableEnvironment(t)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(strings.Repeat("s", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GREPNEST_SCIM_TOKEN_FILE", tokenFile)
	t.Setenv("GREPNEST_PUBLIC_URL", "https://grepnest.example")

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.SCIM.Enabled || got.SCIM.TokenFile != tokenFile || got.SCIM.PublicURL.String() != "https://grepnest.example/" {
		t.Fatalf("SCIM = %#v", got.SCIM)
	}
}

func TestLoadRejectsInvalidSCIMConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, database, tokenFile, tokenValue, publicURL string
	}{
		{"static mode", "", "token", "", "https://grepnest.example"},
		{"token value environment", "durable", "", strings.Repeat("s", 32), ""},
		{"directory token file", "durable", "directory", "", "https://grepnest.example"},
		{"missing public origin", "durable", "token", "", ""},
		{"HTTP public origin", "durable", "token", "", "http://grepnest.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setValidEnvironment(t)
			if test.database != "" {
				setDurableEnvironment(t)
			}
			tokenFile := ""
			switch test.tokenFile {
			case "token":
				tokenFile = filepath.Join(t.TempDir(), "token")
				if err := os.WriteFile(tokenFile, []byte(strings.Repeat("s", 32)), 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				tokenFile = t.TempDir()
			}
			t.Setenv("GREPNEST_SCIM_TOKEN_FILE", tokenFile)
			t.Setenv("GREPNEST_SCIM_TOKEN", test.tokenValue)
			t.Setenv("GREPNEST_PUBLIC_URL", test.publicURL)
			if _, err := Load(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Load() error = %v", err)
			}
		})
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

func TestLoadIndexerSourceProviderAndArchiveLimits(t *testing.T) {
	setValidEnvironment(t)
	setDurableEnvironment(t)
	t.Setenv("GREPNEST_SOURCE_PROVIDER", "archive")
	t.Setenv("GREPNEST_GITHUB_ARCHIVE_URL", "https://archives.example.com")
	t.Setenv("GREPNEST_ARCHIVE_MAX_DOWNLOAD_BYTES", "11")
	t.Setenv("GREPNEST_ARCHIVE_MAX_EXTRACTED_BYTES", "12")
	t.Setenv("GREPNEST_ARCHIVE_MAX_FILE_BYTES", "13")
	t.Setenv("GREPNEST_ARCHIVE_MAX_FILES", "14")
	t.Setenv("GREPNEST_ARCHIVE_MAX_PATH_BYTES", "15")
	got, err := LoadIndexer()
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceProvider != "archive" || got.GitHub.ArchiveURL != "https://archives.example.com" || got.ArchiveLimits != (ArchiveLimits{MaxDownloadBytes: 11, MaxExtractedBytes: 12, MaxFileBytes: 13, MaxFiles: 14, MaxPathBytes: 15}) {
		t.Fatalf("configuration = %#v", got)
	}
}

func TestLoadIndexerDefaultsToArchiveAndRejectsInvalidArchiveConfiguration(t *testing.T) {
	setValidEnvironment(t)
	setDurableEnvironment(t)
	got, err := LoadIndexer()
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceProvider != "archive" {
		t.Fatalf("source provider = %q", got.SourceProvider)
	}
	for _, test := range []struct{ name, env, value string }{
		{"provider", "GREPNEST_SOURCE_PROVIDER", "other"},
		{"archive URL", "GREPNEST_GITHUB_ARCHIVE_URL", "http://archives.example.com"},
		{"download limit", "GREPNEST_ARCHIVE_MAX_DOWNLOAD_BYTES", "0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setValidEnvironment(t)
			setDurableEnvironment(t)
			t.Setenv(test.env, test.value)
			if _, err := LoadIndexer(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("LoadIndexer() error = %v", err)
			}
		})
	}
}

func TestLoadIndexerArchiveModeDoesNotRequireGitRuntime(t *testing.T) {
	setValidEnvironment(t)
	setDurableEnvironment(t)
	t.Setenv("GREPNEST_SOURCE_PROVIDER", "archive")
	t.Setenv("GREPNEST_GIT_PATH", "")
	if _, err := LoadIndexer(); err != nil {
		t.Fatalf("LoadIndexer() error = %v", err)
	}
	t.Setenv("GREPNEST_SOURCE_PROVIDER", "git")
	if _, err := LoadIndexer(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("git LoadIndexer() error = %v", err)
	}
}

func TestLoadIndexerRequiresOnlyIndexerConfiguration(t *testing.T) {
	setValidEnvironment(t)
	setDurableEnvironment(t)
	for _, name := range []string{
		"GREPNEST_USER_TOKEN", "GREPNEST_ADMIN_TOKEN",
		"GREPNEST_LISTEN_ADDRESS", "GREPNEST_REPOSITORIES_FILE",
		"GREPNEST_USER_REPOSITORIES", "GREPNEST_ADMIN_REPOSITORIES",
		"GREPNEST_USER_INSTALLATION_ID", "GREPNEST_USER_REPOSITORY_IDS",
		"GREPNEST_ADMIN_INSTALLATION_ID", "GREPNEST_ADMIN_REPOSITORY_IDS",
	} {
		t.Setenv(name, "")
	}

	got, err := LoadIndexer()
	if err != nil {
		t.Fatal(err)
	}
	if got.DatabaseURL != "postgres://grepnest:secret@db/grepnest" || got.ZoektURL != "http://127.0.0.1:6070" || got.MetricsListenAddress != ":9090" || got.GitHub.AppID != 123 || got.GitHub.PrivateKeyFile != "/run/secrets/key.pem" || got.GitHub.WebhookSecretFile != "" || got.WorkerID != "worker-1" || got.MaxRepositoryBytes != 5<<30 {
		t.Fatalf("configuration = %#v", got)
	}
}

func TestLoadIndexerRejectsInvalidMetricsAddress(t *testing.T) {
	for _, value := range []string{"9090", ":0", ":65536"} {
		t.Run(value, func(t *testing.T) {
			setDurableEnvironment(t)
			t.Setenv("GREPNEST_METRICS_LISTEN_ADDRESS", value)
			if _, err := LoadIndexer(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("LoadIndexer() error = %v", err)
			}
		})
	}
}

func TestLoadIndexerRejectsMissingCredentials(t *testing.T) {
	for _, name := range []string{"GREPNEST_DATABASE_URL", "GREPNEST_ZOEKT_URL", "GREPNEST_GITHUB_PRIVATE_KEY_FILE"} {
		t.Run(name, func(t *testing.T) {
			setDurableEnvironment(t)
			t.Setenv("GREPNEST_ZOEKT_URL", "http://127.0.0.1:6070")
			t.Setenv(name, "")
			if _, err := LoadIndexer(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("LoadIndexer() error = %v", err)
			}
		})
	}
}

func TestLoadScannerDefaults(t *testing.T) {
	setDurableEnvironment(t)

	got, err := LoadScanner()
	if err != nil {
		t.Fatal(err)
	}
	if got.DatabaseURL != "postgres://grepnest:secret@db/grepnest" ||
		got.DataDir != "/var/lib/grepnest/data" || got.GitPath != "/usr/bin/git" ||
		got.WorkerID != "worker-1" || got.MetricsListenAddress != ":9090" ||
		got.GitHub.AppID != 123 || got.GitHub.WebhookSecretFile != "" ||
		got.MaxRepositoryBytes != 5<<30 || got.MinFreeBytes != 1<<30 || got.ScanTimeout != 15*time.Minute {
		t.Fatalf("configuration = %#v", got)
	}
	wantLimits := GraphScanLimits{
		MaxFileBytes: 2 << 20, MaxTotalBytes: 1 << 30, MaxFiles: 100_000,
		MaxNodes: 500_000, MaxEdges: 2_000_000, ParseTimeout: 30 * time.Second,
		SkipDirectories: []string{"node_modules", "vendor", "target", "build", "dist", ".gradle"},
	}
	if !reflect.DeepEqual(got.Limits, wantLimits) {
		t.Fatalf("limits = %#v, want %#v", got.Limits, wantLimits)
	}
}

func TestLoadScannerAcceptsBoundedOverrides(t *testing.T) {
	setDurableEnvironment(t)
	t.Setenv("GREPNEST_GRAPH_SCAN_MAX_FILE_BYTES", "1")
	t.Setenv("GREPNEST_GRAPH_SCAN_MAX_TOTAL_BYTES", "2")
	t.Setenv("GREPNEST_GRAPH_SCAN_MAX_FILES", "3")
	t.Setenv("GREPNEST_GRAPH_SCAN_MAX_NODES", "4")
	t.Setenv("GREPNEST_GRAPH_SCAN_MAX_EDGES", "5")
	t.Setenv("GREPNEST_GRAPH_SCAN_PARSE_TIMEOUT", "6ms")
	t.Setenv("GREPNEST_GRAPH_SCAN_SKIP_DIRECTORIES", " vendor,dist,vendor,.cache ")
	t.Setenv("GREPNEST_MAX_REPOSITORY_BYTES", "7")
	t.Setenv("GREPNEST_MIN_FREE_BYTES", "8")
	t.Setenv("GREPNEST_GRAPH_SCAN_TIMEOUT", "20m")

	got, err := LoadScanner()
	if err != nil {
		t.Fatal(err)
	}
	want := GraphScanLimits{
		MaxFileBytes: 1, MaxTotalBytes: 2, MaxFiles: 3, MaxNodes: 4, MaxEdges: 5,
		ParseTimeout: 6 * time.Millisecond, SkipDirectories: []string{"vendor", "dist", ".cache"},
	}
	if !reflect.DeepEqual(got.Limits, want) {
		t.Fatalf("limits = %#v, want %#v", got.Limits, want)
	}
	if got.MaxRepositoryBytes != 7 || got.MinFreeBytes != 8 {
		t.Fatalf("storage limits = %d, %d", got.MaxRepositoryBytes, got.MinFreeBytes)
	}
	if got.ScanTimeout != 20*time.Minute {
		t.Fatalf("scan timeout = %v", got.ScanTimeout)
	}
}

func TestLoadScannerRejectsInvalidLimitsAndSkips(t *testing.T) {
	tests := []struct{ name, env, value string }{
		{"zero file bytes", "GREPNEST_GRAPH_SCAN_MAX_FILE_BYTES", "0"},
		{"file bytes over cap", "GREPNEST_GRAPH_SCAN_MAX_FILE_BYTES", "2097153"},
		{"total bytes over cap", "GREPNEST_GRAPH_SCAN_MAX_TOTAL_BYTES", "1073741825"},
		{"files over cap", "GREPNEST_GRAPH_SCAN_MAX_FILES", "100001"},
		{"nodes over cap", "GREPNEST_GRAPH_SCAN_MAX_NODES", "500001"},
		{"edges over cap", "GREPNEST_GRAPH_SCAN_MAX_EDGES", "2000001"},
		{"timeout over cap", "GREPNEST_GRAPH_SCAN_PARSE_TIMEOUT", "31s"},
		{"zero repository bytes", "GREPNEST_MAX_REPOSITORY_BYTES", "0"},
		{"zero free bytes", "GREPNEST_MIN_FREE_BYTES", "0"},
		{"zero scan timeout", "GREPNEST_GRAPH_SCAN_TIMEOUT", "0s"},
		{"scan timeout over cap", "GREPNEST_GRAPH_SCAN_TIMEOUT", "31m"},
		{"empty skip", "GREPNEST_GRAPH_SCAN_SKIP_DIRECTORIES", "vendor,,dist"},
		{"slash skip", "GREPNEST_GRAPH_SCAN_SKIP_DIRECTORIES", "vendor/a"},
		{"backslash skip", "GREPNEST_GRAPH_SCAN_SKIP_DIRECTORIES", `vendor\a`},
		{"dot skip", "GREPNEST_GRAPH_SCAN_SKIP_DIRECTORIES", "."},
		{"dotdot skip", "GREPNEST_GRAPH_SCAN_SKIP_DIRECTORIES", ".."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setDurableEnvironment(t)
			t.Setenv(test.env, test.value)
			if _, err := LoadScanner(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("LoadScanner() error = %v", err)
			}
		})
	}
}

func TestLoadScannerRejectsMissingRequiredSettingsWithoutSecrets(t *testing.T) {
	for _, name := range []string{
		"GREPNEST_DATABASE_URL", "GREPNEST_DATA_DIR", "GREPNEST_GIT_PATH",
		"GREPNEST_WORKER_ID", "GREPNEST_GITHUB_PRIVATE_KEY_FILE",
	} {
		t.Run(name, func(t *testing.T) {
			setDurableEnvironment(t)
			t.Setenv(name, "")
			_, err := LoadScanner()
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("LoadScanner() error = %v", err)
			}
			if strings.Contains(err.Error(), "grepnest:secret") {
				t.Fatalf("error exposed credential: %v", err)
			}
		})
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
	if got.Limits.MaxResults != 100 || got.Limits.MaxContextLines != 20 || got.Limits.MaxResponseBytes != 256<<10 ||
		got.Limits.SCIPMaxUploadBytes != 64<<20 || got.Limits.GraphMaxUploadBytes != 64<<20 {
		t.Fatalf("limits = %#v", got.Limits)
	}
}

func TestLoadSCIPUploadLimitIsIndependent(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("GREPNEST_SCIP_MAX_UPLOAD_BYTES", "1048576")
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Limits.SCIPMaxUploadBytes != 1048576 || got.Limits.MaxRequestBytes != 64<<10 {
		t.Fatalf("limits = %#v", got.Limits)
	}
}

func TestLoadGraphUploadLimitIsIndependent(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("GREPNEST_GRAPH_MAX_UPLOAD_BYTES", "1048576")
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Limits.GraphMaxUploadBytes != 1048576 || got.Limits.MaxRequestBytes != 64<<10 {
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
		{"SCIP upload bytes", "GREPNEST_SCIP_MAX_UPLOAD_BYTES", "268435456", "268435457"},
		{"graph upload bytes", "GREPNEST_GRAPH_MAX_UPLOAD_BYTES", "268435456", "268435457"},
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
