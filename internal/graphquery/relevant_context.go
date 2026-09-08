package graphquery

import (
	"context"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphprotocol"
)

var defaultRelevantNodeKinds = []string{
	"function", "method", "class", "interface", "type_alias", "struct", "union", "trait",
	"component", "route", "variable", "constant", "enum", "module", "namespace",
}

var relevantNodeKinds = map[string]bool{
	"repository": true, "symbol": true, "file": true, "module": true, "class": true,
	"struct": true, "interface": true, "trait": true, "protocol": true, "function": true,
	"method": true, "property": true, "field": true, "variable": true, "constant": true,
	"enum": true, "enum_member": true, "type_alias": true, "namespace": true, "parameter": true,
	"import": true, "export": true, "route": true, "component": true, "union": true,
}

var relevantRecoveryKinds = []string{"calls", "extends", "implements", "references", "overrides", "navigates"}

func relevantOptions(options graphprotocol.RelevantContextOptions) (graphprotocol.AppliedRelevantContextOptions, error) {
	result := graphprotocol.AppliedRelevantContextOptions{
		SearchLimit: 3, TraversalDepth: 1, MaxNodes: 20, MinScore: .3,
		NodeKinds: slices.Clone(defaultRelevantNodeKinds),
	}
	if options.SearchLimit != nil {
		result.SearchLimit = *options.SearchLimit
	}
	if options.TraversalDepth != nil {
		result.TraversalDepth = *options.TraversalDepth
	}
	if options.MaxNodes != nil {
		result.MaxNodes = *options.MaxNodes
	}
	if options.MinScore != nil {
		result.MinScore = *options.MinScore
	}
	result.EdgeKinds = slices.Clone(options.EdgeKinds)
	if options.NodeKinds != nil {
		result.NodeKinds = slices.Clone(*options.NodeKinds)
	}
	if options.SeedNames != nil {
		result.SeedNames = slices.Clone(*options.SeedNames)
	}
	if result.SearchLimit < 1 || result.SearchLimit > 20 || result.TraversalDepth < 0 || result.TraversalDepth > 32 || result.MaxNodes < 1 || result.MaxNodes > 100 || math.IsNaN(result.MinScore) || math.IsInf(result.MinScore, 0) || result.MinScore < 0 || result.MinScore > 1e9 || len(result.EdgeKinds) > 13 || len(result.NodeKinds) > 32 || len(result.SeedNames) > 32 {
		return graphprotocol.AppliedRelevantContextOptions{}, ErrInvalidRequest
	}
	for _, kind := range result.EdgeKinds {
		if _, ok := graphartifact.ParseRelationship(kind); !ok {
			return graphprotocol.AppliedRelevantContextOptions{}, ErrInvalidRequest
		}
	}
	for _, kind := range result.NodeKinds {
		if !relevantNodeKinds[kind] {
			return graphprotocol.AppliedRelevantContextOptions{}, ErrInvalidRequest
		}
	}
	for _, name := range result.SeedNames {
		if name == "" || len(name) > 16384 || !utf8.ValidString(name) {
			return graphprotocol.AppliedRelevantContextOptions{}, ErrInvalidRequest
		}
	}
	return result, nil
}

