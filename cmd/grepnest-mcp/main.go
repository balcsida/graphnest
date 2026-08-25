package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/agentskills"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const upstreamTimeout = 5 * time.Second

var errUsage = errors.New("usage: grepnest-mcp [install-skills [--root PATH]]")

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv("GREPNEST_SERVER_URL"), os.Getenv("GREPNEST_TOKEN"), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, serverURL, token string, transport mcp.Transport) error {
	if len(args) == 0 {
		return runProxy(ctx, serverURL, token, transport)
	}
	if args[0] != "install-skills" {
		return errUsage
	}
	flags := flag.NewFlagSet("install-skills", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", ".", "repository root")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return errUsage
	}
	return agentskills.Install(*root)
}

func runProxy(ctx context.Context, serverURL, token string, transport mcp.Transport) error {
	if serverURL == "" || token == "" {
		return errors.New("GREPNEST_SERVER_URL and GREPNEST_TOKEN are required")
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, upstreamTimeout)
	defer cancelStartup()
	client := mcp.NewClient(&mcp.Implementation{Name: "grepnest-mcp", Version: "0.1.0"}, nil)
	upstream, err := client.Connect(startupCtx, &mcp.StreamableClientTransport{
		Endpoint: strings.TrimRight(serverURL, "/") + "/mcp",
		HTTPClient: &http.Client{Timeout: upstreamTimeout, Transport: bearerTransport{
			token: token, base: http.DefaultTransport,
		}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return err
	}
	defer upstream.Close()

	tools, err := upstream.ListTools(startupCtx, nil)
	if err != nil {
		return err
	}
	cancelStartup()
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
