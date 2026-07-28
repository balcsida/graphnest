package graphruntime

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/grepnest/grepnest/internal/graphquery"
	"github.com/grepnest/grepnest/internal/graphtransport"
	"github.com/grepnest/grepnest/internal/ladybug"
	"github.com/grepnest/grepnest/internal/observability"
)

type Config struct {
	DatabasePath, ListenAddress                string
	InternalSecret                             []byte
	ReadConnections                            int
	SyncInterval, QueryTimeout, InterruptGrace time.Duration
	QueryLimits                                graphquery.Limits
}

type Runtime struct {
	handler           http.Handler
	syncOnce, runSync func(context.Context) error
	serve             func(context.Context) error
	closeDB           func() error
	runMu             sync.RWMutex
	closeOnce         sync.Once
	closeErr          error
	metrics           *observability.Metrics
}

func New(ctx context.Context, config Config, source ladybug.SnapshotSource, logger *slog.Logger) (*Runtime, error) {
	if source == nil {
		return nil, errors.New("graph snapshot source is required")
	}
	if config.ListenAddress == "" {
		return nil, errors.New("graph listen address is required")
	}
	if config.SyncInterval <= 0 {
		return nil, errors.New("graph sync interval must be positive")
	}
	if err := os.MkdirAll(filepath.Dir(config.DatabasePath), 0o700); err != nil {
		return nil, err
	}
	database, err := ladybug.Open(ladybug.Options{
		Path: config.DatabasePath, ReadConnections: config.ReadConnections,
		QueryTimeout: config.QueryTimeout, InterruptGrace: config.InterruptGrace,
	})
	if err != nil {
		return nil, err
	}
	if err := database.EnsureSchema(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	syncer := &ladybug.Syncer{Source: source, Database: database, Interval: config.SyncInterval}
	service := &graphquery.Service{Database: database, Limits: config.QueryLimits}
	metrics := observability.New()
	queryHandler := graphtransport.NewHandler(config.InternalSecret, service, graphtransport.Limits{
		RequestTimeout: config.QueryTimeout,
	})
	handler := instrument(queryHandler, metrics)
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	mux.Handle("/", handler)
	runtime := &Runtime{
		handler: handler, syncOnce: syncer.SyncOnce, closeDB: database.Close, metrics: metrics,
		runSync: func(ctx context.Context) error {
			ticker := time.NewTicker(config.SyncInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-ticker.C:
					started := time.Now()
					if err := syncer.SyncOnce(ctx); err != nil {
						metrics.ObserveGraphSync("error", time.Since(started))
						logger.Error("graph synchronization failed", "error", err)
					} else {
						metrics.ObserveGraphSync("success", time.Since(started))
					}
				}
			}
		},
	}
	runtime.serve = func(ctx context.Context) error {
		listener, err := net.Listen("tcp", config.ListenAddress)
		if err != nil {
			return err
		}
		server := &http.Server{
			Handler: mux, ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout: 10 * time.Second, IdleTimeout: time.Minute,
		}
		done := make(chan error, 1)
		go func() { done <- server.Serve(listener) }()
		select {
		case err := <-done:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			shutdownErr := server.Shutdown(shutdownCtx)
			if shutdownErr != nil {
				_ = server.Close()
			}
			serveErr := <-done
			if errors.Is(serveErr, http.ErrServerClosed) {
				serveErr = nil
			}
			return errors.Join(shutdownErr, serveErr)
		}
	}
	return runtime, nil
}

func (runtime *Runtime) Handler() http.Handler { return runtime.handler }

func (runtime *Runtime) Run(ctx context.Context) error {
	runtime.runMu.RLock()
	defer runtime.runMu.RUnlock()
	started := time.Now()
	if err := runtime.syncOnce(ctx); err != nil {
		if runtime.metrics != nil {
			runtime.metrics.ObserveGraphSync("error", time.Since(started))
			runtime.metrics.SetGraphReady(false)
		}
		return err
	}
	if runtime.metrics != nil {
		runtime.metrics.ObserveGraphSync("success", time.Since(started))
		runtime.metrics.SetGraphReady(true)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, 2)
	go func() { results <- runtime.serve(runCtx) }()
	go func() { results <- runtime.runSync(runCtx) }()
	first := <-results
	cancel()
	second := <-results
	return errors.Join(cleanCancellation(first, ctx), cleanCancellation(second, ctx))
}

func cleanCancellation(err error, parent context.Context) error {
	if errors.Is(err, context.Canceled) && parent.Err() != nil {
		return nil
	}
	return err
}

func (runtime *Runtime) Close() error {
	runtime.closeOnce.Do(func() {
		runtime.runMu.Lock()
		defer runtime.runMu.Unlock()
		if runtime.closeDB != nil {
			runtime.closeErr = runtime.closeDB()
		}
		if runtime.metrics != nil {
			runtime.metrics.SetGraphReady(false)
		}
	})
	return runtime.closeErr
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (writer *statusWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusWriter) Write(body []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	return writer.ResponseWriter.Write(body)
}

func instrument(next http.Handler, metrics *observability.Metrics) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		recorded := &statusWriter{ResponseWriter: writer}
		next.ServeHTTP(recorded, request)
		if operation := operation(request.URL.Path); operation != "" {
			result := "success"
			if recorded.status >= http.StatusBadRequest {
				result = "error"
			}
			metrics.ObserveGraphQuery(operation, result, time.Since(started))
		}
		if request.URL.Path == "/readyz" {
			metrics.SetGraphReady(recorded.status < http.StatusBadRequest)
		}
	})
}

func operation(path string) string {
	switch path {
	case "/internal/v1/graph/context":
		return "context"
	case "/internal/v1/graph/impact":
		return "impact"
	case "/internal/v1/graph/trace":
		return "trace"
	case "/internal/v1/graph/cypher":
		return "cypher"
	default:
		return ""
	}
}
