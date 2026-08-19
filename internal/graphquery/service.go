package graphquery

import (
	"context"
	"errors"
	"sort"

	"github.com/grepnest/grepnest/internal/graphprotocol"
)

var (
	ErrInvalidRequest = errors.New("invalid graph query")
)

const (
	defaultCategoryLimit = 100
	defaultImpactDepth   = 3
	defaultTraceDepth    = 10
	defaultMaxDepth      = 32
	defaultMaxTraceDepth = 30
	defaultMaxNodes      = 1_000
	defaultMaxEdges      = 5_000
	defaultMaxFanout     = 100
)

type Limits struct {
	PerCategory, DefaultImpactDepth, MaxDepth int
	DefaultTraceDepth, MaxTraceDepth, MaxRows int
	MaxNodes, MaxEdges, MaxFanout             int
}

type Service struct {
	Store  Store
	Limits Limits
}

func (service *Service) Health(ctx context.Context) error {
	store := service.queryStore()
	if store == nil {
		return errors.New("graph database is unavailable")
	}
	return store.Health(ctx)
}

func (service *Service) queryStore() Store {
	if service == nil {
		return nil
	}
	return service.Store
}

type readyScope struct {
	snapshots       []graphprotocol.RepositorySnapshot
	queries         []QuerySnapshot
	boundaries      []graphprotocol.Boundary
	commits         map[string]string
	selected        []graphprotocol.RepositorySnapshot
	selectedQueries []QuerySnapshot
	selectedID      int64
}

type nodeKey struct {
	repositoryID int64
	uid          string
}

func (service *Service) ready(ctx context.Context, scope graphprotocol.Scope) (readyScope, error) {
	store := service.queryStore()
	if store == nil || len(scope.Repositories) == 0 {
		return readyScope{}, ErrInvalidRequest
	}
	manifests, err := store.Manifests(ctx)
	if err != nil {
		return readyScope{}, err
	}
	ready := readyScope{commits: map[string]string{}}
	seen := map[int64]struct{}{}
	selectedExists := scope.SelectedRepositoryID == 0
	for _, snapshot := range scope.Repositories {
		if snapshot.ID <= 0 || snapshot.Commit == "" {
			return readyScope{}, ErrInvalidRequest
		}
		if _, duplicate := seen[snapshot.ID]; duplicate {
			return readyScope{}, ErrInvalidRequest
		}
		seen[snapshot.ID] = struct{}{}
		if scope.SelectedRepositoryID == snapshot.ID {
			selectedExists = true
			ready.selectedID = snapshot.ID
		}
		manifest, ok := manifests[snapshot.ID]
		if !ok || manifest.Commit != snapshot.Commit {
			reason := "graph_not_ready"
			if !ok {
				reason = "graph_missing"
			}
			ready.boundaries = append(ready.boundaries, graphprotocol.Boundary{
				RepositoryID: snapshot.ID, Repository: snapshot.Name, Reason: reason,
			})
			continue
		}
		ready.snapshots = append(ready.snapshots, snapshot)
		querySnapshot := QuerySnapshot{RepositoryID: snapshot.ID, UploadID: manifest.UploadID, Commit: snapshot.Commit}
		ready.queries = append(ready.queries, querySnapshot)
		ready.commits[snapshot.Name] = snapshot.Commit
		if scope.SelectedRepositoryID == snapshot.ID {
			ready.selected = append(ready.selected, snapshot)
			ready.selectedQueries = append(ready.selectedQueries, querySnapshot)
		}
	}
	if !selectedExists {
		return readyScope{}, ErrInvalidRequest
	}
	sort.Slice(ready.snapshots, func(left, right int) bool {
		return ready.snapshots[left].ID < ready.snapshots[right].ID
	})
	return ready, nil
}

func (ready readyScope) querySnapshots(selected bool) []QuerySnapshot {
	if selected && ready.selectedID != 0 {
		return ready.selectedQueries
	}
	return ready.queries
}

func (ready readyScope) selectorSnapshots() []graphprotocol.RepositorySnapshot {
	if ready.selectedID != 0 {
		return ready.selected
	}
	return ready.snapshots
}

func (service *Service) limits() Limits {
	limits := service.Limits
	if limits.PerCategory <= 0 || limits.PerCategory > defaultCategoryLimit {
		limits.PerCategory = defaultCategoryLimit
	}
	if limits.MaxDepth <= 0 || limits.MaxDepth > defaultMaxDepth {
		limits.MaxDepth = defaultMaxDepth
	}
	if limits.DefaultImpactDepth <= 0 {
		limits.DefaultImpactDepth = defaultImpactDepth
	} else if limits.DefaultImpactDepth > limits.MaxDepth {
		limits.DefaultImpactDepth = limits.MaxDepth
	}
	if limits.MaxTraceDepth <= 0 || limits.MaxTraceDepth > defaultMaxTraceDepth {
		limits.MaxTraceDepth = defaultMaxTraceDepth
	}
	if limits.DefaultTraceDepth <= 0 {
		limits.DefaultTraceDepth = defaultTraceDepth
	} else if limits.DefaultTraceDepth > limits.MaxTraceDepth {
		limits.DefaultTraceDepth = limits.MaxTraceDepth
	}
	if limits.MaxRows <= 0 || limits.MaxRows > 1_000 {
		limits.MaxRows = 1_000
	}
	if limits.MaxNodes <= 0 || limits.MaxNodes > defaultMaxNodes {
		limits.MaxNodes = defaultMaxNodes
	}
	if limits.MaxEdges <= 0 || limits.MaxEdges > defaultMaxEdges {
		limits.MaxEdges = defaultMaxEdges
	}
	if limits.MaxFanout <= 0 || limits.MaxFanout > defaultMaxFanout {
		limits.MaxFanout = defaultMaxFanout
	}
	return limits
}

func selectedRelations(requested []string) ([]string, error) {
	valid := map[string]struct{}{"calls": {}, "references": {}, "extends": {}, "implements": {}}
	if len(requested) == 0 {
		return []string{"calls", "references", "extends", "implements"}, nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(requested))
	for _, relation := range requested {
		if _, ok := valid[relation]; !ok {
			return nil, ErrInvalidRequest
		}
		if _, duplicate := seen[relation]; duplicate {
			continue
		}
		seen[relation] = struct{}{}
		out = append(out, relation)
	}
	return out, nil
}

func appendBoundary(boundaries []graphprotocol.Boundary, reason string, depth int) []graphprotocol.Boundary {
	return append(boundaries, graphprotocol.Boundary{Reason: reason, Depth: depth})
}
