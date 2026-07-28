//go:build unix

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/graphcommand"
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
	return graphcommand.RunStandalone(ctx, settings, logger)
}
