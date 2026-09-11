//go:build integration

package postgres

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
)

func TestGraphDiscoveryVariantsPlan(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Nodes = nil
	a.Edges = nil
	a.Files = nil
	a.Unresolved = nil
	for i := 0; i < 4000; i++ {
		key := fmt.Sprint(i)
		name := "UnrelatedRecord" + key
		if i == 123 {
			name = "SettlementLedger"
		}
		if i == 124 {
			name = "SettlementService"
		}
		a.Nodes = append(a.Nodes, &graphv2.Node{SourceId: key, Occurrence: key, Kind: "function", Name: name})
	}
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "variants-plan"}, a); err != nil {
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
	scope := graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	for _, mode := range []string{"prefix", "substring", "segment"} {
		t.Run(mode, func(t *testing.T) {
			tracer.queries = nil
			if mode == "segment" {
				page, e := service.SegmentMatches(t.Context(), graphprotocol.SegmentRequest{Scope: scope, Words: []string{"settlement"}, Limit: 2})
				if e != nil || len(page.Matches) != 2 {
					t.Fatalf("segment=%+v %v", page, e)
				}
			} else {
				value := "Settlement"
				if mode == "substring" {
					value = "TLEMENT"
				}
				page, e := service.Entities(t.Context(), graphprotocol.EntitiesRequest{Scope: scope, Selector: graphprotocol.EntitySelector{NameMatch: &graphprotocol.NameSelector{Mode: mode, Value: value}}, Limit: 2})
				if e != nil || len(page.Entities) != 2 {
					t.Fatalf("names=%+v %v", page, e)
				}
			}
			var statement graphBenchmarkStatement
			for _, q := range tracer.queries {
				if strings.Contains(q.SQL, "name_candidates as materialized") || strings.Contains(q.SQL, "live as materialized") {
					statement = q
				}
			}
			if statement.SQL == "" || !strings.Contains(statement.SQL, "u.id=scope.upload_id") || !strings.Contains(statement.SQL, "u.repository_id=scope.repository_id") || !strings.Contains(statement.SQL, "u.commit=scope.commit") {
				t.Fatal("missing immutable candidate scope")
			}
			rows, e := s.pool.Query(t.Context(), "explain (analyze,buffers) "+statement.SQL, statement.Args...)
			if e != nil {
				t.Fatal(e)
			}
			defer rows.Close()
			lines := []string{}
			for rows.Next() {
				var line string
				if e = rows.Scan(&line); e != nil {
					t.Fatal(e)
				}
				lines = append(lines, line)
				t.Log(line)
			}
			if e = rows.Err(); e != nil {
				t.Fatal(e)
			}
			plan := strings.Join(lines, "\n")
			index := map[string]string{"prefix": "graph_v2_discovery_prefix", "substring": "graph_v2_discovery_selector_grams", "segment": "graph_v2_discovery_segments"}[mode]
			if !strings.Contains(plan, index) || !strings.Contains(plan, "Limit") || strings.Contains(plan, "Seq Scan on graph_v2_discovery") {
				t.Fatalf("unbounded/unindexed variant plan:\n%s", plan)
			}
		})
	}
}

