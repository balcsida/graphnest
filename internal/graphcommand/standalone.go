package graphcommand

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/graphquery"
	"github.com/grepnest/grepnest/internal/graphruntime"
	"github.com/grepnest/grepnest/internal/postgres"
)

func RunStandalone(ctx context.Context, settings config.Graph, logger *slog.Logger) error {
	pool, err := postgres.Open(ctx, settings.DatabaseURL)
	if err != nil {
		return err
	}
	var runtime *graphruntime.Runtime
	command := standaloneRuntime{
		ping:    pool.Ping,
		migrate: func(ctx context.Context) error { return postgres.Migrate(ctx, pool) },
		newGraph: func(ctx context.Context) error {
			var err error
			runtime, err = graphruntime.New(ctx, runtimeConfig(settings), postgres.New(pool), logger)
			return err
		},
		runGraph:    func(ctx context.Context) error { return runtime.Run(ctx) },
		closeGraph:  func() { _ = runtime.Close() },
		closeSource: pool.Close,
	}
	return command.run(ctx)
}

type standaloneRuntime struct {
	ping, migrate, newGraph, runGraph func(context.Context) error
	closeGraph, closeSource           func()
}

func (runtime standaloneRuntime) run(ctx context.Context) error {
	defer runtime.closeSource()
	for _, initialize := range []func(context.Context) error{runtime.ping, runtime.migrate, runtime.newGraph} {
		if err := initialize(ctx); err != nil {
			return err
		}
	}
	defer runtime.closeGraph()
	return runtime.runGraph(ctx)
}

func runtimeConfig(settings config.Graph) graphruntime.Config {
	return graphruntime.Config{
		DatabasePath:  filepath.Join(settings.DataDir, "grepnest.lbug"),
		ListenAddress: settings.ListenAddress, InternalSecret: settings.InternalSecret,
		ReadConnections: settings.ReadConnections, SyncInterval: settings.SyncInterval,
		QueryTimeout: settings.QueryTimeout, InterruptGrace: settings.InterruptGrace,
		QueryLimits: graphquery.Limits{
			PerCategory:        settings.QueryLimits.PerCategory,
			DefaultImpactDepth: settings.QueryLimits.DefaultImpactDepth,
			MaxDepth:           settings.QueryLimits.MaxDepth,
			DefaultTraceDepth:  settings.QueryLimits.DefaultTraceDepth,
			MaxTraceDepth:      settings.QueryLimits.MaxTraceDepth, MaxRows: settings.QueryLimits.MaxRows,
			MaxNodes: settings.QueryLimits.MaxNodes,
			MaxEdges: settings.QueryLimits.MaxEdges, MaxFanout: settings.QueryLimits.MaxFanout,
		},
	}
}