// RelevantContext composes bounded discovery and traversal in one immutable
// generation. It preserves edge occurrences even when their triples repeat.
func (service *Service) RelevantContext(ctx context.Context, request graphprotocol.RelevantContextRequest) (graphprotocol.RelevantContextResponse, error) {
	if service == nil || len(request.Query) > 16384 || !utf8.ValidString(request.Query) {
		return graphprotocol.RelevantContextResponse{}, ErrInvalidRequest
	}
	options, err := relevantOptions(request.Options)
	if err != nil {
		return graphprotocol.RelevantContextResponse{}, err
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	limits := service.limits()
	options.SearchLimit = min(options.SearchLimit, limits.MaxRows)
	options.TraversalDepth = min(options.TraversalDepth, limits.MaxDepth)
	options.MaxNodes = min(options.MaxNodes, limits.MaxNodes)
	if strings.TrimSpace(request.Query) == "" {
		ready, err := service.readyEntities(ctx, request.Scope)
		if err != nil {
			return graphprotocol.RelevantContextResponse{}, err
		}
		result := graphprotocol.RelevantContextResponse{Status: graphprotocol.StatusNotFound, Confidence: "high", Generations: ready.publicGenerations(), Options: options}
		if err := ready.current(ctx); err != nil {
			return graphprotocol.RelevantContextResponse{}, err
		}
		return result, nil
	}

	discoveryLimit := min(100, options.SearchLimit*5)
	discovery, err := service.Discover(ctx, graphprotocol.DiscoverRequest{
		Scope: request.Scope, Query: request.Query, Limit: discoveryLimit,
		Symbols: options.SeedNames,
	})
	if err != nil {
		return graphprotocol.RelevantContextResponse{}, err
	}
	if request.Options.SeedNames == nil {
		rankRelevantQueryEvidence(discovery.Matches, request.Query)
	} else {
		rankRelevantExactNames(discovery.Matches)
	}
	result := graphprotocol.RelevantContextResponse{
		Status: graphprotocol.StatusNotFound, Confidence: discovery.Confidence,
		Generations: discovery.Generations, Options: options,
	}
	boundary := func(reason string, depth int) {
		result.Partial = true
		for _, value := range result.Boundaries {
			if value.Reason == reason && value.Depth == depth {
				return
			}
		}
		result.Boundaries = append(result.Boundaries, graphprotocol.Boundary{Reason: reason, Depth: depth})
	}
	if discovery.CandidateTruncated || discovery.ResultTruncated {
		boundary("discovery_limit", 0)
	}
	if discovery.Confidence == "low" {
		boundary("weak_entry_point", 0)
	}
	allowedKind := func(kind string) bool { return len(options.NodeKinds) == 0 || slices.Contains(options.NodeKinds, kind) }
	nodes := map[nodeKey]bool{}
	nodeIDs := map[string]bool{}
	addNode := func(entity graphprotocol.Entity) bool {
		if entity.Fact == nil || entity.ID == "" || entity.Fact.Occurrence == "" || !allowedKind(entity.Fact.Kind) {
			return false
		}
		key := nodeKey{entity.RepositoryID, entity.Fact.Occurrence}
		if nodes[key] {
			return true
		}
		if len(result.Nodes) >= options.MaxNodes {
			boundary("node_limit", entity.Depth)
			return false
		}
		nodes[key] = true
		nodeIDs[entity.ID] = true
		result.Nodes = append(result.Nodes, entity)
		return true
	}
	for _, match := range discovery.Matches {
		if len(result.EntryPoints) >= options.SearchLimit || match.Score < options.MinScore || match.Entity.Fact == nil || !allowedKind(match.Entity.Fact.Kind) || !addNode(match.Entity) {
			continue
		}
		result.EntryPoints = append(result.EntryPoints, match)
		result.Roots = append(result.Roots, match.Entity.ID)
	}
	if len(result.Roots) == 0 {
		if err := service.ValidateGenerations(ctx, request.Scope, result.Generations); err != nil {
			return graphprotocol.RelevantContextResponse{}, err
		}
		return result, nil
	}
	result.Status = graphprotocol.StatusOK

	query := &Service{Store: service.Store, Limits: service.Limits}
	query.Limits.MaxNodes = options.MaxNodes
	query.Limits.MaxEdges = min(limits.MaxEdges, options.MaxNodes*13)
	query.Limits.MaxFanout = min(limits.MaxFanout, options.MaxNodes)
	relations := options.EdgeKinds
	if len(relations) == 0 {
		for _, relation := range graphartifact.Relationships() {
			relations = append(relations, relation.Name)
		}
	}
	pendingEdges := []graphprotocol.Evidence{}
	merge := func(graph graphprotocol.TraverseResponse, addNodes bool, depthOffset int, keepDepthBoundary bool) error {
		if !sameRelevantGeneration(result.Generations, graph.Generations) {
			return ErrGenerationChanged
		}
		if graph.Partial {
			for _, item := range graph.Boundaries {
				if item.Reason != "depth_limit" || keepDepthBoundary {
					boundary(item.Reason, item.Depth+depthOffset)
				}
			}
		}
		if addNodes {
			for _, entity := range graph.Entities {
				if entity.Depth > 0 {
					entity.Depth += depthOffset
				}
				addNode(entity)
			}
		}
		pendingEdges = append(pendingEdges, graph.Edges...)
		return nil
	}
	traverse := func(entity graphprotocol.Entity, selected []string, direction string, depth int, addNodes bool, depthOffset int, keepDepthBoundary bool) error {
		graph, err := query.Traverse(ctx, graphprotocol.TraverseRequest{Scope: request.Scope, Root: graphprotocol.EntitySelector{Occurrence: &entity.Fact.Occurrence}, Relations: selected, Direction: direction, MaxDepth: depth})
		if err != nil {
			return err
		}
		return merge(graph, addNodes, depthOffset, keepDepthBoundary)
	}
	if options.TraversalDepth > 0 {
		frontier := slices.Clone(result.Nodes)
		for level := 0; level < options.TraversalDepth && len(frontier) > 0; level++ {
			next := []graphprotocol.Entity{}
			for _, root := range frontier {
				for _, direction := range []string{"incoming", "outgoing"} {
					before := len(result.Nodes)
					if err := traverse(root, relations, direction, 1, true, level, level+1 == options.TraversalDepth); err != nil {
						return graphprotocol.RelevantContextResponse{}, err
					}
					next = append(next, result.Nodes[before:]...)
				}
			}
			frontier = next
		}
	}

	if options.TraversalDepth > 0 {
		// Dedicated type expansion is independent of the caller's BFS edge filter.
		// Two passes retain parents, children and siblings within a quarter-budget.
		typeKind := func(kind string) bool {
			return slices.Contains([]string{"class", "interface", "struct", "union", "trait", "protocol"}, kind)
		}
		hierarchyLimit, hierarchyAdded := (options.MaxNodes+3)/4, 0
		seenHierarchy := map[nodeKey]bool{}
		for pass := 0; pass < 2 && hierarchyAdded < hierarchyLimit; pass++ {
			candidates := slices.Clone(result.Nodes)
			before := len(result.Nodes)
			for _, candidate := range candidates {
				key := nodeKey{candidate.RepositoryID, candidate.Fact.Occurrence}
				if seenHierarchy[key] || !typeKind(candidate.Fact.Kind) {
					continue
				}
				seenHierarchy[key] = true
				for _, direction := range []string{"incoming", "outgoing"} {
					prior := len(result.Nodes)
					query.Limits.MaxNodes = min(options.MaxNodes, hierarchyLimit-hierarchyAdded+1)
					if err := traverse(candidate, []string{"extends", "implements"}, direction, 1, true, 0, true); err != nil {
						return graphprotocol.RelevantContextResponse{}, err
					}
					hierarchyAdded += len(result.Nodes) - prior
					if hierarchyAdded >= hierarchyLimit {
						break
					}
				}
				if hierarchyAdded >= hierarchyLimit {
					break
				}
			}
			if len(result.Nodes) == before {
				break
			}
		}

		// Recover evidence between retained nodes without admitting more nodes.
		query.Limits.MaxNodes = options.MaxNodes
		for _, entity := range slices.Clone(result.Nodes) {
			if err := traverse(entity, relevantRecoveryKinds, "outgoing", 1, false, 0, true); err != nil {
				return graphprotocol.RelevantContextResponse{}, err
			}
		}
	}
	edges := map[nodeKey]bool{}
	for _, edge := range pendingEdges {
		if edge.Fact == nil || edge.Fact.Occurrence == "" {
			return graphprotocol.RelevantContextResponse{}, ErrGenerationChanged
		}
		if !nodeIDs[edge.SourceID] || !nodeIDs[edge.TargetID] {
			continue
		}
		key := nodeKey{edge.RepositoryID, edge.Fact.Occurrence}
		if !edges[key] {
			edges[key] = true
			result.Edges = append(result.Edges, edge)
		}
	}
	if err := entityResponseSize(result); err != nil {
		return graphprotocol.RelevantContextResponse{}, err
	}
	if err := service.ValidateGenerations(ctx, request.Scope, result.Generations); err != nil {
		return graphprotocol.RelevantContextResponse{}, err
	}
	return result, nil
}

func sameRelevantGeneration(a, b []graphprotocol.Generation) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].RepositoryID != b[i].RepositoryID || a[i].UploadID != b[i].UploadID || a[i].Commit != b[i].Commit {
			return false
		}
	}
	return true
}

