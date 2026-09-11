package graphservice

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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

type taskContextTestBackend struct {
	inspectionBackend
	relevant         graphprotocol.RelevantContextResponse
	relevantRequests []graphprotocol.RelevantContextRequest
	relevantErr      error
	segmentErr       error
	segmentCalls     int
	afterRelevant    func()
}

func newTaskContextInspectionBackend() *inspectionBackend {
	repository := readyRepository("acme/visible")
	return &inspectionBackend{
		entity:     graphprotocol.Entity{RepositoryID: 101, ID: "identity", Fact: &graphv2.Node{SourceId: "source", Occurrence: "decl:1", Name: "value", Kind: "function", Path: proto.String("source.go")}},
		generation: graphprotocol.Generation{RepositoryID: 101, UploadID: 1, Commit: repository.IndexedSHA, Producer: &graphv2.Producer{Name: "codegraph"}},
	}
}

func (b *taskContextTestBackend) RelevantContext(_ context.Context, request graphprotocol.RelevantContextRequest) (graphprotocol.RelevantContextResponse, error) {
	b.relevantRequests = append(b.relevantRequests, request)
	if b.afterRelevant != nil {
		b.afterRelevant()
	}
	return b.relevant, b.relevantErr
}

func (b *taskContextTestBackend) SegmentMatches(_ context.Context, request graphprotocol.SegmentRequest) (graphprotocol.SegmentResponse, error) {
	b.segmentCalls++
	if b.segmentErr != nil {
		return graphprotocol.SegmentResponse{}, b.segmentErr
	}
	match := b.entity
	match.Fact = proto.Clone(match.Fact).(*graphv2.Node)
	match.Fact.Name = "processGreeting"
	return graphprotocol.SegmentResponse{Matches: []graphprotocol.SegmentMatch{{Entity: match}}, Generations: []graphprotocol.Generation{b.generation}}, nil
}

func TestBuildTaskContextOptionsExposeOnlyBuilderSurface(t *testing.T) {
	typeOf := reflect.TypeOf(BuildTaskContextOptions{})
	want := []string{"SearchLimit", "TraversalDepth", "MaxNodes", "MinScore", "MaxCodeBlocks", "MaxCodeBlockSize", "IncludeCode"}
	if typeOf.NumField() != len(want) {
		t.Fatalf("builder option fields=%d, want %d", typeOf.NumField(), len(want))
	}
	for index, name := range want {
		if field := typeOf.Field(index); field.Anonymous || field.Name != name {
			t.Fatalf("builder option field %d=%+v, want %s", index, field, name)
		}
	}

	base := newTaskContextInspectionBackend()
	backend := &taskContextTestBackend{inspectionBackend: *base, relevant: graphprotocol.RelevantContextResponse{Status: graphprotocol.StatusNotFound, Generations: []graphprotocol.Generation{base.generation}}}
	service := &Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("acme/visible")}}, Backend: backend}
	search, depth, nodes, score, include := 4, 2, 9, 0.7, false
	_, err := service.BuildTaskContext(t.Context(), principalFor(101), BuildTaskContextRequest{
		Repo: api.GraphRepositorySelector{ID: 101}, Query: "context",
		Options: BuildTaskContextOptions{SearchLimit: &search, TraversalDepth: &depth, MaxNodes: &nodes, MinScore: &score, IncludeCode: &include},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(backend.relevantRequests) != 1 {
		t.Fatalf("relevant requests=%d", len(backend.relevantRequests))
	}
	got := backend.relevantRequests[0].Options
	if got.SearchLimit != &search || got.TraversalDepth != &depth || got.MaxNodes != &nodes || got.MinScore != &score || got.EdgeKinds != nil || got.NodeKinds != nil || got.SeedNames != nil {
		t.Fatalf("builder option mapping changed: %+v", got)
	}
}

func TestFindRelevantContextDerivesSeedsUnlessExplicitlyEmpty(t *testing.T) {
	base := newTaskContextInspectionBackend()
	backend := &taskContextTestBackend{inspectionBackend: *base, relevant: graphprotocol.RelevantContextResponse{Status: graphprotocol.StatusNotFound, Generations: []graphprotocol.Generation{base.generation}}}
	service := &Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("acme/visible")}}, Backend: backend}

	if _, err := service.FindRelevantContext(t.Context(), principalFor(101), FindRelevantContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "process greeting"}); err != nil {
		t.Fatal(err)
	}
	if backend.segmentCalls != 1 || len(backend.relevantRequests) != 1 || backend.relevantRequests[0].Options.SeedNames == nil || len(*backend.relevantRequests[0].Options.SeedNames) != 1 || (*backend.relevantRequests[0].Options.SeedNames)[0] != "processGreeting" {
		t.Fatalf("derived seed missing: calls=%d requests=%+v", backend.segmentCalls, backend.relevantRequests)
	}

	empty := []string{}
	if _, err := service.FindRelevantContext(t.Context(), principalFor(101), FindRelevantContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "process greeting", Options: graphprotocol.RelevantContextOptions{SeedNames: &empty}}); err != nil {
		t.Fatal(err)
	}
	if backend.segmentCalls != 1 || backend.relevantRequests[1].Options.SeedNames == nil || len(*backend.relevantRequests[1].Options.SeedNames) != 0 {
		t.Fatalf("explicit empty seed list changed: calls=%d request=%+v", backend.segmentCalls, backend.relevantRequests[1])
	}
}

