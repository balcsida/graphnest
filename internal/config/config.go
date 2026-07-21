package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var ErrInvalid = errors.New("invalid configuration")

type Limits struct {
	DefaultResults, MaxResults           int
	DefaultContextLines, MaxContextLines int
	DefaultTimeout, MaxTimeout           time.Duration
	MaxRequestBytes, MaxResponseBytes    int64
	SCIPMaxUploadBytes                   int64
}

type GitHub struct {
	WebURL, APIURL, UploadURL, GitURL string
	PrivateKeyFile, WebhookSecretFile string
	APIVersion, CAFile                string
	AppID                             int64
}

type Indexer struct {
	DatabaseURL, ZoektURL, MetricsListenAddress         string
	GitHub                                              GitHub
	DataDir, IndexDir, GitPath, ZoektGitIndex, WorkerID string
	MinFreeBytes, MaxRepositoryBytes                    int64
}

type Config struct {
	ListenAddress, ZoektURL, RepositoriesFile string
	UserToken, AdminToken                     string
	UserRepositories, AdminRepositories       []string
	DatabaseURL                               string
	GitHub                                    GitHub
	Indexer                                   Indexer
	UserInstallationID, AdminInstallationID   int64
	UserRepositoryIDs, AdminRepositoryIDs     []int64
	Limits                                    Limits
}

func Load() (Config, error) {
	zoektURL := os.Getenv("GREPNEST_ZOEKT_URL")
	parsedURL, err := url.Parse(zoektURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return Config{}, invalid("GREPNEST_ZOEKT_URL must be an HTTP(S) URL")
	}

	config := Config{
		ListenAddress:     valueOr("GREPNEST_LISTEN_ADDRESS", ":8080"),
		ZoektURL:          zoektURL,
		RepositoriesFile:  os.Getenv("GREPNEST_REPOSITORIES_FILE"),
		UserToken:         os.Getenv("GREPNEST_USER_TOKEN"),
		AdminToken:        os.Getenv("GREPNEST_ADMIN_TOKEN"),
		UserRepositories:  split(os.Getenv("GREPNEST_USER_REPOSITORIES")),
		AdminRepositories: split(os.Getenv("GREPNEST_ADMIN_REPOSITORIES")),
		Limits: Limits{
			DefaultResults:      25,
			MaxResults:          100,
			DefaultContextLines: 3,
			MaxContextLines:     20,
			DefaultTimeout:      5 * time.Second,
			MaxTimeout:          5 * time.Second,
			MaxRequestBytes:     64 << 10,
			MaxResponseBytes:    256 << 10,
			SCIPMaxUploadBytes:  64 << 20,
		},
	}
	if config.UserToken == "" || config.AdminToken == "" || config.UserToken == config.AdminToken {
		return Config{}, invalid("distinct tokens are required")
	}
	if err := loadLimits(&config.Limits); err != nil {
		return Config{}, err
	}
	if databaseURL := os.Getenv("GREPNEST_DATABASE_URL"); databaseURL != "" {
		parsed, err := url.Parse(databaseURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
			return Config{}, invalid("GREPNEST_DATABASE_URL must be a PostgreSQL URL")
		}
		config.DatabaseURL = databaseURL
		if config.GitHub, err = loadGitHub(true); err != nil {
			return Config{}, err
		}
		if config.UserInstallationID, err = requiredInt64("GREPNEST_USER_INSTALLATION_ID"); err != nil {
			return Config{}, err
		}
		if config.AdminInstallationID, err = requiredInt64("GREPNEST_ADMIN_INSTALLATION_ID"); err != nil {
			return Config{}, err
		}
		if config.UserRepositoryIDs, err = repositoryIDs("GREPNEST_USER_REPOSITORY_IDS"); err != nil {
			return Config{}, err
		}
		if config.AdminRepositoryIDs, err = repositoryIDs("GREPNEST_ADMIN_REPOSITORY_IDS"); err != nil {
			return Config{}, err
		}
	} else if config.RepositoriesFile == "" {
		return Config{}, invalid("repository file is required in static mode")
	}
	return config, nil
}

