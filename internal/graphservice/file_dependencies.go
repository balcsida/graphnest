package graphservice

import (
	"context"
	"math"
	"reflect"
	"slices"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/pkg/api"
)

type AffectedTestsRequest struct {
	Repo         api.GraphRepositorySelector
	Branch       string
	ChangedFiles []string
	MaxDepth     int
	TestGlob     string
	Limit        int
}

type FileDependencyRequest struct {
	Repo          api.GraphRepositorySelector
	Branch        string
	Paths         []string
	MinConfidence float64
	Limit         int
}

type fileDependencyQueryBackend interface {
	FileDependencyPairs(context.Context, graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error)
	FileDependencies(context.Context, graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error)
	FileDependents(context.Context, graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error)
	FileDependentCounts(context.Context, graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error)
	FileReachCounts(context.Context, graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error)
	CircularDependencies(context.Context, graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error)
}

type affectedTestsQueryBackend interface {
	AffectedTests(context.Context, graphprotocol.AffectedTestsRequest) (graphprotocol.AffectedTestsResponse, error)
}

func (s *Service) FileDependencyPairs(ctx context.Context, principal authn.Principal, request FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	return s.fileDependencyOperation(ctx, principal, request, "pairs")
}

func (s *Service) FileDependencies(ctx context.Context, principal authn.Principal, request FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	return s.fileDependencyOperation(ctx, principal, request, "dependencies")
}

func (s *Service) FileDependents(ctx context.Context, principal authn.Principal, request FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	return s.fileDependencyOperation(ctx, principal, request, "dependents")
}

func (s *Service) FileDependentCounts(ctx context.Context, principal authn.Principal, request FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	return s.fileDependencyOperation(ctx, principal, request, "dependent_counts")
}

func (s *Service) FileReachCounts(ctx context.Context, principal authn.Principal, request FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	return s.fileDependencyOperation(ctx, principal, request, "reach_counts")
}

func (s *Service) CircularDependencies(ctx context.Context, principal authn.Principal, request FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	return s.fileDependencyOperation(ctx, principal, request, "cycles")
}

