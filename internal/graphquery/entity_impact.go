package graphquery

import (
	"context"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphprotocol"
)

var callGraphRelations = []string{"calls", "references", "imports", "instantiates", "navigates"}

var qualifiedNameKinds = []string{"class", "union", "function", "method", "interface", "type_alias", "variable", "constant"}

var filteredSubgraphKinds = []string{"file", "module", "class", "struct", "union", "interface", "trait", "function", "method", "variable", "constant", "enum", "type_alias"}

type AnalysisEntityQuery struct {
	Snapshot         QuerySnapshot
	Paths, Kinds     []string
	QualifiedPattern string
	QualifiedLiteral *string
	QualifiedParts   []string
	QualifiedPrefix  string
	QualifiedSuffix  string
	Exported         *bool
	Order            string
	Limit            int
}

type AnalysisEvidenceQuery struct {
	Snapshot    QuerySnapshot
	Occurrences []string
	Limit       int
}

type AnalysisStore interface {
	EntityStore
	QueryAnalysisEntities(context.Context, AnalysisEntityQuery) ([]graphprotocol.Entity, error)
	QueryAnalysisEvidence(context.Context, AnalysisEvidenceQuery) ([]graphprotocol.Evidence, error)
}

func (service *Service) analysisReady(ctx context.Context, scope graphprotocol.Scope) (entityReady, AnalysisStore, error) {
	ready, err := service.readyEntities(ctx, scope)
	if err != nil {
		return entityReady{}, nil, err
	}
	store, ok := ready.store.(AnalysisStore)
	if !ok || len(ready.selected) != 1 {
		return entityReady{}, nil, ErrInvalidRequest
	}
	return ready, store, nil
}

func analysisDepth(value *int, fallback, ceiling int) (int, bool, error) {
	if value != nil && *value < 0 {
		return 0, false, ErrInvalidRequest
	}
	depth := fallback
	if value != nil {
		depth = *value
	}
	if depth > ceiling {
		return ceiling, true, nil
	}
	return depth, false, nil
}

func analysisRoot(ctx context.Context, ready entityReady, occurrence string) (graphprotocol.Entity, string, error) {
	if occurrence == "" || len(occurrence) > 16_384 || !utf8.ValidString(occurrence) {
		return graphprotocol.Entity{}, "", ErrInvalidRequest
	}
	rows, err := ready.store.QueryEntities(ctx, EntityQuery{Snapshots: ready.selected, Selector: graphprotocol.EntitySelector{Occurrence: &occurrence}, Limit: 2})
	if err != nil {
		return graphprotocol.Entity{}, "", err
	}
	if len(rows) == 0 {
		return graphprotocol.Entity{}, graphprotocol.StatusNotFound, nil
	}
	if len(rows) != 1 || rows[0].RepositoryID != ready.selected[0].RepositoryID || rows[0].Fact == nil || rows[0].Fact.Occurrence != occurrence {
		return graphprotocol.Entity{}, "", ErrGenerationChanged
	}
	return rows[0], graphprotocol.StatusOK, nil
}

func finishSubgraph(ctx context.Context, ready entityReady, result graphprotocol.SubgraphResponse) (graphprotocol.SubgraphResponse, error) {
	ids := make(map[string]bool, len(result.Entities))
	for _, entity := range result.Entities {
		ids[entity.ID] = true
	}
	for _, edge := range result.Edges {
		if !ids[edge.SourceID] || !ids[edge.TargetID] {
			return graphprotocol.SubgraphResponse{}, ErrGenerationChanged
		}
	}
	if err := ready.publicEntities(result.Entities); err != nil {
		return graphprotocol.SubgraphResponse{}, err
	}
	for i := range result.Edges {
		id := ready.publicIDs[result.Edges[i].RepositoryID]
		if id == 0 {
			return graphprotocol.SubgraphResponse{}, ErrGenerationChanged
		}
		result.Edges[i].RepositoryID = id
	}
	result.Generations = ready.publicGenerations()
	if err := ready.current(ctx); err != nil {
		return graphprotocol.SubgraphResponse{}, err
	}
	result.Analysis = analysisState(ready)
	if err := entityResponseSize(result); err != nil {
		return graphprotocol.SubgraphResponse{}, err
	}
	return result, nil
}

