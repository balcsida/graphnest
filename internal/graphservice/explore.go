package graphservice

import (
	"context"
	"github.com/balcsida/graphnest/internal/authn"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/pkg/api"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type ExploreConfig struct {
	graphprotocol.DiscoveryConfig
	LineNumbers *bool `json:"line_numbers,omitempty"`
	Adaptive    *bool `json:"adaptive,omitempty"`
}
type ExploreRequest struct {
	Repo                                                      api.GraphRepositorySelector
	Branch, Query                                             string
	Symbols, Files, RequiredOccurrences                       []string
	Limit, CandidateLimit, MaxFiles, SourceUnits, SourceBytes int
	Config                                                    ExploreConfig
}
type ExploreSelection struct {
	EntityID  string            `json:"entity_id"`
	Range     *graphv2.Location `json:"range,omitempty"`
	Segment   int               `json:"segment"`
	Selection *SourceSelection  `json:"selection,omitempty"`
	Status    string            `json:"status"`
}
type ExploreFile struct {
	Path        string                 `json:"path"`
	Fact        *graphv2.File          `json:"fact,omitempty"`
	Entities    []graphprotocol.Entity `json:"entities"`
	Pinned      bool                   `json:"pinned"`
	Required    bool                   `json:"required"`
	Score       float64                `json:"score"`
	Reservation int                    `json:"reservation_utf16_units"`
	Status      string                 `json:"status"`
	Mode        string                 `json:"mode"`
	Segments    []InspectionSource     `json:"segments"`
	Selections  []ExploreSelection     `json:"selections"`
	Boundaries  []string               `json:"boundaries,omitempty"`
}
type ExploreUsage struct {
	SourceReads  int `json:"source_reads"`
	SourceBytes  int `json:"source_bytes"`
	SourceUnits  int `json:"source_utf16_units"`
	GraphQueries int `json:"graph_queries"`
}
type ExploreResponse struct {
	Discovery        graphprotocol.DiscoverResponse   `json:"discovery"`
	Files            []ExploreFile                    `json:"files"`
	Relationships    []graphprotocol.TraverseResponse `json:"relationships"`
	Generations      []graphprotocol.Generation       `json:"generations"`
	CorpusFiles      int64                            `json:"corpus_files"`
	LineNumbers      bool                             `json:"line_numbers"`
	Complete         bool                             `json:"complete"`
	Boundaries       []string                         `json:"boundaries,omitempty"`
	Handoffs         []string                         `json:"handoffs,omitempty"`
	Usage            ExploreUsage                     `json:"usage"`
	SourceUnitsLimit int                              `json:"source_utf16_units_limit"`
	SourceBytesLimit int                              `json:"source_bytes_limit"`
	FileLimit        int                              `json:"file_limit"`
}
type allocationCandidate struct {
	Path          string
	Score, Worth  float64
	Spine, Pinned bool
}
type exploreAllocation struct {
	Allowances map[string]int
	Cliffed    []string
	Pool       int
}
type sourceBudget struct{ Units, Files int }

func exploreBudget(count int64) sourceBudget {
	if count < 150 {
		return sourceBudget{13000, 4}
	}
	if count < 500 {
		return sourceBudget{18000, 5}
	}
	return sourceBudget{24000, 8}
}

