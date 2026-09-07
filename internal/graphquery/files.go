package graphquery

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/balcsida/graphnest/internal/graphprotocol"
)

type FileStore interface {
	QueryFiles(context.Context, FileQuery) ([]graphprotocol.IndexedFile, error)
}

type FileQuery struct {
	Snapshots          []QuerySnapshot
	Path               *string
	Directory, Pattern string
	Offset, Limit      int
}

// NormalizeFilePath accepts repository-relative paths, including ./ and redundant
// separators. Parent traversal, absolute paths and platform separators are refused.
func NormalizeFilePath(value string) (string, error) {
	if len(value) > 16384 || !utf8.ValidString(value) || strings.ContainsAny(value, "\\\x00") || path.IsAbs(value) {
		return "", ErrInvalidRequest
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", ErrInvalidRequest
		}
	}
	return path.Clean(value), nil
}

func (service *Service) IndexedFiles(ctx context.Context, req graphprotocol.FilesRequest) (graphprotocol.FilesResponse, error) {
	if service == nil || req.Limit < 0 || req.Limit > 100 || len(req.Cursor) > 512 || len(req.Glob) > 1024 || !utf8.ValidString(req.Glob) || strings.ContainsAny(req.Glob, "\x00\\") {
		return graphprotocol.FilesResponse{}, ErrInvalidRequest
	}
	directory, err := NormalizeFilePath(req.Directory)
	if err != nil {
		return graphprotocol.FilesResponse{}, err
	}
	if directory == "." {
		directory = ""
	}
	if req.Path != nil {
		p, err := NormalizeFilePath(*req.Path)
		if err != nil || p == "." {
			return graphprotocol.FilesResponse{}, ErrInvalidRequest
		}
		req.Path = &p
	}
	pattern := ""
	if req.Glob != "" {
		glob, err := NormalizeFilePath(req.Glob)
		if err != nil {
			return graphprotocol.FilesResponse{}, err
		}
		patterns, _ := PathPatterns([]string{"/" + glob})
		if len(patterns) != 1 {
			return graphprotocol.FilesResponse{}, ErrInvalidRequest
		}
		pattern = patterns[0]
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, err := service.readyEntities(ctx, req.Scope)
	if err != nil {
		return graphprotocol.FilesResponse{}, err
	}
	store, ok := ready.store.(FileStore)
	if !ok {
		return graphprotocol.FilesResponse{}, ErrInvalidRequest
	}
	limit := req.Limit
	maximum := min(100, service.limits().MaxRows)
	if limit == 0 || limit > maximum {
		limit = maximum
	}
	encoded, _ := json.Marshal(struct {
		Snapshots          []QuerySnapshot
		Selected           int64
		Path               *string
		Directory, Pattern string
		Limit              int
	}{ready.snapshots, req.Scope.SelectedRepositoryID, req.Path, directory, pattern, limit})
	fingerprint := sha256.Sum256(encoded)
	offset := 0
	if req.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(req.Cursor)
		var cursor entityCursor
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Offset <= 0 || cursor.Offset > 10_000_000 {
			return graphprotocol.FilesResponse{}, ErrInvalidRequest
		}
		if cursor.Fingerprint != fingerprint {
			return graphprotocol.FilesResponse{}, ErrGenerationChanged
		}
		offset = cursor.Offset
	}
	files, err := store.QueryFiles(ctx, FileQuery{Snapshots: ready.selected, Path: req.Path, Directory: directory, Pattern: pattern, Offset: offset, Limit: limit + 1})
	if err != nil {
		return graphprotocol.FilesResponse{}, err
	}
	selected := map[int64]bool{}
	for _, snapshot := range ready.selected {
		selected[snapshot.RepositoryID] = true
	}
	for i := range files {
		if !selected[files[i].RepositoryID] || files[i].Fact == nil {
			return graphprotocol.FilesResponse{}, ErrGenerationChanged
		}
		files[i].RepositoryID = ready.publicIDs[files[i].RepositoryID]
	}
	result := graphprotocol.FilesResponse{Files: files, Generations: ready.publicGenerations()}
	if len(files) > limit {
		result.Files = files[:limit]
		data, _ := json.Marshal(entityCursor{fingerprint, offset + limit})
		result.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	if err := entityResponseSize(result); err != nil {
		return graphprotocol.FilesResponse{}, err
	}
	if err := ready.current(ctx); err != nil {
		return graphprotocol.FilesResponse{}, err
	}
	return result, nil
}

// ValidateGenerations closes a composition after source I/O. The scope must still
// be authorized by the caller; generation IDs are public GitHub repository IDs.
func (service *Service) ValidateGenerations(ctx context.Context, scope graphprotocol.Scope, expected []graphprotocol.Generation) error {
	if service == nil {
		return ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, err := service.readyEntities(ctx, scope)
	if err != nil {
		return err
	}
	current := ready.publicGenerations()
	if len(current) != len(expected) {
		return ErrGenerationChanged
	}
	for i, g := range current {
		if g.RepositoryID != expected[i].RepositoryID || g.UploadID != expected[i].UploadID || g.Commit != expected[i].Commit {
			return ErrGenerationChanged
		}
	}
	return ready.current(ctx)
}
