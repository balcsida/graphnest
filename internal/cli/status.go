package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/balcsida/graphnest/internal/client"
	"github.com/balcsida/graphnest/pkg/api"
)

// statusReport is the JSON document printed by graph status.
type statusReport struct {
	Repository api.RepositorySummary `json:"repository"`
	Graph      api.GraphStatus       `json:"graph"`
}

func runGraphStatus(ctx context.Context, args []string, env Environment, stdout, stderr io.Writer) error {
	flags := newFlags("status", "graphnest graph status --repository-id N [--timeout D]", stderr)
	repositoryID := flags.Int64("repository-id", 0, "GraphNest repository ID")
	timeout := flags.Duration("timeout", defaultTimeout, "time limit for the server requests")
	if err := parse(flags, args); err != nil {
		return err
	}
	if *repositoryID <= 0 {
		return usageError{"--repository-id must be a positive integer"}
	}
	if *timeout <= 0 {
		return usageError{"--timeout must be positive"}
	}
	config, err := client.FromEnv(env.Getenv, env.ReadFile, storedLogins(env))
	if err != nil {
		return err
	}
	config.Timeout = *timeout
	server, err := client.New(config)
	if err != nil {
		return err
	}
	var report statusReport
	if report.Repository, err = server.Repository(ctx, *repositoryID); err != nil {
		return describeServerError(err)
	}
	if report.Graph, err = server.GraphStatus(ctx, *repositoryID); err != nil {
		return describeServerError(err)
	}
	return writeJSON(stdout, report)
}

func describeServerError(err error) error {
	var serverErr *client.Error
	if !errors.As(err, &serverErr) {
		return err
	}
	if serverErr.Code == "" {
		if serverErr.Status == http.StatusUnauthorized {
			return fmt.Errorf("%w; check the token or run graphnest login", err)
		}
		return err
	}
	text := fmt.Sprintf("server error: code=%s message=%q", serverErr.Code, serverErr.Message)
	if serverErr.Retryable {
		text += " (retryable)"
	}
	if serverErr.Status == http.StatusUnauthorized {
		text += "; check the token or run graphnest login"
	}
	return errors.New(text)
}