func TestGraphDiscoveryVariantsRebuildRollback(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	pub, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "variant-rebuild"}, a)
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err = s.pool.QueryRow(t.Context(), "select count(*) from graph_v2_discovery where upload_id=$1 and original_name is not null", pub.Upload.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(t.Context(), "update graph_uploads set discovery_version=1 where id=$1", pub.Upload.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(t.Context(), "alter table graph_v2_discovery add constraint reject_variant_rebuild check (original_name is null) not valid"); err != nil {
		t.Fatal(err)
	}
	if err = s.RebuildGraphDiscovery(t.Context(), id, pub.Upload.ID); err == nil {
		t.Fatal("rebuild failure was not observed")
	}
	var version, count int
	if err = s.pool.QueryRow(t.Context(), "select discovery_version,(select count(*) from graph_v2_discovery where upload_id=$1 and original_name is not null) from graph_uploads where id=$1", pub.Upload.ID).Scan(&version, &count); err != nil {
		t.Fatal(err)
	}
	if version != 1 || count != before {
		t.Fatalf("failed rebuild changed version/rows: %d %d want 1/%d", version, count, before)
	}
	if _, err = s.pool.Exec(t.Context(), "alter table graph_v2_discovery drop constraint reject_variant_rebuild"); err != nil {
		t.Fatal(err)
	}
	if err = s.RebuildGraphDiscovery(t.Context(), id, pub.Upload.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.pool.QueryRow(t.Context(), "select discovery_version from graph_uploads where id=$1", pub.Upload.ID).Scan(&version); err != nil || version != 4 {
		t.Fatalf("rebuilt version=%d err=%v", version, err)
	}
}

func TestGraphDiscoveryVersionFourPreservesVariants(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Nodes = []*graphv2.Node{
		{SourceId: "one", Occurrence: "one", Kind: "function", Name: "processGreeting"},
		{SourceId: "two", Occurrence: "two", Kind: "function", Name: "testGreeting"},
	}
	a.Edges = nil
	a.Files = nil
	a.Unresolved = nil
	pub, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "classification-version"}, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(t.Context(), "update graph_uploads set discovery_version=2 where id=$1", pub.Upload.ID); err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: s}
	scope := graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	if _, err = service.Discover(t.Context(), graphprotocol.DiscoverRequest{Scope: scope, Query: "greeting"}); !errors.Is(err, graphquery.ErrDiscoveryUnavailable) {
		t.Fatalf("version-two discovery=%v", err)
	}
	page, err := service.Entities(t.Context(), graphprotocol.EntitiesRequest{Scope: scope, Selector: graphprotocol.EntitySelector{NameMatch: &graphprotocol.NameSelector{Mode: "prefix", Value: "process"}}})
	if err != nil || len(page.Entities) != 1 || page.Entities[0].Fact.Name != "processGreeting" {
		t.Fatalf("version-two exact variant=%+v err=%v", page, err)
	}
	segments, err := service.SegmentMatches(t.Context(), graphprotocol.SegmentRequest{Scope: scope, Words: []string{"greeting"}})
	if err != nil || len(segments.Matches) != 2 {
		t.Fatalf("version-two segments=%+v err=%v", segments, err)
	}
	if err = s.RebuildGraphDiscovery(t.Context(), id, pub.Upload.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Discover(t.Context(), graphprotocol.DiscoverRequest{Scope: scope, Query: "greeting"}); err != nil {
		t.Fatalf("version-three discovery=%v", err)
	}
}

func TestGraphDiscoveryVariantsOracle(t *testing.T) {
	fixture := os.Getenv("GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE")
	if fixture == "" {
		t.Skip("requires exported v2 fixture")
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	a, err := graphartifact.ParseV2(data, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile("../../test/fixtures/codegraph/discovery-variants.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		ImmutableReference struct{ Commit string }
		Calls              []struct {
			ID, Method              string
			Args                    []json.RawMessage
			Answer                  json.RawMessage
			ReturnedCount           int
			KnownMatchingPopulation int
		}
	}
	if err = json.Unmarshal(data, &oracle); err != nil {
		t.Fatal(err)
	}
	if oracle.ImmutableReference.Commit != "b9ca4b7981116909900368cc1686a1074cd4d4c1" || len(oracle.Calls) != 16 {
		t.Fatal("oracle provenance changed")
	}
	s, id := readyGraphStore(t, a.Commit)
	if _, err = s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "variants-oracle"}, a); err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: s}
	scope := graphprotocol.Scope{SelectedRepositoryID: id, Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	original := map[string]*graphv2.Node{}
	for _, n := range a.Nodes {
		original[n.SourceId] = n
	}
	for _, c := range oracle.Calls {
		if c.Method == "getProjectNameTokens" {
			// Source evidence is exercised by TestProjectTokensPinnedOracle in
			// internal/graphservice/project_tokens_test.go.
			continue
		}
		t.Run(c.ID, func(t *testing.T) {
			if c.Method == "getSegmentMatches" {
				var words []string
				if err = json.Unmarshal(c.Args[0], &words); err != nil {
					t.Fatal(err)
				}
				page, e := service.SegmentMatches(t.Context(), graphprotocol.SegmentRequest{Scope: scope, Words: words})
				if e != nil {
					t.Fatal(e)
				}
				type answer struct {
					Name, Kind, FilePath string
					StartLine            int
					MatchedWords         []string
				}
				want := []answer{}
				if err = json.Unmarshal(c.Answer, &want); err != nil {
					t.Fatal(err)
				}
				got := []answer{}
				for _, m := range page.Matches {
					n := m.Entity.Fact
					if !proto.Equal(n, original[n.SourceId]) {
						t.Fatal("representative original lost")
					}
					got = append(got, answer{n.Name, n.Kind, n.GetPath(), int(n.Location.GetStart().GetLine()) + 1, m.MatchedWords})
				}
				sort.Slice(got, func(i, j int) bool { return got[i].Name < got[j].Name })
				sort.Slice(want, func(i, j int) bool { return want[i].Name < want[j].Name })
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("segment got=%+v want=%+v", got, want)
				}
				t.Logf("%s membership=%d", c.ID, len(got))
				return
			}
			var value string
			if err = json.Unmarshal(c.Args[0], &value); err != nil {
				t.Fatal(err)
			}
			selector := graphprotocol.EntitySelector{}
			limit := 0
			switch c.Method {
			case "getNodesByQualifiedName":
				selector.QualifiedName = &value
			case "getNodesInFile":
				selector.Path = &value
			case "getNodesByKind":
				selector.Kind = value
			case "getNodesByName":
				selector.Name = &value
			case "getNodesByNamePrefix":
				selector.NameMatch = &graphprotocol.NameSelector{Mode: "prefix", Value: value}
				if len(c.Args) > 1 {
					if err = json.Unmarshal(c.Args[1], &limit); err != nil {
						t.Fatal(err)
					}
				}
			case "getNodesByNameSubstring":
				selector.NameMatch = &graphprotocol.NameSelector{Mode: "substring", Value: value}
				if len(c.Args) > 1 {
					var options struct {
						Kinds         []string
						ExcludePrefix bool
						Limit         int
					}
					if err = json.Unmarshal(c.Args[1], &options); err != nil {
						t.Fatal(err)
					}
					selector.NameMatch.Kinds = options.Kinds
					selector.NameMatch.ExcludePrefix = options.ExcludePrefix
					limit = options.Limit
				}
			default:
				t.Fatal(c.Method)
			}
			var answer []map[string]any
			if err = json.Unmarshal(c.Answer, &answer); err != nil {
				t.Fatal(err)
			}
			want := map[string]bool{}
			for _, n := range answer {
				want[n["id"].(string)] = true
			}
			if selector.NameMatch == nil {
				limit = 2
			}
			req := graphprotocol.EntitiesRequest{Scope: scope, Selector: selector, Limit: limit}
			all := []graphprotocol.Entity{}
			first := true
			for {
				page, e := service.Entities(t.Context(), req)
				if e != nil {
					t.Fatal(e)
				}
				if first && c.KnownMatchingPopulation > 0 && len(page.Entities) != c.ReturnedCount {
					t.Fatal("explicit equivalent budget lost")
				}
				first = false
				all = append(all, page.Entities...)
				if page.NextCursor == "" {
					break
				}
				req.Cursor = page.NextCursor
			}
			// The two limit-two captures exhaust only their returned arrays. Paging must
			// still recover the third overload, as established by the uncapped captures.
			if c.KnownMatchingPopulation > 0 {
				if len(all) != c.KnownMatchingPopulation {
					t.Fatalf("incomplete page union=%d", len(all))
				}
			} else if len(all) != len(want) {
				t.Fatalf("membership=%d want=%d", len(all), len(want))
			}
			actual := map[string]bool{}
			for _, e := range all {
				n := e.Fact
				actual[n.SourceId] = true
				if !proto.Equal(n, original[n.SourceId]) {
					t.Fatal("original fact changed")
				}
				if c.KnownMatchingPopulation == 0 && !want[n.SourceId] {
					t.Fatalf("unexpected id %s", n.SourceId)
				}
			}
			for _, n := range answer {
				fact := original[n["id"].(string)]
				if fact == nil || !actual[fact.SourceId] {
					t.Fatal("required oracle fact absent")
				}
				assertDiscoveryOracleFact(t, n, fact)
			}
			if c.Method == "getNodesInFile" || c.Method == "getNodesByName" {
				sort.SliceStable(all, func(i, j int) bool {
					a, b := all[i].Fact, all[j].Fact
					if c.Method == "getNodesByName" && a.GetPath() != b.GetPath() {
						return a.GetPath() < b.GetPath()
					}
					return a.Location.GetStart().GetLine() < b.Location.GetStart().GetLine()
				})
				for i, e := range all {
					if int(e.Fact.Location.GetStart().GetLine())+1 != int(answer[i]["startLine"].(float64)) || c.Method == "getNodesByName" && e.Fact.GetPath() != answer[i]["filePath"] {
						t.Fatal("primary order projection differs")
					}
				}
			}
			t.Logf("%s reference=%d paged membership=%d", c.ID, c.ReturnedCount, len(all))
		})
	}
}

