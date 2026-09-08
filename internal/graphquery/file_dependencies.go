package graphquery

import (
	"context"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/balcsida/graphnest/internal/graphprotocol"
)

const maxFileDependencyPaths = 64

type FileDependencyQuery struct {
	Snapshot      QuerySnapshot
	Paths         []string
	Direction     string
	MinConfidence float64
	Limit         int
}

type FileDependencyPairRow struct {
	RepositoryID int64
	Source       string
	Target       string
	References   int
}

type FileDependencyStore interface {
	QueryFileDependencyPairs(context.Context, FileDependencyQuery) ([]FileDependencyPairRow, error)
}

func (service *Service) dependencyRequest(ctx context.Context, request graphprotocol.FileDependencyRequest, direction string) (entityReady, []FileDependencyPairRow, []graphprotocol.Boundary, bool, error) {
	if service == nil || request.Scope.SelectedRepositoryID == 0 || len(request.Paths) > maxFileDependencyPaths || request.Limit < 0 || math.IsNaN(request.MinConfidence) || math.IsInf(request.MinConfidence, 0) || request.MinConfidence < 0 || request.MinConfidence > 1 {
		return entityReady{}, nil, nil, false, ErrInvalidRequest
	}
	paths, err := dependencyPaths(request.Paths)
	if err != nil {
		return entityReady{}, nil, nil, false, err
	}
	ready, err := service.readyEntities(ctx, request.Scope)
	if err != nil {
		return entityReady{}, nil, nil, false, err
	}
	if direction != "" && len(paths) == 0 {
		return ready, nil, nil, false, nil
	}
	limit := service.limits().MaxEdges
	if request.Limit > 0 && request.Limit < limit {
		limit = request.Limit
	}
	rows, truncated, err := dependencyRows(ctx, ready, paths, direction, request.MinConfidence, limit)
	if err != nil {
		return entityReady{}, nil, nil, false, err
	}
	var boundaries []graphprotocol.Boundary
	if truncated {
		boundaries = append(boundaries, graphprotocol.Boundary{Reason: "edge_limit"})
	}
	return ready, rows, boundaries, truncated, nil
}

