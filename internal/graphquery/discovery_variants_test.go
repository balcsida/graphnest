package graphquery

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/graphprotocol"
)

func TestNameSelectorDefaultsAndCursor(t *testing.T) {
	for mode, want := range map[string]int{"prefix": 20, "substring": 30} {
		t.Run(mode, func(t *testing.T) {
			store := &entityTestStore{lookup: func(_ context.Context, q EntityQuery) ([]graphprotocol.Entity, error) {
				if q.Limit != want+1 {
					t.Fatalf("default lookup limit=%d want %d", q.Limit, want+1)
				}
				rows := make([]graphprotocol.Entity, q.Limit)
				for i := range rows {
					rows[i] = testEntity("same")
				}
				return rows, nil
			}}
			service := &Service{Store: store}
			req := graphprotocol.EntitiesRequest{Scope: entityTestScope(), Selector: graphprotocol.EntitySelector{NameMatch: &graphprotocol.NameSelector{Mode: mode, Value: "norm"}}}
			page, err := service.Entities(t.Context(), req)
			if err != nil || len(page.Entities) != want || page.NextCursor == "" {
				t.Fatalf("page=%+v err=%v", page, err)
			}
			for _, change := range []string{"value", "mode", "kinds", "exclude", "limit", "generation"} {
				t.Run(change, func(t *testing.T) {
					changed := req
					selector := *req.Selector.NameMatch
					changed.Selector.NameMatch = &selector
					changed.Cursor = page.NextCursor
					switch change {
					case "value":
						selector.Value = "other"
					case "mode":
						selector.Mode = "prefix"
						if mode == "prefix" {
							selector.Mode = "substring"
						}
					case "kinds":
						selector.Kinds = []string{"method"}
					case "exclude":
						selector.ExcludePrefix = true
						if mode == "prefix" {
							selector.Mode = "substring"
						}
					case "limit":
						changed.Limit = 2
					case "generation":
						store.changed = true
					}
					got, e := service.Entities(t.Context(), changed)
					if e == nil || len(got.Entities) > 0 {
						t.Fatalf("changed cursor accepted: %+v %v", got, e)
					}
					store.changed = false
				})
			}
		})
	}
}

func TestNameSelectorValidation(t *testing.T) {
	for _, s := range []graphprotocol.NameSelector{{Mode: "other"}, {Mode: "prefix", ExcludePrefix: true}, {Mode: "substring", Value: strings.Repeat("x", 16385)}, {Mode: "substring", Kinds: make([]string, 33)}} {
		_, err := (&Service{Store: &entityTestStore{}}).Entities(t.Context(), graphprotocol.EntitiesRequest{Scope: entityTestScope(), Selector: graphprotocol.EntitySelector{NameMatch: &s}})
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("selector=%+v err=%v", s, err)
		}
	}
}

func TestSegmentVocabularyAndPluralWords(t *testing.T) {
	if got := IdentifierSegments("HTMLParser_parse123_123_services_service"); !reflect.DeepEqual(got, []string{"html", "parser", "parse123", "services", "service"}) {
		t.Fatalf("segments=%v", got)
	}
	if got := SegmentWordVariants("services"); !reflect.DeepEqual(got, []string{"services", "service"}) {
		t.Fatalf("variants=%v", got)
	}
	if got := SegmentWordVariants("caches"); !reflect.DeepEqual(got, []string{"caches", "cach", "cache"}) {
		t.Fatalf("variants=%v", got)
	}
	if got := IdentifierSegments("résolution"); !reflect.DeepEqual(got, []string{"résolution"}) {
		t.Fatalf("diacritics lost: %v", got)
	}
}

type segmentTestStore struct {
	entityTestStore
	matches []graphprotocol.SegmentMatch
}

func (s *segmentTestStore) QuerySegments(ctx context.Context, q SegmentSearch) ([]graphprotocol.SegmentMatch, error) {
	return s.matches, ctx.Err()
}

func TestSegmentScopeCancellationAndBounds(t *testing.T) {
	for _, mode := range []string{"cancel", "hidden", "lookahead", "generation", "oversized", "invalid_limit", "invalid_words", "nul"} {
		t.Run(mode, func(t *testing.T) {
			store := &segmentTestStore{matches: []graphprotocol.SegmentMatch{{Entity: testEntity("a"), MatchedWords: []string{"hello"}}}}
			service := &Service{Store: store}
			req := graphprotocol.SegmentRequest{Scope: entityTestScope(), Words: []string{"hello"}, Limit: 1}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "cancel":
				cancel()
			case "hidden":
				store.matches[0].Entity.RepositoryID = 99
			case "lookahead":
				hidden := store.matches[0]
				hidden.Entity.RepositoryID = 99
				store.matches = append(store.matches, hidden)
			case "generation":
				store.changed = true
			case "oversized":
				store.matches[0].Entity.Fact.Name = strings.Repeat("x", MaxEntityQueryBytes)
			case "invalid_limit":
				req.Limit = 101
			case "invalid_words":
				req.Words = make([]string, 33)
			case "nul":
				req.Words = []string{"a\x00b"}
			}
			got, err := service.SegmentMatches(ctx, req)
			if err == nil || len(got.Matches) > 0 || len(got.Generations) > 0 || got.Truncated {
				t.Fatalf("partial output %s: %+v %v", mode, got, err)
			}
		})
	}
}

func TestNameSelectorCancellationHiddenAndBytes(t *testing.T) {
	for _, mode := range []string{"prefix", "substring"} {
		for _, failure := range []string{"cancel", "hidden", "oversized"} {
			t.Run(mode+failure, func(t *testing.T) {
				store := &entityTestStore{lookup: func(context.Context, EntityQuery) ([]graphprotocol.Entity, error) {
					rows := []graphprotocol.Entity{testEntity("a"), testEntity("b")}
					if failure == "hidden" {
						rows[1].RepositoryID = 99
					}
					if failure == "oversized" {
						rows[0].Fact.Name = strings.Repeat("x", MaxEntityQueryBytes)
					}
					return rows, nil
				}}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if failure == "cancel" {
					cancel()
				}
				got, err := (&Service{Store: store}).Entities(ctx, graphprotocol.EntitiesRequest{Scope: entityTestScope(), Selector: graphprotocol.EntitySelector{NameMatch: &graphprotocol.NameSelector{Mode: mode, Value: "a"}}, Limit: 1})
				if err == nil || len(got.Entities) > 0 || got.NextCursor != "" {
					t.Fatalf("partial names=%+v %v", got, err)
				}
			})
		}
	}
}