func loadGitHub(requireWebhookSecret bool) (GitHub, error) {
	github := GitHub{
		WebURL:         os.Getenv("GREPNEST_GITHUB_WEB_URL"),
		APIURL:         os.Getenv("GREPNEST_GITHUB_API_URL"),
		UploadURL:      os.Getenv("GREPNEST_GITHUB_UPLOAD_URL"),
		GitURL:         os.Getenv("GREPNEST_GITHUB_GIT_URL"),
		PrivateKeyFile: os.Getenv("GREPNEST_GITHUB_PRIVATE_KEY_FILE"),
		APIVersion:     valueOr("GREPNEST_GITHUB_API_VERSION", "2022-11-28"),
		CAFile:         os.Getenv("GREPNEST_GITHUB_CA_FILE"),
	}
	if requireWebhookSecret {
		github.WebhookSecretFile = os.Getenv("GREPNEST_GITHUB_WEBHOOK_SECRET_FILE")
	}
	var err error
	if github.AppID, err = requiredInt64("GREPNEST_GITHUB_APP_ID"); err != nil {
		return GitHub{}, err
	}
	for _, endpoint := range []struct{ name, value string }{
		{"GREPNEST_GITHUB_WEB_URL", github.WebURL}, {"GREPNEST_GITHUB_API_URL", github.APIURL},
		{"GREPNEST_GITHUB_UPLOAD_URL", github.UploadURL}, {"GREPNEST_GITHUB_GIT_URL", github.GitURL},
	} {
		parsed, err := url.Parse(endpoint.value)
		if err != nil || parsed.Host == "" || parsed.Scheme != "https" {
			return GitHub{}, invalid(endpoint.name + " must be an HTTPS URL")
		}
	}
	if github.PrivateKeyFile == "" || (requireWebhookSecret && github.WebhookSecretFile == "") {
		return GitHub{}, invalid("GitHub secret file paths are required")
	}
	return github, nil
}

func LoadIndexer() (Indexer, error) {
	indexer := Indexer{
		DatabaseURL:          os.Getenv("GREPNEST_DATABASE_URL"),
		ZoektURL:             os.Getenv("GREPNEST_ZOEKT_URL"),
		MetricsListenAddress: valueOr("GREPNEST_METRICS_LISTEN_ADDRESS", ":9090"),
		DataDir:              os.Getenv("GREPNEST_DATA_DIR"),
		IndexDir:             os.Getenv("GREPNEST_INDEX_DIR"),
		GitPath:              os.Getenv("GREPNEST_GIT_PATH"),
		ZoektGitIndex:        os.Getenv("GREPNEST_ZOEKT_GIT_INDEX"),
		WorkerID:             os.Getenv("GREPNEST_WORKER_ID"),
	}
	parsedDatabase, databaseErr := url.Parse(indexer.DatabaseURL)
	if databaseErr != nil || parsedDatabase.Host == "" || (parsedDatabase.Scheme != "postgres" && parsedDatabase.Scheme != "postgresql") {
		return Indexer{}, invalid("GREPNEST_DATABASE_URL must be a PostgreSQL URL")
	}
	parsedZoekt, zoektErr := url.Parse(indexer.ZoektURL)
	if zoektErr != nil || parsedZoekt.Host == "" || (parsedZoekt.Scheme != "http" && parsedZoekt.Scheme != "https") {
		return Indexer{}, invalid("GREPNEST_ZOEKT_URL must be an HTTP(S) URL")
	}
	var err error
	if indexer.GitHub, err = loadGitHub(false); err != nil {
		return Indexer{}, err
	}
	if indexer.DataDir == "" || indexer.IndexDir == "" || indexer.GitPath == "" || indexer.ZoektGitIndex == "" || indexer.WorkerID == "" {
		return Indexer{}, invalid("indexer paths and worker ID are required")
	}
	if indexer.MinFreeBytes, err = requiredInt64("GREPNEST_MIN_FREE_BYTES"); err != nil {
		return Indexer{}, err
	}
	if indexer.MaxRepositoryBytes, err = strconv.ParseInt(valueOr("GREPNEST_MAX_REPOSITORY_BYTES", "5368709120"), 10, 64); err != nil || indexer.MaxRepositoryBytes <= 0 {
		return Indexer{}, invalid("GREPNEST_MAX_REPOSITORY_BYTES must be a positive integer")
	}
	_, port, err := net.SplitHostPort(indexer.MetricsListenAddress)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 {
		return Indexer{}, invalid("GREPNEST_METRICS_LISTEN_ADDRESS must be a host:port address")
	}
	return indexer, nil
}

