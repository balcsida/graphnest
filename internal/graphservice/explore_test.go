package graphservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
	"google.golang.org/protobuf/proto"
)

type exploreBackend struct {
	*inspectionBackend
	matches  []graphprotocol.DiscoveryMatch
	noEntry  bool
	requests []graphprotocol.DiscoverRequest
	graph    *graphprotocol.TraverseResponse
}

func (b *exploreBackend) Traverse(ctx context.Context, r graphprotocol.TraverseRequest) (graphprotocol.TraverseResponse, error) {
	if b.graph != nil {
		return *b.graph, nil
	}
	return b.inspectionBackend.Traverse(ctx, r)
}

func (b *exploreBackend) Discover(_ context.Context, r graphprotocol.DiscoverRequest) (graphprotocol.DiscoverResponse, error) {
	b.requests = append(b.requests, r)
	status, confidence := "candidates", "discovery_only"
	if b.noEntry {
		status, confidence = "no_entry_point", "low"
	}
	return graphprotocol.DiscoverResponse{Status: status, Confidence: confidence, Matches: b.matches, Generations: []graphprotocol.Generation{b.generation}}, nil
}
func (b *exploreBackend) IndexedFiles(ctx context.Context, r graphprotocol.FilesRequest) (graphprotocol.FilesResponse, error) {
	v, e := b.inspectionBackend.IndexedFiles(ctx, r)
	if r.IncludeCount {
		v.TotalFiles = proto.Int64(13)
	}
	return v, e
}
func exploreFixture() (*Service, *exploreBackend, *fakeRepositoryStore) {
	s, b, store := inspectionFixture()
	b.entity.Fact.Kind = "function"
	eb := &exploreBackend{inspectionBackend: b, matches: []graphprotocol.DiscoveryMatch{{Entity: b.entity, File: b.file, Score: 40, Pinned: true}}}
	s.Backend = eb
	return s, eb, store
}
func TestExploreAllocationOracle(t *testing.T) {
	data, err := os.ReadFile("testdata/explore-allocation-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Count      int64
		Candidates []allocationCandidate
		Budget     struct {
			MaxOutputChars  int `json:"maxOutputChars"`
			DefaultMaxFiles int `json:"defaultMaxFiles"`
		}
		Allowances map[string]int
		Cliffed    []string
		Pool       int
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		budget := exploreBudget(c.Count)
		if budget.Units != c.Budget.MaxOutputChars || budget.Files != c.Budget.DefaultMaxFiles {
			t.Fatalf("tier %d: %+v", c.Count, budget)
		}
		got := allocateExplore(c.Candidates, budget.Units, budget.Files)
		if !reflect.DeepEqual(got.Allowances, c.Allowances) || !slices.Equal(got.Cliffed, c.Cliffed) || got.Pool != c.Pool {
			t.Fatalf("allocation count=%d got=%+v want=%+v", c.Count, got, c)
		}
	}
}
func TestExploreCompositionSourceAndAuthority(t *testing.T) {
	for _, mode := range []string{"ok", "grant", "generation", "sha", "cancel", "hidden", "unreadable", "oversized", "budget"} {
		t.Run(mode, func(t *testing.T) {
			s, b, store := exploreFixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reader := s.Files
			reads := 0
			s.Files = inspectionReader(func(ctx context.Context, p authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
				reads++
				v, e := reader.ReadFileAt(ctx, p, r, sha)
				switch mode {
				case "grant":
					store.repositories = nil
				case "generation":
					b.changed = true
				case "sha":
					store.repositories[0].IndexedSHA = strings.Repeat("b", 40)
				case "cancel":
					cancel()
				case "unreadable":
					return api.ReadFileResponse{}, errors.New("private source details")
				case "oversized":
					return api.ReadFileResponse{}, repository.ErrFileTooLarge
				}
				return v, e
			})
			if mode == "hidden" {
				b.matches[0].Entity.RepositoryID = 999
			}
			r := ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "hello"}
			if mode == "budget" {
				r.SourceBytes = 2
			}
			got, err := s.Explore(ctx, principalFor(101), r)
			if slices.Contains([]string{"grant", "generation", "sha", "cancel", "hidden"}, mode) {
				if err == nil || !reflect.DeepEqual(got, ExploreResponse{}) {
					t.Fatalf("%s leaked %+v err=%v", mode, got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if reads != 1 || len(got.Files) != 1 || len(got.Discovery.Matches) != 1 || got.CorpusFiles != 13 {
				t.Fatalf("composition=%+v reads=%d", got, reads)
			}
			if mode != "ok" {
				if got.Complete || got.Files[0].Status == "ok" {
					t.Fatalf("source failure became complete: %+v", got)
				}
				return
			}
			if !got.Complete || len(got.Files[0].Segments) != 1 || got.Files[0].Segments[0].Content != "😀 hello\r" {
				t.Fatalf("verbatim source: %+v", got)
			}
			selection := got.Files[0].Selections[0]
			if selection.Selection == nil || got.Files[0].Segments[selection.Segment].Content[selection.Selection.StartByte:selection.Selection.EndByte] != "hello" || !proto.Equal(selection.Range, b.entity.Fact.Location) {
				t.Fatalf("UTF16 evidence: %+v", selection)
			}
			if len(got.Relationships) != 2 || !got.LineNumbers {
				t.Fatalf("missing graph/config: %+v", got)
			}
		})
	}
}
func TestExploreNoEntryFilePinConfiguration(t *testing.T) {
	s, b, _ := exploreFixture()
	b.noEntry = true
	b.matches = nil
	b.entity = graphprotocol.Entity{}
	b.file.Generated = proto.Bool(true)
	r := ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "the way it works", Files: []string{"unicode.ts"}, Config: ExploreConfig{LineNumbers: proto.Bool(false), Adaptive: proto.Bool(false), DiscoveryConfig: graphprotocol.DiscoveryConfig{NoMultiterm: true}}}
	got, err := s.Explore(t.Context(), principalFor(101), r)
	if err != nil || got.Discovery.Status != "no_entry_point" || got.Discovery.Confidence != "low" || len(got.Handoffs) == 0 || len(got.Files) != 1 || !got.Files[0].Pinned || len(got.Files[0].Segments) != 1 || got.LineNumbers || !b.requests[0].Config.NoMultiterm {
		t.Fatalf("pin/no-entry/config=%+v err=%v", got, err)
	}
}
func TestExploreOversizedRequiredWindow(t *testing.T) {
	s, b, _ := exploreFixture()
	content := strings.Repeat("前😀\r\n", 500) + "export function hello() { return 42; }\r\n" + strings.Repeat("後😀\r\n", 500)
	b.file.Size = int64(len(content))
	b.entity.Fact.Location = &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(500), Character: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(500), Character: proto.Int32(37)}}
	b.matches[0].Entity = b.entity
	s.Files = inspectionReader(func(_ context.Context, _ authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
		lines := strings.Split(content, "\n")
		start := max(1, r.StartLine)
		end := r.EndLine
		if end == 0 {
			end = len(lines)
		}
		end = min(end, len(lines))
		return api.ReadFileResponse{RepositoryID: r.RepositoryID, Path: r.Path, IndexedSHA: sha, BlobSHA: "blob", StartLine: start, EndLine: end, Content: strings.Join(lines[start-1:end], "\n")}, nil
	})
	got, err := s.Explore(t.Context(), principalFor(101), ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "hello", SourceUnits: 900, SourceBytes: 1800, RequiredOccurrences: []string{"decl:1"}})
	if err != nil || len(got.Files) != 1 || len(got.Files[0].Segments) == 0 {
		t.Fatalf("window=%+v err=%v", got, err)
	}
	f := got.Files[0]
	if f.Mode != "window" || !strings.Contains(f.Segments[0].Content, "export function hello()") || got.Usage.SourceBytes > 1800 || got.Usage.SourceUnits > 900 {
		t.Fatalf("lost required answer/window bounds: %+v", got)
	}
	for _, seg := range f.Segments {
		if !strings.Contains(content, seg.Content) {
			t.Fatal("synthetic bytes labeled source")
		}
	}
}
func TestExploreBounds(t *testing.T) {
	s, _, _ := exploreFixture()
	for _, r := range []ExploreRequest{{MaxFiles: 21}, {SourceUnits: 100001}, {SourceBytes: 256<<10 + 1}, {Files: []string{"../secret"}}} {
		r.Repo = api.GraphRepositorySelector{ID: 101}
		if _, err := s.Explore(t.Context(), principalFor(101), r); err == nil {
			t.Fatalf("invalid request: %+v", r)
		}
	}
	s.Limits.MaxResponseBytes = 1
	got, err := s.Explore(t.Context(), principalFor(101), ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "hello"})
	if !errors.Is(err, graphquery.ErrQuerySize) || !reflect.DeepEqual(got, ExploreResponse{}) {
		t.Fatalf("response bound=%+v err=%v", got, err)
	}
}