func analysisState(ready entityReady) *graphprotocol.AnalysisState {
	unresolved := 0
	for _, selected := range ready.selected {
		for _, generation := range ready.generations {
			if generation.RepositoryID == selected.RepositoryID {
				unresolved += generation.UnresolvedCount
				break
			}
		}
	}
	return &graphprotocol.AnalysisState{
		Freshness:  graphprotocol.AnalysisFreshnessCurrent,
		Coverage:   graphprotocol.AnalysisCoverageNotAssessed,
		Unresolved: unresolved,
		Complete:   true,
	}
}

func (service *Service) CallGraph(ctx context.Context, request graphprotocol.EntityImpactRequest) (graphprotocol.SubgraphResponse, error) {
	if service == nil {
		return graphprotocol.SubgraphResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, _, err := service.analysisReady(ctx, request.Scope)
	if err != nil {
		return graphprotocol.SubgraphResponse{}, err
	}
	depth, capped, err := analysisDepth(request.MaxDepth, 2, service.limits().MaxDepth)
	if err != nil {
		return graphprotocol.SubgraphResponse{}, err
	}
	root, status, err := analysisRoot(ctx, ready, request.Occurrence)
	if err != nil {
		return graphprotocol.SubgraphResponse{}, err
	}
	result := graphprotocol.SubgraphResponse{Status: status}
	if status == graphprotocol.StatusNotFound {
		return finishSubgraph(ctx, ready, result)
	}
	result.Roots, result.Entities = []string{root.ID}, []graphprotocol.Entity{root}
	if capped {
		result.Partial = true
		result.Boundaries = appendBoundary(result.Boundaries, "depth_limit", depth)
	}
	limits := service.limits()
	entityIndexes := map[string]int{root.Fact.Occurrence: 0}
	seenEdges := map[string]bool{}
	for _, direction := range []string{"incoming", "outgoing"} {
		visited := map[string]bool{root.Fact.Occurrence: true}
		var walk func(graphprotocol.Entity, int) error
		walk = func(parent graphprotocol.Entity, level int) error {
			rows, partial, queryErr := analysisNeighbors(ctx, ready, parent, callGraphRelations, direction, limits.MaxFanout)
			if queryErr != nil {
				return queryErr
			}
			if partial {
				result.Partial = true
				result.Boundaries = appendBoundary(result.Boundaries, "fanout_limit", level+1)
			}
			if level >= depth {
				if len(rows) > 0 {
					result.Partial = true
					result.Boundaries = appendBoundary(result.Boundaries, "depth_limit", depth)
				}
				return nil
			}
			for _, row := range rows {
				occurrence := row.Entity.Fact.Occurrence
				if visited[occurrence] {
					continue
				}
				index, entityExists := entityIndexes[occurrence]
				edgeExists := seenEdges[row.Edge.Fact.Occurrence]
				if !entityExists && len(result.Entities) >= limits.MaxNodes {
					result.Partial = true
					result.Boundaries = appendBoundary(result.Boundaries, "node_limit", level+1)
					continue
				}
				if !edgeExists && len(result.Edges) >= limits.MaxEdges {
					result.Partial = true
					result.Boundaries = appendBoundary(result.Boundaries, "edge_limit", level+1)
					continue
				}
				if !entityExists {
					row.Entity.Depth = level + 1
					index = len(result.Entities)
					entityIndexes[occurrence] = index
					result.Entities = append(result.Entities, row.Entity)
				} else if level+1 < result.Entities[index].Depth {
					result.Entities[index].Depth = level + 1
				}
				if !edgeExists {
					seenEdges[row.Edge.Fact.Occurrence] = true
					result.Edges = append(result.Edges, row.Edge)
				}
				visited[occurrence] = true
				if err := walk(row.Entity, level+1); err != nil {
					return err
				}
			}
			return nil
		}
		if err := walk(root, 0); err != nil {
			return graphprotocol.SubgraphResponse{}, err
		}
	}
	return finishSubgraph(ctx, ready, result)
}

func analysisNeighbors(ctx context.Context, ready entityReady, parent graphprotocol.Entity, relations []string, direction string, limit int) ([]EntityNeighbor, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	var snapshot QuerySnapshot
	for _, candidate := range ready.selected {
		if candidate.RepositoryID == parent.RepositoryID {
			snapshot = candidate
			break
		}
	}
	if snapshot.UploadID == 0 || parent.Fact == nil {
		return nil, false, ErrGenerationChanged
	}
	rows := []EntityNeighbor{}
	partial := false
	for _, relation := range relations {
		remaining := limit - len(rows)
		if remaining <= 0 {
			partial = true
			break
		}
		perRelation := min(remaining, 100)
		found, err := ready.store.EntityNeighbors(ctx, EntityNeighborQuery{Snapshot: snapshot, Occurrence: parent.Fact.Occurrence, Relation: relation, Direction: direction, Limit: perRelation + 1})
		if err != nil {
			return nil, false, err
		}
		if len(found) > perRelation {
			found, partial = found[:perRelation], true
		}
		for _, row := range found {
			if row.Entity.RepositoryID != parent.RepositoryID || row.Entity.Fact == nil || row.Edge.RepositoryID != parent.RepositoryID || row.Edge.Fact == nil {
				return nil, false, ErrGenerationChanged
			}
		}
		rows = append(rows, found...)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i].Edge.Fact, rows[j].Edge.Fact
		aRelation, _ := graphartifact.RelationshipFromWire(a.Kind)
		bRelation, _ := graphartifact.RelationshipFromWire(b.Kind)
		aRank, bRank := slices.Index(relations, aRelation.Name), slices.Index(relations, bRelation.Name)
		if aRank != bRank {
			return aRank < bRank
		}
		aID, aErr := strconv.ParseUint(a.SourceId, 10, 64)
		bID, bErr := strconv.ParseUint(b.SourceId, 10, 64)
		if aErr == nil && bErr == nil && aID != bID {
			return aID < bID
		}
		if a.SourceId != b.SourceId {
			return a.SourceId < b.SourceId
		}
		if a.Occurrence != b.Occurrence {
			return a.Occurrence < b.Occurrence
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Target < b.Target
	})
	return rows, partial, nil
}

