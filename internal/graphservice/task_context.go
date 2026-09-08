package graphservice

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/balcsida/graphnest/internal/authn"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/pkg/api"
)

type taskContextBackend interface {
	RelevantContext(context.Context, graphprotocol.RelevantContextRequest) (graphprotocol.RelevantContextResponse, error)
	SegmentMatches(context.Context, graphprotocol.SegmentRequest) (graphprotocol.SegmentResponse, error)
}

type FindRelevantContextRequest struct {
	Repo    api.GraphRepositorySelector
	Branch  string
	Query   string
	Options graphprotocol.RelevantContextOptions
}

type BuildTaskContextOptions struct {
	SearchLimit      *int     `json:"search_limit,omitempty"`
	TraversalDepth   *int     `json:"traversal_depth,omitempty"`
	MaxNodes         *int     `json:"max_nodes,omitempty"`
	MinScore         *float64 `json:"min_score,omitempty"`
	MaxCodeBlocks    *int     `json:"max_code_blocks,omitempty"`
	MaxCodeBlockSize *int     `json:"max_code_block_size,omitempty"`
	IncludeCode      *bool    `json:"include_code,omitempty"`
}

type BuildTaskContextRequest struct {
	Repo        api.GraphRepositorySelector
	Branch      string
	Query       string
	Title       string
	Description string
	Options     BuildTaskContextOptions
}

type AppliedBuildTaskContextOptions struct {
	graphprotocol.AppliedRelevantContextOptions
	MaxCodeBlocks    int  `json:"max_code_blocks"`
	MaxCodeBlockSize int  `json:"max_code_block_size"`
	IncludeCode      bool `json:"include_code"`
}

type TaskCodeBlock struct {
	Entity             graphprotocol.Entity `json:"entity"`
	Path               string               `json:"path"`
	Language           string               `json:"language,omitempty"`
	Range              *graphv2.Location    `json:"range,omitempty"`
	IndexedSHA         string               `json:"indexed_sha,omitempty"`
	BlobSHA            string               `json:"blob_sha,omitempty"`
	StartLine          int                  `json:"start_line,omitempty"`
	EndLine            int                  `json:"end_line,omitempty"`
	Content            string               `json:"content,omitempty"`
	Status             string               `json:"status"`
	OriginalUTF16Units *int                 `json:"original_utf16_units,omitempty"`
	ReturnedUTF16Units int                  `json:"returned_utf16_units"`
	Truncated          bool                 `json:"truncated"`
	Complete           bool                 `json:"complete"`
}

type TaskContextStats struct {
	NodeCount           int `json:"node_count"`
	EdgeCount           int `json:"edge_count"`
	FileCount           int `json:"file_count"`
	CodeBlockCount      int `json:"code_block_count"`
	TotalCodeUTF16Units int `json:"total_code_utf16_units"`
	SourceReads         int `json:"source_reads"`
	SourceBytes         int `json:"source_bytes"`
}

type TaskContext struct {
	Query        string                                `json:"query"`
	Summary      string                                `json:"summary"`
	Graph        graphprotocol.RelevantContextResponse `json:"graph"`
	EntryPoints  []graphprotocol.DiscoveryMatch        `json:"entry_points"`
	CodeBlocks   []TaskCodeBlock                       `json:"code_blocks"`
	RelatedFiles []string                              `json:"related_files"`
	Stats        TaskContextStats                      `json:"stats"`
	Options      AppliedBuildTaskContextOptions        `json:"options"`
	Boundaries   []string                              `json:"boundaries,omitempty"`
	Generations  []graphprotocol.Generation            `json:"generations"`
	Complete     bool                                  `json:"complete"`
}

