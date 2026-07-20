package mcpserver

import (
	"context"
	"errors"

	"github.com/grepnest/grepnest/internal/httpapi"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/search"
	"github.com/grepnest/grepnest/internal/zoekt"
	"github.com/grepnest/grepnest/pkg/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	errInvalidSearch = errors.New("search query is invalid")
	errUnavailable   = errors.New("search service is unavailable")
)

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
	RepositoryID int64 `json:"repository_id" jsonschema:"GitHub repository ID"`
}

type readFileInput struct {
	RepositoryID int64  `json:"repository_id" jsonschema:"GitHub repository ID"`
	Path         string `json:"path" jsonschema:"repository-relative file path"`
	StartLine    int    `json:"start_line,omitempty" jsonschema:"first line to return"`
	EndLine      int    `json:"end_line,omitempty" jsonschema:"last line to return"`
}

type repositoryListOutput struct {
	Repositories []api.RepositorySummary `json:"repositories"`
}

func New(service *search.Service, repositoryServices ...*repository.Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "grepnest", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name: "search_code", Description: "Search code contents when you know a symbol, string, or code expression.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input searchInput) (*mcp.CallToolResult, output, error) {
		return runSearch(ctx, service, api.SearchRequest{
			Query: input.Query, Repositories: input.Repositories, Limit: input.Limit,
			ContextLines: input.ContextLines, MaxResponseBytes: input.MaxOutputBytes,
		})
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "find_files", Description: "Find files by path regular expression when file names or paths are known.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input findInput) (*mcp.CallToolResult, output, error) {
		return runSearch(ctx, service, api.SearchRequest{
			Query: "file:" + input.Pattern, Repositories: input.Repositories,
			Limit: input.Limit, MaxResponseBytes: input.MaxOutputBytes,
		})
	})
	if len(repositoryServices) == 0 || repositoryServices[0] == nil {
		return server
	}
	repositories := repositoryServices[0]
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_repositories", Description: "List repositories visible to you and their current index status.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, repositoryListOutput, error) {
		items, err := repositories.List(ctx, httpapi.PrincipalFromContext(ctx))
		if err != nil {
			return nil, repositoryListOutput{}, errors.New(httpapi.RepositoryErrorMessage(err))
		}
		if items == nil {
			items = []api.RepositorySummary{}
		}
		return nil, repositoryListOutput{Repositories: items}, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_repository_status", Description: "Inspect desired and indexed revisions before relying on search results.",
		InputSchema: map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"repository_id"},
			"properties": map[string]any{"repository_id": positiveIntegerSchema("GitHub repository ID")},
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input repositoryIDInput) (*mcp.CallToolResult, api.RepositorySummary, error) {
		status, err := repositories.Status(ctx, httpapi.PrincipalFromContext(ctx), input.RepositoryID)
		if err != nil {
			return nil, api.RepositorySummary{}, errors.New(httpapi.RepositoryErrorMessage(err))
		}
		return nil, status, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "read_file", Description: "Read a bounded file or line range at the repository's indexed revision.",
		InputSchema: map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"repository_id", "path"},
			"properties": map[string]any{
				"repository_id": positiveIntegerSchema("GitHub repository ID"),
				"path":          map[string]any{"type": "string", "minLength": 1, "description": "repository-relative file path"},
				"start_line":    positiveIntegerSchema("first line to return"),
				"end_line":      positiveIntegerSchema("last line to return"),
			},
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input readFileInput) (*mcp.CallToolResult, api.ReadFileResponse, error) {
		file, err := repositories.ReadFile(ctx, httpapi.PrincipalFromContext(ctx), api.ReadFileRequest{
			RepositoryID: input.RepositoryID, Path: input.Path, StartLine: input.StartLine, EndLine: input.EndLine,
		})
		if err != nil {
			return nil, api.ReadFileResponse{}, errors.New(httpapi.RepositoryErrorMessage(err))
		}
		return nil, file, nil
	})
	return server
}

func positiveIntegerSchema(description string) map[string]any {
	return map[string]any{"type": "integer", "minimum": 1, "description": description}
}

func runSearch(ctx context.Context, service *search.Service, input api.SearchRequest) (*mcp.CallToolResult, output, error) {
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
	return nil, output{Matches: response.Matches, Truncated: response.Truncated}, nil
}
