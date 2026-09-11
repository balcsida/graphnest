package graphquery

import (
	"context"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/balcsida/graphnest/internal/graphprotocol"
)

var ErrDiscoveryUnavailable = errors.New("graph discovery projection is unavailable; rebuild the generation")

// DiscoveryStore accepts the same trusted internal generation scope as EntityStore.
// Returned facts must be bounded before decoding; the service rechecks generation.
type DiscoveryStore interface {
	QueryDiscovery(context.Context, DiscoverySearch) ([]graphprotocol.DiscoveryMatch, error)
}

type DiscoverySearch struct {
	Snapshots                      []QuerySnapshot
	Query                          DiscoveryQuery
	Terms                          []string
	Groups                         [][]string
	Symbols, Files, Deprioritize   []string
	ExplicitSymbols, ExplicitFiles []string
	Limit                          int
	Fuzzy                          bool
	NoMultiterm                    bool
}

func (service *Service) Discover(ctx context.Context, req graphprotocol.DiscoverRequest) (graphprotocol.DiscoverResponse, error) {
	if service == nil {
		return graphprotocol.DiscoverResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	if len(req.Query) > 16384 || !utf8.ValidString(req.Query) || req.Limit < 0 || req.Limit > 100 || req.CandidateLimit < 0 || req.CandidateLimit > 1000 {
		return graphprotocol.DiscoverResponse{}, ErrInvalidRequest
	}
	for _, values := range [][]string{req.Symbols, req.Files, req.Config.ProjectTerms, req.Config.Deprioritize} {
		if len(values) > 32 {
			return graphprotocol.DiscoverResponse{}, ErrInvalidRequest
		}
		for _, v := range values {
			if v == "" || len(v) > 16384 || !utf8.ValidString(v) {
				return graphprotocol.DiscoverResponse{}, ErrInvalidRequest
			}
		}
	}
	ready, err := service.readyEntities(ctx, req.Scope)
	if err != nil {
		return graphprotocol.DiscoverResponse{}, err
	}
	store, ok := ready.store.(DiscoveryStore)
	if !ok {
		return graphprotocol.DiscoverResponse{}, ErrDiscoveryUnavailable
	}
	limit := req.Limit
	if limit == 0 {
		limit = 20
	}
	candidates := req.CandidateLimit
	if candidates == 0 {
		candidates = max(100, limit*5)
	}
	if candidates < limit {
		return graphprotocol.DiscoverResponse{}, ErrInvalidRequest
	}
	query := ParseDiscoveryQuery(req.Query)
	singleCharacter := utf8.RuneCountInString(NormalizeDiscovery(query.Text)) == 1
	project := []string{}
	for _, term := range req.Config.ProjectTerms {
		if normalized := discoveryProjectToken(term); len(normalized) >= 5 {
			project = append(project, normalized)
		}
	}
	terms := DiscoveryTerms(query.Text, project)
	if singleCharacter {
		terms = []string{NormalizeDiscovery(query.Text)}
	}
	// Explicitly bound query expansion rather than silently dropping late terms.
	if len(terms) > 128 {
		return graphprotocol.DiscoverResponse{}, ErrInvalidRequest
	}
	groups := [][]string{}
	seenWords := map[string]bool{}
	for _, word := range strings.Fields(query.Text) {
		normalized := NormalizeDiscovery(word)
		if seenWords[normalized] {
			continue
		}
		seenWords[normalized] = true
		ts := DiscoveryTerms(word, project)
		if singleCharacter {
			ts = terms
		}
		if len(ts) > 0 {
			groups = append(groups, ts)
		}
	}
	symbols := []string{}
	files := []string{}
	for _, v := range req.Symbols {
		symbols = append(symbols, NormalizeDiscovery(v))
	}
	for _, v := range req.Files {
		files = append(files, NormalizeDiscovery(v))
	}
	explicitSymbols, explicitFiles := slices.Clone(symbols), slices.Clone(files)
	// Whole query words are symbol/file pins, not their common-word segments.
	for _, word := range discoveryTokens.FindAllString(query.Text, -1) {
		if len(word) >= 2 || singleCharacter {
			normalized := NormalizeDiscovery(strings.Trim(word, `"`))
			if !slices.Contains(project, discoveryProjectToken(normalized)) {
				symbols = append(symbols, normalized)
				files = append(files, normalized)
			}
		}
	}
	matches, err := store.QueryDiscovery(ctx, DiscoverySearch{Snapshots: ready.selected, Query: query, Terms: terms, Groups: groups, Symbols: symbols, Files: files, Deprioritize: req.Config.Deprioritize, ExplicitSymbols: explicitSymbols, ExplicitFiles: explicitFiles, Limit: candidates + 1, NoMultiterm: req.Config.NoMultiterm})
	if err != nil {
		return graphprotocol.DiscoverResponse{}, err
	}
	result := graphprotocol.DiscoverResponse{Status: "no_entry_point", Confidence: "low", Matches: []graphprotocol.DiscoveryMatch{}, Generations: ready.publicGenerations(), CandidateLimit: candidates, CandidatesExamined: min(len(matches), candidates), CandidateTruncated: len(matches) > candidates, Coverage: "semantic_discovery_only"}
	for i := range matches {
		m := &matches[i]
		if m.Entity.Fact == nil || ready.publicIDs[m.Entity.RepositoryID] == 0 {
			return graphprotocol.DiscoverResponse{}, ErrGenerationChanged
		}
		selected := false
		for _, s := range ready.selected {
			selected = selected || s.RepositoryID == m.Entity.RepositoryID
		}
		if !selected {
			return graphprotocol.DiscoverResponse{}, ErrGenerationChanged
		}
		m.Entity.RepositoryID = ready.publicIDs[m.Entity.RepositoryID]
		if math.IsNaN(m.Score) || math.IsInf(m.Score, 0) {
			return graphprotocol.DiscoverResponse{}, ErrInvalidRequest
		}
	}
	if len(matches) > candidates {
		matches = matches[:candidates]
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.Fuzzy && b.Fuzzy && a.EditDistance != b.EditDistance {
			return a.EditDistance < b.EditDistance
		}
		if a.Pinned != b.Pinned {
			return a.Pinned
		}
		// Corroboration is a tier only for concrete entry candidates, never isolated
		// variables that happen to repeat two query words.
		ca := a.MatchedTerms >= 2 && (a.UsageCount > 0 || discoveryStrongKind(a.Entity.Fact.Kind)) && !a.Generated && !a.Ambient && !a.Test && !a.Deprioritized
		cb := b.MatchedTerms >= 2 && (b.UsageCount > 0 || discoveryStrongKind(b.Entity.Fact.Kind)) && !b.Generated && !b.Ambient && !b.Test && !b.Deprioritized
		if !req.Config.NoMultiterm && ca != cb {
			return ca
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Entity.RepositoryID != b.Entity.RepositoryID {
			return a.Entity.RepositoryID < b.Entity.RepositoryID
		}
		return a.Entity.Fact.Occurrence < b.Entity.Fact.Occurrence
	})
	result.ResultTruncated = len(matches) > limit
	if len(matches) > limit {
		matches = matches[:limit]
	}
	filterOnly := query.Text == "" && len(query.Kinds)+len(query.Languages)+len(query.Paths)+len(query.Names) > 0
	credible := false
	lowConfidenceMatch := singleCharacter && len(matches) > 0
	for _, m := range matches {
		credible = credible || (m.Pinned && m.Entity.Fact.Path != nil && slices.Contains(files, NormalizeDiscovery(m.Entity.Fact.GetPath()))) || slices.Contains(explicitSymbols, NormalizeDiscovery(m.Entity.Fact.Name)) || slices.Contains(explicitFiles, NormalizeDiscovery(m.Entity.Fact.GetPath())) || filterOnly || (len(terms) > 0 && (discoveryStrongKind(m.Entity.Fact.Kind) || m.UsageCount > 0))
		lowConfidenceMatch = lowConfidenceMatch || (m.Pinned && slices.Contains(symbols, NormalizeDiscovery(m.Entity.Fact.Name)))
	}
	if credible || lowConfidenceMatch {
		result.Status = "candidates"
		if credible && !singleCharacter {
			result.Confidence = "discovery_only"
		}
		result.Matches = matches
	}
	if err = entityResponseSize(result); err != nil {
		return graphprotocol.DiscoverResponse{}, err
	}
	if err = ready.current(ctx); err != nil {
		return graphprotocol.DiscoverResponse{}, err
	}
	return result, nil
}

func discoveryStrongKind(kind string) bool {
	return slices.Contains([]string{"function", "method", "class", "struct", "union", "interface", "trait", "protocol", "component", "route", "enum", "type_alias"}, kind)
}