func assertDiscoveryOracleFact(t *testing.T, want map[string]any, n *graphv2.Node) {
	t.Helper()
	got := map[string]any{"id": n.SourceId, "name": n.Name, "kind": n.Kind, "qualifiedName": n.QualifiedName, "filePath": n.GetPath(), "language": n.Language, "startLine": float64(n.Location.GetStart().GetLine() + 1), "endLine": float64(n.Location.GetEnd().GetLine() + 1), "startColumn": float64(n.Location.GetStart().GetCharacter()), "endColumn": float64(n.Location.GetEnd().GetCharacter()), "isExported": n.GetIsExported(), "isAsync": n.GetIsAsync(), "isStatic": n.GetIsStatic(), "isAbstract": n.GetIsAbstract(), "updatedAt": float64(n.GetUpdatedAt())}
	for k, p := range map[string]*string{"signature": n.Signature, "visibility": n.Visibility, "returnType": n.ReturnType, "docstring": n.Documentation} {
		if p != nil {
			got[k] = *p
		} else {
			got[k] = nil
		}
	}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Fatalf("%s fact %s=%v want=%v", n.SourceId, k, got[k], v)
		}
	}
}

func TestGraphNameSelectors(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Nodes = nil
	a.Edges = nil
	a.Files = nil
	a.Unresolved = nil
	names := []string{"normalize", "Normalize", "xNORMALIZE", "éclair", "Éclair", "percent%name", "underscore_name", "before\x00After", strings.Repeat("q", 4000) + "End"}
	for i := 0; i < 35; i++ {
		names = append(names, fmt.Sprintf("normalize%02d", i))
	}
	for i, name := range names {
		key := fmt.Sprint(i)
		kind := "function"
		if i%2 == 0 {
			kind = "method"
		}
		a.Nodes = append(a.Nodes, &graphv2.Node{SourceId: key, Occurrence: key, Kind: kind, Name: name, Path: proto.String("same.ts")})
	}
	pub, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "variants"}, a)
	if err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: s}
	scope := graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	cases := []graphprotocol.NameSelector{{Mode: "prefix", Value: "norm"}, {Mode: "prefix", Value: "Norm"}, {Mode: "substring", Value: "NORM"}, {Mode: "substring", Value: "norm", Kinds: []string{"method"}, ExcludePrefix: true}, {Mode: "substring", Value: "é"}, {Mode: "substring", Value: "É"}, {Mode: "substring", Value: "%"}, {Mode: "substring", Value: "_"}, {Mode: "substring", Value: "\x00after"}, {Mode: "prefix", Value: strings.Repeat("q", 3000)}, {Mode: "substring", Value: "absent"}, {Mode: "prefix", Value: ""}}
	for _, selector := range cases {
		t.Run(selector.Mode+fmt.Sprintf("-%x", selector.Value[:min(len(selector.Value), 12)]), func(t *testing.T) {
			want := []string{}
			for _, n := range a.Nodes {
				match := strings.HasPrefix(n.Name, selector.Value)
				if selector.Mode == "substring" {
					match = strings.Contains(graphquery.FoldName(n.Name), graphquery.FoldName(selector.Value)) && (!selector.ExcludePrefix || !strings.HasPrefix(graphquery.FoldName(n.Name), graphquery.FoldName(selector.Value)))
				}
				if len(selector.Kinds) > 0 && n.Kind != selector.Kinds[0] {
					match = false
				}
				if match {
					want = append(want, n.Occurrence)
				}
			}
			req := graphprotocol.EntitiesRequest{Scope: scope, Selector: graphprotocol.EntitySelector{NameMatch: &selector}, Limit: 2}
			got := []string{}
			for {
				page, e := service.Entities(t.Context(), req)
				if e != nil {
					t.Fatal(e)
				}
				for _, entity := range page.Entities {
					got = append(got, entity.Fact.Occurrence)
					for _, n := range a.Nodes {
						if n.Occurrence == entity.Fact.Occurrence && !proto.Equal(n, entity.Fact) {
							t.Fatal("original fact changed")
						}
					}
				}
				if page.NextCursor == "" {
					break
				}
				req.Cursor = page.NextCursor
			}
			sort.Strings(got)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("membership got %v want %v", got, want)
			}
		})
	}
	for mode, want := range map[string]int{"prefix": 20, "substring": 30} {
		page, e := service.Entities(t.Context(), graphprotocol.EntitiesRequest{Scope: scope, Selector: graphprotocol.EntitySelector{NameMatch: &graphprotocol.NameSelector{Mode: mode, Value: "norm"}}})
		if e != nil || len(page.Entities) != want || page.NextCursor == "" {
			t.Fatalf("default %s=%d %v", mode, len(page.Entities), e)
		}
	}
	req := graphprotocol.EntitiesRequest{Scope: scope, Selector: graphprotocol.EntitySelector{NameMatch: &cases[0]}, Limit: 2}
	page, err := service.Entities(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(t.Context(), "update graph_uploads set discovery_version=1 where id=$1", pub.Upload.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Entities(t.Context(), req); !errors.Is(err, graphquery.ErrDiscoveryUnavailable) {
		t.Fatalf("old projection=%v", err)
	}
	if err = s.RebuildGraphDiscovery(t.Context(), id, pub.Upload.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "replacement", ExpectedActiveID: pub.Upload.ID}, a); err != nil {
		t.Fatal(err)
	}
	req.Cursor = page.NextCursor
	if _, err = service.Entities(t.Context(), req); !errors.Is(err, graphquery.ErrGenerationChanged) {
		t.Fatalf("replaced cursor=%v", err)
	}
}