func TestExploreRequiredDisjointWindows(t *testing.T) {
	s, b, _ := exploreFixture()
	lines := make([]string, 4000)
	for index := range lines {
		lines[index] = "filler"
	}
	lines[100] = "first"
	lines[3100] = "second"
	first := b.entity
	first.Fact = proto.Clone(first.Fact).(*graphv2.Node)
	first.Fact.Location = &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(100), Character: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(100), Character: proto.Int32(5)}}
	second := first
	second.ID = "second"
	second.Fact = proto.Clone(first.Fact).(*graphv2.Node)
	second.Fact.Occurrence = "second"
	second.Fact.Name = "second"
	second.Fact.Location = &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(3100), Character: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(3100), Character: proto.Int32(6)}}
	b.entity = first
	b.matches = []graphprotocol.DiscoveryMatch{{Entity: first, File: b.file, Pinned: true, Score: 20}, {Entity: second, File: b.file, Pinned: true, Score: 20}}
	s.Files = inspectionReader(func(_ context.Context, _ authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
		start := max(1, r.StartLine)
		end := min(len(lines), start+999)
		return api.ReadFileResponse{RepositoryID: r.RepositoryID, Path: r.Path, IndexedSHA: sha, BlobSHA: "blob", StartLine: start, EndLine: end, Content: strings.Join(lines[start-1:end], "\n"), Truncated: end < len(lines)}, nil
	})
	got, err := s.Explore(t.Context(), principalFor(101), ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "first second", SourceUnits: 900})
	if err != nil || len(got.Files) != 1 || got.Usage.SourceReads > 20 || got.Usage.SourceUnits > 900 {
		t.Fatalf("windows: %+v %v", got, err)
	}
	for _, selection := range got.Files[0].Selections {
		if selection.Status != "ok" {
			t.Fatalf("lost required disjoint range: %+v", selection)
		}
	}
}