func dependencyPaths(values []string) ([]string, error) {
	paths := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		path, err := NormalizeFilePath(value)
		if err != nil || path == "." {
			return nil, ErrInvalidRequest
		}
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func dependencyRows(ctx context.Context, ready entityReady, paths []string, direction string, confidence float64, limit int) ([]FileDependencyPairRow, bool, error) {
	store, ok := ready.store.(FileDependencyStore)
	if !ok || len(ready.selected) != 1 || direction != "" && direction != "source" && direction != "target" || limit <= 0 {
		return nil, false, ErrInvalidRequest
	}
	rows, err := store.QueryFileDependencyPairs(ctx, FileDependencyQuery{Snapshot: ready.selected[0], Paths: paths, Direction: direction, MinConfidence: confidence, Limit: limit + 1})
	if err != nil {
		return nil, false, err
	}
	for _, row := range rows {
		if row.RepositoryID != ready.selected[0].RepositoryID || row.References <= 0 || !validDependencyPath(row.Source) || !validDependencyPath(row.Target) || row.Source == row.Target {
			return nil, false, ErrGenerationChanged
		}
	}
	if len(rows) > limit {
		return rows[:limit], true, nil
	}
	return rows, false, nil
}

func validDependencyPath(value string) bool {
	path, err := NormalizeFilePath(value)
	return err == nil && path != "." && path == value
}

func fileDependencyResponse(ctx context.Context, ready entityReady, response graphprotocol.FileDependencyResponse) (graphprotocol.FileDependencyResponse, error) {
	response.Generations = ready.publicGenerations()
	if err := entityResponseSize(response); err != nil {
		return graphprotocol.FileDependencyResponse{}, err
	}
	if err := ready.current(ctx); err != nil {
		return graphprotocol.FileDependencyResponse{}, err
	}
	return response, nil
}

func (service *Service) FileDependencyPairs(ctx context.Context, request graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	if service == nil {
		return graphprotocol.FileDependencyResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, rows, boundaries, partial, err := service.dependencyRequest(ctx, request, "")
	if err != nil {
		return graphprotocol.FileDependencyResponse{}, err
	}
	response := graphprotocol.FileDependencyResponse{Boundaries: boundaries, Partial: partial}
	for _, row := range rows {
		response.Pairs = append(response.Pairs, graphprotocol.FileDependencyPair{Source: row.Source, Target: row.Target, References: row.References})
	}
	return fileDependencyResponse(ctx, ready, response)
}

func (service *Service) FileDependencies(ctx context.Context, request graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	return service.fileDependencyList(ctx, request, "source")
}

func (service *Service) FileDependents(ctx context.Context, request graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	return service.fileDependencyList(ctx, request, "target")
}

func (service *Service) fileDependencyList(ctx context.Context, request graphprotocol.FileDependencyRequest, direction string) (graphprotocol.FileDependencyResponse, error) {
	if service == nil || len(request.Paths) != 1 {
		return graphprotocol.FileDependencyResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, rows, boundaries, partial, err := service.dependencyRequest(ctx, request, direction)
	if err != nil {
		return graphprotocol.FileDependencyResponse{}, err
	}
	files := make([]string, 0, len(rows))
	for _, row := range rows {
		if direction == "source" {
			files = append(files, row.Target)
		} else {
			files = append(files, row.Source)
		}
	}
	slices.Sort(files)
	return fileDependencyResponse(ctx, ready, graphprotocol.FileDependencyResponse{Files: files, Boundaries: boundaries, Partial: partial})
}

func (service *Service) FileDependentCounts(ctx context.Context, request graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	return service.fileDependencyCounts(ctx, request, "target")
}

func (service *Service) FileReachCounts(ctx context.Context, request graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	return service.fileDependencyCounts(ctx, request, "source")
}

func (service *Service) fileDependencyCounts(ctx context.Context, request graphprotocol.FileDependencyRequest, direction string) (graphprotocol.FileDependencyResponse, error) {
	if service == nil {
		return graphprotocol.FileDependencyResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, rows, boundaries, partial, err := service.dependencyRequest(ctx, request, direction)
	if err != nil {
		return graphprotocol.FileDependencyResponse{}, err
	}
	counts := map[string]*graphprotocol.FileDependencyCount{}
	for _, row := range rows {
		path := row.Source
		if direction == "target" {
			path = row.Target
		}
		count := counts[path]
		if count == nil {
			count = &graphprotocol.FileDependencyCount{Path: path}
			counts[path] = count
		}
		if direction == "target" {
			count.Dependents++
		} else {
			count.Reaches++
			count.References += row.References
		}
	}
	paths := make([]string, 0, len(counts))
	for path := range counts {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	response := graphprotocol.FileDependencyResponse{Boundaries: boundaries, Partial: partial}
	for _, path := range paths {
		response.Counts = append(response.Counts, *counts[path])
	}
	return fileDependencyResponse(ctx, ready, response)
}

func (service *Service) CircularDependencies(ctx context.Context, request graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	if service == nil || len(request.Paths) != 0 {
		return graphprotocol.FileDependencyResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, rows, boundaries, partial, err := service.dependencyRequest(ctx, request, "")
	if err != nil {
		return graphprotocol.FileDependencyResponse{}, err
	}
	return fileDependencyResponse(ctx, ready, graphprotocol.FileDependencyResponse{Cycles: dependencyCycles(rows), Boundaries: boundaries, Partial: partial})
}

func dependencyCycles(pairs []FileDependencyPairRow) [][]string {
	graph := map[string][]string{}
	for _, pair := range pairs {
		graph[pair.Source] = append(graph[pair.Source], pair.Target)
		if graph[pair.Target] == nil {
			graph[pair.Target] = []string{}
		}
	}
	for node := range graph {
		slices.Sort(graph[node])
	}
	visited, recursionStack := map[string]bool{}, map[string]bool{}
	cycles := [][]string{}
	var visit func(string, []string)
	visit = func(node string, path []string) {
		if recursionStack[node] {
			if start := slices.Index(path, node); start >= 0 {
				cycles = append(cycles, slices.Clone(path[start:]))
			}
			return
		}
		if visited[node] {
			return
		}
		visited[node], recursionStack[node] = true, true
		for _, next := range graph[node] {
			visit(next, append(slices.Clone(path), node))
		}
		delete(recursionStack, node)
	}
	nodes := make([]string, 0, len(graph))
	for node := range graph {
		nodes = append(nodes, node)
	}
	slices.Sort(nodes)
	for _, node := range nodes {
		if !visited[node] {
			visit(node, nil)
		}
	}
	return cycles
}

func (service *Service) AffectedTests(ctx context.Context, request graphprotocol.AffectedTestsRequest) (graphprotocol.AffectedTestsResponse, error) {
	if service == nil || request.Scope.SelectedRepositoryID == 0 || len(request.ChangedFiles) == 0 || len(request.ChangedFiles) > maxFileDependencyPaths || request.MaxDepth < 0 || request.Limit < 0 || len(request.TestGlob) > 1024 || !utf8.ValidString(request.TestGlob) || strings.ContainsRune(request.TestGlob, '\x00') {
		return graphprotocol.AffectedTestsResponse{}, ErrInvalidRequest
	}
	changed := make([]string, 0, len(request.ChangedFiles))
	for _, value := range request.ChangedFiles {
		path, err := NormalizeFilePath(value)
		if err != nil || path == "." {
			return graphprotocol.AffectedTestsResponse{}, ErrInvalidRequest
		}
		changed = append(changed, path)
	}
	isTest, err := affectedTestMatcher(request.TestGlob)
	if err != nil {
		return graphprotocol.AffectedTestsResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, err := service.readyEntities(ctx, request.Scope)
	if err != nil {
		return graphprotocol.AffectedTestsResponse{}, err
	}
	limit := service.limits().MaxEdges
	if request.Limit > 0 && request.Limit < limit {
		limit = request.Limit
	}
	depthLimit := request.MaxDepth
	if depthLimit == 0 {
		depthLimit = 5
	}
	if depthLimit > service.limits().MaxDepth {
		depthLimit = service.limits().MaxDepth
	}
	affected, traversed := map[string]bool{}, map[string]bool{}
	response := graphprotocol.AffectedTestsResponse{ChangedFiles: changed, AffectedTests: []string{}}
	usedRows := 0
	for _, root := range changed {
		if isTest(root) {
			affected[root] = true
			continue
		}
		visited := map[string]bool{root: true}
		frontier := []string{root}
		for depth := 0; depth < depthLimit && len(frontier) > 0; depth++ {
			remaining := limit - usedRows
			if remaining <= 0 {
				response.Boundaries = appendBoundary(response.Boundaries, "edge_limit", depth)
				response.Partial = true
				break
			}
			rows, truncated, queryErr := dependencyRows(ctx, ready, frontier, "target", 0, remaining)
			if queryErr != nil {
				return graphprotocol.AffectedTestsResponse{}, queryErr
			}
			usedRows += len(rows)
			next := []string{}
			for _, pair := range rows {
				if visited[pair.Source] {
					continue
				}
				visited[pair.Source] = true
				traversed[pair.Source] = true
				if isTest(pair.Source) {
					affected[pair.Source] = true
				} else {
					next = append(next, pair.Source)
				}
			}
			if truncated {
				response.Boundaries = appendBoundary(response.Boundaries, "edge_limit", depth+1)
				response.Partial = true
				break
			}
			frontier = next
			if depth+1 == depthLimit && len(frontier) > 0 {
				response.Boundaries = appendBoundary(response.Boundaries, "depth_limit", depthLimit)
				response.Partial = true
			}
		}
	}
	for path := range affected {
		response.AffectedTests = append(response.AffectedTests, path)
	}
	slices.Sort(response.AffectedTests)
	response.TotalDependentsTraversed = len(traversed)
	response.Generations = ready.publicGenerations()
	if err := entityResponseSize(response); err != nil {
		return graphprotocol.AffectedTestsResponse{}, err
	}
	if err := ready.current(ctx); err != nil {
		return graphprotocol.AffectedTestsResponse{}, err
	}
	return response, nil
}

func affectedTestMatcher(glob string) (func(string) bool, error) {
	if glob == "" {
		return func(path string) bool {
			return strings.Contains(path, ".spec.") || strings.Contains(path, ".test.") || strings.Contains(path, "/__tests__/") || strings.Contains(path, "/test/") || strings.Contains(path, "/tests/") || strings.Contains(path, "/e2e/") || strings.Contains(path, "/spec/")
		}, nil
	}
	var expression strings.Builder
	for offset := 0; offset < len(glob); offset++ {
		if glob[offset] == '*' {
			if offset+1 < len(glob) && glob[offset+1] == '*' {
				expression.WriteString(".+")
				offset++
			} else {
				expression.WriteString("[^/]*")
			}
			continue
		}
		if strings.ContainsRune(`+[]{}()^$|\`, rune(glob[offset])) || glob[offset] == '.' {
			expression.WriteByte('\\')
		}
		expression.WriteByte(glob[offset])
	}
	pattern, err := regexp.Compile(expression.String())
	if err != nil {
		return nil, err
	}
	return pattern.MatchString, nil
}
