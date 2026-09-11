//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"google.golang.org/protobuf/proto"
)

func TestGraphDiscoveryFieldsAndBounds(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Nodes = nil
	a.Edges = nil
	a.Files = nil
	a.Unresolved = nil
	fields := []string{"name", "qualified", "signature", "documentation", "path", "kind", "language", "long", "nul", "file"}
	for i, field := range fields {
		n := &graphv2.Node{SourceId: field, Occurrence: field, Kind: "function", Name: fmt.Sprintf("ordinary%d", i), Path: proto.String(fmt.Sprintf("src/file%d.go", i)), Language: "go"}
		switch field {
		case "name":
			n.Name = "HTMLParser"
		case "qualified":
			n.QualifiedName = "system.RetryScheduler"
		case "signature":
			n.Signature = proto.String("func Unremarkable(cache EvictionPolicy)")
		case "documentation":
			n.Documentation = proto.String("Refreshes authentication credentials")
		case "path":
			n.Path = proto.String("src/settlement/invoice.go")
		case "kind":
			n.Kind = "interface"
		case "language":
			n.Language = "rust"
		case "file":
			n.Kind = "file"
			n.Path = proto.String("lonely.conf")
		case "long":
			n.Name = strings.Repeat("q", 4000) + "NeedleTail"
		case "nul":
			n.Name = "before\x00AfterNul"
		}
		a.Nodes = append(a.Nodes, n)
	}
	pub, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "discovery"}, a)
	if err != nil {
		t.Fatal(err)
	}
	scope := graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	service := &graphquery.Service{Store: s}
	for query, want := range map[string]string{"html parser": "name", "lonely.conf": "file", "ml": "name", "retry scheduler": "qualified", "eviction": "signature", "credentials": "documentation", "settlement invoice": "path", "kind:interface": "kind", "interface": "kind", "language:rust": "language", strings.Repeat("q", 2500): "long", "NeedleTail": "long", "before\x00AfterNul": "nul"} {
		got, err := service.Discover(t.Context(), graphprotocol.DiscoverRequest{Scope: scope, Query: query, Limit: 1})
		if err != nil || len(got.Matches) != 1 || got.Matches[0].Entity.Fact.Occurrence != want {
			t.Fatalf("%q: matches=%+v err=%v", query, got.Matches, err)
		}
		var original *graphv2.Node
		for _, n := range a.Nodes {
			if n.Occurrence == want {
				original = n
			}
		}
		if !proto.Equal(got.Matches[0].Entity.Fact, original) || got.Matches[0].Entity.RepositoryID != 101 || len(got.Generations) != 1 || got.Generations[0].UploadID != pub.Upload.ID {
			t.Fatal("lost original/provenance")
		}
	}
	for _, query := range []string{"how does this code work", "unfindablexyz", "kind:invalidzz", "unknown:absentzz", "lang:invalidzz"} {
		got, err := service.Discover(t.Context(), graphprotocol.DiscoverRequest{Scope: scope, Query: query})
		if err != nil || got.Status != "no_entry_point" || len(got.Matches) != 0 {
			t.Fatalf("weak %s: %+v %v", query, got, err)
		}
	}
	req := graphprotocol.DiscoverRequest{Scope: scope, Query: "kind:function", Limit: 2, CandidateLimit: 3}
	before, err := service.Discover(t.Context(), req)
	if err != nil || len(before.Matches) != 2 || !before.CandidateTruncated || !before.ResultTruncated {
		t.Fatalf("bounds=%+v %v", before, err)
	}
	after, err := service.Discover(t.Context(), req)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("unstable limit")
	}
	hiddenID := seedReadyRepository(t, s, 202, a.Commit)
	hidden := proto.Clone(a).(*graphv2.Artifact)
	hidden.Repository = "202"
	hidden.ContentHash = nil
	if _, err = s.ReplaceGraphV2(t.Context(), hiddenID, GraphPublication{Publisher: "hidden"}, hidden); err != nil {
		t.Fatal(err)
	}
	after, err = service.Discover(t.Context(), req)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("scope leak")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = service.Discover(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	if _, err = s.pool.Exec(t.Context(), "update graph_uploads set discovery_version=0 where id=$1", pub.Upload.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Discover(t.Context(), req); !errors.Is(err, graphquery.ErrDiscoveryUnavailable) {
		t.Fatalf("unbuilt=%v", err)
	}
	if err = s.RebuildGraphDiscovery(t.Context(), id, pub.Upload.ID); err != nil {
		t.Fatal(err)
	}
	after, err = service.Discover(t.Context(), req)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("rebuild: %v", err)
	}
	if _, err = s.pool.Exec(t.Context(), "update repositories set indexed_sha=$2 where id=$1", id, testSHA('b')); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Discover(t.Context(), req); !errors.Is(err, graphquery.ErrGenerationChanged) {
		t.Fatalf("drift=%v", err)
	}
}

