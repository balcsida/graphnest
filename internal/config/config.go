package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/grepnest/grepnest/internal/graphtransport"
)

var ErrInvalid = errors.New("invalid configuration")

type Limits struct {
	DefaultResults, MaxResults              int
	DefaultContextLines, MaxContextLines    int
	DefaultTimeout, MaxTimeout              time.Duration
	MaxRequestBytes, MaxResponseBytes       int64
	SCIPMaxUploadBytes, GraphMaxUploadBytes int64
}

type GitHub struct {
	WebURL, APIURL, UploadURL, GitURL, ArchiveURL string
	PrivateKeyFile, WebhookSecretFile             string
	APIVersion, CAFile                            string
	AppID                                         int64
}

type Indexer struct {
	DatabaseURL, ZoektURL, MetricsListenAddress         string
	GitHub                                              GitHub
	Graph                                               Graph
	DataDir, IndexDir, GitPath, ZoektGitIndex, WorkerID string
	MinFreeBytes, MaxRepositoryBytes                    int64
	SourceProvider                                      string
	ArchiveLimits                                       ArchiveLimits
}

type ArchiveLimits struct {
	MaxDownloadBytes, MaxExtractedBytes, MaxFileBytes int64
	MaxFiles, MaxPathBytes                            int
}

type Graph struct {
	Mode, URL, ListenAddress, DataDir, SecretFile, DatabaseURL string
	InternalSecret                                             []byte
	SyncInterval, QueryTimeout, InterruptGrace                 time.Duration
	ReadConnections, DefaultImpactDepth, MaxImpactDepth        int
	DefaultTraceDepth, MaxTraceDepth, MaxRows                  int
	MaxNodes, MaxEdges                                         int
	MaxRequestBytes, MaxResponseBytes                          int64
	QueryLimits                                                GraphQueryLimits
}

type GraphQueryLimits struct {
	PerCategory, DefaultImpactDepth, MaxDepth int
	DefaultTraceDepth, MaxTraceDepth, MaxRows int
	MaxNodes, MaxEdges, MaxFanout             int
}

type GraphScanLimits struct {
	MaxFileBytes, MaxTotalBytes  int64
	MaxFiles, MaxNodes, MaxEdges int
	ParseTimeout                 time.Duration
	SkipDirectories              []string
}

type Scanner struct {
	DatabaseURL, DataDir, GitPath, WorkerID, MetricsListenAddress string
	GitHub                                                        GitHub
	Limits                                                        GraphScanLimits
	MinFreeBytes, MaxRepositoryBytes                              int64
	ScanTimeout                                                   time.Duration
}

type Config struct {
	ListenAddress, ZoektURL, RepositoriesFile string
	UserToken, AdminToken                     string
	UserRepositories, AdminRepositories       []string
	DatabaseURL                               string
	GitHub                                    GitHub
	Graph                                     Graph
	Indexer                                   Indexer
	UserInstallationID, AdminInstallationID   int64
	UserRepositoryIDs, AdminRepositoryIDs     []int64
	Limits                                    Limits
	SSO                                       SSO
	SCIM                                      SCIM
}

type SCIM struct {
	Enabled   bool
	TokenFile string
	PublicURL *url.URL
}

func Load() (Config, error) {
	zoektURL := os.Getenv("GREPNEST_ZOEKT_URL")
	parsedURL, err := url.Parse(zoektURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return Config{}, invalid("GREPNEST_ZOEKT_URL must be an HTTP(S) URL")
	}

	config := Config{
		ListenAddress:    valueOr("GREPNEST_LISTEN_ADDRESS", ":8080"),
		ZoektURL:         zoektURL,
		RepositoriesFile: os.Getenv("GREPNEST_REPOSITORIES_FILE"),
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
			GraphMaxUploadBytes: 64 << 20,
		},
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
		if config.Graph, err = loadServerGraph(); err != nil {
			return Config{}, err
		}
	} else {
		config.UserToken = os.Getenv("GREPNEST_USER_TOKEN")
		config.AdminToken = os.Getenv("GREPNEST_ADMIN_TOKEN")
		config.UserRepositories = split(os.Getenv("GREPNEST_USER_REPOSITORIES"))
		config.AdminRepositories = split(os.Getenv("GREPNEST_ADMIN_REPOSITORIES"))
		if config.RepositoriesFile == "" {
			return Config{}, invalid("repository file is required in static mode")
		}
		if config.UserToken == "" || config.AdminToken == "" || config.UserToken == config.AdminToken {
			return Config{}, invalid("distinct tokens are required")
		}
	}
	if config.SSO, err = loadSSO(config.DatabaseURL); err != nil {
		return Config{}, err
	}
	if config.SCIM, err = loadSCIM(config.DatabaseURL); err != nil {
		return Config{}, err
	}
	return config, nil
}

