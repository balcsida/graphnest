package ladybug

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"time"

	"github.com/grepnest/grepnest/internal/graphartifact"
)

type SnapshotSource interface {
	GraphManifests(context.Context) ([]graphartifact.Manifest, error)
	GraphArtifact(context.Context, int64, int64) (graphartifact.Artifact, error)
}

type Syncer struct {
	Source   SnapshotSource
	Database *Database
	Interval time.Duration
}

func (syncer *Syncer) SyncOnce(ctx context.Context) error {
	if syncer.Source == nil || syncer.Database == nil {
		return errors.New("ladybug synchronizer requires a source and database")
	}
	manifests, err := syncer.Source.GraphManifests(ctx)
	if err != nil {
		return err
	}
	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].RepositoryID < manifests[j].RepositoryID
	})
	for i := 1; i < len(manifests); i++ {
		if manifests[i-1].RepositoryID == manifests[i].RepositoryID {
			return errors.New("ladybug source contains duplicate repository manifests")
		}
	}
	current, err := syncer.Database.Manifests(ctx)
	if err != nil {
		return err
	}
	authoritative := make(map[int64]struct{}, len(manifests))
	for _, manifest := range manifests {
		authoritative[manifest.RepositoryID] = struct{}{}
		if sameManifest(current[manifest.RepositoryID], manifest) {
			continue
		}
		artifact, err := syncer.Source.GraphArtifact(ctx, manifest.RepositoryID, manifest.UploadID)
		if err != nil {
			return err
		}
		if err := syncer.Database.ReplaceRepository(ctx, manifest, artifact); err != nil {
			return err
		}
	}
	var absent []int64
	for repositoryID := range current {
		if _, ok := authoritative[repositoryID]; !ok {
			absent = append(absent, repositoryID)
		}
	}
	sort.Slice(absent, func(i, j int) bool { return absent[i] < absent[j] })
	for _, repositoryID := range absent {
		if err := syncer.Database.DeleteRepository(ctx, repositoryID); err != nil {
			return err
		}
	}
	return nil
}

func (syncer *Syncer) Run(ctx context.Context) error {
	if syncer.Interval <= 0 {
		return errors.New("ladybug synchronization interval must be positive")
	}
	if err := syncer.SyncOnce(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(syncer.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := syncer.SyncOnce(ctx); err != nil {
				return err
			}
		}
	}
}

func sameManifest(left, right graphartifact.Manifest) bool {
	return left.RepositoryID == right.RepositoryID &&
		left.UploadID == right.UploadID &&
		left.Commit == right.Commit &&
		left.Source == right.Source &&
		left.SchemaVersion == right.SchemaVersion &&
		bytes.Equal(left.ContentHash, right.ContentHash)
}
