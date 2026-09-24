package graphquery

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphprotocol"
)

const (
	maxAncestorDepth        = 8
	maxDescendantDepth      = 6
	maxHierarchyRows        = 400
	maxOverrideAncestors    = 12
	maxShownAncestors       = 24
	maxShownDescendants     = 240
	dispatchMinImplementers = 8
)

var hierarchyRelations = []string{"extends", "implements"}

var hierarchyKinds = map[string]bool{
	"class": true, "interface": true, "struct": true, "trait": true,
	"protocol": true, "enum": true, "type_alias": true, "union": true,
}

var overridableKinds = map[string]bool{"method": true, "function": true, "property": true, "field": true}

type HierarchyCountQuery struct {
	Snapshot   QuerySnapshot
	Occurrence string
}

type HierarchyCount struct {
	Subtypes, Implementers int
}

type HierarchyStore interface {
	AnalysisStore
	CountHierarchyChildren(context.Context, HierarchyCountQuery) (HierarchyCount, error)
}

func (service *Service) TypeRelations(ctx context.Context, request graphprotocol.TypeRelationsRequest) (graphprotocol.TypeRelationsResponse, error) {
	if service == nil {
		return graphprotocol.TypeRelationsResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, _, err := service.analysisReady(ctx, request.Scope)
	if err != nil {
		return graphprotocol.TypeRelationsResponse{}, err
	}
	root, status, err := analysisRoot(ctx, ready, request.Occurrence)
	if err != nil {
		return graphprotocol.TypeRelationsResponse{}, err
	}
	result := graphprotocol.TypeRelationsResponse{Status: status, TypeKnowledge: graphprotocol.TypeKnowledgeUnknown}
	if status == graphprotocol.StatusOK {
		limits := service.limits()
		load := func(relations []string, direction string) ([]EntityNeighbor, bool, error) {
			return analysisNeighbors(ctx, ready, root, relations, direction, limits.MaxEdges)
		}
		outgoing, partial, err := load([]string{"type_of", "returns"}, "outgoing")
		if err != nil {
			return graphprotocol.TypeRelationsResponse{}, err
		}
		result.Types = groupRelated(outgoing, nil)
		if len(result.Types) > 0 {
			result.TypeKnowledge = graphprotocol.TypeKnowledgeRecorded
		}
		incomingTypes, incomingPartial, err := load([]string{"type_of"}, "incoming")
		if err != nil {
			return graphprotocol.TypeRelationsResponse{}, err
		}
		result.Users = groupRelated(incomingTypes, nil)
		incomingReturns, returnsPartial, err := load([]string{"returns"}, "incoming")
		if err != nil {
			return graphprotocol.TypeRelationsResponse{}, err
		}
		result.Returners = groupRelated(incomingReturns, nil)
		references, referencesPartial, err := load([]string{"references"}, "outgoing")
		if err != nil {
			return graphprotocol.TypeRelationsResponse{}, err
		}
		result.ReferencedTypes = groupRelated(references, func(entity graphprotocol.Entity) bool {
			return entity.Fact != nil && hierarchyKinds[entity.Fact.Kind]
		})
		overrides, overridesPartial, err := load([]string{"overrides"}, "outgoing")
		if err != nil {
			return graphprotocol.TypeRelationsResponse{}, err
		}
		result.RecordedOverrides = groupRelated(overrides, nil)
		result.Partial = partial || incomingPartial || returnsPartial || referencesPartial || overridesPartial
		if result.Partial {
			result.Boundaries = appendBoundary(result.Boundaries, "edge_limit", 1)
		}
	}
	return finishTypeRelations(ctx, ready, result)
}

func groupRelated(rows []EntityNeighbor, keep func(graphprotocol.Entity) bool) []graphprotocol.RelatedEntities {
	result := []graphprotocol.RelatedEntities{}
	indexes := map[string]int{}
	for _, row := range rows {
		if keep != nil && !keep(row.Entity) {
			continue
		}
		relation := ""
		if row.Edge.Fact != nil {
			relation = row.Edge.Fact.Kind.String()
			if relationValue, ok := relationshipName(row.Edge.Fact.Kind); ok {
				relation = relationValue
			}
		}
		key := relation + "\x00" + row.Entity.ID
		index, exists := indexes[key]
		if !exists {
			index = len(result)
			indexes[key] = index
			result = append(result, graphprotocol.RelatedEntities{Entity: row.Entity, Relation: relation})
		}
		result[index].Edges = append(result[index].Edges, row.Edge)
	}
	return result
}

func relationshipName(kind interface{ String() string }) (string, bool) {
	for _, relation := range graphartifact.Relationships() {
		if relation.WireKind().String() == kind.String() {
			return relation.Name, true
		}
	}
	return "", false
}

func finishTypeRelations(ctx context.Context, ready entityReady, result graphprotocol.TypeRelationsResponse) (graphprotocol.TypeRelationsResponse, error) {
	for _, groups := range [][]graphprotocol.RelatedEntities{result.Types, result.Users, result.Returners, result.ReferencedTypes, result.RecordedOverrides} {
		for i := range groups {
			if err := publicRelated(ready, &groups[i]); err != nil {
				return graphprotocol.TypeRelationsResponse{}, err
			}
		}
	}
	if err := ready.current(ctx); err != nil {
		return graphprotocol.TypeRelationsResponse{}, err
	}
	result.Generations = ready.publicGenerations()
	result.Analysis = analysisState(ready)
	if err := entityResponseSize(result); err != nil {
		return graphprotocol.TypeRelationsResponse{}, err
	}
	return result, nil
}

func publicRelated(ready entityReady, group *graphprotocol.RelatedEntities) error {
	entities := []graphprotocol.Entity{group.Entity}
	if err := ready.publicEntities(entities); err != nil {
		return err
	}
	group.Entity = entities[0]
	for i := range group.Edges {
		id := ready.publicIDs[group.Edges[i].RepositoryID]
		if id == 0 {
			return ErrGenerationChanged
		}
		group.Edges[i].RepositoryID = id
	}
	return nil
}

func (service *Service) TypeHierarchy(ctx context.Context, request graphprotocol.TypeHierarchyRequest) (graphprotocol.TypeHierarchyResponse, error) {
	if service == nil {
		return graphprotocol.TypeHierarchyResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	ready, analysisStore, err := service.analysisReady(ctx, request.Scope)
	if err != nil {
		return graphprotocol.TypeHierarchyResponse{}, err
	}
	store, ok := analysisStore.(HierarchyStore)
	if !ok {
		return graphprotocol.TypeHierarchyResponse{}, ErrInvalidRequest
	}
	root, status, err := analysisRoot(ctx, ready, request.Occurrence)
	if err != nil {
		return graphprotocol.TypeHierarchyResponse{}, err
	}
	result := graphprotocol.TypeHierarchyResponse{Status: status}
	if status == graphprotocol.StatusNotFound {
		return finishTypeHierarchy(ctx, ready, result)
	}
	result.Focus = root
	if root.Fact == nil || !hierarchyKinds[root.Fact.Kind] {
		return finishTypeHierarchy(ctx, ready, result)
	}
	result.Ancestors.Items, result.Partial, err = walkHierarchyAncestors(ctx, ready, root)
	if err != nil {
		return graphprotocol.TypeHierarchyResponse{}, err
	}
	if result.Partial {
		result.Boundaries = appendBoundary(result.Boundaries, "hierarchy_limit", maxAncestorDepth)
	}
	var descendantPartial bool
	result.Descendants.Items, result.DirectSubtypes, result.DirectImplementers, result.Bounded, descendantPartial, err = walkHierarchyDescendants(ctx, ready, store, root)
	if err != nil {
		return graphprotocol.TypeHierarchyResponse{}, err
	}
	if descendantPartial {
		result.Partial = true
		result.Boundaries = appendBoundary(result.Boundaries, "hierarchy_limit", maxDescendantDepth)
	}
	result.Polymorphic = result.DirectImplementers >= dispatchMinImplementers
	result.DerivedOverrides, err = matchDerivedOverrides(ctx, ready, root, result.Ancestors.Items)
	if err != nil {
		return graphprotocol.TypeHierarchyResponse{}, err
	}
	result.Ancestors = hierarchyRows(result.Ancestors.Items, maxShownAncestors)
	result.Descendants = hierarchyRows(result.Descendants.Items, maxShownDescendants)
	return finishTypeHierarchy(ctx, ready, result)
}

func walkHierarchyAncestors(ctx context.Context, ready entityReady, root graphprotocol.Entity) ([]graphprotocol.HierarchyEntry, bool, error) {
	result := []graphprotocol.HierarchyEntry{}
	seen := map[string]bool{root.Fact.Occurrence: true}
	frontier := []graphprotocol.Entity{root}
	partial := false
	for depth := 1; depth <= maxAncestorDepth && len(frontier) > 0; depth++ {
		level := []graphprotocol.HierarchyEntry{}
		for _, parent := range frontier {
			rows, bounded, err := hierarchyNeighbors(ctx, ready, parent, "outgoing")
			if err != nil {
				return nil, false, err
			}
			partial = partial || bounded
			for _, row := range rows {
				level = append(level, hierarchyEntry(row, parent.ID, depth))
			}
		}
		sortHierarchy(level)
		frontier = nil
		for _, entry := range level {
			occurrence := entry.Entity.Fact.Occurrence
			if seen[occurrence] {
				continue
			}
			seen[occurrence] = true
			result = append(result, entry)
			frontier = append(frontier, entry.Entity)
		}
		if depth == maxAncestorDepth && len(frontier) > 0 {
			for _, parent := range frontier {
				rows, bounded, err := hierarchyNeighbors(ctx, ready, parent, "outgoing")
				if err != nil {
					return nil, false, err
				}
				partial = partial || bounded
				for _, row := range rows {
					if !seen[row.Entity.Fact.Occurrence] {
						partial = true
						break
					}
				}
			}
		}
	}
	return result, partial, nil
}

func walkHierarchyDescendants(ctx context.Context, ready entityReady, store HierarchyStore, root graphprotocol.Entity) ([]graphprotocol.HierarchyEntry, int, int, bool, bool, error) {
	count, err := store.CountHierarchyChildren(ctx, HierarchyCountQuery{Snapshot: ready.selected[0], Occurrence: root.Fact.Occurrence})
	if err != nil {
		return nil, 0, 0, false, false, err
	}
	if count.Subtypes < 0 || count.Implementers < 0 || count.Implementers > count.Subtypes {
		return nil, 0, 0, false, false, ErrGenerationChanged
	}
	result := []graphprotocol.HierarchyEntry{}
	indexes := map[string]int{}
	seen := map[string]bool{root.Fact.Occurrence: true}
	frontier := []graphprotocol.Entity{root}
	bounded, partial := false, false
	for depth := 1; depth <= maxDescendantDepth && len(frontier) > 0; depth++ {
		level := []graphprotocol.HierarchyEntry{}
		for _, parent := range frontier {
			rows, queryBounded, queryErr := hierarchyNeighbors(ctx, ready, parent, "incoming")
			if queryErr != nil {
				return nil, 0, 0, false, false, queryErr
			}
			if queryBounded {
				bounded, partial = true, true
			}
			for _, row := range rows {
				level = append(level, hierarchyEntry(row, parent.ID, depth))
			}
		}
		sortHierarchy(level)
		frontier = nil
		levelSeen := map[string]bool{}
		for _, entry := range level {
			occurrence := entry.Entity.Fact.Occurrence
			if seen[occurrence] || levelSeen[occurrence] {
				continue
			}
			levelSeen[occurrence] = true
			if len(result) >= maxHierarchyRows {
				bounded, partial = true, true
				if index, exists := indexes[entry.ParentID]; exists {
					result[index].HiddenSubtypes++
				}
				continue
			}
			seen[occurrence] = true
			indexes[entry.Entity.ID] = len(result)
			result = append(result, entry)
			frontier = append(frontier, entry.Entity)
		}
		if bounded && len(result) >= maxHierarchyRows {
			break
		}
		if depth == maxDescendantDepth && len(frontier) > 0 {
			for _, parent := range frontier {
				children, countErr := store.CountHierarchyChildren(ctx, HierarchyCountQuery{Snapshot: ready.selected[0], Occurrence: parent.Fact.Occurrence})
				if countErr != nil {
					return nil, 0, 0, false, false, countErr
				}
				if children.Subtypes > 0 {
					bounded, partial = true, true
					if index, exists := indexes[parent.ID]; exists {
						result[index].HiddenSubtypes = children.Subtypes
					}
				}
			}
		}
	}
	if count.Subtypes > len(result) && len(result) >= maxHierarchyRows {
		bounded, partial = true, true
	}
	return result, count.Subtypes, count.Implementers, bounded, partial, nil
}

func hierarchyNeighbors(ctx context.Context, ready entityReady, parent graphprotocol.Entity, direction string) ([]EntityNeighbor, bool, error) {
	rows := []EntityNeighbor{}
	partial := false
	for _, relation := range hierarchyRelations {
		remaining := maxHierarchyRows - len(rows)
		if remaining <= 0 {
			partial = true
			break
		}
		found, err := ready.store.EntityNeighbors(ctx, EntityNeighborQuery{Snapshot: ready.selected[0], Occurrence: parent.Fact.Occurrence, Relation: relation, Direction: direction, Limit: remaining + 1})
		if err != nil {
			return nil, false, err
		}
		if len(found) > remaining {
			found, partial = found[:remaining], true
		}
		for _, row := range found {
			if row.Entity.RepositoryID != parent.RepositoryID || row.Entity.Fact == nil || row.Edge.RepositoryID != parent.RepositoryID || row.Edge.Fact == nil {
				return nil, false, ErrGenerationChanged
			}
		}
		rows = append(rows, found...)
	}
	return rows, partial, nil
}

func hierarchyEntry(row EntityNeighbor, parentID string, depth int) graphprotocol.HierarchyEntry {
	relation, _ := relationshipName(row.Edge.Fact.Kind)
	entry := graphprotocol.HierarchyEntry{Entity: row.Entity, Depth: depth, ParentID: parentID, Relation: relation, Edge: row.Edge, Synthesized: row.Edge.Fact.GetProvenance() == "heuristic"}
	for _, extension := range row.Edge.Fact.Extensions {
		if extension.Namespace != "codegraph.edge-metadata" {
			continue
		}
		var metadata struct {
			SynthesizedBy string `json:"synthesizedBy"`
			Via           string `json:"via"`
			RegisteredAt  string `json:"registeredAt"`
		}
		if json.Unmarshal(extension.Json, &metadata) == nil {
			entry.Via = metadata.SynthesizedBy
			if entry.Via == "" {
				entry.Via = metadata.Via
			}
			entry.RegisteredAt = metadata.RegisteredAt
		}
		break
	}
	return entry
}

func sortHierarchy(entries []graphprotocol.HierarchyEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Relation != b.Relation {
			return a.Relation == "extends"
		}
		if a.Entity.Fact.Name != b.Entity.Fact.Name {
			return a.Entity.Fact.Name < b.Entity.Fact.Name
		}
		if a.Entity.Fact.GetPath() != b.Entity.Fact.GetPath() {
			return a.Entity.Fact.GetPath() < b.Entity.Fact.GetPath()
		}
		if a.Entity.Fact.GetLocation().GetStart().GetLine() != b.Entity.Fact.GetLocation().GetStart().GetLine() {
			return a.Entity.Fact.GetLocation().GetStart().GetLine() < b.Entity.Fact.GetLocation().GetStart().GetLine()
		}
		return a.Entity.Fact.Occurrence < b.Entity.Fact.Occurrence
	})
}

