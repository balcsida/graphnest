package graphquery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/balcsida/graphnest/internal/graphprotocol"
)

type classificationTestStore struct {
	entityTestStore
	query                   FileClassificationQuery
	rows                    []FileClassificationRow
	generated, total, calls int64
	afterClassification     func()
	afterCount              func()
}

func (s *classificationTestStore) ClassifyFiles(_ context.Context, q FileClassificationQuery) ([]FileClassificationRow, error) {
	s.query = q
	s.calls++
	if s.afterClassification != nil {
		s.afterClassification()
	}
	return append([]FileClassificationRow(nil), s.rows...), nil
}

func (s *classificationTestStore) CountGeneratedFiles(context.Context, []QuerySnapshot) (int64, int64, error) {
	if s.afterCount != nil {
		s.afterCount()
	}
	return s.generated, s.total, nil
}

func TestGeneratedFilenamePinnedOracle(t *testing.T) {
	data, err := os.ReadFile("../../test/fixtures/codegraph/file-classification.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Reference string `json:"reference"`
		Probes    []struct {
			Pattern   string `json:"pattern"`
			Path      string `json:"path"`
			Generated bool   `json:"generated"`
		} `json:"filenameProbes"`
	}
	if err = json.Unmarshal(data, &oracle); err != nil {
		t.Fatal(err)
	}
	if oracle.Reference != "b9ca4b7981116909900368cc1686a1074cd4d4c1" || len(oracle.Probes) != 52 {
		t.Fatal("wrong generated filename oracle")
	}
	patterns := map[string]bool{}
	for _, probe := range oracle.Probes {
		patterns[probe.Pattern] = true
		if got := GeneratedFilename(probe.Path); got != probe.Generated {
			t.Fatalf("%s path %q generated=%t want=%t", probe.Pattern, probe.Path, got, probe.Generated)
		}
	}
	if len(patterns) != 26 {
		t.Fatalf("covered patterns=%d want=26", len(patterns))
	}
}

