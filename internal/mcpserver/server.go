package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/grepnest/grepnest/internal/graphservice"
	"github.com/grepnest/grepnest/internal/httpapi"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/scipgraph"
	"github.com/grepnest/grepnest/internal/search"
	"github.com/grepnest/grepnest/internal/zoekt"
	"github.com/grepnest/grepnest/pkg/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	errInvalidSearch = errors.New("search query is invalid")
	errUnavailable   = errors.New("search service is unavailable")
	errOutputBudget  = errors.New("tool output exceeds configured budget")
)

const (
	maxRepositoryItems = 100
	maxToolOutputBytes = int64(256 << 10)
)

type Limits struct {
	MaxItems       int
	MaxOutputBytes int64
}

type Services struct {
	Search       *search.Service
	Repositories *repository.Service
	SCIP         *scipgraph.Service
	Graph        *graphservice.Service
}

type searchInput struct {
	Query          string   `json:"query" jsonschema:"search expression"`
	Repositories   []string `json:"repositories,omitempty" jsonschema:"repository names to search"`
	Limit          int      `json:"limit,omitempty" jsonschema:"maximum matches"`
	ContextLines   int      `json:"context_lines,omitempty" jsonschema:"context lines around each match"`
	MaxOutputBytes int64    `json:"max_output_bytes,omitempty" jsonschema:"maximum output bytes"`
}

type findInput struct {
	Pattern        string   `json:"pattern" jsonschema:"Zoekt path regular expression"`
	Repositories   []string `json:"repositories,omitempty" jsonschema:"repository names to search"`
	Limit          int      `json:"limit,omitempty" jsonschema:"maximum matches"`
	MaxOutputBytes int64    `json:"max_output_bytes,omitempty" jsonschema:"maximum output bytes"`
}

type output struct {
	Matches   []api.SearchMatch `json:"matches"`
	Truncated bool              `json:"truncated"`
}

type repositoryIDInput struct {
	RepositoryID   int64 `json:"repository_id" jsonschema:"GitHub repository ID"`
	MaxOutputBytes int64 `json:"max_output_bytes,omitempty" jsonschema:"maximum output bytes"`
}

type readFileInput struct {
	RepositoryID   int64  `json:"repository_id" jsonschema:"GitHub repository ID"`
	Path           string `json:"path" jsonschema:"repository-relative file path"`
	StartLine      int    `json:"start_line,omitempty" jsonschema:"first line to return"`
	EndLine        int    `json:"end_line,omitempty" jsonschema:"last line to return"`
	MaxOutputBytes int64  `json:"max_output_bytes,omitempty" jsonschema:"maximum output bytes"`
}

type listInput struct {
	Limit          int   `json:"limit,omitempty" jsonschema:"maximum repositories"`
	MaxOutputBytes int64 `json:"max_output_bytes,omitempty" jsonschema:"maximum output bytes"`
}

type repositoryListOutput struct {
	Repositories []api.RepositorySummary `json:"repositories"`
	Truncated    bool                    `json:"truncated"`
}

func New(service *search.Service, repositoryServices ...*repository.Service) *mcp.Server {
	var repositories *repository.Service
	if len(repositoryServices) > 0 {
		repositories = repositoryServices[0]
	}
	return NewWithLimits(Services{Search: service, Repositories: repositories}, Limits{})
}