func loadSCIM(databaseURL string) (SCIM, error) {
	tokenFile, token := os.Getenv("GREPNEST_SCIM_TOKEN_FILE"), os.Getenv("GREPNEST_SCIM_TOKEN")
	if token != "" {
		return SCIM{}, invalid("GREPNEST_SCIM_TOKEN is not supported; use GREPNEST_SCIM_TOKEN_FILE")
	}
	if tokenFile == "" {
		return SCIM{}, nil
	}
	if databaseURL == "" {
		return SCIM{}, invalid("GREPNEST_DATABASE_URL is required for SCIM")
	}
	info, err := os.Stat(tokenFile)
	if err != nil || !info.Mode().IsRegular() {
		return SCIM{}, invalid("GREPNEST_SCIM_TOKEN_FILE must be a regular file")
	}
	publicURL, err := parseHTTPSOrigin("GREPNEST_PUBLIC_URL", os.Getenv("GREPNEST_PUBLIC_URL"))
	if err != nil {
		return SCIM{}, err
	}
	return SCIM{Enabled: true, TokenFile: tokenFile, PublicURL: publicURL}, nil
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
	github.ArchiveURL = os.Getenv("GREPNEST_GITHUB_ARCHIVE_URL")
	if github.ArchiveURL == "" {
		if parsed, _ := url.Parse(github.WebURL); parsed != nil && parsed.Hostname() == "github.com" {
			github.ArchiveURL = "https://codeload.github.com"
		} else {
			github.ArchiveURL = github.WebURL
		}
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
		{"GREPNEST_GITHUB_ARCHIVE_URL", github.ArchiveURL},
	} {
		parsed, err := url.Parse(endpoint.value)
		if err != nil || parsed.Host == "" || parsed.Scheme != "https" || parsed.User != nil {
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
		SourceProvider:       valueOr("GREPNEST_SOURCE_PROVIDER", "git"),
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
	if indexer.SourceProvider != "git" && indexer.SourceProvider != "archive" {
		return Indexer{}, invalid("GREPNEST_SOURCE_PROVIDER must be archive or git")
	}
	archiveValues := []struct {
		name        string
		value       string
		destination *int64
	}{
		{"GREPNEST_ARCHIVE_MAX_DOWNLOAD_BYTES", "1073741824", &indexer.ArchiveLimits.MaxDownloadBytes},
		{"GREPNEST_ARCHIVE_MAX_EXTRACTED_BYTES", "5368709120", &indexer.ArchiveLimits.MaxExtractedBytes},
		{"GREPNEST_ARCHIVE_MAX_FILE_BYTES", "536870912", &indexer.ArchiveLimits.MaxFileBytes},
	}
	for _, setting := range archiveValues {
		if *setting.destination, err = strconv.ParseInt(valueOr(setting.name, setting.value), 10, 64); err != nil || *setting.destination <= 0 {
			return Indexer{}, invalid(setting.name + " must be a positive integer")
		}
	}
	if value, parseErr := strconv.Atoi(valueOr("GREPNEST_ARCHIVE_MAX_FILES", "200000")); parseErr != nil || value <= 0 {
		return Indexer{}, invalid("GREPNEST_ARCHIVE_MAX_FILES must be a positive integer")
	} else {
		indexer.ArchiveLimits.MaxFiles = value
	}
	if value, parseErr := strconv.Atoi(valueOr("GREPNEST_ARCHIVE_MAX_PATH_BYTES", "4096")); parseErr != nil || value <= 0 {
		return Indexer{}, invalid("GREPNEST_ARCHIVE_MAX_PATH_BYTES must be a positive integer")
	} else {
		indexer.ArchiveLimits.MaxPathBytes = value
	}
	if err := validListenAddress(indexer.MetricsListenAddress); err != nil {
		return Indexer{}, err
	}
	if indexer.Graph, err = loadGraph(false); err != nil {
		return Indexer{}, err
	}
	return indexer, nil
}

func LoadGraph() (Graph, error) { return loadGraph(true) }

func loadServerGraph() (Graph, error) {
	graph, err := loadGraph(true)
	if err != nil {
		return Graph{}, err
	}
	parsed, err := url.Parse(graph.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.EscapedPath() != "" && parsed.EscapedPath() != "/") {
		return Graph{}, invalid("GREPNEST_GRAPH_URL must be an HTTP(S) URL without credentials, query, fragment, or path")
	}
	return graph, nil
}

func loadGraph(force bool) (Graph, error) {
	graph := Graph{
		Mode:          valueOr("GREPNEST_GRAPH_MODE", "embedded"),
		URL:           os.Getenv("GREPNEST_GRAPH_URL"),
		ListenAddress: valueOr("GREPNEST_GRAPH_LISTEN_ADDRESS", "127.0.0.1:8081"),
		DataDir:       valueOr("GREPNEST_GRAPH_DATA_DIR", "/var/lib/grepnest/graph"),
		SecretFile:    os.Getenv("GREPNEST_GRAPH_SECRET_FILE"),
		DatabaseURL:   os.Getenv("GREPNEST_DATABASE_URL"),
		SyncInterval:  30 * time.Second, QueryTimeout: 5 * time.Second,
		InterruptGrace: 2 * time.Second, ReadConnections: 8,
		DefaultImpactDepth: 3, MaxImpactDepth: 32,
		DefaultTraceDepth: 10, MaxTraceDepth: 30,
		MaxRows: 1_000, MaxNodes: 1_000, MaxEdges: 5_000,
		MaxRequestBytes: 64 << 10, MaxResponseBytes: 256 << 10,
		QueryLimits: GraphQueryLimits{
			PerCategory: 100, DefaultImpactDepth: 3, MaxDepth: 32,
			DefaultTraceDepth: 10, MaxTraceDepth: 30, MaxRows: 1_000,
			MaxNodes: 1_000, MaxEdges: 5_000, MaxFanout: 100,
		},
	}
	if graph.Mode != "embedded" && graph.Mode != "separate" {
		return Graph{}, invalid("GREPNEST_GRAPH_MODE must be embedded or separate")
	}
	if err := validListenAddress(graph.ListenAddress); err != nil {
		return Graph{}, invalid("GREPNEST_GRAPH_LISTEN_ADDRESS must be a host:port address")
	}
	for name, target := range map[string]*time.Duration{
		"GREPNEST_GRAPH_SYNC_INTERVAL":   &graph.SyncInterval,
		"GREPNEST_GRAPH_QUERY_TIMEOUT":   &graph.QueryTimeout,
		"GREPNEST_GRAPH_INTERRUPT_GRACE": &graph.InterruptGrace,
	} {
		if err := durationValue(name, target); err != nil {
			return Graph{}, err
		}
	}
	if err := intValue("GREPNEST_GRAPH_READ_CONNECTIONS", &graph.ReadConnections); err != nil {
		return Graph{}, err
	}
	if err := int64Value("GREPNEST_GRAPH_MAX_REQUEST_BYTES", &graph.MaxRequestBytes); err != nil {
		return Graph{}, err
	}
	if err := int64Value("GREPNEST_GRAPH_MAX_RESPONSE_BYTES", &graph.MaxResponseBytes); err != nil {
		return Graph{}, err
	}
	for name, target := range map[string]*int{
		"GREPNEST_GRAPH_DEFAULT_IMPACT_DEPTH": &graph.DefaultImpactDepth,
		"GREPNEST_GRAPH_MAX_IMPACT_DEPTH":     &graph.MaxImpactDepth,
		"GREPNEST_GRAPH_DEFAULT_TRACE_DEPTH":  &graph.DefaultTraceDepth,
		"GREPNEST_GRAPH_MAX_TRACE_DEPTH":      &graph.MaxTraceDepth,
		"GREPNEST_GRAPH_MAX_ROWS":             &graph.MaxRows,
		"GREPNEST_GRAPH_MAX_NODES":            &graph.MaxNodes,
		"GREPNEST_GRAPH_MAX_EDGES":            &graph.MaxEdges,
	} {
		if err := intValue(name, target); err != nil {
			return Graph{}, err
		}
	}
	if graph.ReadConnections > 32 {
		graph.ReadConnections = 32
	}
	if graph.QueryTimeout > 5*time.Second || graph.InterruptGrace > 2*time.Second {
		return Graph{}, invalid("graph timeouts exceed safety caps")
	}
	if graph.MaxRequestBytes > 64<<10 || graph.MaxResponseBytes > 256<<10 {
		return Graph{}, invalid("graph public limits exceed safety caps")
	}
	if graph.MaxImpactDepth > 32 || graph.MaxTraceDepth > 30 || graph.MaxRows > 1_000 ||
		graph.MaxNodes > 1_000 || graph.MaxEdges > 5_000 ||
		graph.DefaultImpactDepth > graph.MaxImpactDepth || graph.DefaultTraceDepth > graph.MaxTraceDepth {
		return Graph{}, invalid("graph query limits exceed safety caps")
	}
	graph.QueryLimits.MaxDepth = graph.MaxImpactDepth
	graph.QueryLimits.DefaultImpactDepth = graph.DefaultImpactDepth
	graph.QueryLimits.DefaultTraceDepth = graph.DefaultTraceDepth
	graph.QueryLimits.MaxTraceDepth = graph.MaxTraceDepth
	graph.QueryLimits.MaxRows = graph.MaxRows
	graph.QueryLimits.MaxNodes = graph.MaxNodes
	graph.QueryLimits.MaxEdges = graph.MaxEdges
	if force || graph.Mode == "embedded" {
		parsed, err := url.Parse(graph.DatabaseURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
			return Graph{}, invalid("GREPNEST_DATABASE_URL must be a PostgreSQL URL")
		}
		secret, err := readSecretFile(graph.SecretFile, 4<<10)
		if err != nil {
			return Graph{}, err
		}
		graph.InternalSecret = append([]byte(nil), secret...)
	}
	return graph, nil
}

func readSecretFile(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, invalid("GREPNEST_GRAPH_SECRET_FILE must be a secure regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, invalid("GREPNEST_GRAPH_SECRET_FILE cannot be read")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o077 != 0 || !os.SameFile(info, opened) {
		return nil, invalid("GREPNEST_GRAPH_SECRET_FILE must be a secure regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || len(data) == 0 || int64(len(data)) > maxBytes {
		return nil, invalid("GREPNEST_GRAPH_SECRET_FILE size is invalid")
	}
	if bytes.HasSuffix(data, []byte("\r\n")) {
		data = data[:len(data)-2]
	} else if bytes.HasSuffix(data, []byte("\n")) {
		data = data[:len(data)-1]
	}
	if !graphtransport.ValidBearerToken(data) {
		return nil, invalid("GREPNEST_GRAPH_SECRET_FILE must contain an RFC 6750 bearer token")
	}
	return data, nil
}

func LoadScanner() (Scanner, error) {
	scanner := Scanner{
		DatabaseURL:          os.Getenv("GREPNEST_DATABASE_URL"),
		DataDir:              os.Getenv("GREPNEST_DATA_DIR"),
		GitPath:              os.Getenv("GREPNEST_GIT_PATH"),
		WorkerID:             os.Getenv("GREPNEST_WORKER_ID"),
		MetricsListenAddress: valueOr("GREPNEST_METRICS_LISTEN_ADDRESS", ":9090"),
		ScanTimeout:          15 * time.Minute,
		Limits: GraphScanLimits{
			MaxFileBytes: 2 << 20, MaxTotalBytes: 1 << 30, MaxFiles: 100_000,
			MaxNodes: 500_000, MaxEdges: 2_000_000, ParseTimeout: 30 * time.Second,
			SkipDirectories: []string{"node_modules", "vendor", "target", "build", "dist", ".gradle"},
		},
	}
	parsed, err := url.Parse(scanner.DatabaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return Scanner{}, invalid("GREPNEST_DATABASE_URL must be a PostgreSQL URL")
	}
	if scanner.GitHub, err = loadGitHub(false); err != nil {
		return Scanner{}, err
	}
	if scanner.DataDir == "" || scanner.GitPath == "" || scanner.WorkerID == "" {
		return Scanner{}, invalid("scanner paths and worker ID are required")
	}
	if err := loadStorageLimits(&scanner.MinFreeBytes, &scanner.MaxRepositoryBytes); err != nil {
		return Scanner{}, err
	}
	if err := durationValue("GREPNEST_GRAPH_SCAN_TIMEOUT", &scanner.ScanTimeout); err != nil {
		return Scanner{}, err
	}
	if scanner.ScanTimeout > 30*time.Minute {
		return Scanner{}, invalid("GREPNEST_GRAPH_SCAN_TIMEOUT exceeds safety cap")
	}
	if err := loadGraphScanLimits(&scanner.Limits); err != nil {
		return Scanner{}, err
	}
	if err := validListenAddress(scanner.MetricsListenAddress); err != nil {
		return Scanner{}, err
	}
	return scanner, nil
}

func loadStorageLimits(minFreeBytes, maxRepositoryBytes *int64) error {
	var err error
	if *minFreeBytes, err = strconv.ParseInt(valueOr("GREPNEST_MIN_FREE_BYTES", "1073741824"), 10, 64); err != nil || *minFreeBytes <= 0 {
		return invalid("GREPNEST_MIN_FREE_BYTES must be a positive integer")
	}
	if *maxRepositoryBytes, err = strconv.ParseInt(valueOr("GREPNEST_MAX_REPOSITORY_BYTES", "5368709120"), 10, 64); err != nil || *maxRepositoryBytes <= 0 {
		return invalid("GREPNEST_MAX_REPOSITORY_BYTES must be a positive integer")
	}
	return nil
}

func loadGraphScanLimits(limits *GraphScanLimits) error {
	if err := int64Value("GREPNEST_GRAPH_SCAN_MAX_FILE_BYTES", &limits.MaxFileBytes); err != nil {
		return err
	}
	if err := int64Value("GREPNEST_GRAPH_SCAN_MAX_TOTAL_BYTES", &limits.MaxTotalBytes); err != nil {
		return err
	}
	if err := intValue("GREPNEST_GRAPH_SCAN_MAX_FILES", &limits.MaxFiles); err != nil {
		return err
	}
	if err := intValue("GREPNEST_GRAPH_SCAN_MAX_NODES", &limits.MaxNodes); err != nil {
		return err
	}
	if err := intValue("GREPNEST_GRAPH_SCAN_MAX_EDGES", &limits.MaxEdges); err != nil {
		return err
	}
	if err := durationValue("GREPNEST_GRAPH_SCAN_PARSE_TIMEOUT", &limits.ParseTimeout); err != nil {
		return err
	}
	if limits.MaxFileBytes > 2<<20 || limits.MaxTotalBytes > 1<<30 || limits.MaxFiles > 100_000 ||
		limits.MaxNodes > 500_000 || limits.MaxEdges > 2_000_000 || limits.ParseTimeout > 30*time.Second {
		return invalid("graph scan limits exceed safety caps")
	}
	if value, present := os.LookupEnv("GREPNEST_GRAPH_SCAN_SKIP_DIRECTORIES"); present {
		var err error
		if limits.SkipDirectories, err = skipDirectories(value); err != nil {
			return err
		}
	}
	return nil
}

func skipDirectories(value string) ([]string, error) {
	seen := map[string]bool{}
	var result []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `/\`) {
			return nil, invalid("GREPNEST_GRAPH_SCAN_SKIP_DIRECTORIES must contain directory names")
		}
		if !seen[part] {
			seen[part] = true
			result = append(result, part)
		}
	}
	return result, nil
}

func validListenAddress(address string) error {
	_, port, err := net.SplitHostPort(address)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 {
		return invalid("GREPNEST_METRICS_LISTEN_ADDRESS must be a host:port address")
	}
	return nil
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
	if err := int64Value("GREPNEST_GRAPH_MAX_UPLOAD_BYTES", &limits.GraphMaxUploadBytes); err != nil {
		return err
	}
	if limits.MaxResults > 100 || limits.MaxContextLines > 20 || limits.MaxTimeout > 5*time.Second || limits.MaxRequestBytes > 64<<10 ||
		limits.MaxResponseBytes > 256<<10 || limits.SCIPMaxUploadBytes > 256<<20 || limits.GraphMaxUploadBytes > 256<<20 {
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