func TestExploreUTF16BudgetRetainsBMPBytes(t *testing.T) {
	s, b, _ := exploreFixture()
	content := strings.Repeat("界", 22000)
	b.file.Size = int64(len(content))
	b.entity = graphprotocol.Entity{}
	b.matches = nil
	b.noEntry = true
	s.Files = inspectionReader(func(_ context.Context, _ authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
		return api.ReadFileResponse{RepositoryID: r.RepositoryID, Path: r.Path, IndexedSHA: sha, BlobSHA: "blob", StartLine: 1, EndLine: 1, Content: content}, nil
	})
	got, err := s.Explore(t.Context(), principalFor(101), ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Files: []string{"unicode.ts"}, SourceUnits: 32000})
	if err != nil || got.Usage.SourceUnits != 22000 || got.Usage.SourceBytes != 66000 || len(got.Files[0].Segments) != 1 || got.Files[0].Segments[0].Content != content {
		t.Fatalf("UTF16 reservation became byte truncation: units=%d bytes=%d err=%v", got.Usage.SourceUnits, got.Usage.SourceBytes, err)
	}
}

func TestExploreAdaptiveFamily(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "adaptive", false: "full"}[enabled], func(t *testing.T) {
			s, b, _ := exploreFixture()
			b.matches[0].Entity.Fact.Name = "root"
			b.entity.Fact.Name = "root"
			graph := graphprotocol.TraverseResponse{Status: "ok", Generations: []graphprotocol.Generation{b.generation}, Entities: []graphprotocol.Entity{b.entity}}
			for index, path := range []string{"peer1.ts", "peer2.ts", "peer3.ts"} {
				e := b.entity
				e.ID = path
				e.Fact = proto.Clone(e.Fact).(*graphv2.Node)
				e.Fact.Occurrence = path
				e.Fact.Name = "Peer"
				e.Fact.Kind = "class"
				e.Fact.Path = proto.String(path)
				e.Fact.Location = &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(0), Character: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(4), Character: proto.Int32(1)}}
				b.matches = append(b.matches, graphprotocol.DiscoveryMatch{Entity: e, File: &graphv2.File{Path: path}, Score: 30})
				graph.Entities = append(graph.Entities, e)
				graph.Edges = append(graph.Edges, graphprotocol.Evidence{RepositoryID: 101, SourceID: e.ID, TargetID: "super", Fact: &graphv2.Edge{Occurrence: path, Source: path, Target: "super", Kind: graphv2.EdgeKind_EDGE_KIND_IMPLEMENTS}})
				_ = index
			}
			b.graph = &graph
			s.Files = inspectionReader(func(_ context.Context, _ authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
				body := "class Peer {\n  work() {\n    return 42;\n  }\n}"
				return api.ReadFileResponse{RepositoryID: r.RepositoryID, Path: r.Path, IndexedSHA: sha, BlobSHA: "blob", StartLine: 1, EndLine: 5, Content: body}, nil
			})
			got, err := s.Explore(t.Context(), principalFor(101), ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "root", Config: ExploreConfig{Adaptive: proto.Bool(enabled)}})
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range got.Files {
				if f.Path == "unicode.ts" {
					continue
				}
				want := "whole"
				if enabled {
					want = "skeleton"
				}
				if f.Mode != want || len(f.Segments) != 1 {
					t.Fatalf("mode %v: %+v", enabled, f)
				}
				if enabled && f.Segments[0].Content != "class Peer {" {
					t.Fatalf("synthetic skeleton: %+v", f.Segments)
				}
			}
		})
	}
}

