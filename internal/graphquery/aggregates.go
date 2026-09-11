package graphquery

import (
	"context"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
)

const maxAggregateValues = 5_000

type AggregateFanQuery struct {
	Snapshot QuerySnapshot
	Values   []string
	Incoming bool
}

type AggregateNamesQuery struct {
	Snapshot  QuerySnapshot
	Values    []string
	Operation string
	Limit     int
}

type AggregateLimitQuery struct {
	Snapshot QuerySnapshot
	Limit    int
}

type AggregateModuleQuery struct {
	Snapshot         QuerySnapshot
	Assignments      []graphprotocol.ModuleAssignment
	Kinds, PairKinds []int16
	MinConfidence    float64
	Limit            int
}

type AggregateModuleRow struct {
	Source, Target, Kind, From, To string
	Count, Declared, Uncertain     int
}

type AggregateUnresolvedQuery struct {
	Snapshot QuerySnapshot
	Values   []string
	Path     string
	Limit    int
}

type AggregateStore interface {
	AggregateStats(context.Context, QuerySnapshot) (graphprotocol.GraphStats, error)
	QueryFan(context.Context, AggregateFanQuery) ([]graphprotocol.AggregateCount, error)
	QueryNodeMetrics(context.Context, QuerySnapshot, string) (graphprotocol.NodeMetrics, error)
	QueryAggregateNames(context.Context, AggregateNamesQuery) ([]string, error)
	QueryTopDependedOn(context.Context, AggregateLimitQuery) ([]graphprotocol.DependedOn, error)
	QueryTopCallingFiles(context.Context, AggregateLimitQuery) ([]graphprotocol.CallingFile, error)
	QueryFileNodes(context.Context, QuerySnapshot, []string) ([]graphprotocol.Entity, error)
	QueryModuleAggregation(context.Context, AggregateModuleQuery) ([]AggregateModuleRow, error)
	QueryUnresolvedReferences(context.Context, AggregateUnresolvedQuery) ([]graphprotocol.UnresolvedReference, error)
}

func (service *Service) aggregateReady(ctx context.Context, scope graphprotocol.Scope) (entityReady, AggregateStore, error) {
	ready, err := service.readyEntities(ctx, scope)
	if err != nil {
		return entityReady{}, nil, err
	}
	store, ok := ready.store.(AggregateStore)
	if !ok || len(ready.selected) != 1 {
		return entityReady{}, nil, ErrInvalidRequest
	}
	return ready, store, nil
}

func finishAggregate(ctx context.Context, ready entityReady, value any) error {
	if err := entityResponseSize(value); err != nil {
		return err
	}
	return ready.current(ctx)
}

func aggregateValues(values []string) ([]string, error) {
	if len(values) > maxAggregateValues {
		return nil, ErrInvalidRequest
	}
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		if len(value) > 16_384 || !utf8.ValidString(value) {
			return nil, ErrInvalidRequest
		}
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out, nil
}

func (service *Service) GraphStats(ctx context.Context, scope graphprotocol.Scope) (graphprotocol.GraphStatsResponse, error) {
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, store, err := service.aggregateReady(ctx, scope)
	if err != nil {
		return graphprotocol.GraphStatsResponse{}, err
	}
	stats, err := store.AggregateStats(ctx, ready.selected[0])
	if err != nil {
		return graphprotocol.GraphStatsResponse{}, err
	}
	generation := ready.generations[0]
	if stats.NodeCount != generation.NodeCount || stats.EdgeCount != generation.EdgeCount || stats.UnresolvedCount != generation.UnresolvedCount {
		return graphprotocol.GraphStatsResponse{}, ErrGenerationChanged
	}
	result := graphprotocol.GraphStatsResponse{Stats: stats, Generations: ready.publicGenerations()}
	if err = finishAggregate(ctx, ready, result); err != nil {
		return graphprotocol.GraphStatsResponse{}, err
	}
	return result, nil
}

func (service *Service) FanIn(ctx context.Context, request graphprotocol.AggregateValuesRequest) (graphprotocol.AggregateCountsResponse, error) {
	return service.fan(ctx, request, true)
}

