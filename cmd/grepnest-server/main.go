package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/authz"
	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/httpapi"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/search"
	"github.com/grepnest/grepnest/internal/zoekt"
)

func main() { os.Exit(run()) }

func run() int {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	settings, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		return 1
	}
	handler, err := newHandler(settings)
	if err != nil {
		logger.Error("server setup failed", "error", err)
		return 1
	}
	server := &http.Server{
		Addr: settings.ListenAddress, Handler: handler,
		ReadTimeout: 10 * time.Second, ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("server shutdown failed", "error", err)
		}
	}()
	logger.Info("server listening", "address", settings.ListenAddress)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server listen failed", "error", err)
		return 1
	}
	return 0
}

func newHandler(settings config.Config) (http.Handler, error) {
	metrics := observability.New()
	registry, err := repository.Load(settings.RepositoriesFile, settings.Limits.MaxRequestBytes)
	if err != nil {
		return nil, err
	}
	authenticator := authn.NewStatic(map[string]authn.Principal{
		settings.UserToken:  {Subject: "user", Method: "bearer", RepositoryNames: settings.UserRepositories},
		settings.AdminToken: {Subject: "admin", Method: "bearer", Administrator: true, RepositoryNames: settings.AdminRepositories},
	})
	backend, err := zoekt.New(settings.ZoektURL, http.DefaultClient, settings.Limits.MaxResponseBytes, metrics)
	if err != nil {
		return nil, err
	}
	service := search.NewService(backend, authz.NewStatic(registry), search.Limits{
		DefaultResults: settings.Limits.DefaultResults, MaxResults: settings.Limits.MaxResults,
		DefaultContextLines: settings.Limits.DefaultContextLines, MaxContextLines: settings.Limits.MaxContextLines,
		DefaultTimeout: settings.Limits.DefaultTimeout, MaxTimeout: settings.Limits.MaxTimeout,
		MaxResponseBytes: settings.Limits.MaxResponseBytes,
	})
	mux := http.NewServeMux()
	httpapi.RegisterSystem(mux, backend, metrics.Handler())
	httpapi.RegisterSearch(mux, authenticator, service, settings.Limits.MaxRequestBytes)
	return metrics.WrapHTTP(mux), nil
}
