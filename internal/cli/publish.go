package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/balcsida/graphnest/internal/client"
	"github.com/balcsida/graphnest/pkg/api"
)

const publishAttempts = 3

// publishRequest is one artifact to publish to a repository.
type publishRequest struct {
	RepositoryID       int64
	Commit             string
	Producer           string // the artifact's producer name
	Data               []byte
	ExpectedGeneration *int64 // nil: use the generation observed in the preflight
	ReplaceProducer    bool
	Timeout            time.Duration // per request
}

// serverReport is what the preflight and the final status read observed.
type serverReport struct {
	IndexedSHA string                     `json:"indexed_sha"`
	Permitted  bool                       `json:"permitted"`
	Before     *api.GraphActiveGeneration `json:"active_generation_before"`
	After      *api.GraphActiveGeneration `json:"active_generation_after"`
}

// publicationReport describes the upload that took effect.
type publicationReport struct {
	ExpectedGeneration int64                      `json:"expected_generation"`
	ReplaceProducer    bool                       `json:"replace_producer"`
	Attempts           int                        `json:"attempts"`
	Result             api.GraphPublicationResult `json:"result"`
}

const grantHint = "an administrator can grant it with PUT /v1/graph/publication-grants"

// publishArtifact runs the preflight, uploads with retries and reads the status again.
// It prints nothing on stdout; a refusal or failure is returned as an error.
func publishArtifact(ctx context.Context, env Environment, stderr io.Writer, req publishRequest) (*serverReport, *publicationReport, error) {
	config, err := client.FromEnv(env.Getenv, env.ReadFile)
	if err != nil {
		return nil, nil, err
	}
	config.Timeout = req.Timeout
	server, err := client.New(config)
	if err != nil {
		return nil, nil, err
	}
	summary, err := server.Repository(ctx, req.RepositoryID)
	if err != nil {
		return nil, nil, describeServerError(err)
	}
	status, err := server.GraphStatus(ctx, req.RepositoryID)
	if err != nil {
		return nil, nil, describeServerError(err)
	}
	var active *api.GraphActiveGeneration
	permitted := status.Publication != nil && status.Publication.Permitted
	if status.Publication != nil {
		active = status.Publication.ActiveGeneration
	}
	var observed int64
	if active != nil {
		observed = active.ID
	}
	switch {
	case summary.IndexedSHA != req.Commit:
		return nil, nil, fmt.Errorf("the server indexes %s but the artifact is for %s; run CodeGraph on a checkout of %s and import again", summary.IndexedSHA, req.Commit, summary.IndexedSHA)
	case !permitted:
		return nil, nil, fmt.Errorf("this token may not publish to repository %d; %s", req.RepositoryID, grantHint)
	case req.ExpectedGeneration != nil && *req.ExpectedGeneration != observed:
		return nil, nil, fmt.Errorf("the active published generation is %d, not %d; read the status again before publishing", observed, *req.ExpectedGeneration)
	case active != nil && active.Producer != req.Producer && !req.ReplaceProducer:
		return nil, nil, fmt.Errorf("the active generation was published by %s; pass --replace-producer to replace it", active.Producer)
	}

	sleep := env.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	var result api.GraphPublicationResult
	attempts := 0
	for {
		attempts++
		result, err = server.Publish(ctx, req.RepositoryID, req.Commit, observed, req.ReplaceProducer, req.Data)
		if err == nil || attempts == publishAttempts || !retryable(ctx, err) {
			break
		}
		if err := sleep(ctx, time.Duration(attempts)*time.Second); err != nil {
			return nil, nil, err
		}
	}
	if err != nil {
		return nil, nil, describePublishError(req.RepositoryID, err)
	}

	report := &serverReport{IndexedSHA: summary.IndexedSHA, Permitted: permitted, Before: active}
	if after, err := server.GraphStatus(ctx, req.RepositoryID); err != nil {
		// The upload took effect; null marks the generation as unknown.
		fmt.Fprintln(stderr, "warning: published, but the status could not be read afterwards:", describeServerError(err))
	} else if after.Publication != nil {
		report.After = after.Publication.ActiveGeneration
	}
	return report, &publicationReport{ExpectedGeneration: observed, ReplaceProducer: req.ReplaceProducer, Attempts: attempts, Result: result}, nil
}

// retryable reports whether an upload error is transient: a transport failure or a server-side one, never a 4xx.
func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var serverErr *client.Error
	if errors.As(err, &serverErr) {
		return serverErr.Status >= 500 || serverErr.Retryable
	}
	return true
}

func describePublishError(repositoryID int64, err error) error {
	var serverErr *client.Error
	if !errors.As(err, &serverErr) {
		return err
	}
	switch serverErr.Code {
	case "generation_conflict":
		return errors.New("another publication or a new indexed commit landed since the preflight; read the status and retry")
	case "producer_conflict":
		return errors.New("the active generation belongs to another producer; pass --replace-producer")
	case "not_indexed":
		return errors.New("the indexed commit changed; run CodeGraph on the new commit and import again")
	case "forbidden":
		return fmt.Errorf("this token may not publish to repository %s; %s", strconv.FormatInt(repositoryID, 10), grantHint)
	}
	return describeServerError(err)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
