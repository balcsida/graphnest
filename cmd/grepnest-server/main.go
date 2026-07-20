package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/authz"
	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/httpapi"
	"github.com/grepnest/grepnest/internal/mcpserver"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/search"
	"github.com/grepnest/grepnest/internal/webhook"
	"github.com/grepnest/grepnest/internal/zoekt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	searchNodeID           = "primary"
	reconcileInterval      = 5 * time.Minute
	maxPrivateKeyBytes     = 64 << 10
	maxWebhookKeyBytes     = 64 << 10
	maxCABytes             = 1 << 20
	maxGitHubResponseBytes = 2 << 20
)

func main() { os.Exit(run()) }

func run() int {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	settings, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		return 1
	}
	handler, closeRuntime, err := newRuntime(ctx, settings, logger)
	if err != nil {
		logger.Error("server setup failed")
		return 1
	}
	defer closeRuntime()
	server := &http.Server{
		Addr: settings.ListenAddress, Handler: handler,
		ReadTimeout: 10 * time.Second, ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: time.Minute,
	}
	logger.Info("server listening", "address", settings.ListenAddress)
	if err := serveHTTP(ctx, server, logger); err != nil {
		logger.Error("server listen failed", "error", err)
		return 1
	}
	return 0
}

type shutdownServer interface {
	ListenAndServe() error
	Shutdown(context.Context) error
}

func serveHTTP(ctx context.Context, server shutdownServer, logger *slog.Logger) error {
	listenDone := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				logger.Error("server shutdown failed", "error", err)
			}
		case <-listenDone:
		}
	}()
	err := server.ListenAndServe()
	close(listenDone)
	<-shutdownDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func newHandler(settings config.Config) (http.Handler, error) {
	if settings.DatabaseURL != "" {
		return nil, errors.New("durable server requires a runtime context")
	}
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
	return newAPIHandler(settings, metrics, authenticator, service, nil, nil, nil, backend), nil
}

func newRuntime(ctx context.Context, settings config.Config, logger *slog.Logger) (http.Handler, func(), error) {
	if settings.DatabaseURL == "" {
		handler, err := newHandler(settings)
		return handler, func() {}, err
	}
	return newDurableRuntime(ctx, settings, logger)
}

func newDurableRuntime(ctx context.Context, settings config.Config, logger *slog.Logger) (http.Handler, func(), error) {
	privateKey, err := readBoundedFile(settings.GitHub.PrivateKeyFile, maxPrivateKeyBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("read GitHub private key: %w", err)
	}
	webhookSecret, err := readBoundedFile(settings.GitHub.WebhookSecretFile, maxWebhookKeyBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("read GitHub webhook secret: %w", err)
	}
	var caPEM []byte
	if settings.GitHub.CAFile != "" {
		caPEM, err = readBoundedFile(settings.GitHub.CAFile, maxCABytes)
		if err != nil {
			return nil, nil, fmt.Errorf("read GitHub CA: %w", err)
		}
	}
	endpoints, err := githubEndpoints(settings.GitHub)
	if err != nil {
		return nil, nil, err
	}
	httpClient, err := githubapp.NewHTTPClient(caPEM, endpoints, 10*time.Second)
	if err != nil {
		return nil, nil, err
	}
	signer, err := githubapp.NewSigner(settings.GitHub.AppID, privateKey, nil)
	if err != nil {
		return nil, nil, err
	}
	pool, err := postgres.Open(ctx, settings.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (http.Handler, func(), error) {
		pool.Close()
		return nil, nil, err
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		return fail(err)
	}
	metrics := observability.New()
	store := postgres.New(pool)
	if err := store.UpsertSearchNode(ctx, searchNodeID, settings.ZoektURL); err != nil {
		return fail(err)
	}
	githubClient := githubapp.NewClient(endpoints, httpClient, signer, settings.GitHub.APIVersion, maxGitHubResponseBytes, nil, metrics)
	reconciler := githubapp.NewReconciler(githubClient, store)
	loopCtx, cancel := context.WithCancel(ctx)
	done, err := startPeriodic(loopCtx, reconcileInterval, reconciler.All, func(ctx context.Context) error {
		return refreshQueueDepths(ctx, store, metrics)
	}, func(error) { logger.Error("durable background refresh failed") })
	if err != nil {
		cancel()
		return fail(err)
	}
	reconcileRequests := make(chan int64, 64)
	reconcileDone := startReconcileRequests(loopCtx, reconcileRequests, reconciler.Installation, func(error) {
		logger.Error("webhook reconciliation failed")
	})
	backend, err := zoekt.New(settings.ZoektURL, http.DefaultClient, settings.Limits.MaxResponseBytes, metrics)
	if err != nil {
		cancel()
		<-done
		<-reconcileDone
		return fail(err)
	}
	authenticator := authn.NewStatic(map[string]authn.Principal{
		settings.UserToken:  {Subject: "user", Method: "bearer", InstallationID: settings.UserInstallationID, RepositoryIDs: settings.UserRepositoryIDs},
		settings.AdminToken: {Subject: "admin", Method: "bearer", Administrator: true, InstallationID: settings.AdminInstallationID, RepositoryIDs: settings.AdminRepositoryIDs},
	})
	searchService := search.NewService(backend, authz.NewPostgres(store), searchLimits(settings))
	repositoryService := &repository.Service{Store: store, GitHub: githubClient}
	processor := webhook.NewGitHubProcessor(store, reconcileRequests, metrics)
	handler := newAPIHandler(settings, metrics, authenticator, searchService, repositoryService, webhookSecret, processor, durableReadiness{pool: pool, zoekt: backend})
	return handler, func() {
		cancel()
		<-done
		<-reconcileDone
		pool.Close()
	}, nil
}

func newAPIHandler(settings config.Config, metrics *observability.Metrics, authenticator authn.Authenticator, service *search.Service, repositories *repository.Service, webhookSecret []byte, processor webhook.Processor, checker httpapi.ReadyChecker) http.Handler {
	mux := http.NewServeMux()
	httpapi.RegisterSystem(mux, checker, metrics.Handler())
	httpapi.RegisterSearch(mux, authenticator, service, settings.Limits.MaxRequestBytes)
	if repositories != nil {
		httpapi.RegisterRepositories(mux, authenticator, repositories, settings.Limits.MaxRequestBytes)
	}
	if processor != nil {
		httpapi.RegisterGitHubWebhook(mux, webhookSecret, 1<<20, processor)
	}
	mcpServer := mcpserver.NewWithLimits(service, repositories, mcpserver.Limits{
		MaxItems: settings.Limits.MaxResults, MaxOutputBytes: settings.Limits.MaxResponseBytes,
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, nil)
	mux.Handle("/mcp", httpapi.AuthenticateBearer(authenticator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request.Body = http.MaxBytesReader(writer, request.Body, settings.Limits.MaxRequestBytes)
		mcpHandler.ServeHTTP(writer, request)
	})))
	return metrics.WrapHTTP(mux)
}

