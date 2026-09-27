package graphquery

import (
	"cmp"
	"context"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
)

// Name-addressed callers, callees and impact, ported from CodeGraph b9ca4b7's
// MCP handlers (src/mcp/tools.ts handleCallers/handleCallees/handleImpact) and
// src/graph/named-symbol-flow.ts findAllSymbols/matchesSymbol.

var (
	aritySpelling      = regexp.MustCompile(`^(.+)/(\d{1,3})$`)
	qualifiedArity     = regexp.MustCompile(`/(\d{1,3})$`)
	trailingArity      = regexp.MustCompile(`/\d{1,3}$`)
	qualifierSeparator = regexp.MustCompile(`::|[./]`)
	trailingExtension  = regexp.MustCompile(`\.[^.]+$`)
	nixOptionPath      = regexp.MustCompile(`^[a-z][\w'-]*(?:\.[\w'-]+)+$`)
	rustPathPrefixes   = map[string]bool{"crate": true, "super": true, "self": true}
)

// symbolCallRelations is upstream's getCallers/getCallees edge set in the
// order SQLite walks its IN list over (target|source, kind): kind ascending,
// then row order, which v2 generations keep as the edge ordinal.
var symbolCallRelations = []string{"calls", "imports", "instantiates", "navigates", "references"}

const symbolSearchLimit = 50