func (s *Service) fileDependencyOperation(ctx context.Context, principal authn.Principal, request FileDependencyRequest, operation string) (graphprotocol.FileDependencyResponse, error) {
	if len(request.Paths) > 64 || request.Limit < 0 || math.IsNaN(request.MinConfidence) || math.IsInf(request.MinConfidence, 0) || request.MinConfidence < 0 || request.MinConfidence > 1 {
		return graphprotocol.FileDependencyResponse{}, ErrInvalidRequest
	}
	paths := make([]string, 0, len(request.Paths))
	seen := map[string]bool{}
	for _, value := range request.Paths {
		path, err := graphquery.NormalizeFilePath(value)
		if err != nil || path == "." {
			return graphprotocol.FileDependencyResponse{}, ErrInvalidRequest
		}
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	inspection, err := s.inspectionScope(ctx, principal, request.Repo, request.Branch)
	if err != nil {
		return graphprotocol.FileDependencyResponse{}, err
	}
	backend, ok := s.Backend.(fileDependencyQueryBackend)
	if !ok {
		return graphprotocol.FileDependencyResponse{}, ErrGraphNotReady
	}
	query := graphprotocol.FileDependencyRequest{Scope: inspection.scope, Paths: paths, MinConfidence: request.MinConfidence, Limit: request.Limit}
	var result graphprotocol.FileDependencyResponse
	switch operation {
	case "pairs":
		result, err = backend.FileDependencyPairs(ctx, query)
	case "dependencies":
		result, err = backend.FileDependencies(ctx, query)
	case "dependents":
		result, err = backend.FileDependents(ctx, query)
	case "dependent_counts":
		result, err = backend.FileDependentCounts(ctx, query)
	case "reach_counts":
		result, err = backend.FileReachCounts(ctx, query)
	case "cycles":
		result, err = backend.CircularDependencies(ctx, query)
	default:
		return graphprotocol.FileDependencyResponse{}, ErrInvalidRequest
	}
	if err != nil {
		return graphprotocol.FileDependencyResponse{}, err
	}
	if !validFileDependencyResponse(result) {
		return graphprotocol.FileDependencyResponse{}, ErrGraphNotReady
	}
	if err := s.finishInspection(ctx, principal, inspection, result.Generations, result); err != nil {
		return graphprotocol.FileDependencyResponse{}, err
	}
	return result, nil
}

func validFileDependencyResponse(result graphprotocol.FileDependencyResponse) bool {
	validPaths := func(paths []string) bool {
		for index, value := range paths {
			path, err := graphquery.NormalizeFilePath(value)
			if err != nil || path == "." || path != value || index > 0 && paths[index-1] >= value {
				return false
			}
		}
		return true
	}
	if !validPaths(result.Files) || result.Partial != (len(result.Boundaries) > 0) {
		return false
	}
	for index, pair := range result.Pairs {
		if pair.References <= 0 || !validPaths([]string{pair.Source}) || !validPaths([]string{pair.Target}) || pair.Source == pair.Target || index > 0 && (result.Pairs[index-1].Source > pair.Source || result.Pairs[index-1].Source == pair.Source && result.Pairs[index-1].Target >= pair.Target) {
			return false
		}
	}
	for index, count := range result.Counts {
		if !validPaths([]string{count.Path}) || count.Dependents < 0 || count.Reaches < 0 || count.References < 0 || count.Dependents+count.Reaches+count.References == 0 || index > 0 && result.Counts[index-1].Path >= count.Path {
			return false
		}
	}
	for index, cycle := range result.Cycles {
		if len(cycle) < 2 {
			return false
		}
		seen := map[string]bool{}
		for _, value := range cycle {
			path, err := graphquery.NormalizeFilePath(value)
			if err != nil || path == "." || path != value || seen[path] {
				return false
			}
			seen[path] = true
		}
		for previous := 0; previous < index; previous++ {
			if slices.Equal(result.Cycles[previous], cycle) {
				return false
			}
		}
	}
	for _, boundary := range result.Boundaries {
		if boundary.Reason != "edge_limit" || boundary.RepositoryID != 0 || boundary.Repository != "" || boundary.Depth < 0 {
			return false
		}
	}
	return true
}

func (s *Service) AffectedTests(ctx context.Context, principal authn.Principal, request AffectedTestsRequest) (graphprotocol.AffectedTestsResponse, error) {
	if len(request.ChangedFiles) == 0 || len(request.ChangedFiles) > 64 || request.MaxDepth < 0 || request.Limit < 0 || len(request.TestGlob) > 1024 {
		return graphprotocol.AffectedTestsResponse{}, ErrInvalidRequest
	}
	changed := make([]string, 0, len(request.ChangedFiles))
	for _, value := range request.ChangedFiles {
		path, err := graphquery.NormalizeFilePath(value)
		if err != nil || path == "." {
			return graphprotocol.AffectedTestsResponse{}, ErrInvalidRequest
		}
		changed = append(changed, path)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	inspection, err := s.inspectionScope(ctx, principal, request.Repo, request.Branch)
	if err != nil {
		return graphprotocol.AffectedTestsResponse{}, err
	}
	backend, ok := s.Backend.(affectedTestsQueryBackend)
	if !ok {
		return graphprotocol.AffectedTestsResponse{}, ErrGraphNotReady
	}
	result, err := backend.AffectedTests(ctx, graphprotocol.AffectedTestsRequest{Scope: inspection.scope, ChangedFiles: changed, MaxDepth: request.MaxDepth, TestGlob: request.TestGlob, Limit: request.Limit})
	if err != nil {
		return graphprotocol.AffectedTestsResponse{}, err
	}
	if !reflect.DeepEqual(result.ChangedFiles, changed) || result.TotalDependentsTraversed < 0 || result.Partial != (len(result.Boundaries) > 0) {
		return graphprotocol.AffectedTestsResponse{}, ErrGraphNotReady
	}
	for index, path := range result.AffectedTests {
		normalized, normalizeErr := graphquery.NormalizeFilePath(path)
		if normalizeErr != nil || normalized == "." || normalized != path || index > 0 && result.AffectedTests[index-1] >= path {
			return graphprotocol.AffectedTestsResponse{}, ErrGraphNotReady
		}
	}
	for _, boundary := range result.Boundaries {
		if boundary.RepositoryID != 0 || boundary.Repository != "" || boundary.Depth < 0 || boundary.Reason != "edge_limit" && boundary.Reason != "depth_limit" {
			return graphprotocol.AffectedTestsResponse{}, ErrGraphNotReady
		}
	}
	if err := s.finishInspection(ctx, principal, inspection, result.Generations, result); err != nil {
		return graphprotocol.AffectedTestsResponse{}, err
	}
	return result, nil
}