func matchDerivedOverrides(ctx context.Context, ready entityReady, focus graphprotocol.Entity, ancestors []graphprotocol.HierarchyEntry) ([]graphprotocol.DerivedOverride, error) {
	if len(ancestors) == 0 {
		return nil, nil
	}
	own, _, err := containedMembers(ctx, ready, focus)
	if err != nil || len(own) == 0 {
		return nil, err
	}
	type base struct {
		member graphprotocol.Entity
		owner  graphprotocol.HierarchyEntry
	}
	byName := map[string]base{}
	for _, ancestor := range ancestors[:min(len(ancestors), maxOverrideAncestors)] {
		members, _, memberErr := containedMembers(ctx, ready, ancestor.Entity)
		if memberErr != nil {
			return nil, memberErr
		}
		for _, member := range members {
			if _, exists := byName[member.Fact.Name]; !exists {
				byName[member.Fact.Name] = base{member: member, owner: ancestor}
			}
		}
	}
	result := []graphprotocol.DerivedOverride{}
	for _, member := range own {
		if !overridableKinds[member.Fact.Kind] {
			continue
		}
		match, exists := byName[member.Fact.Name]
		if !exists || match.member.ID == member.ID {
			continue
		}
		result = append(result, graphprotocol.DerivedOverride{Member: member, BaseMember: match.member, BaseType: match.owner.Entity, Relation: match.owner.Relation, SignatureUncertain: true})
	}
	return result, nil
}

