package graphquery

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphprotocol"
)

var ErrGenerationChanged = errors.New("graph generation is unavailable or changed")
var ErrQuerySize = errors.New("graph query response exceeds byte limit")

// MaxEntityQueryBytes bounds entity responses and each store result batch.
const MaxEntityQueryBytes = 4 << 20

type entityReady struct {
	store       EntityStore
	snapshots   []QuerySnapshot
	selected    []QuerySnapshot
	generations []graphprotocol.Generation
	publicIDs   map[int64]int64
}

func (service *Service) entityContext(ctx context.Context) (context.Context, context.CancelFunc) {
	duration := service.Limits.MaxDuration
	if duration <= 0 || duration > 5*time.Second {
		duration = 5 * time.Second
	}
	return context.WithTimeout(ctx, duration)
}

func (service *Service) readyEntities(ctx context.Context, scope graphprotocol.Scope) (entityReady, error) {
	if err := ctx.Err(); err != nil {
		return entityReady{}, err
	}
	store, ok := service.queryStore().(EntityStore)
	if !ok || len(scope.Repositories) == 0 || len(scope.Repositories) > 1000 {
		return entityReady{}, ErrInvalidRequest
	}
	ready := entityReady{store: store, publicIDs: map[int64]int64{}}
	public := map[int64]bool{}
	selected := scope.SelectedRepositoryID == 0
	for _, r := range scope.Repositories {
		if r.ID <= 0 || r.GitHubID <= 0 || r.Commit == "" || len(r.Commit) > 128 || ready.publicIDs[r.ID] != 0 || public[r.GitHubID] {
			return entityReady{}, ErrInvalidRequest
		}
		ready.publicIDs[r.ID] = r.GitHubID
		public[r.GitHubID] = true
		selected = selected || r.ID == scope.SelectedRepositoryID
		ready.snapshots = append(ready.snapshots, QuerySnapshot{RepositoryID: r.ID, Commit: r.Commit})
	}
	if !selected {
		return entityReady{}, ErrInvalidRequest
	}
	slices.SortFunc(ready.snapshots, func(a, b QuerySnapshot) int {
		if a.RepositoryID < b.RepositoryID {
			return -1
		}
		if a.RepositoryID > b.RepositoryID {
			return 1
		}
		return 0
	})
	generations, err := store.EntityGenerations(ctx, ready.snapshots)
	if err != nil {
		return entityReady{}, err
	}
	if len(generations) != len(ready.snapshots) {
		return entityReady{}, ErrGenerationChanged
	}
	for i, g := range generations {
		snap := &ready.snapshots[i]
		if g.RepositoryID != snap.RepositoryID || g.Repository != strconv.FormatInt(ready.publicIDs[snap.RepositoryID], 10) || g.Commit != snap.Commit || g.UploadID <= 0 || g.Producer == nil {
			return entityReady{}, ErrGenerationChanged
		}
		snap.UploadID = g.UploadID
		if scope.SelectedRepositoryID == 0 || scope.SelectedRepositoryID == snap.RepositoryID {
			ready.selected = append(ready.selected, *snap)
		}
	}
	ready.generations = generations
	return ready, nil
}

func (ready entityReady) current(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := ready.store.EntityGenerations(ctx, ready.snapshots)
	if err != nil {
		return err
	}
	if len(current) != len(ready.generations) {
		return ErrGenerationChanged
	}
	for i, g := range current {
		if g.RepositoryID != ready.generations[i].RepositoryID || g.UploadID != ready.generations[i].UploadID || g.Commit != ready.generations[i].Commit {
			return ErrGenerationChanged
		}
	}
	return ctx.Err()
}

func validEntitySelector(s graphprotocol.EntitySelector) bool {
	for _, p := range []*string{s.Occurrence, s.Name, s.QualifiedName, s.Path} {
		if p != nil && (len(*p) > 16384 || !utf8.ValidString(*p)) {
			return false
		}
	}
	return len(s.Kind) <= 64 && utf8.ValidString(s.Kind)
}

type entityCursor struct {
	Fingerprint [32]byte
	Offset      int
}

