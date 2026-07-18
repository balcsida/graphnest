package mcpserver

import (
	"context"

	"github.com/grepnest/grepnest/internal/httpapi"
	"github.com/grepnest/grepnest/internal/search"
	"github.com/grepnest/grepnest/pkg/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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

func New(service *search.Service) *mcp.Server {
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
	return server
}

func runSearch(ctx context.Context, service *search.Service, input api.SearchRequest) (*mcp.CallToolResult, output, error) {
	response, err := service.Search(ctx, httpapi.PrincipalFromContext(ctx), input)
	if err != nil {
		return nil, output{}, err
	}
	if response.Matches == nil {
		response.Matches = []api.SearchMatch{}
	}
	return nil, output{Matches: response.Matches}, nil
}