func (service *Service) FanOut(ctx context.Context, request graphprotocol.AggregateValuesRequest) (graphprotocol.AggregateCountsResponse, error) {
	return service.fan(ctx, request, false)
}

func (service *Service) fan(ctx context.Context, request graphprotocol.AggregateValuesRequest, incoming bool) (graphprotocol.AggregateCountsResponse, error) {
	if service == nil {
		return graphprotocol.AggregateCountsResponse{}, ErrInvalidRequest
	}
	values, err := aggregateValues(request.Values)
	if err != nil {
		return graphprotocol.AggregateCountsResponse{}, err
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, store, err := service.aggregateReady(ctx, request.Scope)
	if err != nil {
		return graphprotocol.AggregateCountsResponse{}, err
	}
	result := graphprotocol.AggregateCountsResponse{Generations: ready.publicGenerations()}
	if len(values) > 0 {
		result.Counts, err = store.QueryFan(ctx, AggregateFanQuery{Snapshot: ready.selected[0], Values: values, Incoming: incoming})
	}
	if err != nil {
		return graphprotocol.AggregateCountsResponse{}, err
	}
	if err = finishAggregate(ctx, ready, result); err != nil {
		return graphprotocol.AggregateCountsResponse{}, err
	}
	return result, nil
}

func (service *Service) NodeMetrics(ctx context.Context, request graphprotocol.NodeMetricsRequest) (graphprotocol.NodeMetricsResponse, error) {
	if service == nil || len(request.Occurrence) > 16_384 || !utf8.ValidString(request.Occurrence) {
		return graphprotocol.NodeMetricsResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, store, err := service.aggregateReady(ctx, request.Scope)
	if err != nil {
		return graphprotocol.NodeMetricsResponse{}, err
	}
	metrics, err := store.QueryNodeMetrics(ctx, ready.selected[0], request.Occurrence)
	if err != nil {
		return graphprotocol.NodeMetricsResponse{}, err
	}
	result := graphprotocol.NodeMetricsResponse{Metrics: metrics, Generations: ready.publicGenerations()}
	if err = finishAggregate(ctx, ready, result); err != nil {
		return graphprotocol.NodeMetricsResponse{}, err
	}
	return result, nil
}

func (service *Service) aggregateNames(ctx context.Context, request graphprotocol.AggregateValuesRequest, operation string) (graphprotocol.AggregateNamesResponse, error) {
	if service == nil {
		return graphprotocol.AggregateNamesResponse{}, ErrInvalidRequest
	}
	values, err := aggregateValues(request.Values)
	if err != nil {
		return graphprotocol.AggregateNamesResponse{}, err
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, store, err := service.aggregateReady(ctx, request.Scope)
	if err != nil {
		return graphprotocol.AggregateNamesResponse{}, err
	}
	result := graphprotocol.AggregateNamesResponse{Generations: ready.publicGenerations()}
	if len(values) > 0 {
		result.Names, err = store.QueryAggregateNames(ctx, AggregateNamesQuery{Snapshot: ready.selected[0], Values: values, Operation: operation, Limit: maxAggregateValues + 1})
	}
	if err != nil {
		return graphprotocol.AggregateNamesResponse{}, err
	}
	slices.Sort(result.Names)
	if err = finishAggregate(ctx, ready, result); err != nil {
		return graphprotocol.AggregateNamesResponse{}, err
	}
	return result, nil
}

func (service *Service) AmbiguousReferencedNames(ctx context.Context, request graphprotocol.AggregateValuesRequest) (graphprotocol.AggregateNamesResponse, error) {
	return service.aggregateNames(ctx, request, "ambiguous")
}

func (service *Service) LanguagesWithExports(ctx context.Context, request graphprotocol.AggregateValuesRequest) (graphprotocol.AggregateNamesResponse, error) {
	return service.aggregateNames(ctx, request, "exports")
}

func (service *Service) UnresolvedNamesAmong(ctx context.Context, request graphprotocol.AggregateValuesRequest) (graphprotocol.AggregateNamesResponse, error) {
	return service.aggregateNames(ctx, request, "unresolved")
}

func aggregateLimit(limit, maximum int) (int, bool, error) {
	if limit < 0 {
		return 0, false, ErrInvalidRequest
	}
	if limit == 0 {
		return 0, false, nil
	}
	if limit > maximum {
		return maximum, true, nil
	}
	return limit, false, nil
}

func (service *Service) TopDependedOn(ctx context.Context, request graphprotocol.AggregateLimitRequest) (graphprotocol.TopDependedOnResponse, error) {
	if service == nil {
		return graphprotocol.TopDependedOnResponse{}, ErrInvalidRequest
	}
	limit, _, err := aggregateLimit(request.Limit, service.limits().MaxRows)
	if err != nil {
		return graphprotocol.TopDependedOnResponse{}, err
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, store, err := service.aggregateReady(ctx, request.Scope)
	if err != nil {
		return graphprotocol.TopDependedOnResponse{}, err
	}
	result := graphprotocol.TopDependedOnResponse{Generations: ready.publicGenerations()}
	if limit > 0 {
		result.Nodes, err = store.QueryTopDependedOn(ctx, AggregateLimitQuery{ready.selected[0], limit + 1})
		if len(result.Nodes) > limit {
			result.Nodes, result.Partial = result.Nodes[:limit], true
			result.Boundaries = []graphprotocol.Boundary{{Reason: "row_limit"}}
		}
	}
	if err != nil {
		return graphprotocol.TopDependedOnResponse{}, err
	}
	if err = finishAggregate(ctx, ready, result); err != nil {
		return graphprotocol.TopDependedOnResponse{}, err
	}
	return result, nil
}

func (service *Service) TopCallingFiles(ctx context.Context, request graphprotocol.AggregateLimitRequest) (graphprotocol.TopCallingFilesResponse, error) {
	if service == nil {
		return graphprotocol.TopCallingFilesResponse{}, ErrInvalidRequest
	}
	limit, _, err := aggregateLimit(request.Limit, service.limits().MaxRows)
	if err != nil {
		return graphprotocol.TopCallingFilesResponse{}, err
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, store, err := service.aggregateReady(ctx, request.Scope)
	if err != nil {
		return graphprotocol.TopCallingFilesResponse{}, err
	}
	result := graphprotocol.TopCallingFilesResponse{Generations: ready.publicGenerations()}
	if limit > 0 {
		result.Files, err = store.QueryTopCallingFiles(ctx, AggregateLimitQuery{ready.selected[0], limit + 1})
		if len(result.Files) > limit {
			result.Files, result.Partial = result.Files[:limit], true
			result.Boundaries = []graphprotocol.Boundary{{Reason: "row_limit"}}
		}
	}
	if err != nil {
		return graphprotocol.TopCallingFilesResponse{}, err
	}
	if err = finishAggregate(ctx, ready, result); err != nil {
		return graphprotocol.TopCallingFilesResponse{}, err
	}
	return result, nil
}

func (service *Service) FileNodes(ctx context.Context, request graphprotocol.FileNodesRequest) (graphprotocol.EntitiesResponse, error) {
	if service == nil {
		return graphprotocol.EntitiesResponse{}, ErrInvalidRequest
	}
	paths, err := dependencyPaths(request.Paths)
	if err != nil || len(paths) > maxAggregateValues {
		return graphprotocol.EntitiesResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, store, err := service.aggregateReady(ctx, request.Scope)
	if err != nil {
		return graphprotocol.EntitiesResponse{}, err
	}
	result := graphprotocol.EntitiesResponse{Generations: ready.publicGenerations()}
	if len(paths) > 0 {
		result.Entities, err = store.QueryFileNodes(ctx, ready.selected[0], paths)
		if err == nil {
			err = ready.publicEntities(result.Entities)
		}
	}
	if err != nil {
		return graphprotocol.EntitiesResponse{}, err
	}
	if err = finishAggregate(ctx, ready, result); err != nil {
		return graphprotocol.EntitiesResponse{}, err
	}
	return result, nil
}

func relationKinds(values []string) ([]int16, error) {
	if len(values) > 13 {
		return nil, ErrInvalidRequest
	}
	result := make([]int16, 0, len(values))
	seen := map[int16]bool{}
	for _, value := range values {
		relation, ok := graphartifact.ParseRelationship(value)
		kind := int16(relation.Kind)
		if !ok || seen[kind] {
			if !ok {
				return nil, ErrInvalidRequest
			}
			continue
		}
		seen[kind] = true
		result = append(result, kind)
	}
	return result, nil
}

func (service *Service) ModuleAggregation(ctx context.Context, request graphprotocol.ModuleAggregationRequest) (graphprotocol.ModuleAggregationResponse, error) {
	if service == nil || len(request.Assignments) > maxAggregateValues || math.IsNaN(request.MinConfidence) || math.IsInf(request.MinConfidence, 0) || request.MinConfidence < 0 || request.MinConfidence > 1 || request.TopPairsPerLink < 0 || request.TopPairsPerLink > 100 {
		return graphprotocol.ModuleAggregationResponse{}, ErrInvalidRequest
	}
	kinds, err := relationKinds(request.Kinds)
	if err != nil {
		return graphprotocol.ModuleAggregationResponse{}, err
	}
	pairKinds, err := relationKinds(request.PairKinds)
	if err != nil {
		return graphprotocol.ModuleAggregationResponse{}, err
	}
	assignments := make([]graphprotocol.ModuleAssignment, 0, len(request.Assignments))
	positions := map[string]int{}
	for _, assignment := range request.Assignments {
		path, pathErr := NormalizeFilePath(assignment.FilePath)
		if pathErr != nil || path == "." || assignment.Module == "" || len(assignment.Module) > 16_384 || !utf8.ValidString(assignment.Module) || strings.ContainsRune(assignment.Module, '\x00') {
			return graphprotocol.ModuleAggregationResponse{}, ErrInvalidRequest
		}
		assignment.FilePath = path
		if position, exists := positions[path]; exists {
			assignments[position] = assignment
		} else {
			positions[path] = len(assignments)
			assignments = append(assignments, assignment)
		}
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, store, err := service.aggregateReady(ctx, request.Scope)
	if err != nil {
		return graphprotocol.ModuleAggregationResponse{}, err
	}
	result := graphprotocol.ModuleAggregationResponse{Generations: ready.publicGenerations()}
	if len(assignments) > 0 && len(kinds) > 0 {
		rows, queryErr := store.QueryModuleAggregation(ctx, AggregateModuleQuery{ready.selected[0], assignments, kinds, pairKinds, request.MinConfidence, maxAggregateValues + 1})
		if queryErr != nil {
			return graphprotocol.ModuleAggregationResponse{}, queryErr
		}
		result.Links, result.Pairs = foldModuleRows(rows, request.TopPairsPerLink, pairKinds)
	}
	if err = finishAggregate(ctx, ready, result); err != nil {
		return graphprotocol.ModuleAggregationResponse{}, err
	}
	return result, nil
}

func foldModuleRows(rows []AggregateModuleRow, top int, pairKinds []int16) ([]graphprotocol.ModuleLink, []graphprotocol.ModulePair) {
	links := []graphprotocol.ModuleLink{}
	linkIndexes := map[string]int{}
	pairTotals := map[string]graphprotocol.ModulePair{}
	pairKindNames := map[string]bool{}
	for _, kind := range pairKinds {
		if relation, ok := graphartifact.RelationshipFromWire(graphv2.EdgeKind(kind)); ok {
			pairKindNames[relation.Name] = true
		}
	}
	for _, row := range rows {
		key := row.Source + "\x00" + row.Target + "\x00" + row.Kind
		position, ok := linkIndexes[key]
		if !ok {
			position = len(links)
			linkIndexes[key] = position
			links = append(links, graphprotocol.ModuleLink{Source: row.Source, Target: row.Target, Kind: row.Kind})
		}
		links[position].Count += row.Count
		links[position].Declared += row.Declared
		links[position].Uncertain += row.Uncertain
		if top > 0 && row.Count > 0 && pairKindNames[row.Kind] {
			pairKey := row.Source + "\x00" + row.Target + "\x00" + row.From + "\x00" + row.To
			pair := pairTotals[pairKey]
			pair.Source, pair.Target, pair.From, pair.To = row.Source, row.Target, row.From, row.To
			pair.Count += row.Count
			pair.Declared += row.Declared
			pairTotals[pairKey] = pair
		}
	}
	byLink := map[string][]graphprotocol.ModulePair{}
	keys := []string{}
	for _, pair := range pairTotals {
		key := pair.Source + "\x00" + pair.Target
		if byLink[key] == nil {
			keys = append(keys, key)
		}
		byLink[key] = append(byLink[key], pair)
	}
	slices.Sort(keys)
	pairs := []graphprotocol.ModulePair{}
	for _, key := range keys {
		values := byLink[key]
		sort.Slice(values, func(i, j int) bool {
			leftFrom, rightFrom := strings.ToLower(values[i].From), strings.ToLower(values[j].From)
			leftTo, rightTo := strings.ToLower(values[i].To), strings.ToLower(values[j].To)
			return values[i].Declared > values[j].Declared || values[i].Declared == values[j].Declared && (values[i].Count > values[j].Count || values[i].Count == values[j].Count && (leftFrom < rightFrom || leftFrom == rightFrom && (leftTo < rightTo || leftTo == rightTo && (values[i].From < values[j].From || values[i].From == values[j].From && values[i].To < values[j].To))))
		})
		pairs = append(pairs, values[:min(top, len(values))]...)
	}
	return links, pairs
}

func (service *Service) unresolvedReferences(ctx context.Context, request graphprotocol.UnresolvedReferencesRequest, byPath bool) (graphprotocol.UnresolvedReferencesResponse, error) {
	if service == nil || byPath == (request.Path == "") || !byPath && request.Occurrence == "" {
		return graphprotocol.UnresolvedReferencesResponse{}, ErrInvalidRequest
	}
	value := request.Occurrence
	if byPath {
		var err error
		value, err = NormalizeFilePath(request.Path)
		if err != nil || value == "." {
			return graphprotocol.UnresolvedReferencesResponse{}, ErrInvalidRequest
		}
	} else if len(value) > 16_384 || !utf8.ValidString(value) {
		return graphprotocol.UnresolvedReferencesResponse{}, ErrInvalidRequest
	}
	limit := maxAggregateValues
	if request.Limit != nil {
		if *request.Limit < 0 || *request.Limit > maxAggregateValues {
			return graphprotocol.UnresolvedReferencesResponse{}, ErrInvalidRequest
		}
		limit = *request.Limit
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, store, err := service.aggregateReady(ctx, request.Scope)
	if err != nil {
		return graphprotocol.UnresolvedReferencesResponse{}, err
	}
	result := graphprotocol.UnresolvedReferencesResponse{Generations: ready.publicGenerations()}
	if limit > 0 {
		query := AggregateUnresolvedQuery{Snapshot: ready.selected[0], Limit: maxAggregateValues + 1}
		if byPath {
			query.Path = value
		} else {
			query.Values = []string{value}
		}
		result.References, err = store.QueryUnresolvedReferences(ctx, query)
		if err == nil && len(result.References) > maxAggregateValues {
			err = ErrQuerySize
		} else if err == nil && len(result.References) > limit {
			result.References = result.References[:limit]
		}
	}
	if err != nil {
		return graphprotocol.UnresolvedReferencesResponse{}, err
	}
	for index := range result.References {
		if result.References[index].RepositoryID != ready.selected[0].RepositoryID || result.References[index].Fact == nil {
			return graphprotocol.UnresolvedReferencesResponse{}, ErrGenerationChanged
		}
		result.References[index].RepositoryID = ready.publicIDs[result.References[index].RepositoryID]
		if result.References[index].RepositoryID == 0 {
			return graphprotocol.UnresolvedReferencesResponse{}, ErrGenerationChanged
		}
	}
	if err = finishAggregate(ctx, ready, result); err != nil {
		return graphprotocol.UnresolvedReferencesResponse{}, err
	}
	return result, nil
}

func (service *Service) UnresolvedReferencesFrom(ctx context.Context, request graphprotocol.UnresolvedReferencesRequest) (graphprotocol.UnresolvedReferencesResponse, error) {
	return service.unresolvedReferences(ctx, request, false)
}

func (service *Service) UnresolvedReferencesInFile(ctx context.Context, request graphprotocol.UnresolvedReferencesRequest) (graphprotocol.UnresolvedReferencesResponse, error) {
	return service.unresolvedReferences(ctx, request, true)
}
