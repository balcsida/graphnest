package graphservice

import (
	"context"
	"encoding/json"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/pkg/api"
)

// InspectionBackend extends the existing engine without changing legacy wrappers.
type InspectionBackend interface {
	Entities(context.Context, graphprotocol.EntitiesRequest) (graphprotocol.EntitiesResponse, error)
	Traverse(context.Context, graphprotocol.TraverseRequest) (graphprotocol.TraverseResponse, error)
	IndexedFiles(context.Context, graphprotocol.FilesRequest) (graphprotocol.FilesResponse, error)
	ValidateGenerations(context.Context, graphprotocol.Scope, []graphprotocol.Generation) error
}

type ListFilesRequest struct {
	Repo                            api.GraphRepositorySelector
	Branch, Directory, Glob, Cursor string
	Limit                           int
}

type InspectEntityRequest struct {
	Repo        api.GraphRepositorySelector
	Branch      string
	Selector    graphprotocol.EntitySelector
	OmitSource  bool
	SourceBytes int
	Limit       int
	Cursor      string
}

type InspectFileRequest struct {
	Repo                                   api.GraphRepositorySelector
	Branch, Path, Cursor                   string
	Limit, StartLine, EndLine, SourceBytes int
	OmitSource                             bool
}

type InspectionResponse struct {
	Status      string                          `json:"status"`
	Entities    []graphprotocol.Entity          `json:"entities"`
	NextCursor  string                          `json:"next_cursor,omitempty"`
	File        *graphprotocol.IndexedFile      `json:"file,omitempty"`
	Callers     *graphprotocol.TraverseResponse `json:"callers,omitempty"`
	Callees     *graphprotocol.TraverseResponse `json:"callees,omitempty"`
	Structure   *graphprotocol.TraverseResponse `json:"structure,omitempty"`
	Sources     []InspectionSource              `json:"sources"`
	Generations []graphprotocol.Generation      `json:"generations"`
	Boundaries  []string                        `json:"boundaries,omitempty"`
	Complete    bool                            `json:"complete"`
	Coverage    string                          `json:"coverage"`
}

type inspectionScope struct {
	selected Snapshot
	scope    graphprotocol.Scope
	backend  InspectionBackend
}

func (s *Service) inspectionScope(ctx context.Context, p authn.Principal, selector api.GraphRepositorySelector, branch string) (inspectionScope, error) {
	if err := ctx.Err(); err != nil {
		return inspectionScope{}, err
	}
	selected, err := ResolveRepository(ctx, s.Store, p, selector, branch)
	if err != nil {
		return inspectionScope{}, err
	}
	b, ok := s.Backend.(InspectionBackend)
	if !ok {
		return inspectionScope{}, ErrGraphNotReady
	}
	// V2 queries do not expand into unrelated authorized legacy generations.
	scope := graphprotocol.Scope{SelectedRepositoryID: selected.ID, Repositories: []graphprotocol.RepositorySnapshot{{ID: selected.ID, GitHubID: selected.GitHubID, Name: selected.Name, Branch: selected.Branch, Commit: selected.Commit}}}
	return inspectionScope{selected, scope, b}, nil
}

func (i inspectionScope) generations(generations []graphprotocol.Generation) error {
	if len(generations) != 1 || generations[0].RepositoryID != i.selected.GitHubID || generations[0].Commit != i.selected.Commit || generations[0].UploadID <= 0 {
		return ErrGraphNotReady
	}
	return nil
}

func sameInspectionGeneration(a, b []graphprotocol.Generation) error {
	if len(a) != 1 || len(b) != 1 || a[0].RepositoryID != b[0].RepositoryID || a[0].UploadID != b[0].UploadID || a[0].Commit != b[0].Commit {
		return graphquery.ErrGenerationChanged
	}
	return nil
}

func (i inspectionScope) entities(entities []graphprotocol.Entity) error {
	for _, entity := range entities {
		if entity.RepositoryID != i.selected.GitHubID || entity.Fact == nil {
			return ErrGraphNotReady
		}
	}
	return nil
}

func (s *Service) finishInspection(ctx context.Context, p authn.Principal, i inspectionScope, generations []graphprotocol.Generation, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := i.generations(generations); err != nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(data) > s.limits().MaxResponseBytes {
		return graphquery.ErrQuerySize
	}
	if err := i.backend.ValidateGenerations(ctx, i.scope, generations); err != nil {
		return err
	}
	if err := s.reauthorize(ctx, p, i.selected, nil); err != nil {
		return err
	}
	return ctx.Err()
}

func (s *Service) ListFiles(ctx context.Context, p authn.Principal, r ListFilesRequest) (graphprotocol.FilesResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	i, err := s.inspectionScope(ctx, p, r.Repo, r.Branch)
	if err != nil {
		return graphprotocol.FilesResponse{}, err
	}
	result, err := i.backend.IndexedFiles(ctx, graphprotocol.FilesRequest{Scope: i.scope, Directory: r.Directory, Glob: r.Glob, Limit: r.Limit, Cursor: r.Cursor})
	if err != nil {
		return graphprotocol.FilesResponse{}, err
	}
	for _, f := range result.Files {
		if f.RepositoryID != i.selected.GitHubID || f.Fact == nil {
			return graphprotocol.FilesResponse{}, ErrGraphNotReady
		}
	}
	if err := s.finishInspection(ctx, p, i, result.Generations, result); err != nil {
		return graphprotocol.FilesResponse{}, err
	}
	return result, nil
}