// Allocation uses JavaScript-compatible UTF-16 units, independently of the
// source byte and serialized response ceilings. Keep the pinned allocator's
// floors, cliff and proportional reservations; no file races for another's share.
func allocateExplore(c []allocationCandidate, units, maxFiles int) exploreAllocation {
	result := exploreAllocation{Allowances: map[string]int{}}
	weights := make(map[string]float64, len(c))
	top := 0.0
	for _, f := range c {
		w := max(0, f.Score) * max(0, min(1, f.Worth))
		if f.Spine {
			w *= 2
		}
		if math.IsInf(w, 0) || math.IsNaN(w) {
			w = 0
		}
		weights[f.Path] = w
		top = max(top, w)
	}
	for _, f := range c {
		if f.Pinned {
			weights[f.Path] = max(weights[f.Path], top, 1)
		}
	}
	for _, w := range weights {
		top = max(top, w)
	}
	if top <= 0 {
		return result
	}
	admitted := []allocationCandidate{}
	cliff := min(top*.15, 10)
	for _, f := range c {
		if !f.Pinned && !f.Spine && weights[f.Path] < cliff {
			result.Cliffed = append(result.Cliffed, f.Path)
		} else {
			admitted = append(admitted, f)
		}
	}
	if len(admitted) == 0 {
		admitted = append(admitted, c[0])
		result.Cliffed = slices.DeleteFunc(result.Cliffed, func(p string) bool { return p == c[0].Path })
	}
	for _, f := range admitted[min(maxFiles, len(admitted)):] {
		result.Cliffed = append(result.Cliffed, f.Path)
	}
	admitted = admitted[:min(maxFiles, len(admitted))]
	affordable := max(1, units/900)
	if len(admitted) > affordable {
		byWeight := slices.Clone(admitted)
		slices.SortStableFunc(byWeight, func(a, b allocationCandidate) int {
			if weights[a.Path] > weights[b.Path] {
				return -1
			}
			if weights[a.Path] < weights[b.Path] {
				return 1
			}
			return 0
		})
		keep := map[string]bool{}
		for _, f := range byWeight[:affordable] {
			keep[f.Path] = true
		}
		admitted = slices.DeleteFunc(admitted, func(f allocationCandidate) bool {
			if keep[f.Path] || f.Pinned || f.Spine {
				return false
			}
			result.Cliffed = append(result.Cliffed, f.Path)
			return true
		})
	}
	result.Pool = max(0, units-200*len(admitted))
	total := 0.0
	for _, f := range admitted {
		total += weights[f.Path]
	}
	if total <= 0 || len(admitted) == 0 {
		return result
	}
	floors := min(result.Pool, 700*len(admitted))
	spare := max(0, result.Pool-floors)
	ceiling := int(math.Floor(float64(units)*.7 + .5))
	for _, f := range admitted {
		result.Allowances[f.Path] = min(floors/len(admitted)+int(math.Floor(float64(spare)*weights[f.Path]/total)), ceiling)
	}
	return result
}

type discoveryBackend interface {
	Discover(context.Context, graphprotocol.DiscoverRequest) (graphprotocol.DiscoverResponse, error)
}

