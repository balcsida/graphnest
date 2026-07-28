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

func TestLoadGraphDefaultsAndClamps(t *testing.T) {
	secret := writeGraphSecret(t, 0o600)
	t.Setenv("GREPNEST_GRAPH_SECRET_FILE", secret)
	t.Setenv("GREPNEST_DATABASE_URL", "postgres://grepnest:secret@db/grepnest")
	t.Setenv("GREPNEST_GRAPH_READ_CONNECTIONS", "99")

	got, err := LoadGraph()
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "embedded" || got.DataDir != "/var/lib/grepnest/graph" ||
		got.ListenAddress != "127.0.0.1:8081" || got.ReadConnections != 32 ||
		got.SyncInterval != 30*time.Second || got.QueryTimeout != 5*time.Second ||
		got.InterruptGrace != 2*time.Second || string(got.InternalSecret) != "graph-secret" {
		t.Fatalf("graph configuration = %#v", got)
	}
	got.InternalSecret[0] = 'X'
	data, err := os.ReadFile(secret)
	if err != nil || string(data) != "graph-secret" {
		t.Fatalf("secret file changed: %q %v", data, err)
	}
}

func TestLoadGraphNormalizesAndValidatesBearerSecret(t *testing.T) {
	for _, test := range []struct {
		name, secret, want string
		valid              bool
	}{
		{"raw", "abc._~+/-=", "abc._~+/-=", true},
		{"LF", "secret\n", "secret", true},
		{"CRLF", "secret\r\n", "secret", true},
		{"empty", "", "", false},
		{"only LF", "\n", "", false},
		{"space", "secret value", "", false},
		{"control", "secret\x00", "", false},
		{"two LF", "secret\n\n", "", false},
		{"embedded LF", "sec\nret", "", false},
		{"bare CR", "secret\r", "", false},
		{"bad padding", "secret=a", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "secret")
			if err := os.WriteFile(path, []byte(test.secret), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GREPNEST_GRAPH_SECRET_FILE", path)
			t.Setenv("GREPNEST_DATABASE_URL", "postgres://grepnest:secret@db/grepnest")
			got, err := LoadGraph()
			if !test.valid {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("LoadGraph() error = %v", err)
				}
				return
			}
			if err != nil || string(got.InternalSecret) != test.want {
				t.Fatalf("secret=%q error=%v", got.InternalSecret, err)
			}
		})
	}
}

func TestLoadGraphPropagatesQueryOverrides(t *testing.T) {
	t.Setenv("GREPNEST_GRAPH_SECRET_FILE", writeGraphSecret(t, 0o600))
	t.Setenv("GREPNEST_DATABASE_URL", "postgres://grepnest:secret@db/grepnest")
	for name, value := range map[string]string{
		"GREPNEST_GRAPH_DEFAULT_IMPACT_DEPTH": "2",
		"GREPNEST_GRAPH_MAX_IMPACT_DEPTH":     "7",
		"GREPNEST_GRAPH_DEFAULT_TRACE_DEPTH":  "4",
		"GREPNEST_GRAPH_MAX_TRACE_DEPTH":      "9",
		"GREPNEST_GRAPH_MAX_ROWS":             "321",
	} {
		t.Setenv(name, value)
	}
	got, err := LoadGraph()
	if err != nil {
		t.Fatal(err)
	}
	if got.QueryLimits.DefaultImpactDepth != 2 || got.QueryLimits.MaxDepth != 7 ||
		got.QueryLimits.DefaultTraceDepth != 4 || got.QueryLimits.MaxTraceDepth != 9 ||
		got.QueryLimits.MaxRows != 321 {
		t.Fatalf("query limits = %#v", got.QueryLimits)
	}
}

func TestLoadGraphRejectsUnsafeConfiguration(t *testing.T) {
	secret := writeGraphSecret(t, 0o600)
	for _, test := range []struct{ name, env, value string }{
		{"mode", "GREPNEST_GRAPH_MODE", "remote"},
		{"listen", "GREPNEST_GRAPH_LISTEN_ADDRESS", ":0"},
		{"sync interval", "GREPNEST_GRAPH_SYNC_INTERVAL", "0s"},
		{"query timeout", "GREPNEST_GRAPH_QUERY_TIMEOUT", "6s"},
		{"interrupt grace", "GREPNEST_GRAPH_INTERRUPT_GRACE", "3s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GREPNEST_GRAPH_SECRET_FILE", secret)
			t.Setenv(test.env, test.value)
			if _, err := LoadGraph(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("LoadGraph() error = %v", err)
			}
		})
	}
}

func TestLoadGraphRequiresSecureRegularSecretFile(t *testing.T) {
	for _, test := range []struct {
		name string
		path func(*testing.T) string
	}{
		{"missing", func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing") }},
		{"directory", func(t *testing.T) string { return t.TempDir() }},
		{"permissive", func(t *testing.T) string { return writeGraphSecret(t, 0o644) }},
		{"symlink", func(t *testing.T) string {
			target := writeGraphSecret(t, 0o600)
			link := filepath.Join(t.TempDir(), "secret")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GREPNEST_GRAPH_SECRET_FILE", test.path(t))
			if _, err := LoadGraph(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("LoadGraph() error = %v", err)
			}
		})
	}
}

func TestLoadIndexerSeparateModeDoesNotReadGraphSecret(t *testing.T) {
	setDurableEnvironment(t)
	t.Setenv("GREPNEST_ZOEKT_URL", "http://127.0.0.1:6070")
	t.Setenv("GREPNEST_GRAPH_MODE", "separate")
	t.Setenv("GREPNEST_GRAPH_SECRET_FILE", filepath.Join(t.TempDir(), "missing"))

	got, err := LoadIndexer()
	if err != nil {
		t.Fatal(err)
	}
	if got.Graph.Mode != "separate" || got.Graph.InternalSecret != nil {
		t.Fatalf("graph configuration = %#v", got.Graph)
	}
}

func writeGraphSecret(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("graph-secret"), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

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
	if got.DatabaseURL != "postgres://grepnest:secret@db/grepnest" || got.GitHub.WebURL != "https://ghe.example.com" || got.GitHub.APIURL != "https://ghe.example.com/api/v3" || got.GitHub.UploadURL != "https://ghe.example.com/uploads" || got.GitHub.GitURL != "https://ghe.example.com" || got.GitHub.AppID != 123 || got.GitHub.PrivateKeyFile != "/run/secrets/key.pem" || got.GitHub.WebhookSecretFile != "/run/secrets/webhook" || got.GitHub.CAFile != "/run/secrets/ca.pem" || got.GitHub.APIVersion != "2022-11-28" || got.UserInstallationID != 10 || !reflect.DeepEqual(got.UserRepositoryIDs, []int64{101, 102}) || got.AdminInstallationID != 10 || !reflect.DeepEqual(got.AdminRepositoryIDs, []int64{101, 102, 103}) || !reflect.DeepEqual(got.Indexer, Indexer{}) {
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
	if got.DatabaseURL == "" || got.RepositoriesFile != "" || !reflect.DeepEqual(got.Indexer, Indexer{}) {
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
		got.GitHub.AppID != 123 || got.GitHub.WebhookSecretFile != "" {
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
	t.Setenv("GREPNEST_GRAPH_SECRET_FILE", writeGraphSecret(t, 0o600))
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
