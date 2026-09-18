package graphservice

import (
	"context"
	"slices"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/pkg/api"
)

type CapabilityWorkflow struct {
	Name            string `json:"name"`
	ArtifactVersion int    `json:"artifact_version"`
}

type CapabilitiesResponse struct {
	Version                int                       `json:"version"`
	QueryArtifactVersions  []int                     `json:"query_artifact_versions"`
	UploadArtifactVersions []int                     `json:"upload_artifact_versions"`
	Workflows              []CapabilityWorkflow      `json:"workflows"`
	Status                 string                    `json:"status"`
	Freshness              string                    `json:"freshness"`
	RepositoryID           int64                     `json:"repository_id"`
	Repository             string                    `json:"repository"`
	Branch                 string                    `json:"branch"`
	CurrentIndexedCommit   string                    `json:"current_indexed_commit"`
	Generation             *graphprotocol.Generation `json:"generation"`
	ProducerCapabilities   []string                  `json:"producer_capabilities"`
	DiscoveryProjection    bool                      `json:"discovery_projection"`
	DiscoveryCoverage      string                    `json:"discovery_coverage"`
}

func discoveryConfig(config api.GraphDiscoveryConfig) graphprotocol.DiscoveryConfig {
	return graphprotocol.DiscoveryConfig{NoMultiterm: config.NoMultiterm, ProjectTerms: config.ProjectTerms, Deprioritize: config.Deprioritize}
}

func (s *Service) Discover(ctx context.Context, p authn.Principal, r api.GraphDiscoverRequest) (graphprotocol.DiscoverResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	i, err := s.inspectionScope(ctx, p, r.Repo, r.Branch)
	if err != nil {
		return graphprotocol.DiscoverResponse{}, err
	}
	backend, ok := s.Backend.(discoveryBackend)
	if !ok {
		return graphprotocol.DiscoverResponse{}, ErrGraphNotReady
	}
	result, err := backend.Discover(ctx, graphprotocol.DiscoverRequest{Scope: i.scope, Query: r.Query, Limit: r.Limit, CandidateLimit: r.CandidateLimit, Symbols: r.Symbols, Files: r.Files, Config: discoveryConfig(r.Config)})
	if err != nil {
		return graphprotocol.DiscoverResponse{}, err
	}
	if err := i.generations(result.Generations); err != nil {
		return graphprotocol.DiscoverResponse{}, err
	}
	for _, match := range result.Matches {
		if match.Entity.RepositoryID != i.selected.GitHubID || match.Entity.Fact == nil {
			return graphprotocol.DiscoverResponse{}, ErrGraphNotReady
		}
	}
	if err := s.finishInspection(ctx, p, i, result.Generations, result); err != nil {
		return graphprotocol.DiscoverResponse{}, err
	}
	return result, nil
}

func (s *Service) ExplorePublic(ctx context.Context, p authn.Principal, r api.GraphExploreRequest) (ExploreResponse, error) {
	return s.Explore(ctx, p, ExploreRequest{
		Repo: r.Repo, Branch: r.Branch, Query: r.Query, Symbols: r.Symbols, Files: r.Files,
		RequiredOccurrences: r.RequiredOccurrences, Limit: r.Limit, CandidateLimit: r.CandidateLimit,
		MaxFiles: r.MaxFiles, SourceUnits: r.SourceUnits, SourceBytes: r.SourceBytes,
		Config: ExploreConfig{DiscoveryConfig: discoveryConfig(r.Config.GraphDiscoveryConfig), LineNumbers: r.Config.LineNumbers, Adaptive: r.Config.Adaptive, Dedup: r.Config.Dedup},
	})
}

func (s *Service) ListFilesPublic(ctx context.Context, p authn.Principal, r api.GraphFilesRequest) (graphprotocol.FilesResponse, error) {
	return s.ListFiles(ctx, p, ListFilesRequest{Repo: r.Repo, Branch: r.Branch, Directory: r.Directory, Glob: r.Glob, Limit: r.Limit, Cursor: r.Cursor})
}

func (s *Service) Capabilities(ctx context.Context, p authn.Principal, r api.GraphCapabilitiesRequest) (CapabilitiesResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	i, err := s.inspectionScope(ctx, p, r.Repo, r.Branch)
	if err != nil {
		return CapabilitiesResponse{}, err
	}
	backend, ok := s.Backend.(discoveryBackend)
	if !ok {
		return CapabilitiesResponse{}, ErrGraphNotReady
	}
	files, err := i.backend.IndexedFiles(ctx, graphprotocol.FilesRequest{Scope: i.scope, Limit: 1, IncludeCount: true})
	if err != nil {
		return CapabilitiesResponse{}, err
	}
	if err := i.generations(files.Generations); err != nil {
		return CapabilitiesResponse{}, err
	}
	for _, file := range files.Files {
		if file.RepositoryID != i.selected.GitHubID || file.Fact == nil {
			return CapabilitiesResponse{}, ErrGraphNotReady
		}
	}
	probe, err := backend.Discover(ctx, graphprotocol.DiscoverRequest{Scope: i.scope, Limit: 1, CandidateLimit: 1})
	if err != nil {
		return CapabilitiesResponse{}, err
	}
	if err := sameInspectionGeneration(files.Generations, probe.Generations); err != nil {
		return CapabilitiesResponse{}, err
	}
	for _, match := range probe.Matches {
		if match.Entity.RepositoryID != i.selected.GitHubID || match.Entity.Fact == nil {
			return CapabilitiesResponse{}, ErrGraphNotReady
		}
	}
	generation := files.Generations[0]
	result := CapabilitiesResponse{
		Version: 1, QueryArtifactVersions: []int{1, 2}, UploadArtifactVersions: []int{1},
		Workflows: []CapabilityWorkflow{{Name: "context", ArtifactVersion: 1}, {Name: "impact", ArtifactVersion: 1}, {Name: "trace", ArtifactVersion: 1}, {Name: "discover", ArtifactVersion: 2}, {Name: "explore", ArtifactVersion: 2}, {Name: "files", ArtifactVersion: 2}, {Name: "capabilities", ArtifactVersion: 2}},
		Status:    "ready", Freshness: "current", RepositoryID: i.selected.GitHubID, Repository: i.selected.Name,
		Branch: i.selected.Branch, CurrentIndexedCommit: i.selected.Commit, Generation: &generation,
		ProducerCapabilities: slices.Clone(generation.Capabilities), DiscoveryProjection: true, DiscoveryCoverage: probe.Coverage,
	}
	if err := s.finishInspection(ctx, p, i, files.Generations, result); err != nil {
		return CapabilitiesResponse{}, err
	}
	return result, nil
}