func TestFindRelevantContextTreatsSeedAvailabilityFailureAsEmpty(t *testing.T) {
	base := newTaskContextInspectionBackend()
	backend := &taskContextTestBackend{
		inspectionBackend: *base,
		segmentErr:        errors.New("segment projection unavailable"),
		relevant:          graphprotocol.RelevantContextResponse{Status: graphprotocol.StatusNotFound, Generations: []graphprotocol.Generation{base.generation}},
	}
	service := &Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("acme/visible")}}, Backend: backend}

	if _, err := service.FindRelevantContext(t.Context(), principalFor(101), FindRelevantContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "process greeting"}); err != nil {
		t.Fatal(err)
	}
	if len(backend.relevantRequests) != 1 || backend.relevantRequests[0].Options.SeedNames == nil || len(*backend.relevantRequests[0].Options.SeedNames) != 0 {
		t.Fatalf("availability fallback did not pass an explicit empty seed list: %+v", backend.relevantRequests)
	}
}

func TestFindRelevantContextPreservesSeedIntegrityErrors(t *testing.T) {
	for _, want := range []error{context.Canceled, graphquery.ErrGenerationChanged, ErrGraphNotReady} {
		t.Run(want.Error(), func(t *testing.T) {
			base := newTaskContextInspectionBackend()
			backend := &taskContextTestBackend{inspectionBackend: *base, segmentErr: want}
			service := &Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("acme/visible")}}, Backend: backend}
			_, err := service.FindRelevantContext(t.Context(), principalFor(101), FindRelevantContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "process greeting"})
			if !errors.Is(err, want) || len(backend.relevantRequests) != 0 {
				t.Fatalf("seed integrity error=%v requests=%d, want %v and no request", err, len(backend.relevantRequests), want)
			}
		})
	}
}

func TestFindRelevantContextRejectsHiddenRepositoryBeforeSeeds(t *testing.T) {
	base := newTaskContextInspectionBackend()
	backend := &taskContextTestBackend{inspectionBackend: *base, segmentErr: errors.New("must not be observed")}
	service := &Service{Store: &fakeRepositoryStore{}, Backend: backend}
	_, err := service.FindRelevantContext(t.Context(), principalFor(101), FindRelevantContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "process greeting"})
	if !errors.Is(err, ErrRepositoryNotFound) || backend.segmentCalls != 0 || len(backend.relevantRequests) != 0 {
		t.Fatalf("hidden repository error=%v segment=%d relevant=%d", err, backend.segmentCalls, len(backend.relevantRequests))
	}
}