func containedMembers(ctx context.Context, ready entityReady, container graphprotocol.Entity) ([]graphprotocol.Entity, bool, error) {
	found, err := ready.store.EntityNeighbors(ctx, EntityNeighborQuery{Snapshot: ready.selected[0], Occurrence: container.Fact.Occurrence, Relation: "contains", Direction: "outgoing", Limit: maxHierarchyRows + 1})
	if err != nil {
		return nil, false, err
	}
	partial := len(found) > maxHierarchyRows
	if partial {
		found = found[:maxHierarchyRows]
	}
	result := make([]graphprotocol.Entity, len(found))
	for i, row := range found {
		if row.Entity.RepositoryID != container.RepositoryID || row.Entity.Fact == nil || row.Edge.Fact == nil {
			return nil, false, ErrGenerationChanged
		}
		result[i] = row.Entity
	}
	slices.SortFunc(result, func(a, b graphprotocol.Entity) int {
		if a.Fact.GetLocation().GetStart().GetLine() != b.Fact.GetLocation().GetStart().GetLine() {
			return int(a.Fact.GetLocation().GetStart().GetLine() - b.Fact.GetLocation().GetStart().GetLine())
		}
		return strings.Compare(a.Fact.Occurrence, b.Fact.Occurrence)
	})
	return result, partial, nil
}