func TestGraphDiscoveryRealOracle(t *testing.T) {
	file := os.Getenv("GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE")
	if file == "" {
		t.Skip("requires exported real CodeGraph fixture")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	a, err := graphartifact.ParseV2(data, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	s, id := readyGraphStore(t, a.Commit)
	if _, err = s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "codegraph-oracle"}, a); err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: s}
	scope := graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	for _, query := range []string{"processGreeting", "normalize", "normlize", "Named", "greeting"} {
		got, err := service.Discover(t.Context(), graphprotocol.DiscoverRequest{Scope: scope, Query: query, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Matches) == 0 {
			t.Fatalf("real query %s has no answer", query)
		}
		for _, m := range got.Matches {
			found := false
			for _, n := range a.Nodes {
				if n.Occurrence == m.Entity.Fact.Occurrence {
					found = proto.Equal(n, m.Entity.Fact)
				}
			}
			if !found {
				t.Fatal("invented oracle fact")
			}
		}
		t.Logf("query=%s matches=%d first=%s", query, len(got.Matches), got.Matches[0].Entity.Fact.Name)
	}
}

func TestGraphDiscoveryRequiredOracleAnswers(t *testing.T) {
	file := os.Getenv("GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE")
	if file == "" {
		t.Skip("requires exported real CodeGraph fixture")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	a, err := graphartifact.ParseV2(data, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile("../../test/fixtures/codegraph/library-expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		Search []struct {
			Node struct {
				ID string `json:"id"`
			}
		} `json:"lib-searchNodes"`
	}
	if err = json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	if len(expected.Search) != 7 {
		t.Fatal("oracle budget changed")
	}
	s, id := readyGraphStore(t, a.Commit)
	if _, err = s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "oracle"}, a); err != nil {
		t.Fatal(err)
	}
	got, err := (&graphquery.Service{Store: s}).Discover(t.Context(), graphprotocol.DiscoverRequest{Scope: graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}, Query: "normalize", Limit: len(expected.Search)})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range expected.Search {
		found := false
		for _, m := range got.Matches {
			found = found || m.Entity.Fact.SourceId == want.Node.ID
		}
		if !found {
			t.Fatalf("missing required upstream answer %s at budget %d", want.Node.ID, len(expected.Search))
		}
	}
	typo, err := (&graphquery.Service{Store: s}).Discover(t.Context(), graphprotocol.DiscoverRequest{Scope: graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}, Query: "normlize", Limit: 3})
	if err != nil || len(typo.Matches) != 3 {
		t.Fatalf("upstream typo budget: %+v %v", typo, err)
	}
	for _, m := range typo.Matches {
		if m.Entity.Fact.Name != "normalize" || !m.Fuzzy || m.EditDistance != 1 {
			t.Fatalf("typo evidence=%+v", m)
		}
	}

}