func loadLimits(limits *Limits) error {
	if err := intValue("GREPNEST_DEFAULT_RESULTS", &limits.DefaultResults); err != nil {
		return err
	}
	if err := intValue("GREPNEST_MAX_RESULTS", &limits.MaxResults); err != nil {
		return err
	}
	if err := intValue("GREPNEST_DEFAULT_CONTEXT_LINES", &limits.DefaultContextLines); err != nil {
		return err
	}
	if err := intValue("GREPNEST_MAX_CONTEXT_LINES", &limits.MaxContextLines); err != nil {
		return err
	}
	if err := durationValue("GREPNEST_DEFAULT_TIMEOUT", &limits.DefaultTimeout); err != nil {
		return err
	}
	if err := durationValue("GREPNEST_MAX_TIMEOUT", &limits.MaxTimeout); err != nil {
		return err
	}
	if err := int64Value("GREPNEST_MAX_REQUEST_BYTES", &limits.MaxRequestBytes); err != nil {
		return err
	}
	if err := int64Value("GREPNEST_MAX_RESPONSE_BYTES", &limits.MaxResponseBytes); err != nil {
		return err
	}
	if err := int64Value("GREPNEST_SCIP_MAX_UPLOAD_BYTES", &limits.SCIPMaxUploadBytes); err != nil {
		return err
	}
	if limits.MaxResults > 100 || limits.MaxContextLines > 20 || limits.MaxTimeout > 5*time.Second || limits.MaxRequestBytes > 64<<10 || limits.MaxResponseBytes > 256<<10 || limits.SCIPMaxUploadBytes > 256<<20 {
		return invalid("maximums exceed server safety caps")
	}
	if limits.DefaultResults > limits.MaxResults || limits.DefaultContextLines > limits.MaxContextLines || limits.DefaultTimeout > limits.MaxTimeout {
		return invalid("defaults must not exceed maximums")
	}
	return nil
}

func intValue(name string, target *int) error {
	if value := os.Getenv(name); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return invalid(name + " must be positive")
		}
		*target = parsed
	}
	return nil
}

func int64Value(name string, target *int64) error {
	if value := os.Getenv(name); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			return invalid(name + " must be positive")
		}
		*target = parsed
	}
	return nil
}

func requiredInt64(name string) (int64, error) {
	value := os.Getenv(name)
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, invalid(name + " must be positive")
	}
	return parsed, nil
}

func repositoryIDs(name string) ([]int64, error) {
	values := split(os.Getenv(name))
	if len(values) == 0 {
		return nil, invalid(name + " is required")
	}
	result := make([]int64, len(values))
	for index, value := range values {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			return nil, invalid(name + " must contain positive IDs")
		}
		result[index] = parsed
	}
	return result, nil
}

func durationValue(name string, target *time.Duration) error {
	if value := os.Getenv(name); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return invalid(name + " must be positive")
		}
		*target = parsed
	}
	return nil
}

func valueOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func split(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts
}

func invalid(message string) error { return fmt.Errorf("%w: %s", ErrInvalid, message) }