func TestBuildTaskContextPreservesUTF16SourceAndReportsTruncation(t *testing.T) {
	base := newTaskContextInspectionBackend()
	base.entity.Fact.Path = proto.String("unicode.ts")
	base.entity.Fact.Language = "typescript"
	base.entity.Fact.Kind = "function"
	base.entity.Fact.Name = "unicodeCaller"
	base.entity.Fact.Location = &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(0), Character: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(0), Character: proto.Int32(6)}}
	base.filesByPath = map[string]*graphv2.File{"unicode.ts": {Path: "unicode.ts"}}
	backend := &taskContextTestBackend{inspectionBackend: *base}
	backend.relevant = graphprotocol.RelevantContextResponse{
		Status: graphprotocol.StatusOK, Confidence: "high", Nodes: []graphprotocol.Entity{base.entity}, Roots: []string{base.entity.ID},
		EntryPoints: []graphprotocol.DiscoveryMatch{{Entity: base.entity, Score: 1}}, Generations: []graphprotocol.Generation{base.generation},
	}
	reads := 0
	service := &Service{
		Store: &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("acme/visible")}}, Backend: backend,
		Files: inspectionReader(func(_ context.Context, _ authn.Principal, request api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
			reads++
			return api.ReadFileResponse{RepositoryID: request.RepositoryID, Path: request.Path, IndexedSHA: sha, BlobSHA: "blob", StartLine: 1, EndLine: 1, Content: "ab😀cd"}, nil
		}),
	}
	one, four := 1, 4
	got, err := service.BuildTaskContext(t.Context(), principalFor(101), BuildTaskContextRequest{
		Repo: api.GraphRepositorySelector{ID: 101}, Query: "unicodeCaller",
		Options: BuildTaskContextOptions{MaxCodeBlocks: &one, MaxCodeBlockSize: &four},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reads != 1 || len(got.CodeBlocks) != 1 || got.CodeBlocks[0].Content != "ab😀" || got.CodeBlocks[0].ReturnedUTF16Units != 4 || got.CodeBlocks[0].OriginalUTF16Units == nil || *got.CodeBlocks[0].OriginalUTF16Units != 6 || !got.CodeBlocks[0].Truncated || got.CodeBlocks[0].Complete || got.Complete {
		t.Fatalf("UTF-16 source truncation changed: reads=%d result=%+v", reads, got)
	}
	if got.CodeBlocks[0].IndexedSHA != base.generation.Commit || got.CodeBlocks[0].BlobSHA != "blob" || !proto.Equal(got.CodeBlocks[0].Range, base.entity.Fact.Location) {
		t.Fatalf("source provenance changed: %+v", got.CodeBlocks[0])
	}
	if got.Stats.NodeCount != 1 || got.Stats.FileCount != 1 || got.Stats.CodeBlockCount != 1 || got.Stats.TotalCodeUTF16Units != 4 {
		t.Fatalf("wrong task stats: %+v", got.Stats)
	}
}

func TestBuildTaskContextReaderTruncationHasUnknownOriginalSize(t *testing.T) {
	base := newTaskContextInspectionBackend()
	base.entity.Fact.Path = proto.String("source.go")
	base.entity.Fact.Location = &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(0), Character: proto.Int32(3)}}
	base.filesByPath = map[string]*graphv2.File{"source.go": {Path: "source.go"}}
	backend := &taskContextTestBackend{inspectionBackend: *base, relevant: graphprotocol.RelevantContextResponse{
		Status: graphprotocol.StatusOK, Nodes: []graphprotocol.Entity{base.entity}, Roots: []string{base.entity.ID}, Generations: []graphprotocol.Generation{base.generation},
	}}
	service := &Service{
		Store:   &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("acme/visible")}},
		Backend: backend,
		Files: inspectionReader(func(_ context.Context, _ authn.Principal, request api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
			return api.ReadFileResponse{RepositoryID: request.RepositoryID, Path: request.Path, IndexedSHA: sha, StartLine: 1, EndLine: 1, Content: "abc", Truncated: true}, nil
		}),
	}
	got, err := service.BuildTaskContext(t.Context(), principalFor(101), BuildTaskContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "source"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CodeBlocks) != 1 || !got.CodeBlocks[0].Truncated || got.CodeBlocks[0].Complete || got.Complete {
		t.Fatalf("reader truncation was not propagated: %+v", got)
	}
	raw, err := json.Marshal(got.CodeBlocks[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["original_utf16_units"]; ok {
		t.Fatalf("reader truncation claimed a known original size: %s", raw)
	}
}

func TestBuildTaskContextResponseBudgetHasUnknownOriginalSize(t *testing.T) {
	base := newTaskContextInspectionBackend()
	base.entity.Fact.Path = proto.String("source.go")
	base.entity.Fact.Location = &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(0), Character: proto.Int32(20000)}}
	base.filesByPath = map[string]*graphv2.File{"source.go": {Path: "source.go"}}
	backend := &taskContextTestBackend{inspectionBackend: *base, relevant: graphprotocol.RelevantContextResponse{
		Status: graphprotocol.StatusOK, Nodes: []graphprotocol.Entity{base.entity}, Roots: []string{base.entity.ID}, Generations: []graphprotocol.Generation{base.generation},
	}}
	service := &Service{
		Store:   &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("acme/visible")}},
		Backend: backend,
		Limits:  Limits{MaxResponseBytes: 16 << 10},
		Files: inspectionReader(func(_ context.Context, _ authn.Principal, request api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
			return api.ReadFileResponse{RepositoryID: request.RepositoryID, Path: request.Path, IndexedSHA: sha, StartLine: 1, EndLine: 1, Content: strings.Repeat("x", 20000)}, nil
		}),
	}
	got, err := service.BuildTaskContext(t.Context(), principalFor(101), BuildTaskContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "source"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CodeBlocks) != 1 || got.CodeBlocks[0].Status != "budget_exhausted" || !got.CodeBlocks[0].Truncated || got.CodeBlocks[0].Complete || got.Complete {
		t.Fatalf("response budget truncation was not propagated: %+v", got)
	}
	raw, err := json.Marshal(got.CodeBlocks[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "original_utf16_units") {
		t.Fatalf("response budget claimed a known original size: %s", raw)
	}
}