func hierarchyRows(items []graphprotocol.HierarchyEntry, displayLimit int) graphprotocol.HierarchyRows {
	return graphprotocol.HierarchyRows{Items: items, Total: len(items), Shown: min(len(items), displayLimit), Truncated: len(items) > displayLimit}
}

func finishTypeHierarchy(ctx context.Context, ready entityReady, result graphprotocol.TypeHierarchyResponse) (graphprotocol.TypeHierarchyResponse, error) {
	entities := []*graphprotocol.Entity{&result.Focus}
	for i := range result.Ancestors.Items {
		entities = append(entities, &result.Ancestors.Items[i].Entity)
	}
	for i := range result.Descendants.Items {
		entities = append(entities, &result.Descendants.Items[i].Entity)
	}
	for i := range result.DerivedOverrides {
		entities = append(entities, &result.DerivedOverrides[i].Member, &result.DerivedOverrides[i].BaseMember, &result.DerivedOverrides[i].BaseType)
	}
	for _, entity := range entities {
		if entity.Fact == nil {
			continue
		}
		values := []graphprotocol.Entity{*entity}
		if err := ready.publicEntities(values); err != nil {
			return graphprotocol.TypeHierarchyResponse{}, err
		}
		*entity = values[0]
	}
	for _, entries := range [][]graphprotocol.HierarchyEntry{result.Ancestors.Items, result.Descendants.Items} {
		for i := range entries {
			id := ready.publicIDs[entries[i].Edge.RepositoryID]
			if id == 0 {
				return graphprotocol.TypeHierarchyResponse{}, ErrGenerationChanged
			}
			entries[i].Edge.RepositoryID = id
		}
	}
	if err := ready.current(ctx); err != nil {
		return graphprotocol.TypeHierarchyResponse{}, err
	}
	result.Generations = ready.publicGenerations()
	result.Analysis = analysisState(ready)
	if err := entityResponseSize(result); err != nil {
		return graphprotocol.TypeHierarchyResponse{}, err
	}
	return result, nil
}