func searchLimits(settings config.Config) search.Limits {
	return search.Limits{
		DefaultResults: settings.Limits.DefaultResults, MaxResults: settings.Limits.MaxResults,
		DefaultContextLines: settings.Limits.DefaultContextLines, MaxContextLines: settings.Limits.MaxContextLines,
		DefaultTimeout: settings.Limits.DefaultTimeout, MaxTimeout: settings.Limits.MaxTimeout,
		MaxResponseBytes: settings.Limits.MaxResponseBytes,
	}
}

func readBoundedFile(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("file exceeds size limit")
	}
	if len(data) == 0 {
		return nil, errors.New("file is empty")
	}
	return data, nil
}

func startPeriodic(ctx context.Context, interval time.Duration, reconcile, refresh func(context.Context) error, onError func(error)) (<-chan struct{}, error) {
	if err := reconcile(ctx); err != nil {
		return nil, err
	}
	if err := refresh(ctx); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, operation := range []func(context.Context) error{reconcile, refresh} {
					if err := operation(ctx); err != nil && onError != nil {
						onError(err)
					}
				}
			}
		}
	}()
	return done, nil
}

func startReconcileRequests(ctx context.Context, requests <-chan int64, reconcile func(context.Context, int64) error, onError func(error)) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case installationID := <-requests:
				if err := reconcile(ctx, installationID); err != nil {
					onError(err)
				}
			}
		}
	}()
	return done
}

func refreshQueueDepths(ctx context.Context, store *postgres.Store, metrics *observability.Metrics) error {
	depths, err := store.QueueDepths(ctx)
	if err != nil {
		return err
	}
	for _, state := range []string{"queued", "running", "succeeded", "failed", "superseded"} {
		metrics.SetQueueDepth(state, depths[state])
	}
	return nil
}

func githubEndpoints(settings config.GitHub) (githubapp.Endpoints, error) {
	values := []string{settings.WebURL, settings.APIURL, settings.UploadURL, settings.GitURL}
	parsed := make([]*url.URL, len(values))
	for index, value := range values {
		var err error
		if parsed[index], err = url.Parse(value); err != nil {
			return githubapp.Endpoints{}, errors.New("invalid GitHub endpoint")
		}
	}
	return githubapp.Endpoints{Web: parsed[0], API: parsed[1], Upload: parsed[2], Git: parsed[3]}, nil
}

type durableReadiness struct {
	pool  interface{ Ping(context.Context) error }
	zoekt httpapi.ReadyChecker
}

func (checker durableReadiness) Health(ctx context.Context) error {
	if err := checker.pool.Ping(ctx); err != nil {
		return err
	}
	return checker.zoekt.Health(ctx)
}