func (service *Service) Usages(ctx context.Context, request graphprotocol.EntityRequest) (graphprotocol.UsagesResponse, error) {
	if service == nil {
		return graphprotocol.UsagesResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, _, err := service.analysisReady(ctx, request.Scope)
	if err != nil {
		return graphprotocol.UsagesResponse{}, err
	}
	root, status, err := analysisRoot(ctx, ready, request.Occurrence)
	if err != nil {
		return graphprotocol.UsagesResponse{}, err
	}
	result := graphprotocol.UsagesResponse{Status: status, Generations: ready.publicGenerations()}
	if status == graphprotocol.StatusOK {
		relations := make([]string, 0, len(graphartifact.Relationships()))
		for _, relation := range graphartifact.Relationships() {
			relations = append(relations, relation.Name)
		}
		rows, partial, queryErr := analysisNeighbors(ctx, ready, root, relations, "incoming", service.limits().MaxEdges)
		if queryErr != nil {
			return graphprotocol.UsagesResponse{}, queryErr
		}
		result.Partial = partial
		if partial {
			result.Boundaries = appendBoundary(result.Boundaries, "edge_limit", 1)
		}
		result.Usages = make([]graphprotocol.Usage, len(rows))
		for i, row := range rows {
			result.Usages[i] = graphprotocol.Usage{Entity: row.Entity, Edge: row.Edge}
		}
		entities := make([]graphprotocol.Entity, len(result.Usages))
		for i := range result.Usages {
			entities[i] = result.Usages[i].Entity
		}
		if err = ready.publicEntities(entities); err != nil {
			return graphprotocol.UsagesResponse{}, err
		}
		for i := range result.Usages {
			result.Usages[i].Entity = entities[i]
			id := ready.publicIDs[result.Usages[i].Edge.RepositoryID]
			if id == 0 {
				return graphprotocol.UsagesResponse{}, ErrGenerationChanged
			}
			result.Usages[i].Edge.RepositoryID = id
		}
	}
	if err = ready.current(ctx); err != nil {
		return graphprotocol.UsagesResponse{}, err
	}
	result.Analysis = analysisState(ready)
	if err = entityResponseSize(result); err != nil {
		return graphprotocol.UsagesResponse{}, err
	}
	return result, nil
}

func (service *Service) ImpactRadius(ctx context.Context, request graphprotocol.EntityImpactRequest) (graphprotocol.SubgraphResponse, error) {
	if service == nil {
		return graphprotocol.SubgraphResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, _, err := service.analysisReady(ctx, request.Scope)
	if err != nil {
		return graphprotocol.SubgraphResponse{}, err
	}
	depth, capped, err := analysisDepth(request.MaxDepth, 3, service.limits().MaxDepth)
	if err != nil {
		return graphprotocol.SubgraphResponse{}, err
	}
	root, status, err := analysisRoot(ctx, ready, request.Occurrence)
	if err != nil {
		return graphprotocol.SubgraphResponse{}, err
	}
	result := graphprotocol.SubgraphResponse{Status: status}
	if status == graphprotocol.StatusNotFound {
		return finishSubgraph(ctx, ready, result)
	}
	result.Roots, result.Entities = []string{root.ID}, []graphprotocol.Entity{root}
	if capped {
		result.Partial = true
		result.Boundaries = appendBoundary(result.Boundaries, "depth_limit", depth)
	}
	limits := service.limits()
	bestDepth := map[string]int{root.Fact.Occurrence: 0}
	entityIndexes := map[string]int{root.Fact.Occurrence: 0}
	seenEdges := map[string]bool{}
	nonContains := []string{}
	for _, relation := range graphartifact.Relationships() {
		if relation.Name != "contains" {
			nonContains = append(nonContains, relation.Name)
		}
	}
	admit := func(row EntityNeighbor, rowDepth int) (bool, bool) {
		occurrence := row.Entity.Fact.Occurrence
		index, entityExists := entityIndexes[occurrence]
		oldDepth := bestDepth[occurrence]
		edgeExists := seenEdges[row.Edge.Fact.Occurrence]
		if !entityExists && len(result.Entities) >= limits.MaxNodes {
			result.Partial = true
			result.Boundaries = appendBoundary(result.Boundaries, "node_limit", rowDepth)
			return false, false
		}
		if !edgeExists && len(result.Edges) >= limits.MaxEdges {
			result.Partial = true
			result.Boundaries = appendBoundary(result.Boundaries, "edge_limit", rowDepth)
			return false, false
		}
		if !entityExists {
			row.Entity.Depth = rowDepth
			index = len(result.Entities)
			entityIndexes[occurrence] = index
			bestDepth[occurrence] = rowDepth
			result.Entities = append(result.Entities, row.Entity)
		} else if rowDepth < oldDepth {
			bestDepth[occurrence] = rowDepth
			result.Entities[index].Depth = rowDepth
		}
		if !edgeExists {
			seenEdges[row.Edge.Fact.Occurrence] = true
			result.Edges = append(result.Edges, row.Edge)
		}
		return true, !entityExists || rowDepth < oldDepth
	}
	levels := make([][]graphprotocol.Entity, depth+1)
	levels[0] = append(levels[0], root)
	for level := 0; level <= depth; level++ {
		for index := 0; index < len(levels[level]); index++ {
			parent := levels[level][index]
			if bestDepth[parent.Fact.Occurrence] != level {
				continue
			}
			if level >= depth {
				rows, partial, queryErr := analysisNeighbors(ctx, ready, parent, nonContains, "incoming", limits.MaxFanout)
				if queryErr != nil {
					return graphprotocol.SubgraphResponse{}, queryErr
				}
				if isImpactContainer(parent.Fact.Kind) {
					members, memberPartial, memberErr := analysisNeighbors(ctx, ready, parent, []string{"contains"}, "outgoing", limits.MaxFanout)
					if memberErr != nil {
						return graphprotocol.SubgraphResponse{}, memberErr
					}
					rows, partial = append(rows, members...), partial || memberPartial
				}
				if len(rows) > 0 || partial {
					result.Partial = true
					result.Boundaries = appendBoundary(result.Boundaries, "depth_limit", depth)
				}
				continue
			}
			if isImpactContainer(parent.Fact.Kind) {
				members, partial, queryErr := analysisNeighbors(ctx, ready, parent, []string{"contains"}, "outgoing", limits.MaxFanout)
				if queryErr != nil {
					return graphprotocol.SubgraphResponse{}, queryErr
				}
				if partial {
					result.Partial = true
					result.Boundaries = appendBoundary(result.Boundaries, "fanout_limit", level)
				}
				for _, row := range members {
					admitted, schedule := admit(row, level)
					if admitted && schedule {
						levels[level] = append(levels[level], row.Entity)
					}
				}
			}
			rows, partial, queryErr := analysisNeighbors(ctx, ready, parent, nonContains, "incoming", limits.MaxFanout)
			if queryErr != nil {
				return graphprotocol.SubgraphResponse{}, queryErr
			}
			if partial {
				result.Partial = true
				result.Boundaries = appendBoundary(result.Boundaries, "fanout_limit", level+1)
			}
			for _, row := range rows {
				rowDepth := level + 1
				admitted, schedule := admit(row, rowDepth)
				if admitted && schedule {
					levels[rowDepth] = append(levels[rowDepth], row.Entity)
				}
			}
		}
	}
	result.Blast = blastSummary(root, result.Entities, result.Edges, depth)
	return finishSubgraph(ctx, ready, result)
}

func isImpactContainer(kind string) bool {
	switch kind {
	case "class", "interface", "struct", "union", "trait", "protocol", "module", "enum":
		return true
	}
	return false
}

func blastSummary(root graphprotocol.Entity, entities []graphprotocol.Entity, edges []graphprotocol.Evidence, hops int) *graphprotocol.BlastSummary {
	direct := map[string]bool{}
	for _, edge := range edges {
		if edge.TargetID == root.ID {
			direct[edge.SourceID] = true
		}
	}
	isTest, _ := affectedTestMatcher("")
	files := map[string]*graphprotocol.BlastFile{}
	routes := 0
	for _, entity := range entities[1:] {
		if entity.Fact.Kind == "route" {
			routes++
		}
		file := entity.Fact.GetPath()
		if file == "" {
			continue
		}
		entry := files[file]
		if entry == nil {
			entry = &graphprotocol.BlastFile{File: file, Test: isTest(file)}
			files[file] = entry
		}
		entry.Symbols++
	}
	top := make([]graphprotocol.BlastFile, 0, len(files))
	testFiles := 0
	for _, file := range files {
		top = append(top, *file)
		if file.Test {
			testFiles++
		}
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Symbols != top[j].Symbols {
			return top[i].Symbols > top[j].Symbols
		}
		return top[i].File < top[j].File
	})
	if len(top) > 40 {
		top = top[:40]
	}
	return &graphprotocol.BlastSummary{Direct: len(direct), WithinHops: len(entities) - 1, Hops: hops, Files: len(files), TestFiles: testFiles, Routes: routes, TopFiles: top}
}

func (service *Service) ExportedSymbols(ctx context.Context, request graphprotocol.FileEntityRequest) (graphprotocol.EntitiesResponse, error) {
	path, err := NormalizeFilePath(request.Path)
	if service == nil || err != nil || path == "." {
		return graphprotocol.EntitiesResponse{}, ErrInvalidRequest
	}
	value := true
	return service.analysisEntities(ctx, request.Scope, AnalysisEntityQuery{Paths: []string{path}, Exported: &value, Order: "line"})
}

func (service *Service) FindByQualifiedName(ctx context.Context, request graphprotocol.QualifiedNameRequest) (graphprotocol.EntitiesResponse, error) {
	pattern, literal, parts, prefix, suffix, err := qualifiedNamePattern(request.Pattern)
	if service == nil || err != nil {
		return graphprotocol.EntitiesResponse{}, ErrInvalidRequest
	}
	return service.analysisEntities(ctx, request.Scope, AnalysisEntityQuery{
		Kinds: qualifiedNameKinds, QualifiedPattern: pattern, QualifiedLiteral: literal,
		QualifiedParts: parts, QualifiedPrefix: prefix, QualifiedSuffix: suffix, Order: "kind",
	})
}

func qualifiedNamePattern(pattern string) (string, *string, []string, string, string, error) {
	if pattern == "" || len(pattern) > 16_384 || !utf8.ValidString(pattern) {
		return "", nil, nil, "", "", ErrInvalidRequest
	}
	var result, part strings.Builder
	parts := []string{}
	wildcard := false
	flush := func() {
		if part.Len() > 0 {
			parts = append(parts, part.String())
			part.Reset()
		}
	}
	result.WriteByte('^')
	for _, value := range pattern {
		switch value {
		case '*':
			flush()
			wildcard = true
			result.WriteString(".*")
		case '?':
			flush()
			wildcard = true
			result.WriteByte('.')
		default:
			part.WriteRune(value)
			result.WriteString(regexp.QuoteMeta(string(value)))
		}
	}
	flush()
	result.WriteByte('$')
	if !wildcard {
		literal := pattern
		return result.String(), &literal, parts, pattern, pattern, nil
	}
	prefix, suffix := "", ""
	if pattern[0] != '*' && pattern[0] != '?' {
		prefix = parts[0]
	}
	if last := pattern[len(pattern)-1]; last != '*' && last != '?' {
		suffix = parts[len(parts)-1]
	}
	return result.String(), nil, parts, prefix, suffix, nil
}

func (service *Service) analysisEntities(ctx context.Context, scope graphprotocol.Scope, query AnalysisEntityQuery) (graphprotocol.EntitiesResponse, error) {
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, store, err := service.analysisReady(ctx, scope)
	if err != nil {
		return graphprotocol.EntitiesResponse{}, err
	}
	query.Snapshot = ready.selected[0]
	query.Limit = service.limits().MaxNodes + 1
	entities, err := store.QueryAnalysisEntities(ctx, query)
	if err != nil {
		return graphprotocol.EntitiesResponse{}, err
	}
	if len(entities) > service.limits().MaxNodes {
		return graphprotocol.EntitiesResponse{}, ErrQuerySize
	}
	for _, entity := range entities {
		if entity.RepositoryID != ready.selected[0].RepositoryID || entity.Fact == nil {
			return graphprotocol.EntitiesResponse{}, ErrGenerationChanged
		}
	}
	slices.SortFunc(entities, func(a, b graphprotocol.Entity) int {
		if query.Order == "kind" && a.Fact.Kind != b.Fact.Kind {
			return slices.Index(qualifiedNameKinds, a.Fact.Kind) - slices.Index(qualifiedNameKinds, b.Fact.Kind)
		}
		if a.Fact.GetLocation().GetStart().GetLine() != b.Fact.GetLocation().GetStart().GetLine() {
			return int(a.Fact.GetLocation().GetStart().GetLine() - b.Fact.GetLocation().GetStart().GetLine())
		}
		return strings.Compare(a.Fact.Occurrence, b.Fact.Occurrence)
	})
	if err = ready.publicEntities(entities); err != nil {
		return graphprotocol.EntitiesResponse{}, err
	}
	result := graphprotocol.EntitiesResponse{Entities: entities, Generations: ready.publicGenerations()}
	if err = ready.current(ctx); err != nil {
		return graphprotocol.EntitiesResponse{}, err
	}
	result.Analysis = analysisState(ready)
	if err = entityResponseSize(result); err != nil {
		return graphprotocol.EntitiesResponse{}, err
	}
	return result, nil
}

func (service *Service) ModuleStructure(ctx context.Context, request graphprotocol.ScopeRequest) (graphprotocol.ModuleStructureResponse, error) {
	if service == nil {
		return graphprotocol.ModuleStructureResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, store, err := service.analysisReady(ctx, request.Scope)
	if err != nil {
		return graphprotocol.ModuleStructureResponse{}, err
	}
	files, err := store.QueryAnalysisEntities(ctx, AnalysisEntityQuery{Snapshot: ready.selected[0], Kinds: []string{"file"}, Limit: service.limits().MaxNodes + 1})
	if err != nil {
		return graphprotocol.ModuleStructureResponse{}, err
	}
	if len(files) > service.limits().MaxNodes {
		return graphprotocol.ModuleStructureResponse{}, ErrQuerySize
	}
	grouped := map[string][]string{}
	for _, entity := range files {
		if entity.RepositoryID != ready.selected[0].RepositoryID || entity.Fact == nil || entity.Fact.Kind != "file" {
			return graphprotocol.ModuleStructureResponse{}, ErrGenerationChanged
		}
		file := entity.Fact.GetPath()
		directory := path.Dir(file)
		grouped[directory] = append(grouped[directory], file)
	}
	directories := make([]string, 0, len(grouped))
	for directory := range grouped {
		directories = append(directories, directory)
	}
	slices.Sort(directories)
	result := graphprotocol.ModuleStructureResponse{Generations: ready.publicGenerations()}
	for _, directory := range directories {
		slices.Sort(grouped[directory])
		result.Modules = append(result.Modules, graphprotocol.ModuleDirectory{Directory: directory, Files: grouped[directory]})
	}
	if err = ready.current(ctx); err != nil {
		return graphprotocol.ModuleStructureResponse{}, err
	}
	result.Analysis = analysisState(ready)
	if err = entityResponseSize(result); err != nil {
		return graphprotocol.ModuleStructureResponse{}, err
	}
	return result, nil
}

func (service *Service) FilteredSubgraph(ctx context.Context, request graphprotocol.FilteredSubgraphRequest) (graphprotocol.SubgraphResponse, error) {
	if service == nil || len(request.Filter.Paths) > 1_000 || len(request.Filter.Kinds) > len(filteredSubgraphKinds) {
		return graphprotocol.SubgraphResponse{}, ErrInvalidRequest
	}
	paths := make([]string, 0, len(request.Filter.Paths))
	for _, value := range request.Filter.Paths {
		normalized, err := NormalizeFilePath(value)
		if err != nil || normalized == "." {
			return graphprotocol.SubgraphResponse{}, ErrInvalidRequest
		}
		paths = append(paths, normalized)
	}
	kinds := request.Filter.Kinds
	if len(kinds) == 0 {
		kinds = filteredSubgraphKinds
	}
	for _, kind := range kinds {
		if !slices.Contains(filteredSubgraphKinds, kind) {
			return graphprotocol.SubgraphResponse{}, ErrInvalidRequest
		}
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, store, err := service.analysisReady(ctx, request.Scope)
	if err != nil {
		return graphprotocol.SubgraphResponse{}, err
	}
	limits := service.limits()
	entities, err := store.QueryAnalysisEntities(ctx, AnalysisEntityQuery{Snapshot: ready.selected[0], Paths: paths, Kinds: kinds, Exported: request.Filter.Exported, Limit: limits.MaxNodes + 1})
	if err != nil {
		return graphprotocol.SubgraphResponse{}, err
	}
	result := graphprotocol.SubgraphResponse{Status: graphprotocol.StatusOK, Entities: entities}
	if len(entities) > limits.MaxNodes {
		result.Entities = entities[:limits.MaxNodes]
		result.Partial = true
		result.Boundaries = appendBoundary(result.Boundaries, "node_limit", 0)
	}
	includeEdges := request.IncludeEdges == nil || *request.IncludeEdges
	if includeEdges && len(result.Entities) > 0 {
		occurrences := make([]string, len(result.Entities))
		for i, entity := range result.Entities {
			if entity.RepositoryID != ready.selected[0].RepositoryID || entity.Fact == nil {
				return graphprotocol.SubgraphResponse{}, ErrGenerationChanged
			}
			occurrences[i] = entity.Fact.Occurrence
		}
		slices.Sort(occurrences)
		result.Edges, err = store.QueryAnalysisEvidence(ctx, AnalysisEvidenceQuery{Snapshot: ready.selected[0], Occurrences: occurrences, Limit: limits.MaxEdges + 1})
		if err != nil {
			return graphprotocol.SubgraphResponse{}, err
		}
		if len(result.Edges) > limits.MaxEdges {
			result.Edges = result.Edges[:limits.MaxEdges]
			result.Partial = true
			result.Boundaries = appendBoundary(result.Boundaries, "edge_limit", 0)
		}
	}
	return finishSubgraph(ctx, ready, result)
}
