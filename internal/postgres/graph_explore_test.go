//go:build integration

package postgres

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/graphservice"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
	"google.golang.org/protobuf/proto"
)

func TestGraphExploreMultitermCandidateLimit(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash, a.Files, a.Unresolved, a.Edges = nil, nil, nil, nil
	a.Nodes = []*graphv2.Node{{SourceId: "corroborated", Occurrence: "corroborated", Kind: "function", Name: "Data", Documentation: proto.String("item service")}, {SourceId: "broad", Occurrence: "broad", Kind: "function", Name: "ItemView"}}
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "explore-config"}, a); err != nil {
		t.Fatal(err)
	}
	for _, disabled := range []bool{false, true} {
		got, err := (&graphquery.Service{Store: s}).Discover(t.Context(), graphprotocol.DiscoverRequest{Scope: graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}, Query: "item service", Limit: 1, CandidateLimit: 1, Config: graphprotocol.DiscoveryConfig{NoMultiterm: disabled}})
		want := "corroborated"
		if disabled {
			want = "broad"
		}
		if err != nil || len(got.Matches) != 1 || got.Matches[0].Entity.Fact.Occurrence != want {
			t.Fatalf("disabled=%v result=%+v err=%v", disabled, got, err)
		}
	}
}

func TestGraphExploreRealOracle(t *testing.T) {
	path := os.Getenv("GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE")
	if path == "" {
		t.Fatal("required real CodeGraph fixture missing")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := graphartifact.ParseV2(data, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	store, id := readyGraphStore(t, a.Commit)
	publication, err := store.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "explore-oracle"}, a)
	if err != nil {
		t.Fatal(err)
	}
	legacy := seedReadyRepository(t, store, 202, a.Commit)
	if _, err = store.ReplaceGraph(t.Context(), legacy, GraphSourceManaged, artifactFor(legacy, a.Commit, "legacy")); err != nil {
		t.Fatal(err)
	}
	gateway := &inspectionGitHub{t: t, commit: a.Commit}
	service := &graphservice.Service{Store: store, Backend: &graphquery.Service{Store: store}, Files: &repository.Service{Store: store, GitHub: gateway}}
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101, 202}}
	facts := map[string]*graphv2.Node{}
	for _, n := range a.Nodes {
		facts[n.Occurrence] = n
	}
	for _, query := range []string{"processGreeting", "normalize", "consumer.ts", "the way it works", "processGreeting orphanUtility orphan.ts"} {
		t.Run(query, func(t *testing.T) {
			got, err := service.Explore(t.Context(), principal, graphservice.ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: query, MaxFiles: 4, SourceUnits: 13000})
			if err != nil {
				t.Fatal(err)
			}
			if got.CorpusFiles != 13 || got.Usage.SourceReads > 4 || got.Usage.SourceUnits > 13000 || len(got.Generations) != 1 {
				t.Fatalf("budget/provenance: %+v", got)
			}
			encoded, _ := json.Marshal(got)
			if len(encoded) > 256<<10 {
				t.Fatal("response overflow")
			}
			served := map[string]bool{}
			for _, file := range got.Files {
				for _, entity := range file.Entities {
					if !proto.Equal(entity.Fact, facts[entity.Fact.Occurrence]) {
						t.Fatalf("changed occurrence %s", entity.ID)
					}
				}
				for _, segment := range file.Segments {
					original, e := os.ReadFile(filepath.Join("../../test/fixtures/codegraph/source", file.Path))
					if e != nil {
						t.Fatal(e)
					}
					lines := strings.Split(string(original), "\n")
					want := strings.Join(lines[segment.StartLine-1:segment.EndLine], "\n")
					if segment.Content != want || segment.IndexedSHA != a.Commit {
						t.Fatalf("non-verbatim %s", file.Path)
					}
					served[file.Path] = true
				}
			}
			for _, graph := range got.Relationships {
				for _, edge := range graph.Edges {
					found := false
					for _, original := range a.Edges {
						found = found || proto.Equal(edge.Fact, original)
					}
					if !found {
						t.Fatal("lost original relationship occurrence")
					}
				}
			}
			if query == "processGreeting" || query == "consumer.ts" {
				if !served["consumer.ts"] {
					t.Fatalf("lost pinned processGreeting source: %v", served)
				}
			}
			if query == "processGreeting" {
				for _, required := range []string{"core.ts", "model.swift"} {
					if !served[required] {
						t.Fatalf("lost pinned Explore answer %s: %v", required, served)
					}
				}
			}
			if query == "normalize" {
				for _, required := range []string{"core.ts", "Model.java", "model.rb", "Widget.vue"} {
					if !served[required] {
						t.Fatalf("lost required overload source %s: %v", required, served)
					}
				}
			}
			if query == "processGreeting orphanUtility orphan.ts" && (!served["orphan.ts"] || !served["consumer.ts"]) {
				t.Fatalf("lost stateless new-file hint: %v", served)
			}
			if query == "the way it works" && (got.Discovery.Status != "no_entry_point" || got.Discovery.Confidence != "low" || len(got.Handoffs) == 0 || got.Complete) {
				t.Fatalf("invented weak entry: %+v", got)
			}
			t.Logf("query=%q files=%v units=%d bytes=%d response=%d reads=%d", query, served, got.Usage.SourceUnits, got.Usage.SourceBytes, len(encoded), got.Usage.SourceReads)
		})
	}
	for _, mode := range []string{"replacement", "sha", "grant"} {
		t.Run(mode, func(t *testing.T) {
			gateway.after = func() {
				gateway.after = nil
				switch mode {
				case "replacement":
					replacement := proto.Clone(a).(*graphv2.Artifact)
					replacement.ContentHash = nil
					replacement.Nodes[0].Name += " replacement"
					var e error
					publication, e = store.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "explore-oracle", ExpectedActiveID: publication.Upload.ID}, replacement)
					if e != nil {
						t.Fatal(e)
					}
				case "sha":
					if _, e := store.pool.Exec(t.Context(), "update repositories set indexed_sha=$1 where id=$2", strings.Repeat("b", 40), id); e != nil {
						t.Fatal(e)
					}
				case "grant":
					if _, e := store.pool.Exec(t.Context(), "update repositories set enabled=false where id=$1", id); e != nil {
						t.Fatal(e)
					}
				}
			}
			got, e := service.Explore(t.Context(), principal, graphservice.ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "processGreeting"})
			if e == nil || !reflect.DeepEqual(got, graphservice.ExploreResponse{}) {
				t.Fatalf("%s leaked %+v err=%v", mode, got, e)
			}
			if mode == "sha" {
				if _, e = store.pool.Exec(t.Context(), "update repositories set indexed_sha=$1 where id=$2", a.Commit, id); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}
