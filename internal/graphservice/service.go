package graphservice

import (
	"context"
	"errors"
	"sort"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/pkg/api"
)

var (
	ErrInvalidRequest = errors.New("invalid_graph_request")
	ErrAdminRequired  = errors.New("administrator_required")
)

type Limits struct {
	PerCategory, DefaultImpactDepth, MaxDepth int
	DefaultTraceDepth, MaxTraceDepth, MaxRows int
	MaxNodes, MaxEdges, MaxFanout             int
	MaxResponseBytes                          int
}

type ContentReader interface {
	ReadFileAt(context.Context, authn.Principal, api.ReadFileRequest, string) (api.ReadFileResponse, error)
}

type Service struct {
	Store   RepositoryStore
	Backend graphprotocol.QueryEngine
	Files   ContentReader
	Limits  Limits
}

func (s *Service) scope(ctx context.Context, principal authn.Principal, selector api.GraphRepositorySelector, branch string) (Snapshot, graphprotocol.Scope, map[int64]Snapshot, error) {
	selected, err := ResolveRepository(ctx, s.Store, principal, selector, branch)
	if err != nil {
		return Snapshot{}, graphprotocol.Scope{}, nil, err
	}
	repositories, err := s.Store.GraphRepositories(ctx, principal)
	if err != nil {
		return Snapshot{}, graphprotocol.Scope{}, nil, err
	}
	snapshots := make(map[int64]Snapshot, len(repositories))
	for _, repository := range repositories {
		if repository.IndexedSHA == "" {
			continue
		}
		snapshot := Snapshot{ID: repository.ID, GitHubID: repository.GitHubID, Name: repository.Name, Branch: repository.Branch, Commit: repository.IndexedSHA}
		snapshots[snapshot.ID] = snapshot
	}
	if _, ok := snapshots[selected.ID]; !ok {
		return Snapshot{}, graphprotocol.Scope{}, nil, ErrGraphNotReady
	}
	ordered := make([]Snapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		ordered = append(ordered, snapshot)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	scope := graphprotocol.Scope{Repositories: make([]graphprotocol.RepositorySnapshot, 0, len(ordered))}
	for _, snapshot := range ordered {
		scope.Repositories = append(scope.Repositories, graphprotocol.RepositorySnapshot{ID: snapshot.ID, GitHubID: snapshot.GitHubID, Name: snapshot.Name, Branch: snapshot.Branch, Commit: snapshot.Commit})
	}
	return selected, scope, snapshots, nil
}

func (s *Service) reauthorize(ctx context.Context, principal authn.Principal, selected Snapshot, commits map[string]string) error {
	current, err := ResolveRepository(ctx, s.Store, principal, api.GraphRepositorySelector{Name: selected.Name}, selected.Branch)
	if err != nil {
		return err
	}
	if current.ID != selected.ID || current.Commit != selected.Commit {
		return ErrGraphNotReady
	}
	for name, commit := range commits {
		snapshot, err := ResolveRepository(ctx, s.Store, principal, api.GraphRepositorySelector{Name: name}, "")
		if err != nil || snapshot.Commit != commit {
			return ErrGraphNotReady
		}
	}
	return nil
}

func validRelations(relations []string) bool {
	seen := map[string]struct{}{}
	for _, relation := range relations {
		if _, ok := map[string]struct{}{"calls": {}, "references": {}, "extends": {}, "implements": {}}[relation]; !ok {
			return false
		}
		if _, duplicate := seen[relation]; duplicate {
			continue
		}
		seen[relation] = struct{}{}
	}
	return true
}

func (s *Service) limits() Limits {
	limits := s.Limits
	if limits.PerCategory <= 0 || limits.PerCategory > 100 {
		limits.PerCategory = 100
	}
	if limits.MaxDepth <= 0 || limits.MaxDepth > 32 {
		limits.MaxDepth = 32
	}
	if limits.DefaultImpactDepth <= 0 {
		limits.DefaultImpactDepth = 3
	} else if limits.DefaultImpactDepth > limits.MaxDepth {
		limits.DefaultImpactDepth = limits.MaxDepth
	}
	if limits.MaxTraceDepth <= 0 || limits.MaxTraceDepth > 30 {
		limits.MaxTraceDepth = 30
	}
	if limits.DefaultTraceDepth <= 0 {
		limits.DefaultTraceDepth = 10
	} else if limits.DefaultTraceDepth > limits.MaxTraceDepth {
		limits.DefaultTraceDepth = limits.MaxTraceDepth
	}
	if limits.MaxRows <= 0 || limits.MaxRows > 100 {
		limits.MaxRows = 100
	}
	if limits.MaxResponseBytes <= 0 || limits.MaxResponseBytes > 256<<10 {
		limits.MaxResponseBytes = 256 << 10
	}
	return limits
}

func symbol(value graphprotocol.Symbol) api.GraphSymbol {
	return api.GraphSymbol{UID: value.UID, Name: value.Name, Kind: value.Kind, FilePath: value.FilePath, Language: value.Language, Signature: value.Signature, RepositoryID: value.RepositoryID, Range: position(value.Range), Test: value.Test}
}

func candidate(value graphprotocol.Symbol) api.GraphCandidate {
	return api.GraphCandidate{UID: value.UID, Name: value.Name, Kind: value.Kind, FilePath: value.FilePath, Line: int(value.Range.StartLine) + 1}
}
func position(value graphprotocol.Position) api.GraphPosition {
	return api.GraphPosition{StartLine: int(value.StartLine), StartCharacter: int(value.StartCharacter), EndLine: int(value.EndLine), EndCharacter: int(value.EndCharacter)}
}
func reference(value graphprotocol.Relationship) api.GraphReference {
	return api.GraphReference{SourceRepositoryID: value.SourceRepositoryID, TargetRepositoryID: value.TargetRepositoryID, SourceUID: value.SourceUID, TargetUID: value.TargetUID, Kind: value.Kind, Path: value.Path, Range: position(value.Range), Confidence: value.Confidence, ResolutionReason: value.ResolutionReason}
}
func boundary(value graphprotocol.Boundary) api.GraphBoundary {
	return api.GraphBoundary{RepositoryID: value.RepositoryID, Repository: value.Repository, Reason: value.Reason, Depth: value.Depth}
}
func boundaries(values []graphprotocol.Boundary) []api.GraphBoundary {
	result := make([]api.GraphBoundary, len(values))
	for i := range values {
		result[i] = boundary(values[i])
	}
	return result
}