func TestExploreReadAndPathProbeCeilings(t *testing.T) {
	for _, mode := range []string{"reads", "bytes", "paths"} {
		t.Run(mode, func(t *testing.T) {
			s, b, _ := exploreFixture()
			b.matches = nil
			for index := 0; index < 25; index++ {
				path := fmt.Sprintf("file%d.ts", index)
				e := b.entity
				e.ID = path
				e.Fact = proto.Clone(e.Fact).(*graphv2.Node)
				e.Fact.Occurrence = path
				e.Fact.Path = proto.String(path)
				b.matches = append(b.matches, graphprotocol.DiscoveryMatch{Entity: e, File: &graphv2.File{Path: path}, Score: 20})
			}
			reads := 0
			s.Files = inspectionReader(func(_ context.Context, _ authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
				reads++
				content := "😀 hello\r"
				if mode == "bytes" {
					content = strings.Repeat("x", 1<<20)
				}
				return api.ReadFileResponse{RepositoryID: r.RepositoryID, Path: r.Path, IndexedSHA: sha, BlobSHA: "blob", StartLine: 1, EndLine: 1, Content: content}, nil
			})
			r := ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "hello", MaxFiles: 20, SourceUnits: 100000}
			if mode == "paths" {
				b.matches = nil
				b.file = nil
				words := []string{}
				for index := 0; index < 200; index++ {
					words = append(words, fmt.Sprintf("unknown%d.ts", index))
				}
				r.Query = strings.Join(words, " ")
			}
			got, err := s.Explore(t.Context(), principalFor(101), r)
			if err != nil {
				t.Fatal(err)
			}
			if got.Complete {
				t.Fatal("bounded composition claimed complete")
			}
			switch mode {
			case "reads":
				if reads != 20 {
					t.Fatalf("reads=%d", reads)
				}
			case "bytes":
				if reads != 4 {
					t.Fatalf("aggregate reads=%d", reads)
				}
			case "paths":
				if got.Usage.GraphQueries > 22 || !slices.Contains(got.Boundaries, "query_path_limit") {
					t.Fatalf("path probes=%+v", got)
				}
			}
		})
	}
}

