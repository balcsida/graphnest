package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProxyForwardsToolsWithBearerAuthentication(t *testing.T) {
	authenticated := false
	upstream := mcp.NewServer(&mcp.Implementation{Name: "upstream", Version: "1"}, nil)
	mcp.AddTool(upstream, &mcp.Tool{Name: "search_code"}, func(_ context.Context, _ *mcp.CallToolRequest, input struct {
		Query string `json:"query"`
	}) (*mcp.CallToolResult, map[string]any, error) {
		return nil, map[string]any{"query": input.Query}, nil
	})
	upstreamHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/mcp" {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		authenticated = true
		upstreamHandler.ServeHTTP(writer, request)
	}))
	defer httpServer.Close()

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- runProxy(ctx, httpServer.URL, "secret", serverTransport) }()

	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_code", Arguments: map[string]any{"query": "needle"}})
	if err != nil {
		t.Fatal(err)
	}
	if !authenticated {
		t.Fatal("upstream request was not authenticated")
	}
	output, ok := result.StructuredContent.(map[string]any)
	if !ok || output["query"] != "needle" {
		t.Fatalf("structured output = %#v", result.StructuredContent)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil && err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy did not stop after cancellation")
	}
}

func TestProxyBoundsUpstreamStartup(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	httpServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	defer httpServer.Close()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	_, serverTransport := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- runProxy(t.Context(), httpServer.URL, "secret", serverTransport) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("startup unexpectedly succeeded")
		}
	case <-time.After(3*upstreamTimeout + 2*time.Second):
		close(release)
		released = true
		<-done
		t.Fatal("proxy startup remained unbounded")
	}
	select {
	case <-started:
	default:
		t.Fatal("upstream server was not contacted")
	}
}
