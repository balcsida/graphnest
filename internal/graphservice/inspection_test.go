package graphservice

import (
	"context"
	"errors"
	"fmt"
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

type inspectionBackend struct {
	fakeBackend
	entity      graphprotocol.Entity
	generation  graphprotocol.Generation
	changed     bool
	scopes      []graphprotocol.Scope
	file        *graphv2.File
	extra       []graphprotocol.Entity
	neighbors   []graphprotocol.Entity
	filesByPath map[string]*graphv2.File
}

func (b *inspectionBackend) Entities(_ context.Context, r graphprotocol.EntitiesRequest) (graphprotocol.EntitiesResponse, error) {
	b.scopes = append(b.scopes, r.Scope)
	entities := []graphprotocol.Entity{}
	if b.entity.Fact != nil {
		entities = append(entities, b.entity)
	}
	entities = append(entities, b.extra...)
	return graphprotocol.EntitiesResponse{Entities: entities, Generations: []graphprotocol.Generation{b.generation}}, nil
}
func (b *inspectionBackend) Traverse(_ context.Context, r graphprotocol.TraverseRequest) (graphprotocol.TraverseResponse, error) {
	b.scopes = append(b.scopes, r.Scope)
	return graphprotocol.TraverseResponse{Status: "ok", Entities: append([]graphprotocol.Entity{b.entity}, b.neighbors...), Generations: []graphprotocol.Generation{b.generation}}, nil
}
func (b *inspectionBackend) IndexedFiles(_ context.Context, r graphprotocol.FilesRequest) (graphprotocol.FilesResponse, error) {
	b.scopes = append(b.scopes, r.Scope)
	files := []graphprotocol.IndexedFile{}
	file := b.file
	if b.filesByPath != nil && r.Path != nil {
		file = b.filesByPath[*r.Path]
	}
	if file != nil {
		files = append(files, graphprotocol.IndexedFile{RepositoryID: 101, Fact: file})
	}
	return graphprotocol.FilesResponse{Files: files, Generations: []graphprotocol.Generation{b.generation}}, nil
}

func TestInspectionFileExtractionErrors(t *testing.T) {
	for _, mode := range []string{"file", "entity", "related_entity"} {
		for _, content := range []string{"[]", "[\n ]", "null", `{"message":"parse failed"}`, `[{"message":"parse failed","severity":"error","native":{"line":7}}]`} {
			t.Run(mode+"/"+content, func(t *testing.T) {
				s, b, _ := inspectionFixture()
				wantIncomplete := strings.Contains(content, "parse failed")
				fileErrors := &graphv2.Extension{Namespace: "codegraph.extraction-errors", Json: []byte(content)}
				b.file.Errors = fileErrors
				var got InspectionResponse
				var err error
				if mode == "file" {
					b.entity = graphprotocol.Entity{}
					got, err = s.InspectFile(t.Context(), principalFor(101), InspectFileRequest{Repo: api.GraphRepositorySelector{ID: 101}, Path: "unicode.ts"})
				} else {
					if mode == "related_entity" {
						other := b.entity
						other.ID = "related"
						other.Fact = proto.Clone(other.Fact).(*graphv2.Node)
						other.Fact.Occurrence = "related"
						other.Fact.Path = proto.String("related.ts")
						b.neighbors = []graphprotocol.Entity{other}
						b.filesByPath = map[string]*graphv2.File{"unicode.ts": {Path: "unicode.ts"}, "related.ts": {Path: "related.ts", Errors: fileErrors}}
					}
					got, err = s.InspectEntity(t.Context(), principalFor(101), InspectEntityRequest{Repo: api.GraphRepositorySelector{ID: 101}, Selector: graphprotocol.EntitySelector{Occurrence: proto.String("decl:1")}})
				}
				if err != nil || got.Complete == wantIncomplete || slices.Contains(got.Boundaries, "file_extraction_errors") != wantIncomplete {
					t.Fatalf("file extraction boundary: complete=%v boundaries=%v err=%v", got.Complete, got.Boundaries, err)
				}
				if len(got.Sources) == 0 || got.Sources[0].Status != "ok" || got.Sources[0].Content == "" {
					t.Fatalf("extraction failure discarded valid source: %+v", got.Sources)
				}
				if mode == "file" {
					if !proto.Equal(got.File.Fact.Errors, fileErrors) {
						t.Fatal("file errors changed")
					}
				} else {
					index := 0
					if mode == "related_entity" {
						index = 1
						if got.Sources[0].FileErrors != nil {
							t.Fatal("related errors attributed to root")
						}
					}
					if len(got.Sources) <= index || !proto.Equal(got.Sources[index].FileErrors, fileErrors) {
						t.Fatalf("lost original contributing-file evidence: %+v", got.Sources)
					}
				}
			})
		}
	}
}

func TestInspectionFileErrorsSurviveSourceBoundaries(t *testing.T) {
	for _, mode := range []string{"missing_range", "budget", "unreadable", "response_limit"} {
		t.Run(mode, func(t *testing.T) {
			s, b, _ := inspectionFixture()
			b.file.Errors = &graphv2.Extension{Namespace: "codegraph.extraction-errors", Json: []byte(`[{"message":"parse failed"}]`)}
			request := InspectEntityRequest{Repo: api.GraphRepositorySelector{ID: 101}, Selector: graphprotocol.EntitySelector{Occurrence: proto.String("decl:1")}}
			switch mode {
			case "missing_range":
				b.entity.Fact.Location = nil
			case "budget":
				request.SourceBytes = 1
			case "unreadable":
				s.Files = inspectionReader(func(context.Context, authn.Principal, api.ReadFileRequest, string) (api.ReadFileResponse, error) {
					return api.ReadFileResponse{}, errors.New("source unavailable")
				})
			case "response_limit":
				s.Limits.MaxResponseBytes = 16 << 10
				b.file.Errors.Json = []byte(`[{"message":"` + strings.Repeat("x", 20<<10) + `"}]`)
			}
			got, err := s.InspectEntity(t.Context(), principalFor(101), request)
			if mode == "response_limit" {
				if !errors.Is(err, graphquery.ErrQuerySize) || len(got.Sources) != 0 || len(got.Entities) != 0 {
					t.Fatalf("unbounded error evidence=%+v err=%v", got, err)
				}
				return
			}
			if err != nil || got.Complete || !slices.Contains(got.Boundaries, "file_extraction_errors") || len(got.Sources) != 1 || !proto.Equal(got.Sources[0].FileErrors, b.file.Errors) {
				t.Fatalf("lost extraction evidence for %s: %+v err=%v", mode, got, err)
			}
		})
	}
}
func (b *inspectionBackend) ValidateGenerations(_ context.Context, _ graphprotocol.Scope, _ []graphprotocol.Generation) error {
	if b.changed {
		return graphquery.ErrGenerationChanged
	}
	return nil
}

func inspectionFixture() (*Service, *inspectionBackend, *fakeRepositoryStore) {
	r := readyRepository("acme/visible")
	b := &inspectionBackend{entity: graphprotocol.Entity{RepositoryID: 101, ID: "identity", Fact: &graphv2.Node{Occurrence: "decl:1", Name: "hello", Path: proto.String("unicode.ts"), Location: &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(0), Character: proto.Int32(3)}, End: &graphv2.Position{Line: proto.Int32(0), Character: proto.Int32(8)}}}}, generation: graphprotocol.Generation{RepositoryID: 101, UploadID: 1, Commit: r.IndexedSHA}, file: &graphv2.File{Path: "unicode.ts", Size: 18}}
	store := &fakeRepositoryStore{repositories: []repository.Repository{r}}
	s := &Service{Store: store, Backend: b, Files: inspectionReader(func(_ context.Context, _ authn.Principal, req api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
		return api.ReadFileResponse{RepositoryID: req.RepositoryID, Path: req.Path, IndexedSHA: sha, BlobSHA: "blob", StartLine: 1, EndLine: 1, Content: "😀 hello\r"}, nil
	})}
	return s, b, store
}