func NewWithLimits(services Services, limits Limits) *mcp.Server {
	limits = normalizeLimits(limits)
	server := mcp.NewServer(&mcp.Implementation{Name: "grepnest", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name: "search_code", Description: "Search code contents when you know a symbol, string, or code expression.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input searchInput) (*mcp.CallToolResult, output, error) {
		return runSearch(ctx, services.Search, api.SearchRequest{
			Query: input.Query, Repositories: input.Repositories, Limit: input.Limit,
			ContextLines: input.ContextLines, MaxResponseBytes: input.MaxOutputBytes,
		}, outputBudget(input.MaxOutputBytes, limits.MaxOutputBytes))
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "find_files", Description: "Find files by path regular expression when file names or paths are known.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input findInput) (*mcp.CallToolResult, output, error) {
		return runSearch(ctx, services.Search, api.SearchRequest{
			Query: "file:" + input.Pattern, Repositories: input.Repositories,
			Limit: input.Limit, MaxResponseBytes: input.MaxOutputBytes,
		}, outputBudget(input.MaxOutputBytes, limits.MaxOutputBytes))
	})
	if services.SCIP != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name: "navigate_symbol", Description: "Navigate to definitions, references, or implementations for a source symbol.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"repository_id", "path", "line", "character", "operation"},
				"properties": map[string]any{
					"repository_id": positiveIntegerSchema("GitHub repository ID"),
					"path":          map[string]any{"type": "string", "minLength": 1, "description": "repository-relative source path"},
					"line":          positiveIntegerSchema("one-based source line"),
					"character":     map[string]any{"type": "integer", "minimum": 0, "description": "zero-based source character"},
					"operation":     map[string]any{"type": "string", "enum": []string{"definitions", "references", "implementations"}},
				},
			},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input api.SCIPNavigationRequest) (*mcp.CallToolResult, api.SCIPNavigationResponse, error) {
			response, err := services.SCIP.Navigate(ctx, httpapi.PrincipalFromContext(ctx), input)
			if err != nil {
				return nil, api.SCIPNavigationResponse{}, errors.New(httpapi.SCIPErrorMessage(err))
			}
			if !fitsOutput(response, limits.MaxOutputBytes) {
				return nil, api.SCIPNavigationResponse{}, errOutputBudget
			}
			return structuredResult(), response, nil
		})
	}
	registerGraphTools(server, services.Graph, limits)
	repositories := services.Repositories
	if repositories == nil {
		return server
	}
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_repositories", Description: "List repositories visible to you and their current index status.",
		InputSchema: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"limit":            positiveIntegerSchema("maximum repositories"),
				"max_output_bytes": positiveIntegerSchema("maximum output bytes"),
			},
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input listInput) (*mcp.CallToolResult, repositoryListOutput, error) {
		items, err := repositories.List(ctx, httpapi.PrincipalFromContext(ctx))
		if err != nil {
			return nil, repositoryListOutput{}, errors.New(httpapi.RepositoryErrorMessage(err))
		}
		if items == nil {
			items = []api.RepositorySummary{}
		}
		limited, err := limitRepositoryList(items, input, limits)
		return structuredResult(), limited, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_repository_status", Description: "Inspect desired and indexed revisions before relying on search results.",
		InputSchema: map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"repository_id"},
			"properties": map[string]any{
				"repository_id":    positiveIntegerSchema("GitHub repository ID"),
				"max_output_bytes": positiveIntegerSchema("maximum output bytes"),
			},
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input repositoryIDInput) (*mcp.CallToolResult, api.RepositorySummary, error) {
		status, err := repositories.Status(ctx, httpapi.PrincipalFromContext(ctx), input.RepositoryID)
		if err != nil {
			return nil, api.RepositorySummary{}, errors.New(httpapi.RepositoryErrorMessage(err))
		}
		if !fitsOutput(status, outputBudget(input.MaxOutputBytes, limits.MaxOutputBytes)) {
			return nil, api.RepositorySummary{}, errOutputBudget
		}
		return structuredResult(), status, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "read_file", Description: "Read a bounded file or line range at the repository's indexed revision.",
		InputSchema: map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"repository_id", "path"},
			"properties": map[string]any{
				"repository_id":    positiveIntegerSchema("GitHub repository ID"),
				"path":             map[string]any{"type": "string", "minLength": 1, "description": "repository-relative file path"},
				"start_line":       positiveIntegerSchema("first line to return"),
				"end_line":         positiveIntegerSchema("last line to return"),
				"max_output_bytes": positiveIntegerSchema("maximum output bytes"),
			},
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input readFileInput) (*mcp.CallToolResult, api.ReadFileResponse, error) {
		file, err := repositories.ReadFile(ctx, httpapi.PrincipalFromContext(ctx), api.ReadFileRequest{
			RepositoryID: input.RepositoryID, Path: input.Path, StartLine: input.StartLine, EndLine: input.EndLine,
		})
		if err != nil {
			return nil, api.ReadFileResponse{}, errors.New(httpapi.RepositoryErrorMessage(err))
		}
		file, err = limitReadFile(file, input.MaxOutputBytes, limits)
		return structuredResult(), file, err
	})
	return server
}

