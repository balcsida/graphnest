package graphservice

import (
	"context"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/pkg/api"
)

type FileClassificationsRequest struct {
	Repo   api.GraphRepositorySelector
	Branch string
	Paths  []string
}

type GeneratedFileCountRequest struct {
	Repo   api.GraphRepositorySelector
	Branch string
}

type fileClassificationBackend interface {
	FileClassifications(context.Context, graphprotocol.FileClassificationRequest) (graphprotocol.FileClassificationResponse, error)
	GeneratedFileCount(context.Context, graphprotocol.GeneratedFileCountRequest) (graphprotocol.GeneratedFileCountResponse, error)
}

func (s *Service) FileClassifications(ctx context.Context, p authn.Principal, request FileClassificationsRequest) (graphprotocol.FileClassificationResponse, error) {
	if len(request.Paths) > 64 {
		return graphprotocol.FileClassificationResponse{}, ErrInvalidRequest
	}
	paths := make(map[string]bool, len(request.Paths))
	for _, value := range request.Paths {
		path, err := graphquery.NormalizeFilePath(value)
		if err != nil || path == "." {
			return graphprotocol.FileClassificationResponse{}, ErrInvalidRequest
		}
		paths[path] = true
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	i, err := s.inspectionScope(ctx, p, request.Repo, request.Branch)
	if err != nil {
		return graphprotocol.FileClassificationResponse{}, err
	}
	backend, ok := s.Backend.(fileClassificationBackend)
	if !ok {
		return graphprotocol.FileClassificationResponse{}, ErrGraphNotReady
	}
	result, err := backend.FileClassifications(ctx, graphprotocol.FileClassificationRequest{Scope: i.scope, Paths: request.Paths})
	if err != nil {
		return graphprotocol.FileClassificationResponse{}, err
	}
	if err = i.generations(result.Generations); err != nil {
		return graphprotocol.FileClassificationResponse{}, err
	}
	seen := map[string]bool{}
	for _, file := range result.Files {
		if !paths[file.Path] || seen[file.Path] || !file.Present && (file.PersistedGenerated != nil || file.Ambient) {
			return graphprotocol.FileClassificationResponse{}, ErrGraphNotReady
		}
		seen[file.Path] = true
	}
	if len(seen) != len(paths) {
		return graphprotocol.FileClassificationResponse{}, ErrGraphNotReady
	}
	if err = s.finishInspection(ctx, p, i, result.Generations, result); err != nil {
		return graphprotocol.FileClassificationResponse{}, err
	}
	return result, nil
}

func (s *Service) GeneratedFileCount(ctx context.Context, p authn.Principal, request GeneratedFileCountRequest) (graphprotocol.GeneratedFileCountResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	i, err := s.inspectionScope(ctx, p, request.Repo, request.Branch)
	if err != nil {
		return graphprotocol.GeneratedFileCountResponse{}, err
	}
	backend, ok := s.Backend.(fileClassificationBackend)
	if !ok {
		return graphprotocol.GeneratedFileCountResponse{}, ErrGraphNotReady
	}
	result, err := backend.GeneratedFileCount(ctx, graphprotocol.GeneratedFileCountRequest{Scope: i.scope})
	if err != nil {
		return graphprotocol.GeneratedFileCountResponse{}, err
	}
	if result.GeneratedFiles < 0 || result.TotalFiles < result.GeneratedFiles || i.generations(result.Generations) != nil {
		return graphprotocol.GeneratedFileCountResponse{}, ErrGraphNotReady
	}
	if err = s.finishInspection(ctx, p, i, result.Generations, result); err != nil {
		return graphprotocol.GeneratedFileCountResponse{}, err
	}
	return result, nil
}
