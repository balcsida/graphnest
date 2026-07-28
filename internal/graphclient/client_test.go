package graphclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/graphprotocol"
)

func TestNewValidatesConfiguration(t *testing.T) {
	for _, value := range []string{
		"", "ftp://example.com", "http://u:p@example.com", "http://example.com?q=1",
		"http://example.com?", "http://example.com/#x", "http://example.com/#",
		"http://example.com/api", "http://example.com/%2e", "http://example.com/%2F",
	} {
		if _, err := New(value, []byte("secret"), nil, 1024); err == nil {
			t.Fatalf("New(%q) succeeded", value)
		}
	}
	if _, err := New("http://example.com", nil, nil, 1024); err == nil {
		t.Fatal("empty secret accepted")
	}
	if _, err := New("http://example.com", []byte("secret"), nil, 0); err == nil {
		t.Fatal("empty response limit accepted")
	}
}

func TestClientClonesSecretAndRoundTripsFixedMethods(t *testing.T) {
	secret := []byte("right")
	paths := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths <- request.URL.Path
		if request.Header.Get("Authorization") != "Bearer right" {
			http.Error(writer, "bad auth", http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/internal/v1/graph/context":
			_, _ = writer.Write([]byte(`{"status":"found","commits":{}}`))
		case "/internal/v1/graph/impact":
			_, _ = writer.Write([]byte(`{"status":"ok","by_depth":{},"commits":{},"partial":false}`))
		case "/internal/v1/graph/trace":
			_, _ = writer.Write([]byte(`{"status":"no_path","commits":{}}`))
		case "/internal/v1/graph/cypher":
			_, _ = writer.Write([]byte(`{"columns":[],"rows":[],"truncated":false}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, secret, nil, 1024)
	if err != nil {
		t.Fatal(err)
	}
	secret[0] = 'x'
	scope := graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{
		ID: 1, Commit: "0123456789abcdef0123456789abcdef01234567",
	}}}
	if _, err := client.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope, UID: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Impact(t.Context(), graphprotocol.ImpactRequest{Scope: scope, TargetUID: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Trace(t.Context(), graphprotocol.TraceRequest{Scope: scope, SourceUID: "A", TargetUID: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Cypher(t.Context(), graphprotocol.CypherRequest{Admin: true, Statement: "RETURN 1"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/internal/v1/graph/context", "/internal/v1/graph/impact", "/internal/v1/graph/trace", "/internal/v1/graph/cypher"} {
		if got := <-paths; got != want {
			t.Fatalf("path=%q want=%q", got, want)
		}
	}
}

func TestClientBoundsAndMapsErrors(t *testing.T) {
	tests := []struct {
		name  string
		fn    http.HandlerFunc
		code  string
		limit int64
	}{
		{"oversized", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 65))) }, "response_too_large", 64},
		{"unknown status", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "native details", http.StatusTeapot) }, "unavailable", 256},
		{"stable status", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_request","message":"unsafe statement"}}`))
		}, "invalid_request", 256},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.fn)
			defer server.Close()
			client, err := New(server.URL, []byte("secret"), nil, test.limit)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Context(t.Context(), graphprotocol.ContextRequest{})
			var protocolError *Error
			if !errors.As(err, &protocolError) || protocolError.Code != test.code || strings.Contains(err.Error(), "native") || strings.Contains(err.Error(), "statement") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestClientPropagatesContextAndBlocksRedirectAuthLeak(t *testing.T) {
	leaked := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		leaked <- request.Header.Get("Authorization")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client, err := New(source.URL, []byte("secret"), nil, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Context(t.Context(), graphprotocol.ContextRequest{}); err == nil {
		t.Fatal("redirect succeeded")
	}
	select {
	case got := <-leaked:
		t.Fatalf("redirect followed with authorization %q", got)
	default:
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Context(ctx, graphprotocol.ContextRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestClientRejectsOversizedRequest(t *testing.T) {
	client, err := New("http://example.com", []byte("secret"), nil, 1024)
	if err != nil {
		t.Fatal(err)
	}
	client.maxRequestBytes = 32
	_, err = client.Cypher(t.Context(), graphprotocol.CypherRequest{Admin: true, Statement: strings.Repeat("x", 100)})
	var protocolError *Error
	if !errors.As(err, &protocolError) || protocolError.Code != "request_too_large" {
		t.Fatalf("error=%v", err)
	}
}

func TestClientUsesCallerHTTPTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()
	client, err := New(server.URL, []byte("secret"), &http.Client{Timeout: time.Millisecond}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Context(t.Context(), graphprotocol.ContextRequest{}); err == nil {
		t.Fatal("timeout succeeded")
	}
}
