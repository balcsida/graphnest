//go:build integration

package postgres

import (
	"encoding/json"
	"fmt"
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

	t.Run("pinned_file_admission", func(t *testing.T) {
		got, err := service.Explore(t.Context(), principal, graphservice.ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "core.ts Service normalize", MaxFiles: 1, SourceUnits: 13000})
		if err != nil {
			t.Fatalf("exact pinned one-file query rejected: %v", err)
		}
		if got.FileLimit != 1 || got.Usage.SourceReads > 1 || got.Usage.SourceUnits > 13000 || got.Usage.SourceBytes > 256<<10 || len(got.Generations) != 1 {
			t.Fatal("one-file query exceeded original bounds")
		}
		encoded, err := json.Marshal(got)
		if err != nil || len(encoded) > 256<<10 {
			t.Fatal("serialized response bound")
		}
		original, err := os.ReadFile("../../test/fixtures/codegraph/source/core.ts")
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Join(strings.Split(string(original), "\n")[:16], "\n")
		served, omitted := 0, 0
		symbols := map[string]bool{}
		for _, f := range got.Files {
			for _, e := range f.Entities {
				if !proto.Equal(e.Fact, facts[e.Fact.Occurrence]) {
					t.Fatal("original occurrence changed")
				}
			}
			if len(f.Segments) == 0 {
				if f.Required {
					omitted++
					if f.Mode != "pointer" || f.Status != "allocation_cliff" || f.Fact == nil || len(f.Entities) == 0 {
						t.Fatal("omitted named candidate lost pointer evidence")
					}
				}
				continue
			}
			served++
			if f.Path != "core.ts" || !f.Pinned || len(f.Segments) != 1 {
				t.Fatalf("unexpected source admission: %s", f.Path)
			}
			seg := f.Segments[0]
			// Preserve the native reader's final LF/empty EOF line as well as the
			// pinned answer's required original L1-16 span.
			if seg.StartLine != 1 || seg.EndLine != 17 || seg.Content != string(original) || seg.IndexedSHA != a.Commit {
				t.Fatal("core.ts original whole source/provenance lost")
			}
			if strings.Join(strings.Split(seg.Content, "\n")[:16], "\n") != want {
				t.Fatal("required original L1-16 span lost")
			}

			for _, e := range f.Entities {
				symbols[e.Fact.Name] = true
			}
		}
		if served != 1 || omitted == 0 || !symbols["Service"] || !symbols["normalize"] {
			t.Fatalf("source answer missing: served=%d omitted=%d names=%v", served, omitted, symbols)
		}
		if got.Complete || len(got.Handoffs) == 0 {
			t.Fatal("omitted implicit named source became complete")
		}
		boundary := false
		for _, b := range got.Boundaries {
			boundary = boundary || b == "source_boundary"
		}
		if !boundary {
			t.Fatal("source omission boundary missing")
		}
		t.Logf("exact query: core.ts required L1-16 / native EOF L17 units=%d bytes=%d response=%d reads=%d omitted-named=%d", got.Usage.SourceUnits, got.Usage.SourceBytes, len(encoded), got.Usage.SourceReads, omitted)
	})

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

	// Frozen C3 reference: six enabled/disabled overlapping calls plus a two-call
	// core-only restore. Reuse the committed source and imported real facts.
	for _, dedup := range []bool{true, false} {
		t.Run(fmt.Sprintf("session_dedup_%v", dedup), func(t *testing.T) {
			sessionService := &graphservice.Service{Store: store, Backend: &graphquery.Service{Store: store}, Files: &repository.Service{Store: store, GitHub: gateway}}
			current := principal
			current.Subject = "oracle-session"
			current.Method = "local"
			for call, query := range []string{"processGreeting", "processGreeting", "processGreeting orphanUtility orphan.ts", "processGreeting", "processGreeting", "processGreeting"} {
				got, e := sessionService.Explore(t.Context(), current, graphservice.ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: query, MaxFiles: 4, SourceUnits: 13000, SessionID: "frozen-six", Config: graphservice.ExploreConfig{Dedup: proto.Bool(dedup)}})
				if e != nil {
					t.Fatal(e)
				}
				emitted, referenced := map[string]bool{}, map[string]bool{}
				for _, f := range got.Files {
					for _, segment := range f.Segments {
						raw, e := os.ReadFile(filepath.Join("../../test/fixtures/codegraph/source", f.Path))
						if e != nil {
							t.Fatal(e)
						}
						want := strings.Join(strings.Split(string(raw), "\n")[segment.StartLine-1:segment.EndLine], "\n")
						if segment.Content != want || segment.IndexedSHA != a.Commit {
							t.Fatal("changed emitted source")
						}
						emitted[f.Path] = true
					}
					for _, ref := range f.References {
						if ref.Content != "" || ref.Status != "already_seen" || ref.IndexedSHA != a.Commit || ref.BlobSHA == "" {
							t.Fatalf("invalid source pointer: %+v", ref)
						}
						referenced[f.Path] = true
					}
				}
				if call > 0 && dedup {
					if !referenced["core.ts"] || emitted["core.ts"] || got.Usage.DedupSavedUnits <= 0 {
						t.Fatalf("lost frozen dedup shape: %+v", got)
					}
				} else if !emitted["core.ts"] || len(referenced) > 0 || got.Usage.DedupSavedUnits != 0 {
					t.Fatalf("first/disabled source differs: %+v", got)
				}
				if !emitted["consumer.ts"] || call != 2 && !emitted["model.swift"] {
					t.Fatalf("lost subthreshold required sources: %+v", emitted)
				}
				if call == 2 && !emitted["orphan.ts"] {
					t.Fatal("seen history displaced unseen pinned source")
				}
				data, _ := json.Marshal(got)
				if got.Usage.SourceUnits > 13000 || len(data) > 256<<10 {
					t.Fatal("session result exceeds equivalent domain budget")
				}
				t.Logf("call=%d dedup=%v emitted=%v referenced=%v units=%d saved=%d response=%d", call+1, dedup, emitted, referenced, got.Usage.SourceUnits, got.Usage.DedupSavedUnits, len(data))
			}
		})
	}
	t.Run("session_core_restore", func(t *testing.T) {
		sessionService := &graphservice.Service{Store: store, Backend: &graphquery.Service{Store: store}, Files: &repository.Service{Store: store, GitHub: gateway}}
		current := principal
		current.Subject = "restore-session"
		original, err := os.ReadFile("../../test/fixtures/codegraph/source/core.ts")
		if err != nil {
			t.Fatal(err)
		}
		var firstBlob string
		for call := 0; call < 2; call++ {
			// Match both frozen calls exactly: no added file/symbol pins or filters.
			got, e := sessionService.Explore(t.Context(), current, graphservice.ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "core.ts Service normalize", MaxFiles: 1, SourceUnits: 13000, SessionID: "frozen-two"})
			if e != nil {
				t.Fatal(e)
			}
			if got.FileLimit != 1 || got.SourceUnitsLimit != 13000 || got.Usage.SourceReads != 1 || len(got.Generations) != 1 || got.Generations[0].UploadID != publication.Upload.ID || got.Generations[0].Commit != a.Commit {
				t.Fatal("exact restore query changed bounds or generation")
			}
			served := 0
			symbols := map[string]bool{}
			for _, f := range got.Files {
				for _, entity := range f.Entities {
					if !proto.Equal(entity.Fact, facts[entity.Fact.Occurrence]) {
						t.Fatal("restore changed original occurrence")
					}
				}
				if len(f.Segments) == 0 {
					continue
				}
				served++
				if f.Path != "core.ts" || len(f.Segments) != 1 || len(f.References) != 0 {
					t.Fatalf("restore source admission/pointer: %+v", f)
				}
				segment := f.Segments[0]
				if segment.Content != string(original) || segment.StartLine != 1 || segment.EndLine != 17 || segment.IndexedSHA != a.Commit || segment.BlobSHA == "" {
					t.Fatal("restore lost original core.ts source/provenance")
				}
				if call == 0 {
					firstBlob = segment.BlobSHA
				} else if segment.BlobSHA != firstBlob {
					t.Fatal("restore changed blob")
				}
				for _, entity := range f.Entities {
					symbols[entity.Fact.Name] = true
				}
			}
			if served != 1 || !symbols["Service"] || !symbols["normalize"] || got.Usage.SourceUnits != 786 || got.Usage.SourceBytes != len(original) || got.SessionRestored != (call == 1) || got.Usage.DedupSavedUnits != 0 {
				t.Fatalf("lost exact pinned restore shape: %+v", got)
			}
			encoded, err := json.Marshal(got)
			if err != nil || len(encoded) > 256<<10 {
				t.Fatal("restore exceeded response ceiling")
			}
			t.Logf("query=core.ts Service normalize call=%d sourced=%d restored=%v units=%d bytes=%d saved=%d response=%d generation=%d sha=%s blob=%s", call+1, served, got.SessionRestored, got.Usage.SourceUnits, got.Usage.SourceBytes, got.Usage.DedupSavedUnits, len(encoded), got.Generations[0].UploadID, a.Commit, firstBlob)
		}
	})
	for _, mode := range []string{"replacement", "sha", "grant"} {
		t.Run(mode, func(t *testing.T) {
			current := principal
			current.Subject = "current-authorized-session"
			request := graphservice.ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "processGreeting", SessionID: mode}
			if _, e := service.Explore(t.Context(), current, request); e != nil {
				t.Fatal(e)
			}
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
			got, e := service.Explore(t.Context(), current, request)
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