// Explore composes the existing query and exact-source boundaries in one
// selected immutable scope. No authority or source is retained across calls.
func (s *Service) Explore(ctx context.Context, p authn.Principal, r ExploreRequest) (ExploreResponse, error) {
	if r.MaxFiles < 0 || r.MaxFiles > 20 || r.SourceUnits < 0 || r.SourceUnits > 100000 || r.SourceBytes < 0 || r.SourceBytes > 256<<10 || r.Limit < 0 || r.Limit > 100 || r.CandidateLimit < 0 || r.CandidateLimit > 1000 || len(r.RequiredOccurrences) > 20 || len(r.Files) > 20 || len(r.Symbols) > 20 || len(r.Query) > 16384 || !utf8.ValidString(r.Query) {
		return ExploreResponse{}, ErrInvalidRequest
	}
	for _, v := range r.RequiredOccurrences {
		if v == "" || len(v) > 16384 || !utf8.ValidString(v) {
			return ExploreResponse{}, ErrInvalidRequest
		}
	}
	for _, path := range r.Files {
		normalized, err := graphquery.NormalizeFilePath(path)
		if err != nil || normalized == "." || normalized != path {
			return ExploreResponse{}, ErrInvalidRequest
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	i, err := s.inspectionScope(ctx, p, r.Repo, r.Branch)
	if err != nil {
		return ExploreResponse{}, err
	}
	backend, ok := s.Backend.(discoveryBackend)
	if !ok {
		return ExploreResponse{}, ErrGraphNotReady
	}
	corpus, err := i.backend.IndexedFiles(ctx, graphprotocol.FilesRequest{Scope: i.scope, Limit: 1, IncludeCount: true})
	if err != nil {
		return ExploreResponse{}, err
	}
	if err = i.generations(corpus.Generations); err != nil {
		return ExploreResponse{}, err
	}
	if corpus.TotalFiles == nil || *corpus.TotalFiles < int64(len(corpus.Files)) || len(corpus.Files) > 1 {
		return ExploreResponse{}, ErrGraphNotReady
	}
	for _, f := range corpus.Files {
		if f.RepositoryID != i.selected.GitHubID || f.Fact == nil {
			return ExploreResponse{}, ErrGraphNotReady
		}
	}
	config := r.Config.DiscoveryConfig
	discovery, err := backend.Discover(ctx, graphprotocol.DiscoverRequest{Scope: i.scope, Query: r.Query, Symbols: r.Symbols, Files: r.Files, Limit: r.Limit, CandidateLimit: r.CandidateLimit, Config: config})
	if err != nil {
		return ExploreResponse{}, err
	}
	if err = sameInspectionGeneration(corpus.Generations, discovery.Generations); err != nil {
		return ExploreResponse{}, err
	}
	result := ExploreResponse{Discovery: discovery, Generations: corpus.Generations, CorpusFiles: *corpus.TotalFiles, LineNumbers: r.Config.LineNumbers == nil || *r.Config.LineNumbers, Complete: true, Usage: ExploreUsage{GraphQueries: 2}}
	boundary := func(reason string) {
		result.Complete = false
		if !slices.Contains(result.Boundaries, reason) {
			result.Boundaries = append(result.Boundaries, reason)
		}
	}
	if discovery.Status == "no_entry_point" || discovery.Confidence == "low" {
		boundary("weak_entry_point")
		result.Handoffs = append(result.Handoffs, "Name an indexed file, a qualified symbol, or an occurrence to select exact context.")
	}
	if discovery.CandidateTruncated || discovery.ResultTruncated {
		boundary("discovery_limit")
	}
	budget := exploreBudget(result.CorpusFiles)
	if r.SourceUnits > 0 {
		budget.Units = r.SourceUnits
	}
	if r.MaxFiles > 0 {
		budget.Files = r.MaxFiles
	}
	if r.SourceBytes == 0 {
		r.SourceBytes = 256 << 10
	}
	result.SourceUnitsLimit, result.SourceBytesLimit = budget.Units, r.SourceBytes
	byPath := map[string]int{}
	seen := map[string]bool{}
	required := map[string]bool{}
	roots := []graphprotocol.Entity{}
	candidates := map[string]allocationCandidate{}
	ensureFile := func(path string) *ExploreFile {
		at, ok := byPath[path]
		if !ok {
			at = len(result.Files)
			byPath[path] = at
			result.Files = append(result.Files, ExploreFile{Path: path, Status: "pending"})
			candidates[path] = allocationCandidate{Path: path, Worth: 1}
		}
		return &result.Files[at]
	}
	addEntity := func(e graphprotocol.Entity, score float64, pin, need bool) error {
		if err := i.entities([]graphprotocol.Entity{e}); err != nil {
			return err
		}
		if e.ID == "" {
			return ErrGraphNotReady
		}
		if need {
			required[e.ID] = true
		}
		path := e.Fact.GetPath()
		if path == "" {
			boundary("virtual_entity")
			return nil
		}
		f := ensureFile(path)
		if !seen[e.ID] {
			seen[e.ID] = true
			f.Entities = append(f.Entities, e)
		}
		f.Score = max(f.Score, score)
		f.Required = f.Required || need
		f.Pinned = f.Pinned || pin
		candidate := candidates[path]
		candidate.Score = max(candidate.Score, score)
		candidate.Spine = candidate.Spine || need
		candidate.Pinned = candidate.Pinned || pin
		candidates[path] = candidate
		return nil
	}
	for _, path := range r.Files {
		f := ensureFile(path)
		f.Pinned = true
		c := candidates[path]
		c.Pinned = true
		candidates[path] = c
	}
	// A concrete path in the query remains useful even for a file-only record.
	pathProbes := map[string]bool{}
	for _, word := range strings.Fields(r.Query) {
		path := strings.Trim(word, "`\"'")
		if strings.Contains(path, ".") && !strings.Contains(path, ":") {
			if normalized, e := graphquery.NormalizeFilePath(path); e == nil && normalized == path {
				if pathProbes[path] {
					continue
				}
				if len(pathProbes) >= 20 {
					boundary("query_path_limit")
					break
				}
				pathProbes[path] = true
				page, e := i.backend.IndexedFiles(ctx, graphprotocol.FilesRequest{Scope: i.scope, Path: &path, Limit: 1})
				result.Usage.GraphQueries++
				if e != nil {
					return ExploreResponse{}, e
				}
				if e = sameInspectionGeneration(result.Generations, page.Generations); e != nil {
					return ExploreResponse{}, e
				}
				if len(page.Files) > 0 {
					if len(page.Files) != 1 || page.Files[0].RepositoryID != i.selected.GitHubID || page.Files[0].Fact == nil || page.Files[0].Fact.Path != path {
						return ExploreResponse{}, ErrGraphNotReady
					}
					f := ensureFile(path)
					f.Pinned = true
					f.Fact = page.Files[0].Fact
					c := candidates[path]
					c.Pinned = true
					candidates[path] = c
				}
			}
		}
	}
	for _, m := range discovery.Matches {
		need := (m.Pinned && slices.Contains([]string{"function", "method", "constructor", "component"}, m.Entity.Fact.GetKind())) || slices.Contains(r.Symbols, m.Entity.Fact.GetName()) || slices.Contains(r.RequiredOccurrences, m.Entity.Fact.GetOccurrence())
		pin := slices.Contains(r.Files, m.Entity.Fact.GetPath())
		if err = addEntity(m.Entity, m.Score, pin, need); err != nil {
			return ExploreResponse{}, err
		}
		if m.Entity.Fact.GetPath() != "" {
			f := ensureFile(m.Entity.Fact.GetPath())
			if m.File != nil {
				if m.File.Path != f.Path {
					return ExploreResponse{}, ErrGraphNotReady
				}
				f.Fact = m.File
			}
			c := candidates[f.Path]
			worth := 1.0
			if m.Test || m.Deprioritized || m.Ambient {
				worth = .5
			}
			if m.Generated {
				worth = .3
			}
			c.Worth = min(c.Worth, worth)
			candidates[f.Path] = c
		}
		roots = append(roots, m.Entity)
	}
	for _, occurrence := range r.RequiredOccurrences {
		page, e := i.backend.Entities(ctx, graphprotocol.EntitiesRequest{Scope: i.scope, Selector: graphprotocol.EntitySelector{Occurrence: &occurrence}, Limit: 1})
		result.Usage.GraphQueries++
		if e != nil {
			return ExploreResponse{}, e
		}
		if e = sameInspectionGeneration(result.Generations, page.Generations); e != nil {
			return ExploreResponse{}, e
		}
		if len(page.Entities) != 1 || page.NextCursor != "" {
			boundary("required_entity_unavailable")
			continue
		}
		if page.Entities[0].Fact == nil || page.Entities[0].Fact.Occurrence != occurrence {
			return ExploreResponse{}, ErrGraphNotReady
		}
		e = addEntity(page.Entities[0], 1, false, true)
		if e != nil {
			return ExploreResponse{}, e
		}
		if !slices.ContainsFunc(roots, func(v graphprotocol.Entity) bool { return v.ID == page.Entities[0].ID }) {
			roots = append([]graphprotocol.Entity{page.Entities[0]}, roots...)
		}
	}
	// Required roots run first. Twenty one-hop queries bound aggregate graph work
	// even when each underlying engine returns its own maximum neighborhood.
	slices.SortStableFunc(roots, func(a, b graphprotocol.Entity) int {
		if required[a.ID] != required[b.ID] {
			if required[a.ID] {
				return -1
			}
			return 1
		}
		return 0
	})
	nodes, edges, graphBytes := 0, 0, 0
	for rootIndex, root := range roots {
		if rootIndex >= 10 {
			boundary("graph_work_limit")
			break
		}
		for _, direction := range []string{"incoming", "outgoing"} {
			graph, e := i.backend.Traverse(ctx, graphprotocol.TraverseRequest{Scope: i.scope, Root: graphprotocol.EntitySelector{Occurrence: &root.Fact.Occurrence}, Relations: []string{"calls", "extends", "implements"}, Direction: direction, MaxDepth: 1})
			result.Usage.GraphQueries++
			if e != nil {
				return ExploreResponse{}, e
			}
			if e = sameInspectionGeneration(result.Generations, graph.Generations); e != nil {
				return ExploreResponse{}, e
			}
			if e = i.entities(graph.Entities); e != nil {
				return ExploreResponse{}, e
			}
			for _, edge := range graph.Edges {
				if edge.RepositoryID != i.selected.GitHubID || edge.Fact == nil {
					return ExploreResponse{}, ErrGraphNotReady
				}
			}
			if nodes+len(graph.Entities) > 1000 || edges+len(graph.Edges) > 5000 {
				boundary("graph_work_limit")
				continue
			}
			nodes += len(graph.Entities)
			edges += len(graph.Edges)
			if e = graphquery.AddEntityQueryBytes(&graphBytes, graph); e != nil {
				return ExploreResponse{}, e
			}
			result.Relationships = append(result.Relationships, graph)
			if graph.Partial || graph.Status != "ok" {
				boundary("relationship_boundary")
			}
			for _, entity := range graph.Entities {
				if e = addEntity(entity, max(1, candidates[root.Fact.GetPath()].Score*.25), false, false); e != nil {
					return ExploreResponse{}, e
				}
			}
		}
	}
	if len(result.Files) > 100 {
		return ExploreResponse{}, graphquery.ErrQuerySize
	}
	// Relationship-only files need original metadata before allocation too:
	// otherwise generated callees receive an ordinary file's reservation.
	fileBytes := 0
	for index := range result.Files {
		f := &result.Files[index]
		if f.Fact == nil {
			page, e := i.backend.IndexedFiles(ctx, graphprotocol.FilesRequest{Scope: i.scope, Path: &f.Path, Limit: 1})
			result.Usage.GraphQueries++
			if e != nil {
				return ExploreResponse{}, e
			}
			if e = sameInspectionGeneration(result.Generations, page.Generations); e != nil {
				return ExploreResponse{}, e
			}
			if len(page.Files) == 0 {
				f.Status = "file_not_indexed"
				continue
			}
			if len(page.Files) != 1 || page.Files[0].RepositoryID != i.selected.GitHubID || page.Files[0].Fact == nil || page.Files[0].Fact.Path != f.Path {
				return ExploreResponse{}, ErrGraphNotReady
			}
			f.Fact = page.Files[0].Fact
		}
		if e := graphquery.AddEntityQueryBytes(&fileBytes, f.Fact); e != nil {
			return ExploreResponse{}, e
		}
		if f.Fact.GetGenerated() {
			c := candidates[f.Path]
			c.Worth = min(c.Worth, .3)
			candidates[f.Path] = c
		}
	}
	slices.SortStableFunc(result.Files, func(a, b ExploreFile) int {
		if a.Pinned != b.Pinned {
			if a.Pinned {
				return -1
			}
			return 1
		}
		if a.Required != b.Required {
			if a.Required {
				return -1
			}
			return 1
		}
		if a.Score > b.Score {
			return -1
		}
		if a.Score < b.Score {
			return 1
		}
		return 0
	})
	protected := 0
	for _, f := range result.Files {
		if f.Pinned || f.Required {
			protected++
		}
	}
	if protected > 20 || r.MaxFiles > 0 && protected > r.MaxFiles {
		return ExploreResponse{}, ErrInvalidRequest
	}
	budget.Files = max(budget.Files, protected)
	result.FileLimit = budget.Files
	ordered := make([]allocationCandidate, 0, len(result.Files))
	for _, f := range result.Files {
		c := candidates[f.Path]
		if f.Pinned {
			c.Worth = 1
		}
		ordered = append(ordered, c)
	}
	allocation := allocateExplore(ordered, budget.Units, budget.Files)
	if err = s.exploreSources(ctx, p, i, r, &result, allocation, required); err != nil {
		return ExploreResponse{}, err
	}
	for _, f := range result.Files {
		if f.Status != "ok" || len(f.Boundaries) > 0 {
			boundary("source_boundary")
		}
		if f.Fact != nil && hasFileExtractionErrors(f.Fact.Errors) {
			boundary("file_extraction_errors")
		}
	}
	for _, g := range result.Generations {
		if g.UnresolvedCount > 0 {
			boundary("generation_unresolved_analysis")
		}
		if g.DiagnosticCount > 0 {
			boundary("generation_diagnostics")
		}
	}
	if len(result.Files) == 0 {
		boundary("no_source")
		result.Handoffs = append(result.Handoffs, "List the indexed file inventory, then pin a path in Explore.")
	}
	if !result.Complete {
		result.Handoffs = append(result.Handoffs, "Pin an omitted file or require a specific occurrence to focus the next source allocation.")
	}
	if err = s.finishInspection(ctx, p, i, result.Generations, result); err != nil {
		return ExploreResponse{}, err
	}
	return result, nil
}