func TestGraphSegmentEvidence(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Nodes = nil
	a.Edges = nil
	a.Files = nil
	a.Unresolved = nil
	names := []string{"processGreeting", "testGreeting", "servicesService", "productionOnly", "authOne", "authTwo", "fileGreeting", "importGreeting", "duplicateGreeting", "duplicateGreeting"}
	for i := 0; i < 26; i++ {
		names = append(names, fmt.Sprintf("noise%dCommonword", i))
	}
	for i := 0; i < 9; i++ {
		names = append(names, fmt.Sprintf("longerBudget%dGreeting", i))
	}
	for i, name := range names {
		key := fmt.Sprint(i)
		kind := "function"
		if name == "fileGreeting" {
			kind = "file"
		}
		if name == "importGreeting" {
			kind = "import"
		}
		a.Nodes = append(a.Nodes, &graphv2.Node{SourceId: key, Occurrence: key, Kind: kind, Name: name, Path: proto.String("same.ts")})
	}
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "segments"}, a); err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: s}
	scope := graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	for _, tc := range []struct {
		words []string
		want  []string
	}{{nil, nil}, {[]string{"services"}, nil}, {[]string{"production"}, nil}, {[]string{"auth"}, nil}, {[]string{"commonword"}, nil}, {[]string{"process", "greetings"}, []string{"processGreeting"}}, {[]string{"greeting", "process", "auth"}, []string{"processGreeting"}}} {
		page, err := service.SegmentMatches(t.Context(), graphprotocol.SegmentRequest{Scope: scope, Words: tc.words})
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, m := range page.Matches {
			got = append(got, m.Entity.Fact.Name)
			if m.Entity.Fact.Name == "processGreeting" && len(m.MatchedWords) != 2 {
				t.Fatalf("lost original words: %+v", m)
			}
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("words %v got %v want %v", tc.words, got, tc.want)
		}
	}
	page, err := service.SegmentMatches(t.Context(), graphprotocol.SegmentRequest{Scope: scope, Words: []string{"greeting"}})
	if err != nil || len(page.Matches) != 6 {
		t.Fatalf("default=%+v %v", page, err)
	}
	seen := map[string]bool{}
	for _, m := range page.Matches {
		if seen[m.Entity.Fact.Name] || m.Entity.Fact.Kind == "file" || m.Entity.Fact.Kind == "import" || !reflect.DeepEqual(m.MatchedWords, []string{"greeting"}) {
			t.Fatalf("invalid evidence=%+v", m)
		}
		seen[m.Entity.Fact.Name] = true
	}
	if !seen["testGreeting"] || !seen["processGreeting"] {
		t.Fatalf("short stronger answers lost at default: %v", seen)
	}
	all, err := service.SegmentMatches(t.Context(), graphprotocol.SegmentRequest{Scope: scope, Words: []string{"greeting"}, Limit: 100})
	if err != nil || len(all.Matches) != 12 || all.Truncated {
		t.Fatalf("distinct live names=%d err=%v", len(all.Matches), err)
	}
	// A stale proposal cannot manufacture a live name; the original-byte join
	// rejects it even though its segment array supplies both requested words.
	if _, err = s.pool.Exec(t.Context(), `update graph_v2_discovery set original_name=$1,segments=$2 where name='productiononly'`, []byte("GhostProposal"), []string{"ghost", "proposal"}); err != nil {
		t.Fatal(err)
	}
	orphan, err := service.SegmentMatches(t.Context(), graphprotocol.SegmentRequest{Scope: scope, Words: []string{"ghost", "proposal"}})
	if err != nil || len(orphan.Matches) != 0 {
		t.Fatalf("orphan proposal=%+v err=%v", orphan, err)
	}
}
