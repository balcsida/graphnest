//go:build unix

package enrichment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/indexer"
	"github.com/balcsida/graphnest/internal/repository"
)

type Process interface {
	Output(context.Context, string, []string, []string, string) ([]byte, error)
}

type Runner struct {
	Binary  string
	Process Process
	Timeout time.Duration
}

type Status = indexer.EnrichmentStatus

func (runner Runner) Enrich(ctx context.Context, snapshot indexer.Snapshot, repo repository.Repository, targetSHA string) (Status, error) {
	if runner.Binary == "" || runner.Process == nil || runner.Timeout <= 0 || snapshot.Root == "" ||
		snapshot.RepositoryID != repo.ID || snapshot.CommitSHA != targetSHA {
		return Status{}, errors.New("invalid enrichment request")
	}
	commandCtx, cancel := context.WithTimeout(ctx, runner.Timeout)
	defer cancel()
	output, err := runner.Process.Output(commandCtx, runner.Binary, []string{
		"enrich", "--protocol=1", "--root=" + snapshot.Root,
		"--repository-id=" + strconv.FormatInt(repo.ID, 10), "--commit=" + targetSHA,
	}, []string{"LANG=C", "LC_ALL=C"}, "")
	if err != nil {
		if commandCtx.Err() != nil {
			return Status{}, commandCtx.Err()
		}
		return Status{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	var response Response
	if err := decoder.Decode(&response); err != nil {
		return Status{}, fmt.Errorf("decode enrichment output: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Status{}, errors.New("decode enrichment output: trailing data")
	}
	if response.Version != ProtocolVersion || response.Artifact.RepositoryID != repo.ID || response.Artifact.Commit != targetSHA ||
		graphartifact.Validate(response.Artifact, graphartifact.Limits{}) != nil {
		return Status{}, errors.New("invalid enrichment output")
	}
	return Status{Artifact: &response.Artifact}, nil
}