func TestBuildTaskContextMissingSourceIsPartial(t *testing.T) {
	base := newTaskContextInspectionBackend()
	base.entity.Fact.Path = proto.String("missing.ts")
	base.entity.Fact.Kind = "function"
	base.entity.Fact.Location = &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(0)}}
	backend := &taskContextTestBackend{inspectionBackend: *base, relevant: graphprotocol.RelevantContextResponse{Status: graphprotocol.StatusOK, Nodes: []graphprotocol.Entity{base.entity}, Roots: []string{base.entity.ID}, Generations: []graphprotocol.Generation{base.generation}}}
	service := &Service{
		Store: &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("acme/visible")}}, Backend: backend,
		Files: inspectionReader(func(context.Context, authn.Principal, api.ReadFileRequest, string) (api.ReadFileResponse, error) {
			return api.ReadFileResponse{}, errors.New("missing")
		}),
	}
	got, err := service.BuildTaskContext(t.Context(), principalFor(101), BuildTaskContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Complete || len(got.CodeBlocks) != 1 || got.CodeBlocks[0].Status != "file_not_indexed" || got.CodeBlocks[0].Complete {
		t.Fatalf("missing source became complete: %+v", got)
	}
}

func TestTaskContextUTF16LimitDoesNotSplitSurrogatePair(t *testing.T) {
	content, returned, original, truncated := truncateUTF16("abc😀z", 4)
	if content != "abc" || returned != 3 || original != 6 || !truncated {
		t.Fatalf("split UTF-16 pair: content=%q returned=%d original=%d truncated=%v", content, returned, original, truncated)
	}
}

func TestTaskContextUTF16LimitPreservesCRLFPrefix(t *testing.T) {
	content, returned, original, truncated := truncateUTF16("😀\r\nz", 3)
	if content != "😀\r" || returned != 3 || original != 5 || !truncated {
		t.Fatalf("CRLF/surrogate prefix changed: content=%q returned=%d original=%d truncated=%v", content, returned, original, truncated)
	}
}

func TestTaskContextRevalidatesFinalAuthority(t *testing.T) {
	for _, mode := range []string{"same_sha_replacement", "sha", "grant"} {
		t.Run(mode, func(t *testing.T) {
			base := newTaskContextInspectionBackend()
			backend := &taskContextTestBackend{inspectionBackend: *base, relevant: graphprotocol.RelevantContextResponse{Status: graphprotocol.StatusNotFound, Generations: []graphprotocol.Generation{base.generation}}}
			store := &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("acme/visible")}}
			backend.afterRelevant = func() {
				switch mode {
				case "same_sha_replacement":
					backend.changed = true
				case "sha":
					store.repositories[0].IndexedSHA = strings.Repeat("b", 40)
				case "grant":
					store.repositories = nil
				}
			}
			service := &Service{Store: store, Backend: backend}
			if _, err := service.FindRelevantContext(t.Context(), principalFor(101), FindRelevantContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "context"}); err == nil {
				t.Fatal("final authority change was accepted")
			}
		})
	}
}

func TestBuildTaskContextRevalidatesAfterSource(t *testing.T) {
	base := newTaskContextInspectionBackend()
	base.entity.Fact.Path = proto.String("source.go")
	base.entity.Fact.Location = &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(0)}}
	base.filesByPath = map[string]*graphv2.File{"source.go": {Path: "source.go"}}
	backend := &taskContextTestBackend{inspectionBackend: *base, relevant: graphprotocol.RelevantContextResponse{Status: graphprotocol.StatusOK, Nodes: []graphprotocol.Entity{base.entity}, Roots: []string{base.entity.ID}, Generations: []graphprotocol.Generation{base.generation}}}
	store := &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("acme/visible")}}
	service := &Service{Store: store, Backend: backend, Files: inspectionReader(func(_ context.Context, _ authn.Principal, request api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
		store.repositories[0].IndexedSHA = strings.Repeat("b", 40)
		return api.ReadFileResponse{RepositoryID: request.RepositoryID, Path: request.Path, IndexedSHA: sha, BlobSHA: "blob", StartLine: 1, EndLine: 1, Content: "package source"}, nil
	})}
	if _, err := service.BuildTaskContext(t.Context(), principalFor(101), BuildTaskContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "context"}); err == nil {
		t.Fatal("authority change after source was accepted")
	}
}