func TestGraphDiscoveryRankingRetention(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Nodes = nil
	a.Edges = nil
	a.Files = nil
	a.Unresolved = nil
	add := func(id, name, path, doc string, generated bool) {
		a.Nodes = append(a.Nodes, &graphv2.Node{SourceId: id, Occurrence: id, Name: name, Kind: "function", Path: proto.String(path), Documentation: proto.String(doc), Language: "typescript"})
		a.Files = append(a.Files, &graphv2.File{Path: path, Language: "typescript", ContentHash: strings.Repeat("0", 64), Generated: proto.Bool(generated)})
	}
	add("product", "CacheService", "src/cache.ts", "cache service", false)
	add("generated", "CacheService", "generated/cache.ts", "cache service", true)
	add("test", "CacheService", "tests/cache.test.ts", "cache service", false)
	add("ambient", "CacheService", "types/cache.d.ts", "cache service", false)
	a.Nodes[len(a.Nodes)-1].Kind = "interface"
	add("deprioritized", "CacheService", "legacy/cache.ts", "cache service", false)
	for i := 0; i < 130; i++ {
		add(fmt.Sprint(i), "ItemView", fmt.Sprintf("app/item/%d.ts", i), "item item item item", false)
	}
	add("backend", "DataService", "api/item/service.ts", "", false)
	add("helper", "usage", "legacy/usage.ts", "desktop status bar context window", false)
	add("desktop", "DesktopStatusBar", "src/statusbar.ts", "context window usage", false)
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "ranking"}, a); err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: s}
	scope := graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	req := graphprotocol.DiscoverRequest{Scope: scope, Query: "cache service", Limit: 10, Config: graphprotocol.DiscoveryConfig{Deprioritize: []string{"**/legac?/cache.[jt]s"}}}
	got, err := service.Discover(t.Context(), req)
	if err != nil || len(got.Matches) < 5 || got.Matches[0].Entity.Fact.Occurrence != "product" {
		t.Fatalf("penalties=%+v %v", got, err)
	}
	scores := map[string]float64{}
	for _, m := range got.Matches {
		scores[m.Entity.Fact.Occurrence] = m.Score
		if m.File == nil {
			t.Fatal("missing original file evidence")
		}
	}
	if scores["generated"] == 0 || scores["test"] == 0 || scores["ambient"] == 0 || scores["deprioritized"] == 0 {
		t.Fatalf("downranking lost searchable facts: %v", scores)
	}
	if !(scores["product"] > scores["ambient"] && scores["product"] > scores["generated"] && scores["product"] > scores["test"] && scores["product"] > scores["deprioritized"]) {
		t.Fatalf("scores=%v", scores)
	}
	req.Limit = 1
	req.Files = []string{"generated/cache.ts"}
	got, err = service.Discover(t.Context(), req)
	if err != nil || len(got.Matches) != 1 || got.Matches[0].Entity.Fact.Occurrence != "generated" || !got.Matches[0].Pinned {
		t.Fatalf("pin=%+v %v", got, err)
	}
	req.Files = nil
	req.Query = "item service"
	req.CandidateLimit = 100
	got, err = service.Discover(t.Context(), req)
	if err != nil || len(got.Matches) != 1 || got.Matches[0].Entity.Fact.Occurrence != "backend" {
		t.Fatalf("corroboration=%+v %v", got, err)
	}
	req.Query = "desktop status bar context window usage"
	req.Config.Deprioritize = []string{"legacy/"}
	got, err = service.Discover(t.Context(), req)
	if err != nil || len(got.Matches) != 1 || got.Matches[0].Entity.Fact.Occurrence != "desktop" {
		t.Fatalf("deprioritized exact helper won: %+v %v", got, err)
	}

}

