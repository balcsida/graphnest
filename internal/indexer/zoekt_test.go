//go:build unix

package indexer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/observability"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/internal/zoekt"
)

func TestZoektIndexUsesFixedDeadline(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "slow-indexer")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n/bin/sleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	indexer := ZoektIndexer{
		Binary: binary, IndexDir: t.TempDir(), IndexTimeout: 10 * time.Millisecond,
		Runner: Runner{MaxOutput: 1024, KillGrace: time.Millisecond},
	}
	repo := repository.Repository{ZoektID: 7, Name: "acme/repo", Branch: "main", DesiredSHA: "0123456789abcdef0123456789abcdef01234567", WebURL: "https://example.com/acme/repo"}
	started := time.Now()
	err := indexer.Index(t.Context(), repo, "-c")
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("error=%v duration=%s", err, time.Since(started))
	}
}

func TestZoektWaitVisibleRequiresExactRepositoryBranchAndVersion(t *testing.T) {
	responses := []string{
		`{"List":{"ReposMap":{"8":{"Branches":[{"Name":"main","Version":"target"}]}}}}`,
		`{"List":{"ReposMap":{"7":{"Branches":[{"Name":"other","Version":"target"},{"Name":"main","Version":"old"}]}}}}`,
		`{"List":{"ReposMap":{"7":{"Branches":[{"Name":"main","Version":"target"}]}}}}`,
	}
	calls := 0
	searchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/search" {
			searchCalls++
			_, _ = writer.Write([]byte(`{"Result":{"Files":[]}}`))
			return
		}
		index := calls
		calls++
		if index >= len(responses) {
			index = len(responses) - 1
		}
		_, _ = writer.Write([]byte(responses[index]))
	}))
	defer server.Close()
	client, err := zoekt.New(server.URL, server.Client(), 1024, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	indexer := ZoektIndexer{Client: client}
	if err := indexer.WaitVisible(t.Context(), 7, "main", "target"); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if searchCalls != 1 {
		t.Fatalf("readiness search calls = %d, want 1", searchCalls)
	}
}

func TestZoektWaitVisibleStopsWithContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"List":{"ReposMap":{}}}`))
	}))
	defer server.Close()
	client, err := zoekt.New(server.URL, server.Client(), 1024, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Millisecond)
	defer cancel()
	err = (&ZoektIndexer{Client: client}).WaitVisible(ctx, 7, "main", "target")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}
