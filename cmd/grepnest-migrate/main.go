package main

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/grepnest/grepnest/internal/postgres"
)

type migrationRuntime struct {
	ping, migrate func(context.Context) error
	close         func()
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	databaseURL, err := loadDatabaseURL(os.Getenv)
	if err != nil {
		logger.Error("configuration failed", "error", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	pool, err := postgres.Open(ctx, databaseURL)
	if err == nil {
		runtime := migrationRuntime{ping: pool.Ping, migrate: func(ctx context.Context) error { return postgres.Migrate(ctx, pool) }, close: pool.Close}
		err = runtime.run(ctx)
	}
	if err != nil {
		logger.Error("migration failed")
		os.Exit(1)
	}
}

func loadDatabaseURL(getenv func(string) string) (string, error) {
	value := getenv("GREPNEST_DATABASE_URL")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return "", errors.New("GREPNEST_DATABASE_URL must be a PostgreSQL URL")
	}
	return value, nil
}

func (runtime migrationRuntime) run(ctx context.Context) error {
	defer runtime.close()
	if err := runtime.ping(ctx); err != nil {
		return err
	}
	return runtime.migrate(ctx)
}
