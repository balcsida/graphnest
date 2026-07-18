package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := runProxy(context.Background(), os.Getenv("GREPNEST_SERVER_URL"), os.Getenv("GREPNEST_TOKEN"), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runProxy(ctx context.Context, serverURL, token string, transport mcp.Transport) error {
	if serverURL == "" || token == "" {
		return errors.New("GREPNEST_SERVER_URL and GREPNEST_TOKEN are required")
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "grepnest-mcp", Version: "0.1.0"}, nil)
	upstream, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: strings.TrimRight(serverURL, "/") + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{
			token: token, base: http.DefaultTransport,
		}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return err
	}
	defer upstream.Close()

	tools, err := upstream.ListTools(ctx, nil)
	if err != nil {
		return err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "grepnest-mcp", Version: "0.1.0"}, nil)
	for _, tool := range tools.Tools {
		server.AddTool(tool, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return upstream.CallTool(ctx, &mcp.CallToolParams{Name: request.Params.Name, Arguments: request.Params.Arguments})
		})
	}
	return server.Run(ctx, transport)
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (transport bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(request)
}