func TestGraphDiscoveryClassifiesFilesBeforeCandidateLimit(t *testing.T) {
	for _, tc := range []struct {
		name, preferredPath, preferredKind, demotedPath, demotedKind string
		preferredErrors                                              bool
	}{
		{
			name:          "generated filename",
			preferredPath: "src/cache.go",
			preferredKind: "function",
			demotedPath:   "internal/cache_mock.go",
			demotedKind:   "function",
		},
		{
			name:          "structural ambient",
			preferredPath: "types/runtime.d.ts",
			preferredKind: "class",
			demotedPath:   "types/cache.ts",
			demotedKind:   "interface",
		},
		{
			name:            "partial interface with errors",
			preferredPath:   "types/cache-partial.d.ts",
			preferredKind:   "interface",
			preferredErrors: true,
			demotedPath:     "src/fallback.ts",
			demotedKind:     "function",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, id := readyGraphStore(t, testSHA('a'))
			a := storageV2Artifact()
			a.ContentHash = nil
			a.Nodes = []*graphv2.Node{
				{SourceId: "preferred", Occurrence: "a", Kind: tc.preferredKind, Name: "CacheService", Path: proto.String(tc.preferredPath), Language: "typescript", Documentation: proto.String("cache implementation")},
				{SourceId: "demoted", Occurrence: "z", Kind: tc.demotedKind, Name: "CacheService", Path: proto.String(tc.demotedPath), Language: "typescript", Documentation: proto.String("cache implementation")},
			}
			a.Edges = nil
			a.Files = []*graphv2.File{
				{Path: tc.preferredPath, ContentHash: strings.Repeat("a", 64), Language: "typescript"},
				{Path: tc.demotedPath, ContentHash: strings.Repeat("b", 64), Language: "typescript"},
			}
			if tc.preferredErrors {
				a.Files[0].Errors = &graphv2.Extension{Namespace: "codegraph.extraction-errors", Json: []byte(`[{"message":"partial parse"}]`)}
			}
			a.Unresolved = nil
			if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "classification-ranking"}, a); err != nil {
				t.Fatal(err)
			}
			got, err := (&graphquery.Service{Store: s}).Discover(t.Context(), graphprotocol.DiscoverRequest{
				Scope:          graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}},
				Query:          "cache implementation",
				Limit:          1,
				CandidateLimit: 1,
			})
			if err != nil || len(got.Matches) != 1 || got.Matches[0].Entity.Fact.GetPath() != tc.preferredPath {
				t.Fatalf("required answer after classification=%+v err=%v", got.Matches, err)
			}
		})
	}
}

func TestGraphDiscoveryUsageCorroborationBeforeLimit(t *testing.T) {
	for _, kind := range []string{"variable", "constant", "property"} {
		t.Run(kind, func(t *testing.T) {
			s, id := readyGraphStore(t, testSHA('a'))
			a := storageV2Artifact()
			a.ContentHash = nil
			a.Files = nil
			a.Unresolved = nil
			target := &graphv2.Node{SourceId: "target", Occurrence: "target", Kind: kind, Name: "Data", Documentation: proto.String("item service")}
			a.Nodes = []*graphv2.Node{target, {SourceId: "consumer", Occurrence: "consumer", Kind: "function", Name: "Consume"}}
			a.Edges = []*graphv2.Edge{{SourceId: "usage", Occurrence: "usage", Source: "consumer", Target: "target", Kind: graphv2.EdgeKind_EDGE_KIND_REFERENCES}}
			for i := 0; i < 130; i++ {
				key := fmt.Sprintf("distractor-%d", i)
				a.Nodes = append(a.Nodes, &graphv2.Node{SourceId: key, Occurrence: key, Kind: "function", Name: "ItemView"})
			}
			if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "usage-ranking"}, a); err != nil {
				t.Fatal(err)
			}
			service := &graphquery.Service{Store: s}
			scope := graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
			for _, cap := range []int{200, 100, 1} {
				got, err := service.Discover(t.Context(), graphprotocol.DiscoverRequest{Scope: scope, Query: "item service", Limit: 1, CandidateLimit: cap})
				if err != nil || len(got.Matches) != 1 || !proto.Equal(got.Matches[0].Entity.Fact, target) || got.Matches[0].UsageCount != 1 || got.Matches[0].MatchedTerms != 2 {
					t.Fatalf("candidate limit %d loses usage-backed %s: %+v %v", cap, kind, got, err)
				}
				if got.CandidateTruncated != (cap < 131) {
					t.Fatalf("candidate limit %d truncation=%v", cap, got.CandidateTruncated)
				}
			}
		})
	}
}