func (s *Service) InspectEntity(ctx context.Context, p authn.Principal, r InspectEntityRequest) (InspectionResponse, error) {
	if (r.Selector.Occurrence == nil && r.Selector.Name == nil && r.Selector.QualifiedName == nil) || r.Limit < 0 || r.Limit > 100 || len(r.Cursor) > 512 || r.SourceBytes < 0 || r.SourceBytes > 256<<10 {
		return InspectionResponse{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	i, err := s.inspectionScope(ctx, p, r.Repo, r.Branch)
	if err != nil {
		return InspectionResponse{}, err
	}
	limit := r.Limit
	if limit == 0 {
		limit = 100
	}
	page, err := i.backend.Entities(ctx, graphprotocol.EntitiesRequest{Scope: i.scope, Selector: r.Selector, Limit: limit, Cursor: r.Cursor})
	if err != nil {
		return InspectionResponse{}, err
	}
	if err := i.generations(page.Generations); err != nil {
		return InspectionResponse{}, err
	}
	if err := i.entities(page.Entities); err != nil {
		return InspectionResponse{}, err
	}
	result := InspectionResponse{Status: "not_found", Entities: page.Entities, NextCursor: page.NextCursor, Generations: page.Generations, Coverage: "indexed_neighborhood"}
	if len(page.Entities) > 1 || page.NextCursor != "" || r.Cursor != "" {
		result.Status = "ambiguous"
	} else if len(page.Entities) == 1 {
		result.Status = "ok"
		result.Complete = true
		root := page.Entities[0]
		sources := []graphprotocol.Entity{root}
		seen := map[string]bool{root.ID: true}
		for _, direction := range []string{"incoming", "outgoing"} {
			graph, err := i.backend.Traverse(ctx, graphprotocol.TraverseRequest{Scope: i.scope, Root: graphprotocol.EntitySelector{Occurrence: &root.Fact.Occurrence}, Relations: []string{"calls"}, Direction: direction, MaxDepth: 1})
			if err != nil {
				return InspectionResponse{}, err
			}
			if err := sameInspectionGeneration(page.Generations, graph.Generations); err != nil {
				return InspectionResponse{}, err
			}
			if err := i.entities(graph.Entities); err != nil {
				return InspectionResponse{}, err
			}
			for _, edge := range graph.Edges {
				if edge.RepositoryID != i.selected.GitHubID || edge.Fact == nil {
					return InspectionResponse{}, ErrGraphNotReady
				}
			}
			if direction == "incoming" {
				result.Callers = &graph
			} else {
				result.Callees = &graph
			}
			if graph.Partial || graph.Status != "ok" {
				result.Complete = false
				result.Boundaries = append(result.Boundaries, "relationship_boundary")
			}
			for _, entity := range graph.Entities {
				if !seen[entity.ID] {
					seen[entity.ID] = true
					sources = append(sources, entity)
				}
			}
		}
		budget := r.SourceBytes
		if budget == 0 {
			budget = 64 << 10
		}
		if r.OmitSource {
			result.Complete = false
			result.Boundaries = append(result.Boundaries, "source_omitted")
		} else {
			for index, entity := range sources {
				if index >= 20 {
					result.Complete = false
					result.Boundaries = append(result.Boundaries, "source_read_limit")
					break
				}
				source, err := s.entitySource(ctx, p, i, page.Generations, entity, &budget)
				if err != nil {
					return InspectionResponse{}, err
				}
				result.Sources = append(result.Sources, source)
				if source.Status != "ok" {
					result.Complete = false
				}
			}
		}
	}
	inspectionAnalysis(&result)
	if err := s.finishInspection(ctx, p, i, result.Generations, result); err != nil {
		return InspectionResponse{}, err
	}
	return result, nil
}

func inspectionAnalysis(result *InspectionResponse) {
	fileErrors := result.File != nil && hasFileExtractionErrors(result.File.Fact.Errors)
	for _, source := range result.Sources {
		fileErrors = fileErrors || hasFileExtractionErrors(source.FileErrors)
	}
	if fileErrors {
		result.Complete = false
		result.Boundaries = append(result.Boundaries, "file_extraction_errors")
	}
	for _, g := range result.Generations {
		if g.UnresolvedCount > 0 {
			result.Complete = false
			result.Boundaries = append(result.Boundaries, "generation_unresolved_analysis")
		}
		if g.DiagnosticCount > 0 {
			result.Complete = false
			result.Boundaries = append(result.Boundaries, "generation_diagnostics")
		}
	}
}

func hasFileExtractionErrors(evidence *graphv2.Extension) bool {
	if evidence == nil {
		return false
	}
	var entries []json.RawMessage
	// Preserve unknown producer shapes, but do not assume they mean no errors.
	return json.Unmarshal(evidence.Json, &entries) != nil || len(entries) > 0
}

func (s *Service) InspectFile(ctx context.Context, p authn.Principal, r InspectFileRequest) (InspectionResponse, error) {
	filePath, err := graphquery.NormalizeFilePath(r.Path)
	if err != nil || filePath == "." || r.Limit < 0 || r.Limit > 100 || len(r.Cursor) > 512 || r.SourceBytes < 0 || r.SourceBytes > 256<<10 || r.StartLine < 0 || r.EndLine < 0 || r.EndLine != 0 && r.EndLine < r.StartLine {
		return InspectionResponse{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	i, err := s.inspectionScope(ctx, p, r.Repo, r.Branch)
	if err != nil {
		return InspectionResponse{}, err
	}
	files, err := i.backend.IndexedFiles(ctx, graphprotocol.FilesRequest{Scope: i.scope, Path: &filePath, Limit: 1})
	if err != nil {
		return InspectionResponse{}, err
	}
	if err := i.generations(files.Generations); err != nil {
		return InspectionResponse{}, err
	}
	result := InspectionResponse{Status: "not_found", Generations: files.Generations, Coverage: "indexed_file"}
	if len(files.Files) > 0 {
		file := files.Files[0]
		if len(files.Files) != 1 || file.RepositoryID != i.selected.GitHubID || file.Fact == nil || file.Fact.Path != filePath {
			return InspectionResponse{}, ErrGraphNotReady
		}
		result.Status = "ok"
		result.File = &file
		result.Complete = true
		limit := r.Limit
		if limit == 0 {
			limit = 100
		}
		outline, err := i.backend.Entities(ctx, graphprotocol.EntitiesRequest{Scope: i.scope, Selector: graphprotocol.EntitySelector{Path: &filePath}, Limit: limit, Cursor: r.Cursor})
		if err != nil {
			return InspectionResponse{}, err
		}
		if err := sameInspectionGeneration(files.Generations, outline.Generations); err != nil {
			return InspectionResponse{}, err
		}
		if err := i.entities(outline.Entities); err != nil {
			return InspectionResponse{}, err
		}
		result.Entities = outline.Entities
		result.NextCursor = outline.NextCursor
		if outline.NextCursor != "" || r.Cursor != "" {
			result.Complete = false
			result.Boundaries = append(result.Boundaries, "outline_page")
		}
		containers, err := i.backend.Entities(ctx, graphprotocol.EntitiesRequest{Scope: i.scope, Selector: graphprotocol.EntitySelector{Path: &filePath, Kind: "file"}, Limit: 2})
		if err != nil {
			return InspectionResponse{}, err
		}
		if err := sameInspectionGeneration(files.Generations, containers.Generations); err != nil {
			return InspectionResponse{}, err
		}
		if err := i.entities(containers.Entities); err != nil {
			return InspectionResponse{}, err
		}
		if len(containers.Entities) == 1 && containers.NextCursor == "" {
			structure, err := i.backend.Traverse(ctx, graphprotocol.TraverseRequest{Scope: i.scope, Root: graphprotocol.EntitySelector{Occurrence: &containers.Entities[0].Fact.Occurrence}, Relations: []string{"contains"}, Direction: "outgoing", MaxDepth: 32})
			if err != nil {
				return InspectionResponse{}, err
			}
			if err := sameInspectionGeneration(files.Generations, structure.Generations); err != nil {
				return InspectionResponse{}, err
			}
			if err := i.entities(structure.Entities); err != nil {
				return InspectionResponse{}, err
			}
			for _, edge := range structure.Edges {
				if edge.RepositoryID != i.selected.GitHubID || edge.Fact == nil {
					return InspectionResponse{}, ErrGraphNotReady
				}
			}
			result.Structure = &structure
			if structure.Partial || structure.Status != "ok" {
				result.Complete = false
				result.Boundaries = append(result.Boundaries, "structure_boundary")
			}
		} else if len(outline.Entities) > 0 {
			result.Complete = false
			result.Boundaries = append(result.Boundaries, "file_container_unavailable")
		}
		if r.OmitSource {
			result.Complete = false
			result.Boundaries = append(result.Boundaries, "source_omitted")
		} else {
			budget := r.SourceBytes
			if budget == 0 {
				budget = 64 << 10
			}
			source, err := s.readInspectionSource(ctx, p, i, InspectionSource{Path: filePath}, r.StartLine, r.EndLine, &budget, nil)
			if err != nil {
				return InspectionResponse{}, err
			}
			result.Sources = []InspectionSource{source}
			if source.Status != "ok" {
				result.Complete = false
			}
		}
	}
	inspectionAnalysis(&result)
	if err := s.finishInspection(ctx, p, i, result.Generations, result); err != nil {
		return InspectionResponse{}, err
	}
	return result, nil
}