func limitRepositoryList(items []api.RepositorySummary, input listInput, limits Limits) (repositoryListOutput, error) {
	limits = normalizeLimits(limits)
	maxItems := input.Limit
	if maxItems <= 0 || maxItems > limits.MaxItems {
		maxItems = limits.MaxItems
	}
	maxBytes := outputBudget(input.MaxOutputBytes, limits.MaxOutputBytes)
	limited := repositoryListOutput{Repositories: []api.RepositorySummary{}, Truncated: len(items) > 0}
	if !fitsOutput(limited, maxBytes) {
		return repositoryListOutput{}, errOutputBudget
	}
	for index, item := range items {
		if index == maxItems {
			break
		}
		candidate := repositoryListOutput{Repositories: append(limited.Repositories, item), Truncated: index+1 < len(items)}
		if !fitsOutput(candidate, maxBytes) {
			break
		}
		limited = candidate
	}
	return limited, nil
}

func limitReadFile(file api.ReadFileResponse, requestedBytes int64, limits Limits) (api.ReadFileResponse, error) {
	maxBytes := outputBudget(requestedBytes, normalizeLimits(limits).MaxOutputBytes)
	if fitsOutput(file, maxBytes) {
		return file, nil
	}
	lines := strings.Split(file.Content, "\n")
	best := 0
	for low, high := 1, len(lines); low <= high; {
		count := low + (high-low)/2
		candidate := filePrefix(file, lines, count)
		if !fitsOutput(candidate, maxBytes) {
			high = count - 1
			continue
		}
		best = count
		low = count + 1
	}
	if best == 0 {
		return api.ReadFileResponse{}, errOutputBudget
	}
	return filePrefix(file, lines, best), nil
}

func filePrefix(file api.ReadFileResponse, lines []string, count int) api.ReadFileResponse {
	file.Content = strings.Join(lines[:count], "\n")
	file.EndLine = file.StartLine + count - 1
	file.Truncated = true
	return file
}

func normalizeLimits(limits Limits) Limits {
	if limits.MaxItems <= 0 || limits.MaxItems > maxRepositoryItems {
		limits.MaxItems = maxRepositoryItems
	}
	if limits.MaxOutputBytes <= 0 || limits.MaxOutputBytes > maxToolOutputBytes {
		limits.MaxOutputBytes = maxToolOutputBytes
	}
	return limits
}

func outputBudget(requested, configured int64) int64 {
	if requested <= 0 || requested > configured {
		return configured
	}
	return requested
}

func fitsOutput(value any, maxBytes int64) bool {
	// Budgets bound successful structured results; fixed safe tool errors remain
	// diagnosable even when a caller requests fewer bytes than their envelope.
	result := structuredResult()
	result.StructuredContent = value
	data, err := json.Marshal(result)
	return err == nil && int64(len(data)) <= maxBytes
}

func structuredResult() *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{}}
}

func positiveIntegerSchema(description string) map[string]any {
	return map[string]any{"type": "integer", "minimum": 1, "description": description}
}

func runSearch(ctx context.Context, service *search.Service, input api.SearchRequest, maxBytes int64) (*mcp.CallToolResult, output, error) {
	response, err := service.Search(ctx, httpapi.PrincipalFromContext(ctx), input)
	if err != nil {
		if errors.Is(err, search.ErrInvalidQuery) || errors.Is(err, zoekt.ErrInvalidQuery) {
			return nil, output{}, errInvalidSearch
		}
		return nil, output{}, errUnavailable
	}
	if response.Matches == nil {
		response.Matches = []api.SearchMatch{}
	}
	limited, err := limitSearchOutput(output{Matches: response.Matches, Truncated: response.Truncated}, maxBytes)
	return structuredResult(), limited, err
}

func limitSearchOutput(value output, maxBytes int64) (output, error) {
	limited := output{Matches: []api.SearchMatch{}, Truncated: value.Truncated || len(value.Matches) > 0}
	if !fitsOutput(limited, maxBytes) {
		return output{}, errOutputBudget
	}
	for index, match := range value.Matches {
		candidate := output{Matches: append(limited.Matches, match), Truncated: value.Truncated || index+1 < len(value.Matches)}
		if !fitsOutput(candidate, maxBytes) {
			break
		}
		limited = candidate
	}
	return limited, nil
}
