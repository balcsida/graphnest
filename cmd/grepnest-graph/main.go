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
	"github.com/grepnest/grepnest/internal/secretstage"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if handled, err := runStageSecretCommand(os.Args[1:]); handled {
		if err != nil {
			logger.Error("graph secret staging failed")
			os.Exit(1)
		}
		return
	}
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

func runStageSecretCommand(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	if len(args) != 3 || args[0] != "stage-secret" {
		return true, secretstage.ErrUsage
	}
	return true, secretstage.Copy(args[1], args[2])
}

func run(ctx context.Context, settings config.Graph, logger *slog.Logger) error {
	return graphcommand.RunStandalone(ctx, settings, logger)
}