func rankRelevantExactNames(matches []graphprotocol.DiscoveryMatch) {
	indices := map[string][]int{}
	for index, match := range matches {
		if match.Entity.Fact == nil {
			continue
		}
		name := NormalizeDiscovery(match.Entity.Fact.GetName())
		if name != "" {
			indices[name] = append(indices[name], index)
		}
	}
	for _, group := range indices {
		if len(group) < 2 {
			continue
		}
		values := make([]graphprotocol.DiscoveryMatch, len(group))
		for index, source := range group {
			values[index] = matches[source]
		}
		sort.SliceStable(values, func(i, j int) bool {
			a, b := values[i], values[j]
			aPenalty := boolInt(a.Generated) + boolInt(a.Ambient) + boolInt(a.Test) + boolInt(a.Deprioritized)
			bPenalty := boolInt(b.Generated) + boolInt(b.Ambient) + boolInt(b.Test) + boolInt(b.Deprioritized)
			if aPenalty != bPenalty {
				return aPenalty < bPenalty
			}
			aSize := len(a.Entity.Fact.GetSignature()) + len(a.Entity.Fact.GetDocumentation())
			bSize := len(b.Entity.Fact.GetSignature()) + len(b.Entity.Fact.GetDocumentation())
			if aSize != bSize {
				return aSize < bSize
			}
			return a.Entity.Fact.GetOccurrence() < b.Entity.Fact.GetOccurrence()
		})
		for index, target := range group {
			matches[target] = values[index]
		}
	}
}

func rankRelevantQueryEvidence(matches []graphprotocol.DiscoveryMatch, query string) {
	terms := []string{}
	for _, term := range IdentifierSegments(query) {
		term = NormalizeDiscovery(term)
		if term != "" {
			terms = append(terms, term)
		}
	}
	evidence := func(match graphprotocol.DiscoveryMatch) int {
		if match.Entity.Fact == nil {
			return 0
		}
		candidate := NormalizeDiscovery(match.Entity.Fact.GetName() + " " + match.Entity.Fact.GetQualifiedName())
		score := 0
		for _, term := range terms {
			if strings.Contains(candidate, term) {
				score++
			} else if strings.HasSuffix(term, "ing") && len(term) > 5 && strings.Contains(candidate, strings.TrimSuffix(term, "ing")) {
				score += 2
			}
		}
		return score
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return evidence(matches[i]) > evidence(matches[j])
	})
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