func TestInspectionExactUTF16SourceAndSelectedScope(t *testing.T) {
	s, b, store := inspectionFixture()
	other := readyRepository("acme/legacy")
	other.ID = 2
	other.GitHubID = 202
	store.repositories = append(store.repositories, other)
	got, err := s.InspectEntity(t.Context(), principalFor(101), InspectEntityRequest{Repo: api.GraphRepositorySelector{ID: 101}, Selector: graphprotocol.EntitySelector{Occurrence: proto.String("decl:1")}})
	if err != nil || got.Status != "ok" || len(got.Sources) != 1 || got.Sources[0].Content != "😀 hello\r" || got.Sources[0].Precision != "utf16" || !got.Complete {
		t.Fatalf("inspection=%+v err=%v", got, err)
	}
	selection := got.Sources[0].Selection
	if selection == nil || got.Sources[0].Content[selection.StartByte:selection.EndByte] != "hello" {
		t.Fatalf("UTF-16 selection=%+v", selection)
	}
	for _, scope := range b.scopes {
		if len(scope.Repositories) != 1 || scope.Repositories[0].ID != 1 {
			t.Fatalf("unrelated generation=%+v", scope)
		}
	}
}

func TestInspectionDiscardsBufferedResultsAfterChange(t *testing.T) {
	for _, change := range []string{"grant", "sha", "generation", "hidden"} {
		t.Run(change, func(t *testing.T) {
			s, b, store := inspectionFixture()
			reader := s.Files
			s.Files = inspectionReader(func(ctx context.Context, p authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
				v, e := reader.ReadFileAt(ctx, p, r, sha)
				switch change {
				case "grant":
					store.repositories = nil
				case "sha":
					store.repositories[0].IndexedSHA = strings.Repeat("b", 40)
				case "generation":
					b.changed = true
				}
				return v, e
			})
			if change == "hidden" {
				b.entity.RepositoryID = 999
			}
			got, err := s.InspectEntity(t.Context(), principalFor(101), InspectEntityRequest{Repo: api.GraphRepositorySelector{ID: 101}, Selector: graphprotocol.EntitySelector{Occurrence: proto.String("decl:1")}})
			if err == nil || len(got.Sources) > 0 || len(got.Entities) > 0 || len(got.Generations) > 0 {
				t.Fatalf("leaked %s: %+v err=%v", change, got, err)
			}
		})
	}
}