func TestExploreRedistributedReservationsFitEnvelope(t *testing.T) {
	s, b, _ := exploreFixture()
	other := b.entity
	other.ID = "other"
	other.Fact = proto.Clone(other.Fact).(*graphv2.Node)
	other.Fact.Path = proto.String("other.ts")
	other.Fact.Occurrence = "other"
	b.matches[0].Pinned = false
	b.matches = append(b.matches, graphprotocol.DiscoveryMatch{Entity: other, File: &graphv2.File{Path: "other.ts"}, Score: 40})
	s.Files = inspectionReader(func(_ context.Context, _ authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
		content := "small source"
		if r.Path == "other.ts" {
			content = strings.Repeat("x", 10000)
		}
		return api.ReadFileResponse{RepositoryID: r.RepositoryID, Path: r.Path, IndexedSHA: sha, BlobSHA: "blob", StartLine: 1, EndLine: 1, Content: content}, nil
	})
	got, err := s.Explore(t.Context(), principalFor(101), ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	reserved := 0
	for _, f := range got.Files {
		reserved += f.Reservation + 200
	}
	if reserved > got.SourceUnitsLimit {
		t.Fatalf("reported reservations overbooked: %d > %d", reserved, got.SourceUnitsLimit)
	}
}

func TestExploreGeneratedRelationshipAllocation(t *testing.T) {
	s, b, _ := exploreFixture()
	generated := b.entity
	generated.ID = "generated"
	generated.Fact = proto.Clone(generated.Fact).(*graphv2.Node)
	generated.Fact.Path = proto.String("generated.ts")
	generated.Fact.Occurrence = "generated"
	b.neighbors = []graphprotocol.Entity{generated}
	b.filesByPath = map[string]*graphv2.File{"unicode.ts": b.file, "generated.ts": {Path: "generated.ts", Generated: proto.Bool(true)}}
	got, err := s.Explore(t.Context(), principalFor(101), ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range got.Files {
		if f.Path == "generated.ts" && (f.Status != "allocation_cliff" || len(f.Segments) > 0 || f.Fact == nil || !f.Fact.GetGenerated()) {
			t.Fatalf("generated relationship consumed answer reservation: %+v", f)
		}
	}
}

func TestExploreReadWindowDoesNotClaimWholeFile(t *testing.T) {
	s, b, _ := exploreFixture()
	b.matches = nil
	b.entity = graphprotocol.Entity{}
	b.file.Size = 10000
	s.Files = inspectionReader(func(_ context.Context, _ authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
		end := r.EndLine
		if end == 0 {
			end = 2000
		}
		truncated := end-r.StartLine+1 > 1000
		end = min(end, r.StartLine+999)
		return api.ReadFileResponse{RepositoryID: r.RepositoryID, Path: r.Path, IndexedSHA: sha, BlobSHA: "blob", StartLine: r.StartLine, EndLine: end, Content: strings.TrimSuffix(strings.Repeat("line\n", end-r.StartLine+1), "\n"), Truncated: truncated}, nil
	})
	got, err := s.Explore(t.Context(), principalFor(101), ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Files: []string{"unicode.ts"}})
	if err != nil || len(got.Files) != 1 || got.Files[0].Mode != "window" || got.Complete {
		t.Fatalf("partial file claimed whole: %+v err=%v", got, err)
	}
}

func TestExploreRequiredWindowPlanning(t *testing.T) {
	for _, mode := range []string{"unequal", "overlap", "small_whole", "small_bmp_whole"} {
		t.Run(mode, func(t *testing.T) {
			s, b, _ := exploreFixture()
			lines := make([]string, 4000)
			for i := range lines {
				lines[i] = "filler"
			}
			first := b.entity
			first.Fact = proto.Clone(first.Fact).(*graphv2.Node)
			first.Fact.Location = &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(100), Character: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(100), Character: proto.Int32(5)}}
			lines[100] = "first"
			second := first
			second.ID = "second"
			second.Fact = proto.Clone(first.Fact).(*graphv2.Node)
			second.Fact.Occurrence = "second"
			second.Fact.Name = "second"
			second.Fact.Location = &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(3100), Character: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(3100), Character: proto.Int32(6)}}
			lines[3100] = "second"
			units := 13000
			if mode == "unequal" {
				units = 1000
				for n := 100; n <= 149; n++ {
					lines[n] = "aaaaaaaaaa"
				}
				first.Fact.Location.End.Line = proto.Int32(149)
				first.Fact.Location.End.Character = proto.Int32(10)
			}
			if mode == "overlap" {
				second.Fact.Location.Start.Line = proto.Int32(1090)
				second.Fact.Location.End.Line = proto.Int32(1110)
			}
			if strings.HasPrefix(mode, "small_") {
				lines = lines[:20]
				if mode == "small_bmp_whole" {
					units = 1000
					for at := range lines {
						lines[at] = strings.Repeat("界", 20)
					}
				}
				first.Fact.Location.Start.Line = proto.Int32(10)
				first.Fact.Location.End.Line = proto.Int32(10)
			}
			b.entity = first
			b.file.Size = int64(len(strings.Join(lines, "\n")))
			b.matches = []graphprotocol.DiscoveryMatch{{Entity: first, File: b.file, Pinned: true, Score: 20}}
			if !strings.HasPrefix(mode, "small_") {
				b.matches = append(b.matches, graphprotocol.DiscoveryMatch{Entity: second, File: b.file, Pinned: true, Score: 20})
			}
			s.Files = inspectionReader(func(_ context.Context, _ authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
				start := max(1, r.StartLine)
				end := min(len(lines), start+999)
				return api.ReadFileResponse{RepositoryID: r.RepositoryID, Path: r.Path, IndexedSHA: sha, BlobSHA: "blob", StartLine: start, EndLine: end, Content: strings.Join(lines[start-1:end], "\n"), Truncated: end < len(lines)}, nil
			})
			got, err := s.Explore(t.Context(), principalFor(101), ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "first second", SourceUnits: units})
			if err != nil {
				t.Fatal(err)
			}
			f := got.Files[0]
			if got.Usage.SourceUnits > f.Reservation || got.Usage.SourceUnits > units {
				t.Fatal("window allocation exceeded")
			}
			for at, segment := range f.Segments {
				if segment.Content != strings.Join(lines[segment.StartLine-1:segment.EndLine], "\n") {
					t.Fatal("window source changed")
				}
				if at > 0 && f.Segments[at-1].EndLine >= segment.StartLine {
					t.Fatal("overlapping buffers duplicated source")
				}
			}
			t.Logf("mode=%s reads=%d reservation=%d units=%d mode=%s selection=%+v", mode, got.Usage.SourceReads, f.Reservation, got.Usage.SourceUnits, f.Mode, f.Selections)
			if strings.HasPrefix(mode, "small_") {
				if f.Mode != "whole" || len(f.Segments) != 1 || f.Segments[0].Content != strings.Join(lines, "\n") {
					t.Fatal("small affordable file lost its prefix")
				}
				return
			}
			for _, sel := range f.Selections {
				if sel.Status != "ok" {
					t.Errorf("required body lost despite fitting total grant: %s %s", sel.EntityID, sel.Status)
				}
			}
		})
	}
}