func (service *Service) Entities(ctx context.Context, request graphprotocol.EntitiesRequest) (graphprotocol.EntitiesResponse, error) {
	if service == nil {
		return graphprotocol.EntitiesResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	if !validEntitySelector(request.Selector) || request.Limit < 0 || len(request.Cursor) > 512 {
		return graphprotocol.EntitiesResponse{}, ErrInvalidRequest
	}
	ready, err := service.readyEntities(ctx, request.Scope)
	if err != nil {
		return graphprotocol.EntitiesResponse{}, err
	}
	limit := request.Limit
	if limit == 0 || limit > service.limits().MaxRows {
		limit = service.limits().MaxRows
	}
	encoded, _ := json.Marshal(struct {
		Snapshots []QuerySnapshot
		Selected  int64
		Selector  graphprotocol.EntitySelector
		Limit     int
	}{ready.snapshots, request.Scope.SelectedRepositoryID, request.Selector, limit})
	fingerprint := sha256.Sum256(encoded)
	offset := 0
	if request.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil {
			return graphprotocol.EntitiesResponse{}, ErrInvalidRequest
		}
		var cursor entityCursor
		if json.Unmarshal(data, &cursor) != nil || cursor.Offset <= 0 || cursor.Offset > 10_000_000 {
			return graphprotocol.EntitiesResponse{}, ErrInvalidRequest
		}
		if cursor.Fingerprint != fingerprint {
			return graphprotocol.EntitiesResponse{}, ErrGenerationChanged
		}
		offset = cursor.Offset
	}
	entities, err := ready.store.QueryEntities(ctx, EntityQuery{Snapshots: ready.selected, Selector: request.Selector, Offset: offset, Limit: limit + 1})
	if err != nil {
		return graphprotocol.EntitiesResponse{}, err
	}
	if err = ready.publicEntities(entities); err != nil {
		return graphprotocol.EntitiesResponse{}, err
	}
	result := graphprotocol.EntitiesResponse{Entities: entities, Generations: ready.generations}
	if len(entities) > limit {
		result.Entities = entities[:limit]
		data, _ := json.Marshal(entityCursor{fingerprint, offset + limit})
		result.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	result.Generations = ready.publicGenerations()
	if err = entityResponseSize(result); err != nil {
		return graphprotocol.EntitiesResponse{}, err
	}
	if err = ready.current(ctx); err != nil {
		return graphprotocol.EntitiesResponse{}, err
	}
	return result, nil
}

func (ready entityReady) publicEntities(entities []graphprotocol.Entity) error {
	for i := range entities {
		id := ready.publicIDs[entities[i].RepositoryID]
		if id == 0 {
			return ErrGenerationChanged
		}
		entities[i].RepositoryID = id
	}
	return nil
}

func (ready entityReady) publicGenerations() []graphprotocol.Generation {
	out := slices.Clone(ready.generations)
	for i := range out {
		out[i].RepositoryID = ready.publicIDs[out[i].RepositoryID]
	}
	return out
}

func entityResponseSize(value any) error {
	used := 0
	return AddEntityQueryBytes(&used, value)
}

// AddEntityQueryBytes lets stores and traversal reject oversized facts before
// accumulating a result set. Final response encoding also checks JSON overhead.
func AddEntityQueryBytes(used *int, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	*used += len(data)
	if *used > MaxEntityQueryBytes {
		return ErrQuerySize
	}
	return nil
}

// Traverse visits entities once while retaining every bounded edge occurrence,
// including parallel edges, self edges and edges back into an already seen cycle.
// Edge endpoints always preserve the producer's original direction.
func (service *Service) Traverse(ctx context.Context, request graphprotocol.TraverseRequest) (graphprotocol.TraverseResponse, error) {
	if service == nil {
		return graphprotocol.TraverseResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	if !validEntitySelector(request.Root) || (request.Root.Occurrence == nil && request.Root.Name == nil && request.Root.QualifiedName == nil) || request.MaxDepth < 0 || math.IsNaN(request.MinConfidence) || math.IsInf(request.MinConfidence, 0) || request.MinConfidence < 0 || request.MinConfidence > 1 {
		return graphprotocol.TraverseResponse{}, ErrInvalidRequest
	}
	if request.Direction == "" {
		request.Direction = "outgoing"
	}
	if request.Direction != "outgoing" && request.Direction != "incoming" {
		return graphprotocol.TraverseResponse{}, ErrInvalidRequest
	}
	relations := request.Relations
	if len(relations) == 0 {
		relations = []string{"calls"}
	}
	if len(relations) > 13 {
		return graphprotocol.TraverseResponse{}, ErrInvalidRequest
	}
	seenRelations := map[string]bool{}
	normalized := []string{}
	for _, name := range relations {
		if _, ok := graphartifact.ParseRelationship(name); !ok {
			return graphprotocol.TraverseResponse{}, ErrInvalidRequest
		}
		if !seenRelations[name] {
			normalized = append(normalized, name)
			seenRelations[name] = true
		}
	}
	ready, err := service.readyEntities(ctx, request.Scope)
	if err != nil {
		return graphprotocol.TraverseResponse{}, err
	}
	limits := service.limits()
	roots, err := ready.store.QueryEntities(ctx, EntityQuery{Snapshots: ready.selected, Selector: request.Root, Limit: limits.MaxNodes + 1})
	if err != nil {
		return graphprotocol.TraverseResponse{}, err
	}
	result := graphprotocol.TraverseResponse{Status: graphprotocol.StatusNotFound, Generations: ready.publicGenerations()}
	boundary := func(reason string, depth int) {
		result.Partial = true
		result.Boundaries = appendBoundary(result.Boundaries, reason, depth)
	}
	ambiguous := len(roots) > 1
	if len(roots) > limits.MaxNodes {
		roots = roots[:limits.MaxNodes]
		boundary("node_limit", 0)
	}
	if len(roots) == 0 || ambiguous {
		if ambiguous {
			result.Status = graphprotocol.StatusAmbiguous
		}
		result.Entities = roots
		if err = ready.publicEntities(result.Entities); err != nil {
			return graphprotocol.TraverseResponse{}, err
		}
		if err = entityResponseSize(result); err != nil {
			return graphprotocol.TraverseResponse{}, err
		}
		if err = ready.current(ctx); err != nil {
			return graphprotocol.TraverseResponse{}, err
		}
		return result, nil
	}
	result.Status = graphprotocol.StatusOK
	depth := request.MaxDepth
	if depth == 0 {
		depth = limits.DefaultImpactDepth
	}
	if depth > limits.MaxDepth {
		depth = limits.MaxDepth
		boundary("depth_limit", depth)
	}
	snapshots := map[int64]QuerySnapshot{}
	for _, s := range ready.snapshots {
		snapshots[s.RepositoryID] = s
	}
	result.Entities = roots
	usedBytes := 0
	if err = AddEntityQueryBytes(&usedBytes, result); err != nil {
		return graphprotocol.TraverseResponse{}, err
	}
	visited := map[nodeKey]bool{{roots[0].RepositoryID, roots[0].Fact.Occurrence}: true}
	seenEdges := map[nodeKey]bool{}
	frontier := roots
	stopped := false
	for level := 0; level <= depth && len(frontier) > 0 && !stopped; level++ {
		next := []graphprotocol.Entity{}
		for _, parent := range frontier {
			count := 0
			for _, relation := range normalized {
				if err = ctx.Err(); err != nil {
					return graphprotocol.TraverseResponse{}, err
				}
				rows, err := ready.store.EntityNeighbors(ctx, EntityNeighborQuery{Snapshot: snapshots[parent.RepositoryID], Occurrence: parent.Fact.Occurrence, Relation: relation, Direction: request.Direction, MinConfidence: request.MinConfidence, Limit: limits.MaxFanout - count + 1})
				if err != nil {
					return graphprotocol.TraverseResponse{}, err
				}
				for _, row := range rows {
					if row.Entity.RepositoryID != parent.RepositoryID || row.Edge.RepositoryID != parent.RepositoryID {
						return graphprotocol.TraverseResponse{}, ErrGenerationChanged
					}
				}
				if level == depth {
					for _, row := range rows {
						if !seenEdges[nodeKey{row.Edge.RepositoryID, row.Edge.Fact.Occurrence}] {
							boundary("depth_limit", depth)
							break
						}
					}
					continue
				}
				if len(rows) > limits.MaxFanout-count {
					rows = rows[:limits.MaxFanout-count]
					boundary("fanout_limit", level+1)
				}
				count += len(rows)
				for _, row := range rows {
					key := nodeKey{row.Entity.RepositoryID, row.Entity.Fact.Occurrence}
					edgeKey := nodeKey{row.Edge.RepositoryID, row.Edge.Fact.Occurrence}
					if seenEdges[edgeKey] {
						continue
					}
					if len(result.Edges) >= limits.MaxEdges {
						boundary("edge_limit", level+1)
						stopped = true
						break
					}
					if !visited[key] {
						if len(result.Entities) >= limits.MaxNodes {
							boundary("node_limit", level+1)
							stopped = true
							break
						}
						visited[key] = true
						row.Entity.Depth = level + 1
						if err = AddEntityQueryBytes(&usedBytes, row.Entity); err != nil {
							return graphprotocol.TraverseResponse{}, err
						}
						result.Entities = append(result.Entities, row.Entity)
						next = append(next, row.Entity)
					}
					seenEdges[edgeKey] = true
					if err = AddEntityQueryBytes(&usedBytes, row.Edge); err != nil {
						return graphprotocol.TraverseResponse{}, err
					}
					result.Edges = append(result.Edges, row.Edge)
				}
				if stopped {
					break
				}
			}
			if stopped {
				break
			}
		}
		sort.Slice(next, func(i, j int) bool {
			if next[i].RepositoryID != next[j].RepositoryID {
				return next[i].RepositoryID < next[j].RepositoryID
			}
			return next[i].Fact.Occurrence < next[j].Fact.Occurrence
		})
		frontier = next
	}
	if err = ready.publicEntities(result.Entities); err != nil {
		return graphprotocol.TraverseResponse{}, err
	}
	for i := range result.Edges {
		id := ready.publicIDs[result.Edges[i].RepositoryID]
		if id == 0 {
			return graphprotocol.TraverseResponse{}, ErrGenerationChanged
		}
		result.Edges[i].RepositoryID = id
	}
	if err = entityResponseSize(result); err != nil {
		return graphprotocol.TraverseResponse{}, err
	}
	if err = ready.current(ctx); err != nil {
		return graphprotocol.TraverseResponse{}, err
	}
	return result, nil
}