func TestFileClassificationsRetainProducerFlagsAndFilenameFallback(t *testing.T) {
	persistedTrue, persistedFalse := true, false
	s := &classificationTestStore{rows: []FileClassificationRow{
		{RepositoryID: 1, Path: "banner.ts", Present: true, PersistedGenerated: &persistedTrue},
		{RepositoryID: 1, Path: "outside/contract.pb.go"},
		{RepositoryID: 1, Path: "ordinary.ts", Present: true, PersistedGenerated: &persistedFalse, Ambient: true},
		{RepositoryID: 1, Path: "absent-flag.ts", Present: true},
	}}
	got, err := (&Service{Store: s}).FileClassifications(t.Context(), graphprotocol.FileClassificationRequest{
		Scope: entityTestScope(),
		Paths: []string{"banner.ts", "outside/contract.pb.go", "ordinary.ts", "absent-flag.ts", "ordinary.ts"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.query.Paths, []string{"banner.ts", "outside/contract.pb.go", "ordinary.ts", "absent-flag.ts"}) || len(got.Files) != 4 {
		t.Fatalf("deduped query=%v result=%+v", s.query.Paths, got.Files)
	}
	want := []graphprotocol.FileClassification{
		{Path: "banner.ts", Present: true, PersistedGenerated: &persistedTrue, Generated: true},
		{Path: "outside/contract.pb.go", Generated: true},
		{Path: "ordinary.ts", Present: true, PersistedGenerated: &persistedFalse, Ambient: true},
		{Path: "absent-flag.ts", Present: true},
	}
	if !reflect.DeepEqual(got.Files, want) || len(got.Generations) != 1 || got.Generations[0].RepositoryID != 101 {
		t.Fatalf("classifications=%+v generations=%+v", got.Files, got.Generations)
	}
}

func TestFileClassificationBoundsAndFreshness(t *testing.T) {
	tooMany := make([]string, 65)
	for i := range tooMany {
		tooMany[i] = string(rune('a'+i%26)) + "/file.ts"
	}
	for _, paths := range [][]string{tooMany, {"../secret"}, {"bad\x00path"}} {
		s := &classificationTestStore{}
		if _, err := (&Service{Store: s}).FileClassifications(t.Context(), graphprotocol.FileClassificationRequest{Scope: entityTestScope(), Paths: paths}); !errors.Is(err, ErrInvalidRequest) || s.generationCalls != 0 || s.calls != 0 {
			t.Fatalf("paths=%d err=%v generation=%d calls=%d", len(paths), err, s.generationCalls, s.calls)
		}
	}
	s := &classificationTestStore{afterClassification: func() {}}
	s.afterClassification = func() { s.changed = true }
	got, err := (&Service{Store: s}).FileClassifications(t.Context(), graphprotocol.FileClassificationRequest{Scope: entityTestScope(), Paths: []string{"ordinary.ts"}})
	if !errors.Is(err, ErrGenerationChanged) || len(got.Files) != 0 || len(got.Generations) != 0 {
		t.Fatalf("changed result=%+v err=%v", got, err)
	}
	hidden := &classificationTestStore{rows: []FileClassificationRow{{RepositoryID: 999, Path: "ordinary.ts", Present: true}}}
	got, err = (&Service{Store: hidden}).FileClassifications(t.Context(), graphprotocol.FileClassificationRequest{Scope: entityTestScope(), Paths: []string{"ordinary.ts"}})
	if !errors.Is(err, ErrGenerationChanged) || len(got.Files) != 0 {
		t.Fatalf("hidden result=%+v err=%v", got, err)
	}
	missing := &classificationTestStore{}
	got, err = (&Service{Store: missing}).FileClassifications(t.Context(), graphprotocol.FileClassificationRequest{Scope: entityTestScope(), Paths: []string{"ordinary.ts"}})
	if !errors.Is(err, ErrGenerationChanged) || len(got.Files) != 0 {
		t.Fatalf("missing row result=%+v err=%v", got, err)
	}
}

func TestFileClassificationEmptyAndGeneratedCount(t *testing.T) {
	s := &classificationTestStore{generated: 2, total: 5}
	service := &Service{Store: s}
	got, err := service.FileClassifications(t.Context(), graphprotocol.FileClassificationRequest{Scope: entityTestScope()})
	if err != nil || len(got.Files) != 0 || len(got.Generations) != 1 || s.calls != 0 {
		t.Fatalf("empty=%+v calls=%d err=%v", got, s.calls, err)
	}
	count, err := service.GeneratedFileCount(t.Context(), graphprotocol.GeneratedFileCountRequest{Scope: entityTestScope()})
	if err != nil || count.GeneratedFiles != 2 || count.TotalFiles != 5 || len(count.Generations) != 1 {
		t.Fatalf("count=%+v err=%v", count, err)
	}
	for _, counts := range [][2]int64{{-1, 0}, {1, -1}, {2, 1}} {
		s.generated, s.total = counts[0], counts[1]
		count, err = service.GeneratedFileCount(t.Context(), graphprotocol.GeneratedFileCountRequest{Scope: entityTestScope()})
		if !errors.Is(err, ErrGenerationChanged) || !reflect.DeepEqual(count, graphprotocol.GeneratedFileCountResponse{}) {
			t.Fatalf("invalid counts=%v result=%+v err=%v", counts, count, err)
		}
	}
	s.generated, s.total = 0, 1
	s.afterCount = func() { s.changed = true }
	count, err = service.GeneratedFileCount(t.Context(), graphprotocol.GeneratedFileCountRequest{Scope: entityTestScope()})
	if !errors.Is(err, ErrGenerationChanged) || !reflect.DeepEqual(count, graphprotocol.GeneratedFileCountResponse{}) {
		t.Fatalf("changed count=%+v err=%v", count, err)
	}
}