func qualifierParts(symbol string) []string {
	var parts []string
	for _, part := range qualifierSeparator.Split(symbol, -1) {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func qualified(symbol string) bool {
	return strings.ContainsAny(symbol, "./") || strings.Contains(symbol, "::")
}

func lastQualifierPart(symbol string) string {
	noArity := trailingArity.ReplaceAllString(symbol, "")
	if noArity == "" {
		noArity = symbol
	}
	parts := qualifierParts(noArity)
	if len(parts) == 0 {
		return symbol
	}
	return parts[len(parts)-1]
}

func matchesSymbol(node *graphv2.Node, symbol string) bool {
	if spelling := aritySpelling.FindStringSubmatch(symbol); spelling != nil {
		if arity := qualifiedArity.FindStringSubmatch(node.GetQualifiedName()); arity != nil {
			if arity[1] != spelling[2] {
				return false
			}
			symbol = spelling[1]
		}
	}
	if node.GetName() == symbol {
		return true
	}
	if node.GetKind() == "file" && trailingExtension.ReplaceAllString(node.GetName(), "") == symbol {
		return true
	}
	if !qualified(symbol) {
		return false
	}
	parts := qualifierParts(symbol)
	if len(parts) < 2 || node.GetName() != parts[len(parts)-1] {
		return false
	}
	if strings.Contains(node.GetQualifiedName(), strings.Join(parts, "::")) {
		return true
	}
	var hints []string
	for _, part := range parts[:len(parts)-1] {
		if !rustPathPrefixes[part] {
			hints = append(hints, part)
		}
	}
	if len(hints) == 0 {
		return false
	}
	var segments []string
	for _, segment := range strings.Split(node.GetPath(), "/") {
		if segment != "" {
			segments = append(segments, segment)
		}
	}
	for _, hint := range hints {
		if !slices.ContainsFunc(segments, func(segment string) bool {
			return segment == hint || trailingExtension.ReplaceAllString(segment, "") == hint
		}) {
			return false
		}
	}
	return true
}

// groupDefinitions keeps one group per (path, qualified name). A file filter
// that matches nothing is reported "unmatched" and every definition is kept.
func groupDefinitions(matches []graphprotocol.Entity, file string) ([][]graphprotocol.Entity, string) {
	pool, filter := matches, ""
	if file != "" {
		wanted := strings.TrimPrefix(file, "./")
		var narrowed []graphprotocol.Entity
		for _, match := range matches {
			path := match.Fact.GetPath()
			if path == wanted || strings.HasSuffix(path, wanted) || strings.HasSuffix(path, "/"+wanted) {
				narrowed = append(narrowed, match)
			}
		}
		filter = "unmatched"
		if len(narrowed) > 0 {
			pool, filter = narrowed, "matched"
		}
	}
	type definition struct{ path, qualified string }
	index := map[definition]int{}
	var groups [][]graphprotocol.Entity
	for _, match := range pool {
		key := definition{match.Fact.GetPath(), match.Fact.GetQualifiedName()}
		if i, ok := index[key]; ok {
			groups[i] = append(groups[i], match)
			continue
		}
		index[key] = len(groups)
		groups = append(groups, []graphprotocol.Entity{match})
	}
	return groups, filter
}

type symbolScope struct {
	ready     entityReady
	internal  map[int64]int64 // public repository ID -> internal
	groups    [][]graphprotocol.Entity
	filter    string
	truncated bool
}

func (service *Service) symbolDefinitions(ctx context.Context, scope graphprotocol.Scope, symbol, file string) (symbolScope, error) {
	if symbol == "" || len(symbol) > 16384 || !utf8.ValidString(symbol) || len(file) > 16384 || !utf8.ValidString(file) {
		return symbolScope{}, ErrInvalidRequest
	}
	ready, err := service.readyEntities(ctx, scope)
	if err != nil {
		return symbolScope{}, err
	}
	result := symbolScope{ready: ready, internal: map[int64]int64{}}
	for internal, public := range ready.publicIDs {
		result.internal[public] = internal
	}
	matches, truncated, err := service.findAllSymbols(ctx, result, scope, symbol)
	if err != nil {
		return symbolScope{}, err
	}
	result.truncated = truncated
	result.groups, result.filter = groupDefinitions(matches, file)
	return result, nil
}

// findAllSymbols returns public entities: the Nix option-path convention, else
// every definition matchesSymbol accepts, generated files last, then by path
// and line.
//
// ponytail: upstream draws candidates from its top 50 searchNodes (SQLite FTS5
// BM25) results, orders same-named definitions by that score, and falls back
// to the best non-exact hit when nothing matches exactly. Without a
// searchNodes port GraphNest uses exact-name candidates, a deterministic
// order, and reports not_found instead of guessing.
func (service *Service) findAllSymbols(ctx context.Context, scope symbolScope, request graphprotocol.Scope, symbol string) ([]graphprotocol.Entity, bool, error) {
	if nixOptionPath.MatchString(symbol) {
		hits, err := service.nixOptionHits(ctx, scope.ready, symbol)
		if err != nil || len(hits) > 0 {
			return hits, false, err
		}
	}
	selectors := []graphprotocol.EntitySelector{{Name: &symbol}}
	if tail := lastQualifierPart(symbol); qualified(symbol) && tail != symbol {
		selectors = append(selectors, graphprotocol.EntitySelector{Name: &tail})
	} else if !qualified(symbol) {
		// A file also matches by its name without the extension.
		selectors = append(selectors, graphprotocol.EntitySelector{NameMatch: &graphprotocol.NameSelector{Mode: "prefix", Value: symbol + ".", Kinds: []string{"file"}}})
	}
	var matches []graphprotocol.Entity
	seen := map[nodeKey]bool{}
	for _, selector := range selectors {
		found, err := scope.ready.store.QueryEntities(ctx, EntityQuery{Snapshots: scope.ready.selected, Selector: selector, Limit: symbolSearchLimit + 1})
		if err != nil {
			return nil, false, err
		}
		for _, entity := range found {
			key := nodeKey{entity.RepositoryID, entity.Fact.GetOccurrence()}
			if !seen[key] && matchesSymbol(entity.Fact, symbol) {
				seen[key] = true
				matches = append(matches, entity)
			}
		}
	}
	if err := scope.ready.publicEntities(matches); err != nil {
		return nil, false, err
	}
	generated, err := service.generatedPaths(ctx, request, matches)
	if err != nil {
		return nil, false, err
	}
	slices.SortStableFunc(matches, func(a, b graphprotocol.Entity) int {
		return cmp.Or(
			cmp.Compare(boolRank(generated[a.Fact.GetPath()]), boolRank(generated[b.Fact.GetPath()])),
			cmp.Compare(a.Fact.GetPath(), b.Fact.GetPath()),
			cmp.Compare(a.Fact.GetLocation().GetStart().GetLine(), b.Fact.GetLocation().GetStart().GetLine()),
			cmp.Compare(a.Fact.GetOccurrence(), b.Fact.GetOccurrence()),
		)
	})
	truncated := len(matches) > symbolSearchLimit
	return matches[:min(len(matches), symbolSearchLimit)], truncated, nil
}

func boolRank(value bool) int {
	if value {
		return 1
	}
	return 0
}

func sameGenerations(a, b []graphprotocol.Generation) bool {
	return slices.EqualFunc(a, b, func(x, y graphprotocol.Generation) bool {
		return x.RepositoryID == y.RepositoryID && x.UploadID == y.UploadID && x.Commit == y.Commit
	})
}

func (service *Service) generatedPaths(ctx context.Context, scope graphprotocol.Scope, entities []graphprotocol.Entity) (map[string]bool, error) {
	var paths []string
	for _, entity := range entities {
		if path := entity.Fact.GetPath(); path != "" && !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
	}
	generated := map[string]bool{}
	for chunk := range slices.Chunk(paths, maxFileClassificationPaths) {
		classified, err := service.FileClassifications(ctx, graphprotocol.FileClassificationRequest{Scope: scope, Paths: chunk})
		if err != nil {
			return nil, err
		}
		for _, file := range classified.Files {
			generated[file.Path] = file.Generated
		}
	}
	return generated, nil
}

// nixOptionHits resolves a dotted Nix option: its `options.<path>` declaration,
// then the exact write, then up to 12 prefixed writes, at most 10 in all.
func (service *Service) nixOptionHits(ctx context.Context, ready entityReady, symbol string) ([]graphprotocol.Entity, error) {
	byName := func(name string) ([]graphprotocol.Entity, error) {
		found, err := ready.store.QueryEntities(ctx, EntityQuery{Snapshots: ready.selected, Selector: graphprotocol.EntitySelector{Name: &name}, Limit: service.limits().MaxNodes})
		slices.SortStableFunc(found, func(a, b graphprotocol.Entity) int {
			return cmp.Or(cmp.Compare(a.Fact.GetPath(), b.Fact.GetPath()), cmp.Compare(a.Fact.GetLocation().GetStart().GetLine(), b.Fact.GetLocation().GetStart().GetLine()))
		})
		return found, err
	}
	declarations, err := byName("options." + symbol)
	if err != nil {
		return nil, err
	}
	writes, err := byName(symbol)
	if err != nil {
		return nil, err
	}
	prefixed, err := ready.store.QueryEntities(ctx, EntityQuery{Snapshots: ready.selected, Selector: graphprotocol.EntitySelector{NameMatch: &graphprotocol.NameSelector{Mode: "prefix", Value: symbol + "."}}, Limit: service.limits().MaxNodes})
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(prefixed, func(a, b graphprotocol.Entity) int { return cmp.Compare(a.Fact.GetName(), b.Fact.GetName()) })
	prefixed = prefixed[:min(len(prefixed), 12)]
	var hits []graphprotocol.Entity
	seen := map[nodeKey]bool{}
	for _, entity := range slices.Concat(declarations, writes, prefixed) {
		key := nodeKey{entity.RepositoryID, entity.Fact.GetOccurrence()}
		if entity.Fact.GetLanguage() == "nix" && !seen[key] && len(hits) < 10 {
			seen[key] = true
			hits = append(hits, entity)
		}
	}
	if err = ready.publicEntities(hits); err != nil {
		return nil, err
	}
	return hits, nil
}

// SymbolCalls lists the callers (incoming) or callees (outgoing) of every
// definition of a name, one section per distinct definition. Each neighbor
// appears once, with the first edge that reached it; limit bounds each
// section (default 20, 1-100) and Truncated reports that more exist.
func (service *Service) SymbolCalls(ctx context.Context, request graphprotocol.SymbolCallsRequest) (graphprotocol.SymbolResponse, error) {
	if service == nil || (request.Direction != "incoming" && request.Direction != "outgoing") {
		return graphprotocol.SymbolResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	limit := request.Limit
	if limit == 0 {
		limit = 20
	}
	limit = min(max(limit, 1), 100)
	scope, err := service.symbolDefinitions(ctx, request.Scope, request.Symbol, request.File)
	if err != nil {
		return graphprotocol.SymbolResponse{}, err
	}
	result := graphprotocol.SymbolResponse{Status: graphprotocol.StatusNotFound, FileFilter: scope.filter, Definitions: []graphprotocol.SymbolDefinition{}, Generations: scope.ready.publicGenerations()}
	if len(scope.groups) > 0 {
		result.Status = graphprotocol.StatusOK
	}
	if scope.truncated {
		result.Partial = true
		result.Boundaries = appendBoundary(result.Boundaries, "candidate_limit", 0)
	}
	snapshots := map[int64]QuerySnapshot{}
	for _, snapshot := range scope.ready.snapshots {
		snapshots[snapshot.RepositoryID] = snapshot
	}
	usedBytes := 0
	for _, group := range scope.groups {
		definition := graphprotocol.SymbolDefinition{Definitions: group}
		seen := map[nodeKey]bool{}
	collect:
		for _, root := range group {
			repository := scope.internal[root.RepositoryID]
			for _, relation := range symbolCallRelations {
				// ponytail: 400 edges per relation and definition; page when a hub needs more.
				rows, err := scope.ready.store.EntityNeighbors(ctx, EntityNeighborQuery{Snapshot: snapshots[repository], Occurrence: root.Fact.GetOccurrence(), Relation: relation, Direction: request.Direction, Limit: 401, ProducerOrder: true})
				if err != nil {
					return graphprotocol.SymbolResponse{}, err
				}
				if len(rows) > 400 {
					rows = rows[:400]
					result.Partial = true
					result.Boundaries = appendBoundary(result.Boundaries, "fanout_limit", 1)
				}
				for _, row := range rows {
					if row.Entity.RepositoryID != repository || row.Edge.RepositoryID != repository {
						return graphprotocol.SymbolResponse{}, ErrGenerationChanged
					}
					key := nodeKey{repository, row.Entity.Fact.GetOccurrence()}
					if row.Entity.Fact.GetOccurrence() == root.Fact.GetOccurrence() || seen[key] {
						continue
					}
					seen[key] = true
					if len(definition.Related) == limit {
						definition.Truncated = true
						break collect
					}
					row.Entity.RepositoryID, row.Edge.RepositoryID = root.RepositoryID, root.RepositoryID
					related := graphprotocol.SymbolRelated{Entity: row.Entity, Edge: row.Edge}
					if err = AddEntityQueryBytes(&usedBytes, related); err != nil {
						return graphprotocol.SymbolResponse{}, err
					}
					definition.Related = append(definition.Related, related)
				}
			}
		}
		result.Definitions = append(result.Definitions, definition)
	}
	if err = scope.ready.current(ctx); err != nil {
		return graphprotocol.SymbolResponse{}, err
	}
	return result, nil
}

// SymbolImpact merges each distinct definition's impact radius (default depth
// 2, 1-10): entities in first-reached order, edges once per source, target and
// kind.
func (service *Service) SymbolImpact(ctx context.Context, request graphprotocol.SymbolImpactRequest) (graphprotocol.SymbolResponse, error) {
	if service == nil {
		return graphprotocol.SymbolResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	depth := request.Depth
	if depth == 0 {
		depth = 2
	}
	depth = min(max(depth, 1), 10)
	scope, err := service.symbolDefinitions(ctx, request.Scope, request.Symbol, request.File)
	if err != nil {
		return graphprotocol.SymbolResponse{}, err
	}
	result := graphprotocol.SymbolResponse{Status: graphprotocol.StatusNotFound, FileFilter: scope.filter, Definitions: []graphprotocol.SymbolDefinition{}, Generations: scope.ready.publicGenerations()}
	if len(scope.groups) > 0 {
		result.Status = graphprotocol.StatusOK
	}
	if scope.truncated {
		result.Partial = true
		result.Boundaries = appendBoundary(result.Boundaries, "candidate_limit", 0)
	}
	for _, group := range scope.groups {
		definition := graphprotocol.SymbolDefinition{Definitions: group}
		entities := map[nodeKey]bool{}
		type edgeKey struct {
			source, target string
			kind           graphv2.EdgeKind
		}
		edges := map[edgeKey]bool{}
		for _, root := range group {
			radius, err := service.ImpactRadius(ctx, graphprotocol.EntityImpactRequest{Scope: request.Scope, Occurrence: root.Fact.GetOccurrence(), MaxDepth: &depth})
			if err != nil {
				return graphprotocol.SymbolResponse{}, err
			}
			if !sameGenerations(radius.Generations, result.Generations) {
				return graphprotocol.SymbolResponse{}, ErrGenerationChanged
			}
			if radius.Partial {
				result.Partial = true
				result.Boundaries = append(result.Boundaries, radius.Boundaries...)
			}
			for _, entity := range radius.Entities {
				if key := (nodeKey{entity.RepositoryID, entity.Fact.GetOccurrence()}); !entities[key] {
					entities[key] = true
					definition.Entities = append(definition.Entities, entity)
				}
			}
			for _, edge := range radius.Edges {
				if key := (edgeKey{edge.Fact.GetSource(), edge.Fact.GetTarget(), edge.Fact.GetKind()}); !edges[key] {
					edges[key] = true
					definition.Edges = append(definition.Edges, edge)
				}
			}
		}
		result.Definitions = append(result.Definitions, definition)
	}
	if err = entityResponseSize(result); err != nil {
		return graphprotocol.SymbolResponse{}, err
	}
	if err = scope.ready.current(ctx); err != nil {
		return graphprotocol.SymbolResponse{}, err
	}
	return result, nil
}
