//go:build unix

package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/internal/search"
	"github.com/balcsida/graphnest/internal/zoekt"
)

type ZoektIndexer struct {
	Binary, IndexDir  string
	Runner            Runner
	Client            *zoekt.Client
	IndexTimeout      time.Duration
	VisibilityTimeout time.Duration
}

func (indexer *ZoektIndexer) Index(ctx context.Context, repo repository.Repository, source string) (resultErr error) {
	if indexer == nil || indexer.Binary == "" || indexer.IndexDir == "" || source == "" || repo.ZoektID == 0 || repo.Name == "" || repo.WebURL == "" || repo.Branch == "" || !validSHA(repo.DesiredSHA) {
		return errors.New("invalid Zoekt indexing job")
	}
	// Scratch space lives inside the index directory: it is the only writable
	// durable path when the root filesystem is read-only, and zoekt-index's
	// final rename stays on one filesystem. Zoekt only loads *.zoekt shards, so
	// the hidden directory is never mistaken for one.
	if err := os.MkdirAll(indexer.IndexDir, 0o750); err != nil {
		return err
	}
	temporaryDirectory, err := os.MkdirTemp(indexer.IndexDir, ".tmp-")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(temporaryDirectory)) }()
	metadata, err := os.CreateTemp(temporaryDirectory, "metadata-*.json")
	if err != nil {
		return err
	}
	metadataPath := metadata.Name()
	description := struct {
		ID       uint32
		Name     string
		URL      string
		Metadata map[string]string
		Branches []struct{ Name, Version string }
	}{ID: repo.ZoektID, Name: repo.Name, URL: repo.WebURL, Metadata: map[string]string{"graphnest_repository_id": strconv.FormatUint(uint64(repo.ZoektID), 10)}, Branches: []struct{ Name, Version string }{{repo.Branch, repo.DesiredSHA}}}
	if err := json.NewEncoder(metadata).Encode(description); err != nil {
		_ = metadata.Close()
		return err
	}
	if err := metadata.Close(); err != nil {
		return err
	}
	arguments := []string{"-index", indexer.IndexDir, "-meta", metadataPath, "-file_limit", "2097152", "-parallelism", "1", "-disable_ctags", source}
	environment := []string{"LANG=C", "LC_ALL=C", "PATH=" + os.Getenv("PATH"), "TMPDIR=" + temporaryDirectory}
	timeout := indexer.IndexTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	indexCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return indexer.Runner.Run(indexCtx, indexer.Binary, arguments, environment, "")
}

func (indexer *ZoektIndexer) WaitVisible(ctx context.Context, repositoryID uint32, branch, version string) error {
	if indexer == nil || indexer.Client == nil || repositoryID == 0 || branch == "" || version == "" {
		return errors.New("invalid Zoekt visibility check")
	}
	timeout := indexer.VisibilityTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	delay := 10 * time.Millisecond
	for {
		repositories, err := indexer.Client.List(ctx, repositoryID)
		if err != nil {
			return err
		}
		for _, repo := range repositories {
			if repo.RepoID == repositoryID && repo.Branch == branch && repo.Version == version {
				_, err := indexer.Client.Search(ctx, search.BackendRequest{
					Query: "repoid:" + strconv.FormatUint(uint64(repositoryID), 10), RepositoryIDs: []uint32{repositoryID}, Limit: 1, Timeout: time.Second,
				})
				return err
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < 250*time.Millisecond {
			delay *= 2
		}
	}
}
