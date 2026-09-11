package graphquery

import (
	"context"
	"regexp"

	"github.com/balcsida/graphnest/internal/graphprotocol"
)

const maxFileClassificationPaths = 64

var generatedFilename = regexp.MustCompile(`(\.pb\.go|\.pulsar\.go|_grpc\.pb\.go|_mock\.go|_mocks\.go|\.generated\.[jt]sx?|\.gen\.[jt]sx?|\.pb\.[jt]s|_pb\.[jt]s|_grpc_pb\.[jt]s|\.min\.m?js|_pb2(_grpc)?\.py|_pb2\.pyi|\.pb\.(cc|h)|\.g\.cs|Grpc\.cs|OuterClass\.java|Grpc\.java|\.pb\.swift|\.g\.dart|\.freezed\.dart|\.pb\.dart|\.pbgrpc\.dart|\.chopper\.dart|\.generated\.rs)$|^mock_[^/]+\.go$`)

type FileClassificationQuery struct {
	Snapshots []QuerySnapshot
	Paths     []string
}

type FileClassificationRow struct {
	RepositoryID       int64
	Path               string
	Present            bool
	PersistedGenerated *bool
	Ambient            bool
}

type FileClassificationStore interface {
	ClassifyFiles(context.Context, FileClassificationQuery) ([]FileClassificationRow, error)
	CountGeneratedFiles(context.Context, []QuerySnapshot) (generated, total int64, err error)
}

func GeneratedFilename(path string) bool {
	return generatedFilename.MatchString(path)
}

func (service *Service) FileClassifications(ctx context.Context, request graphprotocol.FileClassificationRequest) (graphprotocol.FileClassificationResponse, error) {
	if service == nil || request.Scope.SelectedRepositoryID == 0 || len(request.Paths) > maxFileClassificationPaths {
		return graphprotocol.FileClassificationResponse{}, ErrInvalidRequest
	}
	paths := make([]string, 0, len(request.Paths))
	seen := make(map[string]bool, len(request.Paths))
	for _, value := range request.Paths {
		path, err := NormalizeFilePath(value)
		if err != nil || path == "." {
			return graphprotocol.FileClassificationResponse{}, ErrInvalidRequest
		}
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, err := service.readyEntities(ctx, request.Scope)
	if err != nil {
		return graphprotocol.FileClassificationResponse{}, err
	}
	store, ok := ready.store.(FileClassificationStore)
	if !ok {
		return graphprotocol.FileClassificationResponse{}, ErrInvalidRequest
	}
	rows := []FileClassificationRow{}
	if len(paths) > 0 {
		rows, err = store.ClassifyFiles(ctx, FileClassificationQuery{Snapshots: ready.selected, Paths: paths})
		if err != nil {
			return graphprotocol.FileClassificationResponse{}, err
		}
	}
	if len(rows) != len(paths) {
		return graphprotocol.FileClassificationResponse{}, ErrGenerationChanged
	}
	byPath := make(map[string]FileClassificationRow, len(rows))
	for _, row := range rows {
		if row.RepositoryID != ready.selected[0].RepositoryID || !seen[row.Path] || byPath[row.Path].Path != "" || !row.Present && (row.PersistedGenerated != nil || row.Ambient) {
			return graphprotocol.FileClassificationResponse{}, ErrGenerationChanged
		}
		byPath[row.Path] = row
	}
	result := graphprotocol.FileClassificationResponse{Files: make([]graphprotocol.FileClassification, 0, len(paths)), Generations: ready.publicGenerations()}
	for _, path := range paths {
		row := byPath[path]
		file := graphprotocol.FileClassification{Path: path, Present: row.Present, PersistedGenerated: row.PersistedGenerated, Ambient: row.Ambient}
		file.Generated = GeneratedFilename(path) || row.PersistedGenerated != nil && *row.PersistedGenerated
		result.Files = append(result.Files, file)
	}
	if err = entityResponseSize(result); err != nil {
		return graphprotocol.FileClassificationResponse{}, err
	}
	if err = ready.current(ctx); err != nil {
		return graphprotocol.FileClassificationResponse{}, err
	}
	return result, nil
}

func (service *Service) GeneratedFileCount(ctx context.Context, request graphprotocol.GeneratedFileCountRequest) (graphprotocol.GeneratedFileCountResponse, error) {
	if service == nil || request.Scope.SelectedRepositoryID == 0 {
		return graphprotocol.GeneratedFileCountResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, err := service.readyEntities(ctx, request.Scope)
	if err != nil {
		return graphprotocol.GeneratedFileCountResponse{}, err
	}
	store, ok := ready.store.(FileClassificationStore)
	if !ok {
		return graphprotocol.GeneratedFileCountResponse{}, ErrInvalidRequest
	}
	generated, total, err := store.CountGeneratedFiles(ctx, ready.selected)
	if err != nil {
		return graphprotocol.GeneratedFileCountResponse{}, err
	}
	if generated < 0 || total < 0 || generated > total {
		return graphprotocol.GeneratedFileCountResponse{}, ErrGenerationChanged
	}
	result := graphprotocol.GeneratedFileCountResponse{GeneratedFiles: generated, TotalFiles: total, Generations: ready.publicGenerations()}
	if err = ready.current(ctx); err != nil {
		return graphprotocol.GeneratedFileCountResponse{}, err
	}
	return result, nil
}