func (s *Service) FindRelevantContext(ctx context.Context, principal authn.Principal, request FindRelevantContextRequest) (graphprotocol.RelevantContextResponse, error) {
	if len(request.Query) > 16384 || !utf8.ValidString(request.Query) {
		return graphprotocol.RelevantContextResponse{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	i, err := s.inspectionScope(ctx, principal, request.Repo, request.Branch)
	if err != nil {
		return graphprotocol.RelevantContextResponse{}, err
	}
	backend, ok := s.Backend.(taskContextBackend)
	if !ok {
		return graphprotocol.RelevantContextResponse{}, ErrGraphNotReady
	}
	var seedGeneration []graphprotocol.Generation
	if request.Options.SeedNames == nil && request.Query != "" {
		words := taskContextWords(request.Query)
		seeds := []string{}
		if len(words) > 0 {
			segments, err := backend.SegmentMatches(ctx, graphprotocol.SegmentRequest{Scope: i.scope, Words: words, Limit: 8})
			if err != nil {
				if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, graphquery.ErrGenerationChanged) || errors.Is(err, ErrGraphNotReady) {
					return graphprotocol.RelevantContextResponse{}, err
				}
			} else {
				if err := i.generations(segments.Generations); err != nil {
					return graphprotocol.RelevantContextResponse{}, err
				}
				seedGeneration = segments.Generations
				for _, match := range segments.Matches {
					if err := i.entities([]graphprotocol.Entity{match.Entity}); err != nil {
						return graphprotocol.RelevantContextResponse{}, err
					}
					if name := match.Entity.Fact.GetName(); name != "" && !slices.Contains(seeds, name) {
						seeds = append(seeds, name)
					}
				}
			}
		}
		request.Options.SeedNames = &seeds
	}
	result, err := backend.RelevantContext(ctx, graphprotocol.RelevantContextRequest{Scope: i.scope, Query: request.Query, Options: request.Options})
	if err != nil {
		return graphprotocol.RelevantContextResponse{}, err
	}
	if len(seedGeneration) > 0 {
		if err := sameInspectionGeneration(seedGeneration, result.Generations); err != nil {
			return graphprotocol.RelevantContextResponse{}, err
		}
	}
	if err := validateTaskGraph(i, result); err != nil {
		return graphprotocol.RelevantContextResponse{}, err
	}
	if err := s.finishInspection(ctx, principal, i, result.Generations, result); err != nil {
		return graphprotocol.RelevantContextResponse{}, err
	}
	return result, nil
}

func (s *Service) BuildTaskContext(ctx context.Context, principal authn.Principal, request BuildTaskContextRequest) (TaskContext, error) {
	query, err := taskContextQuery(request)
	if err != nil {
		return TaskContext{}, err
	}
	maxBlocks, maxBlockSize, includeCode, err := buildTaskOptions(request.Options)
	if err != nil {
		return TaskContext{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	i, err := s.inspectionScope(ctx, principal, request.Repo, request.Branch)
	if err != nil {
		return TaskContext{}, err
	}
	backend, ok := s.Backend.(taskContextBackend)
	if !ok {
		return TaskContext{}, ErrGraphNotReady
	}
	graph, err := backend.RelevantContext(ctx, graphprotocol.RelevantContextRequest{Scope: i.scope, Query: query, Options: taskRelevantOptions(request.Options)})
	if err != nil {
		return TaskContext{}, err
	}
	if err := validateTaskGraph(i, graph); err != nil {
		return TaskContext{}, err
	}
	result := TaskContext{
		Query: query, Graph: graph, EntryPoints: graph.EntryPoints, Generations: graph.Generations,
		Options:  AppliedBuildTaskContextOptions{AppliedRelevantContextOptions: graph.Options, MaxCodeBlocks: maxBlocks, MaxCodeBlockSize: maxBlockSize, IncludeCode: includeCode},
		Complete: graph.Status == graphprotocol.StatusOK && !graph.Partial,
	}
	for _, value := range graph.Boundaries {
		result.Boundaries = appendBoundary(result.Boundaries, value.Reason)
	}
	result.RelatedFiles = taskContextFiles(graph.Nodes)
	result.Stats.NodeCount, result.Stats.EdgeCount, result.Stats.FileCount = len(graph.Nodes), len(graph.Edges), len(result.RelatedFiles)
	result.Summary = taskContextSummary(graph, result.RelatedFiles)
	if includeCode {
		priority := taskContextCodePriority(graph)
		if len(priority) > maxBlocks {
			priority = priority[:maxBlocks]
			result.Complete = false
			result.Boundaries = appendBoundary(result.Boundaries, "code_block_limit")
		}
		bytesLeft := min(256<<10, s.limits().MaxResponseBytes)
		for _, entity := range priority {
			block := TaskCodeBlock{Entity: entity, Path: entity.Fact.GetPath(), Language: entity.Fact.GetLanguage(), Range: entity.Fact.Location, Status: "unreadable"}
			result.Stats.SourceReads++
			source, sourceErr := s.entitySource(ctx, principal, i, graph.Generations, entity, &bytesLeft)
			if sourceErr != nil {
				if ctx.Err() != nil {
					return TaskContext{}, ctx.Err()
				}
				result.Complete = false
				result.Boundaries = appendBoundary(result.Boundaries, "source_unavailable")
				result.CodeBlocks = append(result.CodeBlocks, block)
				continue
			}
			block.IndexedSHA, block.BlobSHA = source.IndexedSHA, source.BlobSHA
			block.StartLine, block.EndLine, block.Status = source.StartLine, source.EndLine, source.Status
			result.Stats.SourceBytes += len(source.Content)
			var original int
			block.Content, block.ReturnedUTF16Units, original, block.Truncated = truncateUTF16(source.Content, maxBlockSize)
			block.OriginalUTF16Units = &original
			if source.Status == "truncated" || source.Status == "budget_exhausted" {
				block.OriginalUTF16Units = nil
				block.Truncated = true
			}
			block.Complete = source.Status == "ok" && !block.Truncated
			if !block.Complete {
				result.Complete = false
				reason := "source_unavailable"
				if block.Truncated || source.Status == "truncated" || source.Status == "budget_exhausted" {
					reason = "source_truncated"
				}
				result.Boundaries = appendBoundary(result.Boundaries, reason)
			}
			result.Stats.TotalCodeUTF16Units += block.ReturnedUTF16Units
			result.CodeBlocks = append(result.CodeBlocks, block)
		}
	}
	result.Stats.CodeBlockCount = len(result.CodeBlocks)
	if graph.Status != graphprotocol.StatusOK {
		result.Complete = false
		result.Boundaries = appendBoundary(result.Boundaries, "no_entry_point")
	}
	if err := s.finishInspection(ctx, principal, i, result.Generations, result); err != nil {
		return TaskContext{}, err
	}
	return result, nil
}

func taskContextWords(query string) []string {
	words := []string{}
	for _, word := range graphquery.IdentifierSegments(query) {
		if word != "" && !slices.Contains(words, word) {
			words = append(words, word)
			if len(words) == 32 {
				break
			}
		}
	}
	return words
}

func taskContextQuery(request BuildTaskContextRequest) (string, error) {
	if len(request.Query) > 16384 || len(request.Title)+len(request.Description) > 16384 || !utf8.ValidString(request.Query) || !utf8.ValidString(request.Title) || !utf8.ValidString(request.Description) || (request.Query != "" && (request.Title != "" || request.Description != "")) || (request.Title == "" && request.Description != "") {
		return "", ErrInvalidRequest
	}
	if request.Title == "" {
		return request.Query, nil
	}
	if request.Description == "" {
		return request.Title, nil
	}
	return request.Title + ": " + request.Description, nil
}

func buildTaskOptions(options BuildTaskContextOptions) (int, int, bool, error) {
	blocks, size, include := 5, 1500, true
	if options.MaxCodeBlocks != nil {
		blocks = *options.MaxCodeBlocks
	}
	if options.MaxCodeBlockSize != nil {
		size = *options.MaxCodeBlockSize
	}
	if options.IncludeCode != nil {
		include = *options.IncludeCode
	}
	if blocks < 0 || blocks > 20 || size < 0 || size > 100000 || include && (blocks == 0 || size == 0) {
		return 0, 0, false, ErrInvalidRequest
	}
	return blocks, size, include, nil
}

func taskRelevantOptions(options BuildTaskContextOptions) graphprotocol.RelevantContextOptions {
	return graphprotocol.RelevantContextOptions{
		SearchLimit:    options.SearchLimit,
		TraversalDepth: options.TraversalDepth,
		MaxNodes:       options.MaxNodes,
		MinScore:       options.MinScore,
	}
}

func validateTaskGraph(scope inspectionScope, graph graphprotocol.RelevantContextResponse) error {
	if err := scope.generations(graph.Generations); err != nil {
		return err
	}
	if err := scope.entities(graph.Nodes); err != nil {
		return err
	}
	nodeIDs, occurrences := map[string]bool{}, map[string]string{}
	for _, entity := range graph.Nodes {
		nodeIDs[entity.ID] = true
		occurrences[entity.ID] = entity.Fact.Occurrence
	}
	if err := scope.entities(func() []graphprotocol.Entity {
		values := make([]graphprotocol.Entity, len(graph.EntryPoints))
		for index := range graph.EntryPoints {
			values[index] = graph.EntryPoints[index].Entity
		}
		return values
	}()); err != nil {
		return err
	}
	for _, edge := range graph.Edges {
		if edge.RepositoryID != scope.selected.GitHubID || edge.Fact == nil || edge.Fact.Occurrence == "" || occurrences[edge.SourceID] != edge.Fact.Source || occurrences[edge.TargetID] != edge.Fact.Target {
			return ErrGraphNotReady
		}
	}
	for _, root := range graph.Roots {
		if !nodeIDs[root] {
			return ErrGraphNotReady
		}
	}
	return nil
}

func taskContextFiles(nodes []graphprotocol.Entity) []string {
	files := []string{}
	for _, entity := range nodes {
		if entity.Fact != nil && entity.Fact.GetPath() != "" && !slices.Contains(files, entity.Fact.GetPath()) {
			files = append(files, entity.Fact.GetPath())
		}
	}
	sort.Strings(files)
	return files
}

func taskContextCodePriority(graph graphprotocol.RelevantContextResponse) []graphprotocol.Entity {
	byID := map[string]graphprotocol.Entity{}
	for _, entity := range graph.Nodes {
		byID[entity.ID] = entity
	}
	result, seen := []graphprotocol.Entity{}, map[string]bool{}
	add := func(entity graphprotocol.Entity) {
		if entity.Fact != nil && entity.Fact.GetPath() != "" && entity.Fact.Location != nil && !seen[entity.ID] {
			seen[entity.ID] = true
			result = append(result, entity)
		}
	}
	for _, id := range graph.Roots {
		if entity, ok := byID[id]; ok {
			add(entity)
		}
	}
	rootIDs := map[string]bool{}
	for _, id := range graph.Roots {
		rootIDs[id] = true
	}
	candidates := []graphprotocol.Entity{}
	for _, entity := range graph.Nodes {
		if !seen[entity.ID] && slices.Contains([]string{"function", "method"}, entity.Fact.GetKind()) {
			candidates = append(candidates, entity)
		}
	}
	edgeRank := func(id string) (int, string) {
		rootRank, occurrence := 3, "~"
		for _, edge := range graph.Edges {
			if edge.Fact == nil {
				continue
			}
			rootID := ""
			if edge.SourceID == id && rootIDs[edge.TargetID] {
				rootID = edge.TargetID
			} else if edge.TargetID == id && rootIDs[edge.SourceID] {
				rootID = edge.SourceID
			}
			if rootID == "" {
				continue
			}
			candidateRank := 2
			switch byID[rootID].Fact.GetKind() {
			case "function":
				candidateRank = 0
			case "method":
				candidateRank = 1
			}
			if candidateRank < rootRank || candidateRank == rootRank && edge.Fact.GetOccurrence() < occurrence {
				rootRank, occurrence = candidateRank, edge.Fact.GetOccurrence()
			}
		}
		return rootRank, occurrence
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		iRoot, iEdge := edgeRank(candidates[i].ID)
		jRoot, jEdge := edgeRank(candidates[j].ID)
		if iRoot != jRoot {
			return iRoot < jRoot
		}
		return iEdge < jEdge
	})
	for _, entity := range candidates {
		add(entity)
	}
	for _, entity := range graph.Nodes {
		if entity.Fact.GetKind() == "class" {
			add(entity)
		}
	}
	return result
}

func taskContextSummary(graph graphprotocol.RelevantContextResponse, files []string) string {
	names := []string{}
	for _, entry := range graph.EntryPoints {
		if entry.Entity.Fact != nil && entry.Entity.Fact.GetName() != "" && !slices.Contains(names, entry.Entity.Fact.GetName()) {
			names = append(names, entry.Entity.Fact.GetName())
			if len(names) == 3 {
				break
			}
		}
	}
	summary := fmt.Sprintf("Found %d relevant code symbols across %d files.", len(graph.Nodes), len(files))
	if len(names) > 0 {
		summary += " Key entry points: " + strings.Join(names, ", ") + "."
	}
	return fmt.Sprintf("%s %d relationships identified.", summary, len(graph.Edges))
}

func truncateUTF16(value string, limit int) (string, int, int, bool) {
	units, total, end, stopped := 0, 0, len(value), false
	for index, r := range value {
		width := 1
		if r > 0xffff {
			width = 2
		}
		if !stopped && units+width > limit {
			end = index
			stopped = true
		}
		if !stopped {
			units += width
		}
		total += width
	}
	return value[:end], units, total, total > limit
}

func appendBoundary(values []string, value string) []string {
	if value != "" && !slices.Contains(values, value) {
		return append(values, value)
	}
	return values
}