func TestGraphDiscoveryMaximumFactsAndDeadline(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Nodes = nil
	a.Files = nil
	a.Edges = nil
	a.Unresolved = nil
	var doc strings.Builder
	for r := rune(1); doc.Len() < 250000; r++ {
		if unicode.IsLetter(r) {
			doc.WriteRune(r)
			doc.WriteByte(' ')
		}
	}
	doc.WriteString("terminalEvidence")
	n := &graphv2.Node{SourceId: "large", Occurrence: "large", Kind: "function", Name: strings.Repeat("a", 16384), QualifiedName: strings.Repeat("b", 16384), Signature: proto.String(strings.Repeat("c", 16384)), Documentation: proto.String(doc.String())}
	a.Nodes = []*graphv2.Node{n}
	pub, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "maximum"}, a)
	if err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: s}
	scope := graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	got, err := service.Discover(t.Context(), graphprotocol.DiscoverRequest{Scope: scope, Query: "terminalEvidence"})
	if err != nil || len(got.Matches) != 1 || !proto.Equal(got.Matches[0].Entity.Fact, n) {
		t.Fatalf("maximum fact: %v", err)
	}
	// A blocked real candidate projection read must obey the whole-operation clock.
	barrier, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(t.Context())
	if _, err = barrier.Exec(t.Context(), "lock table graph_v2_discovery in access exclusive mode"); err != nil {
		t.Fatal(err)
	}
	service.Limits.MaxDuration = 40 * time.Millisecond
	got, err = service.Discover(t.Context(), graphprotocol.DiscoverRequest{Scope: scope, Query: "terminalEvidence"})
	if !errors.Is(err, context.DeadlineExceeded) || len(got.Matches) > 0 || len(got.Generations) > 0 {
		t.Fatalf("deadline=%+v %v", got, err)
	}
	if err = barrier.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Oversized original payloads are refused before protobuf decoding.
	if _, err = s.pool.Exec(t.Context(), "update graph_v2_nodes set payload=$2 where upload_id=$1", pub.Upload.ID, []byte(strings.Repeat("x", graphquery.MaxEntityQueryBytes+1))); err != nil {
		t.Fatal(err)
	}
	service.Limits.MaxDuration = 0
	if _, err = service.Discover(t.Context(), graphprotocol.DiscoverRequest{Scope: scope, Query: "terminalEvidence"}); !errors.Is(err, graphquery.ErrQuerySize) {
		t.Fatalf("payload ceiling=%v", err)
	}
}

