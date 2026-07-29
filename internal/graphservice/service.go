package graphservice

import (
	"context"
	"errors"
	"sort"
	"time"

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
	Observe func(string, string, time.Duration)
}

func (s *Service) observe(started time.Time, operation string, err *error) {
	if s.Observe == nil {
		return
	}
	result := "success"
	if *err != nil {
		result = "error"
	}
	s.Observe(operation, result, time.Since(started))
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
	scope := graphprotocol.Scope{SelectedRepositoryID: selected.ID, Repositories: make([]graphprotocol.RepositorySnapshot, 0, len(ordered))}
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
	valid := map[string]struct{}{"calls": {}, "references": {}, "extends": {}, "implements": {}}
	seen := map[string]struct{}{}
	for _, relation := range relations {
		if _, ok := valid[relation]; !ok {
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

func publicSymbol(value graphprotocol.Symbol, snapshots map[int64]Snapshot) (api.GraphSymbol, error) {
	snapshot, ok := snapshots[value.RepositoryID]
	if !ok {
		return api.GraphSymbol{}, ErrGraphNotReady
	}
	return api.GraphSymbol{UID: value.UID, Name: value.Name, Kind: value.Kind, FilePath: value.FilePath, Language: value.Language, Signature: value.Signature, RepositoryID: snapshot.GitHubID, Range: position(value.Range), Test: value.Test}, nil
}

func candidate(value graphprotocol.Symbol, snapshots map[int64]Snapshot) (api.GraphCandidate, error) {
	snapshot, ok := snapshots[value.RepositoryID]
	if !ok {
		return api.GraphCandidate{}, ErrGraphNotReady
	}
	return api.GraphCandidate{
		UID: value.UID, Name: value.Name, Kind: value.Kind, FilePath: value.FilePath,
		RepositoryID: snapshot.GitHubID, Line: int(value.Range.StartLine) + 1,
	}, nil
}
func position(value graphprotocol.Position) api.GraphPosition {
	return api.GraphPosition{StartLine: int(value.StartLine), StartCharacter: int(value.StartCharacter), EndLine: int(value.EndLine), EndCharacter: int(value.EndCharacter)}
}
func publicReference(value graphprotocol.Relationship, snapshots map[int64]Snapshot) (api.GraphReference, error) {
	source, sourceOK := snapshots[value.SourceRepositoryID]
	target, targetOK := snapshots[value.TargetRepositoryID]
	if !sourceOK || !targetOK {
		return api.GraphReference{}, ErrGraphNotReady
	}
	return api.GraphReference{SourceRepositoryID: source.GitHubID, TargetRepositoryID: target.GitHubID, SourceUID: value.SourceUID, TargetUID: value.TargetUID, Kind: value.Kind, Path: value.Path, Range: position(value.Range), Confidence: value.Confidence, ResolutionReason: value.ResolutionReason}, nil
}
func publicBoundary(value graphprotocol.Boundary, snapshots map[int64]Snapshot) (api.GraphBoundary, error) {
	if value.RepositoryID == 0 {
		return api.GraphBoundary{Reason: value.Reason, Depth: value.Depth}, nil
	}
	snapshot, ok := snapshots[value.RepositoryID]
	if !ok {
		return api.GraphBoundary{}, ErrGraphNotReady
	}
	return api.GraphBoundary{RepositoryID: snapshot.GitHubID, Repository: snapshot.Name, Reason: value.Reason, Depth: value.Depth}, nil
}
func publicBoundaries(values []graphprotocol.Boundary, snapshots map[int64]Snapshot) ([]api.GraphBoundary, error) {
	result := make([]api.GraphBoundary, 0, len(values))
	for i := range values {
		value, err := publicBoundary(values[i], snapshots)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}