func TestInspectionSourceBoundaries(t *testing.T) {
	for _, mode := range []string{"unreadable", "oversized", "missing_range", "surrogate", "budget", "truncated", "stale"} {
		t.Run(mode, func(t *testing.T) {
			s, b, _ := inspectionFixture()
			budget := 0
			switch mode {
			case "unreadable":
				s.Files = inspectionReader(func(context.Context, authn.Principal, api.ReadFileRequest, string) (api.ReadFileResponse, error) {
					return api.ReadFileResponse{}, errors.New("private backend details")
				})
			case "oversized":
				s.Files = inspectionReader(func(context.Context, authn.Principal, api.ReadFileRequest, string) (api.ReadFileResponse, error) {
					return api.ReadFileResponse{}, repository.ErrFileTooLarge
				})
			case "missing_range":
				b.entity.Fact.Location = nil
			case "surrogate":
				b.entity.Fact.Location.Start.Character = proto.Int32(1)
			case "budget":
				budget = 2
			case "truncated", "stale":
				reader := s.Files
				s.Files = inspectionReader(func(ctx context.Context, p authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
					v, e := reader.ReadFileAt(ctx, p, r, sha)
					if mode == "stale" {
						v.IndexedSHA = "wrong"
					} else {
						v.Truncated = true
					}
					return v, e
				})
			}
			got, err := s.InspectEntity(t.Context(), principalFor(101), InspectEntityRequest{Repo: api.GraphRepositorySelector{ID: 101}, Selector: graphprotocol.EntitySelector{Occurrence: proto.String("decl:1")}, SourceBytes: budget})
			if mode == "stale" {
				if err == nil || len(got.Sources) > 0 {
					t.Fatalf("stale=%+v err=%v", got, err)
				}
				return
			}
			if err != nil || got.Complete || len(got.Sources) != 1 || got.Sources[0].Status == "ok" {
				t.Fatalf("boundary %s=%+v err=%v", mode, got, err)
			}
		})
	}
}

func TestInspectionOverloadsAndFileOnly(t *testing.T) {
	s, b, _ := inspectionFixture()
	other := b.entity
	other.ID = "overload-2"
	other.Fact = proto.Clone(other.Fact).(*graphv2.Node)
	other.Fact.Occurrence = "decl:2"
	b.extra = []graphprotocol.Entity{other}
	got, err := s.InspectEntity(t.Context(), principalFor(101), InspectEntityRequest{Repo: api.GraphRepositorySelector{ID: 101}, Selector: graphprotocol.EntitySelector{Name: proto.String("hello")}})
	if err != nil || got.Status != "ambiguous" || len(got.Entities) != 2 || len(got.Sources) != 0 {
		t.Fatalf("overloads collapsed: %+v err=%v", got, err)
	}
	b.entity = graphprotocol.Entity{}
	b.extra = nil
	b.file.Generated = proto.Bool(true)
	got, err = s.InspectFile(t.Context(), principalFor(101), InspectFileRequest{Repo: api.GraphRepositorySelector{ID: 101}, Path: "unicode.ts"})
	if err != nil || got.Status != "ok" || len(got.Entities) != 0 || !got.File.Fact.GetGenerated() || got.Sources[0].Content != "😀 hello\r" || !got.Complete {
		t.Fatalf("generated file-only lost: %+v err=%v", got, err)
	}
}

