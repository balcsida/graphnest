//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/config"
)

func TestCommandHasNoEnvironmentSecretValue(t *testing.T) {
	if value, ok := os.LookupEnv("GREPNEST_GRAPH_INTERNAL_SECRET"); ok && value != "" {
		t.Fatal("test environment must not use an internal secret value")
	}
}

func TestStandaloneGraphConfigPropagatesLimits(t *testing.T) {
	got := graphRuntimeConfig(config.Graph{QueryLimits: config.GraphQueryLimits{
		DefaultImpactDepth: 2, MaxDepth: 7, DefaultTraceDepth: 4,
		MaxTraceDepth: 9, MaxRows: 321,
	}})
	if got.QueryLimits.DefaultImpactDepth != 2 || got.QueryLimits.MaxDepth != 7 ||
		got.QueryLimits.DefaultTraceDepth != 4 || got.QueryLimits.MaxTraceDepth != 9 ||
		got.QueryLimits.MaxRows != 321 {
		t.Fatalf("runtime limits = %#v", got.QueryLimits)
	}
}

func TestStandaloneInitializesRunsAndCloses(t *testing.T) {
	var events []string
	add := func(name string) func(context.Context) error {
		return func(context.Context) error { events = append(events, name); return nil }
	}
	runtime := standaloneRuntime{
		ping: add("ping"), migrate: add("migrate"), newGraph: add("new graph"),
		runGraph: add("run graph"), closeGraph: func() { events = append(events, "close graph") },
		closeSource: func() { events = append(events, "close source") },
	}
	if err := runtime.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(events, ","); got != "ping,migrate,new graph,run graph,close graph,close source" {
		t.Fatalf("events = %q", got)
	}
}

func TestStandaloneClosesAfterRunError(t *testing.T) {
	want := errors.New("run")
	var events []string
	runtime := standaloneRuntime{
		ping:        func(context.Context) error { return nil },
		migrate:     func(context.Context) error { return nil },
		newGraph:    func(context.Context) error { return nil },
		runGraph:    func(context.Context) error { return want },
		closeGraph:  func() { events = append(events, "graph") },
		closeSource: func() { events = append(events, "source") },
	}
	if err := runtime.run(t.Context()); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if strings.Join(events, ",") != "graph,source" {
		t.Fatalf("close order = %q", events)
	}
}