// This executes the normal planner on the real candidate SQL, without disabling
// sequential scans. It records plan evidence, not production latency claims.
func TestGraphDiscoveryQueryPlan(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Nodes = nil
	a.Files = nil
	a.Edges = nil
	a.Unresolved = nil
	for i := 0; i < 4000; i++ {
		name := fmt.Sprintf("UnrelatedRecord%d", i)
		if i == 2345 {
			name = "SettlementLedger"
		}
		key := fmt.Sprint(i)
		a.Nodes = append(a.Nodes, &graphv2.Node{SourceId: key, Occurrence: key, Kind: "function", Name: name, Path: proto.String(fmt.Sprintf("src/record%d.ts", i)), Documentation: proto.String("An unrelated record with routine documentation")})
	}
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "plan"}, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(t.Context(), "vacuum (analyze) graph_v2_discovery"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(t.Context(), "analyze graph_v2_nodes; analyze graph_uploads"); err != nil {
		t.Fatal(err)
	}
	tracer := &graphBenchmarkTracer{capture: true}
	config := s.pool.Config()
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	service := &graphquery.Service{Store: New(pool)}
	got, err := service.Discover(t.Context(), graphprotocol.DiscoverRequest{Scope: graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}, Query: "settlement", Limit: 10})
	if err != nil || len(got.Matches) != 1 || got.Matches[0].Entity.Fact.Name != "SettlementLedger" {
		t.Fatalf("plan answer=%+v %v", got, err)
	}
	var stmt graphBenchmarkStatement
	for _, q := range tracer.queries {
		if strings.Contains(q.SQL, "ranked as materialized") {
			stmt = q
		}
	}
	if stmt.SQL == "" {
		t.Fatal("candidate SQL not captured")
	}
	rows, err := s.pool.Query(t.Context(), "explain (analyze,buffers,settings) "+stmt.SQL, stmt.Args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err = rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
		t.Log(line)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if output := os.Getenv("GRAPHNEST_DISCOVERY_PLAN_REPORT"); output != "" {
		if err = os.WriteFile(output, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGraphDiscoveryPatternsAndDistance(t *testing.T) {
	s, _ := readyGraphStore(t, testSHA('a'))
	for _, tc := range []struct {
		patterns []string
		path     string
		want     bool
	}{
		{[]string{"**/legac?/cache.[jt]s"}, "legacy/cache.ts", true},
		{[]string{"**/legac?/cache.[jt]s"}, "src/cache.ts", false},
		{[]string{"scripts/"}, "nested/scripts/helper.ts", true},
		{[]string{"*.ts", "!keep.ts"}, "keep.ts", false},
		{[]string{"scripts/", "!scripts/keep.ts"}, "scripts/keep.ts", true},
		{[]string{"scripts/*", "!scripts/keep.ts"}, "scripts/keep.ts", false},
		{[]string{"vendor/**", "!vendor/keep/", "!vendor/keep/**"}, "vendor/keep/a.ts", false},
		{[]string{"/root.ts"}, "nested/root.ts", false},
		{[]string{"/root.ts"}, "root.ts", true},
		{[]string{`\#note.ts`}, "#note.ts", true},
		{[]string{"#note.ts"}, "#note.ts", false},
		{[]string{"file[[:digit:]].ts"}, "file2.ts", true},
		{[]string{"file[!0-9].ts"}, "filea.ts", true},
	} {
		patterns, negative := graphquery.DiscoveryPatterns(tc.patterns)
		var got bool
		if err := s.pool.QueryRow(t.Context(), "select graph_discovery_deprioritized($1,$2,$3)", tc.path, patterns, negative).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("patterns %v path %s =%v want%v regex=%v", tc.patterns, tc.path, got, tc.want, patterns)
		}
	}
	for _, tc := range []struct {
		a, b     string
		distance int
	}{
		{"getUssr", "getUser", 1}, {"process", "prosody", 3}, {"abcd", "abdc", 2}, {"abcdef", "abXdeY", 2}, {"kitten", "sitting", 3}, {"abc", "", 3}, {"éclair", "eclair", 1}, {strings.Repeat("a", 4000) + "b", strings.Repeat("a", 4000) + "c", 1},
	} {
		var got int
		if err := s.pool.QueryRow(t.Context(), "select graph_discovery_distance($1,$2,2)", tc.a, tc.b).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != tc.distance {
			t.Fatalf("distance=%d want=%d", got, tc.distance)
		}
	}
}

func TestGraphDiscoveryOneCharacterRecall(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Nodes = nil
	a.Files = nil
	a.Edges = nil
	a.Unresolved = nil
	for _, name := range []string{"x", "xylophone", "boxed", "a", "alpha", "boat"} {
		a.Nodes = append(a.Nodes, &graphv2.Node{SourceId: name, Occurrence: name, Kind: "variable", Name: name})
	}
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "short-name"}, a); err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: s}
	scope := graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	req := graphprotocol.DiscoverRequest{Scope: scope, Query: "x", Limit: 3}
	got, err := service.Discover(t.Context(), req)
	if err != nil || len(got.Matches) != 3 || got.Matches[0].Entity.Fact.Name != "x" || got.Confidence != "low" {
		t.Fatalf("short-name recall=%+v %v", got, err)
	}
	names := map[string]bool{}
	for _, m := range got.Matches {
		names[m.Entity.Fact.Name] = true
	}
	if !names["xylophone"] || !names["boxed"] || names["unrelated"] {
		t.Fatalf("short prefix/substring=%v", names)
	}
	req.Limit = 1
	got, err = service.Discover(t.Context(), req)
	if err != nil || len(got.Matches) != 1 || got.Matches[0].Entity.Fact.Name != "x" {
		t.Fatalf("exact short name lost=%+v %v", got, err)
	}
	req.Query = "a"
	req.Limit = 3
	got, err = service.Discover(t.Context(), req)
	if err != nil || len(got.Matches) != 3 || got.Matches[0].Entity.Fact.Name != "a" || got.Confidence != "low" {
		t.Fatalf("single stopword recall=%+v %v", got, err)
	}
	req.Query = "boxed"
	req.Limit = 1
	got, err = service.Discover(t.Context(), req)
	if err != nil || got.Status != "candidates" || len(got.Matches) != 1 || got.Matches[0].Entity.Fact.Name != "boxed" || got.Confidence != "low" {
		t.Fatalf("isolated exact name lost=%+v %v", got, err)
	}
	req.Query = "z"
	got, err = service.Discover(t.Context(), req)
	if err != nil || got.Status != "no_entry_point" || len(got.Matches) != 0 {
		t.Fatalf("unmatched short name=%+v %v", got, err)
	}
}