func TestExploreAdaptiveRequiredFile(t *testing.T) {
	for _, family := range []bool{true, false} {
		for _, adaptive := range []bool{true, false} {
			t.Run(fmt.Sprintf("family=%v/adaptive=%v", family, adaptive), func(t *testing.T) {
				s, b, _ := exploreFixture()
				lines := []string{"class Base {", "  entry() {", "    return 'REQUIRED';", "  }", "  other() {"}
				for n := 0; n < 100; n++ {
					lines = append(lines, "    // optional implementation detail")
				}
				lines = append(lines, "  }", "}", "class One extends Base {}", "class Two extends Base {}", "class Three extends Base {}")
				entity := func(id, name, kind string, start, end int) graphprotocol.Entity {
					e := b.entity
					e.ID = id
					e.Fact = proto.Clone(e.Fact).(*graphv2.Node)
					e.Fact.Occurrence = id
					e.Fact.Name = name
					e.Fact.Kind = kind
					e.Fact.Location = &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(int32(start)), Character: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(int32(end)), Character: proto.Int32(int32(len(lines[end])))}}
					return e
				}
				root := entity("entry", "entry", "method", 1, 3)
				other := entity("other", "other", "method", 4, 105)
				base := entity("base", "Base", "class", 0, 106)
				graph := graphprotocol.TraverseResponse{Status: "ok", Generations: []graphprotocol.Generation{b.generation}, Entities: []graphprotocol.Entity{root, other, base}}
				if family {
					for n, name := range []string{"One", "Two", "Three"} {
						e := entity(name, name, "class", 107+n, 107+n)
						graph.Entities = append(graph.Entities, e)
						graph.Edges = append(graph.Edges, graphprotocol.Evidence{RepositoryID: 101, SourceID: e.ID, TargetID: base.ID, Fact: &graphv2.Edge{Occurrence: name, Source: name, Target: base.ID, Kind: graphv2.EdgeKind_EDGE_KIND_EXTENDS}})
					}
				}
				b.graph = &graph
				b.entity = root
				b.file.Size = int64(len(strings.Join(lines, "\n")))
				b.matches = []graphprotocol.DiscoveryMatch{{Entity: root, File: b.file, Pinned: true, Score: 40}}
				units := 13000
				if !family {
					units = 1200
					b.matches = append([]graphprotocol.DiscoveryMatch{{Entity: other, File: b.file, Pinned: true, Score: 40}}, b.matches...)
				}
				s.Files = inspectionReader(func(_ context.Context, _ authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
					start := max(1, r.StartLine)
					return api.ReadFileResponse{RepositoryID: r.RepositoryID, Path: r.Path, IndexedSHA: sha, BlobSHA: "blob", StartLine: start, EndLine: len(lines), Content: strings.Join(lines[start-1:], "\n")}, nil
				})
				got, err := s.Explore(t.Context(), principalFor(101), ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "entry other", RequiredOccurrences: []string{"entry"}, SourceUnits: units, Config: ExploreConfig{Adaptive: proto.Bool(adaptive)}})
				if err != nil {
					t.Fatal(err)
				}
				f := got.Files[0]
				want := "whole"
				if !family {
					want = "window"
				}
				if adaptive {
					want = "focused"
				}
				if f.Mode != want {
					t.Errorf("mode=%s want=%s", f.Mode, want)
				}
				for _, sel := range f.Selections {
					if sel.EntityID == root.ID && sel.Status != "ok" {
						t.Errorf("required body lost: %+v", sel)
					}
				}
				content := ""
				for _, seg := range f.Segments {
					content += seg.Content
					if !strings.Contains(strings.Join(lines, "\n"), seg.Content) {
						t.Fatal("synthetic source")
					}
				}
				if adaptive && strings.Contains(content, "optional implementation detail") {
					t.Error("off-path bodies consumed adaptive allocation")
				}
				if got.Usage.SourceUnits > f.Reservation || got.Usage.SourceUnits > units {
					t.Fatal("allocation exceeded")
				}
				t.Logf("reservation=%d units=%d mode=%s", f.Reservation, got.Usage.SourceUnits, f.Mode)
			})
		}
	}
}

