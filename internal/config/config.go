package config

import (
	"errors"
	"fmt"
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
}

type Config struct {
	ListenAddress, ZoektURL, RepositoriesFile string
	UserToken, AdminToken                     string
	UserRepositories, AdminRepositories       []string
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
		},
	}
	if config.RepositoriesFile == "" || config.UserToken == "" || config.AdminToken == "" || config.UserToken == config.AdminToken {
		return Config{}, invalid("repository file and distinct tokens are required")
	}
	if err := loadLimits(&config.Limits); err != nil {
		return Config{}, err
	}
	return config, nil
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
	if limits.MaxResults > 100 || limits.MaxContextLines > 20 || limits.MaxTimeout > 5*time.Second || limits.MaxRequestBytes > 64<<10 || limits.MaxResponseBytes > 256<<10 {
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