func TestInspectionRetainsPartialAndVirtualEvidence(t *testing.T) {
	for _, mode := range []string{"line_only", "virtual", "ambient", "unresolved"} {
		t.Run(mode, func(t *testing.T) {
			s, b, _ := inspectionFixture()
			switch mode {
			case "line_only":
				b.entity.Fact.Location.Start.Character = nil
			case "virtual":
				b.entity.Fact.Path = nil
			case "ambient":
				b.entity.Fact.Kind = "interface"
				b.entity.Fact.Location.End = nil
			case "unresolved":
				b.generation.UnresolvedCount = 1
			}
			got, err := s.InspectEntity(t.Context(), principalFor(101), InspectEntityRequest{Repo: api.GraphRepositorySelector{ID: 101}, Selector: graphprotocol.EntitySelector{Occurrence: proto.String("decl:1")}})
			if err != nil || len(got.Entities) != 1 || got.Complete {
				t.Fatalf("lost uncertainty %s: %+v err=%v", mode, got, err)
			}
			if mode == "virtual" && got.Sources[0].Status != "virtual_entity" {
				t.Fatalf("virtual source=%+v", got.Sources)
			}
			if mode == "line_only" && (got.Sources[0].Status != "partial_range" || got.Sources[0].Content != "😀 hello\r" || got.Sources[0].Range.Start.Character != nil) {
				t.Fatalf("invented coordinates=%+v", got.Sources)
			}
		})
	}
}

func TestInspectionBoundsAndCancellation(t *testing.T) {
	s, _, _ := inspectionFixture()
	for _, request := range []InspectFileRequest{{Path: "../secret"}, {Path: "unicode.ts", StartLine: 5, EndLine: 1}, {Path: "unicode.ts", Limit: 101}, {Path: "unicode.ts", SourceBytes: 256<<10 + 1}} {
		if _, err := s.InspectFile(t.Context(), principalFor(101), request); err == nil {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
	s.Limits.MaxResponseBytes = 1
	got, err := s.InspectEntity(t.Context(), principalFor(101), InspectEntityRequest{Repo: api.GraphRepositorySelector{ID: 101}, Selector: graphprotocol.EntitySelector{Occurrence: proto.String("decl:1")}})
	if !errors.Is(err, graphquery.ErrQuerySize) || len(got.Entities) > 0 {
		t.Fatalf("response budget=%+v err=%v", got, err)
	}
	s, _, _ = inspectionFixture()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := s.Files
	s.Files = inspectionReader(func(ctx context.Context, p authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
		v, e := reader.ReadFileAt(ctx, p, r, sha)
		cancel()
		return v, e
	})
	got, err = s.InspectEntity(ctx, principalFor(101), InspectEntityRequest{Repo: api.GraphRepositorySelector{ID: 101}, Selector: graphprotocol.EntitySelector{Occurrence: proto.String("decl:1")}})
	if !errors.Is(err, context.Canceled) || len(got.Entities) > 0 {
		t.Fatalf("canceled result=%+v err=%v", got, err)
	}
}

func TestInspectionSourceReadCeiling(t *testing.T) {
	s, b, _ := inspectionFixture()
	for index := 0; index < 25; index++ {
		entity := b.entity
		entity.ID = fmt.Sprint(index)
		entity.Fact = proto.Clone(entity.Fact).(*graphv2.Node)
		entity.Fact.Occurrence = entity.ID
		b.neighbors = append(b.neighbors, entity)
	}
	reads := 0
	reader := s.Files
	s.Files = inspectionReader(func(ctx context.Context, p authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
		reads++
		return reader.ReadFileAt(ctx, p, r, sha)
	})
	got, err := s.InspectEntity(t.Context(), principalFor(101), InspectEntityRequest{Repo: api.GraphRepositorySelector{ID: 101}, Selector: graphprotocol.EntitySelector{Occurrence: proto.String("decl:1")}})
	if err != nil || reads != 20 || len(got.Sources) != 20 || got.Complete {
		t.Fatalf("read ceiling=%d result=%+v err=%v", reads, got, err)
	}
	found := false
	for _, reason := range got.Boundaries {
		found = found || reason == "source_read_limit"
	}
	if !found {
		t.Fatal("missing read limit boundary")
	}
}