// These source/range inputs and allowances are from actual pinned ToolHandler
// adaptive on/off answers. The domain preserves bytes instead of rendered fences.
func TestExploreAdaptivePinnedSourceOracle(t *testing.T) {
	data, err := os.ReadFile("testdata/explore-adaptive-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Path, Source, Marker, OptionalMarker string
		Allowance                            int
		Required, Exact                      []string
		Nodes                                []struct {
			ID, Name, Kind                             string
			StartLine, EndLine, StartColumn, EndColumn int
		}
		Edges []struct{ Source, Target, Kind string }
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		for _, enabled := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/adaptive=%v", c.Path, enabled), func(t *testing.T) {
				f := ExploreFile{Path: c.Path, Required: true, Reservation: c.Allowance}
				required := map[string]bool{}
				for _, id := range c.Required {
					required[id] = true
				}
				for _, n := range c.Nodes {
					f.Entities = append(f.Entities, graphprotocol.Entity{ID: n.ID, Fact: &graphv2.Node{Occurrence: n.ID, Name: n.Name, Kind: n.Kind, Location: &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(int32(n.StartLine - 1)), Character: proto.Int32(int32(n.StartColumn))}, End: &graphv2.Position{Line: proto.Int32(int32(n.EndLine - 1)), Character: proto.Int32(int32(n.EndColumn))}}}})
				}
				graph := graphprotocol.TraverseResponse{}
				for _, e := range c.Edges {
					kind := graphv2.EdgeKind_EDGE_KIND_EXTENDS
					if e.Kind == "implements" {
						kind = graphv2.EdgeKind_EDGE_KIND_IMPLEMENTS
					}
					graph.Edges = append(graph.Edges, graphprotocol.Evidence{SourceID: e.Source, TargetID: e.Target, Fact: &graphv2.Edge{Kind: kind}})
				}
				sources := []InspectionSource{{Path: c.Path, StartLine: 1, EndLine: strings.Count(c.Source, "\n") + 1, Content: c.Source, Status: "ok"}}
				adaptive, exact := exploreAdaptive(f, []graphprotocol.TraverseResponse{graph}, sources, required, c.Exact)
				if !adaptive {
					t.Fatal("applicable pinned adaptive branch was not selected")
				}
				segments := exploreWindows(sources, f.Entities, required, exact, c.Allowance, 256<<10, enabled && adaptive)
				units := 0
				content := ""
				for _, seg := range segments {
					units += sourceUnits(seg.Content)
					content += seg.Content
					if seg.Content != strings.Join(strings.Split(c.Source, "\n")[seg.StartLine-1:seg.EndLine], "\n") {
						t.Fatal("source bytes or line bounds changed")
					}
				}
				if units > c.Allowance || !strings.Contains(content, c.Marker) {
					t.Fatalf("required answer or grant: units=%d allowance=%d", units, c.Allowance)
				}
				if enabled && strings.Contains(content, c.OptionalMarker) {
					t.Fatal("adaptive retained off-path implementation")
				}
				if !enabled && !strings.Contains(content, c.OptionalMarker) {
					t.Fatal("off configuration did not restore original source")
				}
				for _, e := range f.Entities {
					if (c.Path == "family.ts" && required[e.ID]) || exact[e.ID] {
						if selection := exploreSelection(e, segments); selection.Status != "ok" {
							t.Fatalf("required original range lost: %+v", selection)
						}
					}
				}
				t.Logf("allowance=%d units=%d segments=%d required=%s", c.Allowance, units, len(segments), c.Marker)
			})
		}
	}
}
