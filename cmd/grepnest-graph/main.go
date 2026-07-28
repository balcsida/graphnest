//go:build unix

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/graphquery"
	"github.com/grepnest/grepnest/internal/graphruntime"
	"github.com/grepnest/grepnest/internal/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	settings, err := config.LoadGraph()
	if err == nil {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		err = run(ctx, settings, logger)
	}
	if err != nil {
		logger.Error("graph runtime stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, settings config.Graph, logger *slog.Logger) error {
	pool, err := postgres.Open(ctx, settings.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return err
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		return err
	}
	runtime, err := graphruntime.New(ctx, graphruntime.Config{
		DatabasePath:  filepath.Join(settings.DataDir, "grepnest.lbug"),
		ListenAddress: settings.ListenAddress, InternalSecret: settings.InternalSecret,
		ReadConnections: settings.ReadConnections, SyncInterval: settings.SyncInterval,
		QueryTimeout: settings.QueryTimeout, InterruptGrace: settings.InterruptGrace,
		QueryLimits: graphquery.Limits{
			PerCategory: settings.QueryLimits.PerCategory, MaxDepth: settings.QueryLimits.MaxDepth,
			MaxTraceDepth: settings.QueryLimits.MaxTraceDepth, MaxNodes: settings.QueryLimits.MaxNodes,
			MaxEdges: settings.QueryLimits.MaxEdges, MaxFanout: settings.QueryLimits.MaxFanout,
		},
	}, postgres.New(pool), logger)
	if err != nil {
		return err
	}
	defer runtime.Close()
	return runtime.Run(ctx)
}
