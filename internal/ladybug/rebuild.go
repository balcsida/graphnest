package ladybug

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/grepnest/grepnest/internal/graphartifact"
)

func Rebuild(ctx context.Context, source SnapshotSource, options Options) error {
	if source == nil {
		return errors.New("ladybug rebuild source is required")
	}
	if options.Path == "" {
		return errors.New("ladybug database path is required")
	}
	if info, err := os.Lstat(options.Path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("ladybug database path must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := rejectWAL(options.Path); err != nil {
		return err
	}
	manifests, err := source.GraphManifests(ctx)
	if err != nil {
		return err
	}
	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].RepositoryID < manifests[j].RepositoryID
	})
	candidate, err := os.CreateTemp(filepath.Dir(options.Path), filepath.Base(options.Path)+".new-*")
	if err != nil {
		return err
	}
	candidatePath := candidate.Name()
	if err := candidate.Close(); err != nil {
		os.Remove(candidatePath)
		return err
	}
	defer os.Remove(candidatePath)
	defer os.Remove(candidatePath + ".wal")

	candidateOptions := options
	candidateOptions.Path = candidatePath
	db, err := Open(candidateOptions)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = db.Close()
		}
	}()
	if err := db.Update(ctx, func(session *Session) error {
		return EnsureSchema(ctx, session.connection)
	}); err != nil {
		return err
	}
	for _, manifest := range manifests {
		artifact, err := source.GraphArtifact(ctx, manifest.RepositoryID, manifest.UploadID)
		if err != nil {
			return err
		}
		if err := db.ReplaceRepository(ctx, manifest, artifact); err != nil {
			return err
		}
	}
	if err := verifyRebuild(ctx, db, manifests); err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	closed = true
	if err := rejectWAL(candidatePath); err != nil {
		return err
	}
	if err := rejectWAL(options.Path); err != nil {
		return err
	}
	if err := os.Rename(candidatePath, options.Path); err != nil {
		return err
	}
	return nil
}

func rejectWAL(path string) error {
	if _, err := os.Lstat(path + ".wal"); err == nil {
		return errors.New("ladybug database WAL exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func verifyRebuild(ctx context.Context, db *Database, want []graphartifact.Manifest) error {
	got, err := db.Manifests(ctx)
	if err != nil {
		return err
	}
	if len(got) != len(want) {
		return errors.New("ladybug rebuilt manifest count does not match source")
	}
	for _, manifest := range want {
		if !sameManifest(got[manifest.RepositoryID], manifest) {
			return fmt.Errorf("ladybug rebuilt manifest for repository %d does not match source", manifest.RepositoryID)
		}
	}
	var count int64
	if err := db.View(ctx, func(session *Session) error {
		result, err := session.Execute(ctx, `MATCH (r:Repository) RETURN count(r)`, nil, QueryLimits{})
		if err == nil {
			count = result.Rows[0][0].(int64)
		}
		return err
	}); err != nil {
		return err
	}
	if count != int64(len(want)) {
		return fmt.Errorf("ladybug rebuilt repository count = %d, want %d", count, len(want))
	}
	return nil
}
