package graphquery

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphprotocol"
)

// benchmarkQueryStore is the three-symbol call cycle from parityArtifact in
// internal/postgres/graph_query_parity_test.go. It measures the query algorithm,
// not PostgreSQL, transport, authorization, or a CodeGraph-produced corpus.
type benchmarkQueryStore struct {
	stubQueryStore
	manifest graphartifact.Manifest
	symbols  []graphprotocol.Symbol
	edges    []graphprotocol.Relationship
	calls    [3]int // manifests, symbols, neighbors
}

func (s *benchmarkQueryStore) Manifests(context.Context) (map[int64]graphartifact.Manifest, error) {
	s.calls[0]++
	return map[int64]graphartifact.Manifest{1: s.manifest}, nil
}

func (s *benchmarkQueryStore) Symbols(_ context.Context, q SymbolQuery) ([]graphprotocol.Symbol, error) {
	s.calls[1]++
	for _, symbol := range s.symbols {
		if symbol.UID == q.UID {
			return []graphprotocol.Symbol{symbol}, nil
		}
	}
	return nil, nil
}

func (s *benchmarkQueryStore) Neighbors(_ context.Context, q NeighborQuery) ([]Neighbor, error) {
	s.calls[2]++
	var result []Neighbor
	for _, parent := range q.Frontier {
		for _, edge := range s.edges {
			from, to := edge.SourceUID, edge.TargetUID
			if q.Direction == "incoming" {
				from, to = to, from
			}
			if from != parent.UID || edge.Kind != q.Relation || edge.Confidence < q.MinConfidence {
				continue
			}
			for _, symbol := range s.symbols {
				if symbol.UID == to {
					result = append(result, Neighbor{Parent: parent, Symbol: symbol, Edge: edge})
				}
			}
		}
	}
	start := min(q.Offset, len(result))
	return result[start:min(start+q.Limit, len(result))], nil
}

func BenchmarkGraphQueryWarm(b *testing.B) {
	data, err := os.ReadFile("../../test/fixtures/graph/query/parity.json")
	if err != nil {
		b.Fatal(err)
	}
	var golden struct {
		ContextIncomingCalls, ContextOutgoingCalls, ImpactDepth1, ImpactDepth2, Trace []string
	}
	if err := json.Unmarshal(data, &golden); err != nil {
		b.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	scope := graphprotocol.Scope{SelectedRepositoryID: 1, Repositories: []graphprotocol.RepositorySnapshot{{ID: 1, Name: "acme/renamed", Commit: commit}}}
	store := &benchmarkQueryStore{manifest: graphartifact.Manifest{RepositoryID: 1, UploadID: 1, Commit: commit, SchemaVersion: 1}}
	for _, uid := range []string{"root", "next", "last"} {
		store.symbols = append(store.symbols, graphprotocol.Symbol{RepositoryID: 1, UID: uid, Name: uid, Kind: "func", FilePath: "main.go", Test: uid == "last"})
	}
	for i, pair := range [][2]string{{"root", "next"}, {"next", "last"}, {"last", "root"}} {
		store.edges = append(store.edges, graphprotocol.Relationship{SourceRepositoryID: 1, TargetRepositoryID: 1, SourceUID: pair[0], TargetUID: pair[1], Kind: "calls", Confidence: []float64{.9, .7, .6}[i]})
	}
	service := &Service{Store: store}
	ctx := b.Context()
	uids := func(symbols []graphprotocol.Symbol) []string {
		result := make([]string, 0, len(symbols))
		for _, symbol := range symbols {
			result = append(result, symbol.UID)
		}
		return result
	}
	for _, test := range []struct {
		name string
		run  func() (any, error)
		want []string
	}{
		{"context", func() (any, error) {
			return service.Context(ctx, graphprotocol.ContextRequest{Scope: scope, UID: "next", Relations: []string{"calls"}, PerCategoryLimit: 2})
		}, append(slices.Clone(golden.ContextIncomingCalls), golden.ContextOutgoingCalls...)},
		{"impact", func() (any, error) {
			return service.Impact(ctx, graphprotocol.ImpactRequest{Scope: scope, TargetUID: "root", Direction: "downstream", Relations: []string{"calls"}, MaxDepth: 4, IncludeTests: true})
		}, append(slices.Clone(golden.ImpactDepth1), golden.ImpactDepth2...)},
		{"trace", func() (any, error) {
			return service.Trace(ctx, graphprotocol.TraceRequest{Scope: scope, SourceUID: "root", TargetUID: "last", MaxDepth: 4})
		}, golden.Trace},
	} {
		b.Run(test.name, func(b *testing.B) {
			b.StopTimer()
			value, err := test.run()
			if err != nil {
				b.Fatal(err)
			}
			var got []string
			switch response := value.(type) {
			case graphprotocol.ContextResponse:
				got = append(uids(response.Incoming["calls"]), uids(response.Outgoing["calls"])...)
			case graphprotocol.ImpactResponse:
				got = append(uids(response.ByDepth[1]), uids(response.ByDepth[2])...)
			case graphprotocol.TraceResponse:
				got = uids(response.Nodes)
			}
			if !slices.Equal(got, test.want) {
				b.Fatalf("parity fixture mismatch: got %v, want %v", got, test.want)
			}
			for range 100 {
				if _, err := test.run(); err != nil {
					b.Fatal(err)
				}
			}
			store.calls = [3]int{}
			samples := make([]int64, b.N)
			b.ReportAllocs()
			b.StartTimer()
			for i := range b.N {
				start := time.Now()
				if _, err := test.run(); err != nil {
					b.Fatal(err)
				}
				samples[i] = time.Since(start).Nanoseconds()
			}
			b.StopTimer()
			slices.Sort(samples)
			b.ReportMetric(float64(samples[(b.N*50+99)/100-1]), "p50-ns/query")
			b.ReportMetric(float64(samples[(b.N*95+99)/100-1]), "p95-ns/query")
			for i, name := range []string{"manifests", "symbols", "neighbors"} {
				b.ReportMetric(float64(store.calls[i])/float64(b.N), name+"/query")
			}
		})
	}
}
