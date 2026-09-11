package graphquery

import (
	"context"
	"errors"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"reflect"
	"strings"
	"testing"
)

func TestDiscoveryParsingAndTerms(t *testing.T) {
	if got := DiscoveryTerms("MegaProject backend", []string{"megaproject"}); !reflect.DeepEqual(got, []string{"backend"}) {
		t.Fatalf("project segments dominate: %v", got)
	}
	p := ParseDiscoveryQuery(`kind:function kind:class LANG:Go path:"src/my dir" name:auth unknown:thing kind:bogus lang:nope authenticate`)
	if !reflect.DeepEqual(p.Kinds, []string{"function", "class"}) || !reflect.DeepEqual(p.Languages, []string{"go"}) || !reflect.DeepEqual(p.Paths, []string{"src/my dir"}) || !reflect.DeepEqual(p.Names, []string{"auth"}) || p.Text != "unknown:thing kind:bogus lang:nope authenticate" {
		t.Fatalf("parsed=%+v", p)
	}
	for _, query := range []string{"HTMLParser", "html_parser", "html-parser", "html.parser"} {
		terms := DiscoveryTerms(query, nil)
		joined := strings.Join(terms, " ")
		if !strings.Contains(joined, "html") || !strings.Contains(joined, "parser") {
			t.Fatalf("%s: %v", query, terms)
		}
	}
	if got := DiscoveryTerms("how does this code work", nil); len(got) != 0 {
		t.Fatalf("weak terms=%v", got)
	}
	for query, want := range map[string]string{"caching": "cache", "services": "service", "références": "reference"} {
		if !strings.Contains(strings.Join(DiscoveryTerms(query, nil), " "), want) {
			t.Fatalf("normalization %s", query)
		}
	}
}

type discoveryTestStore struct {
	entityTestStore
	discover func(context.Context, DiscoverySearch) ([]graphprotocol.DiscoveryMatch, error)
}

func (s *discoveryTestStore) QueryDiscovery(ctx context.Context, q DiscoverySearch) ([]graphprotocol.DiscoveryMatch, error) {
	return s.discover(ctx, q)
}

func TestDiscoveryGenerationAndCancellation(t *testing.T) {
	for _, mode := range []string{"generation", "cancel", "hidden", "oversize", "isolated", "exact_isolated"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			store := &discoveryTestStore{}
			store.changed = mode == "generation"
			store.discover = func(context.Context, DiscoverySearch) ([]graphprotocol.DiscoveryMatch, error) {
				e := testEntity("entry")
				e.Fact.Name = "entry"
				if mode == "cancel" {
					cancel()
				}
				if mode == "hidden" {
					e.RepositoryID = 99
				}
				if mode == "oversize" {
					v := strings.Repeat("x", MaxEntityQueryBytes)
					e.Fact.Documentation = &v
				}
				if mode == "isolated" || mode == "exact_isolated" {
					e.Fact.Kind = "variable"
				}
				if mode == "isolated" {
					e.Fact.Name = "incidental"
				}
				return []graphprotocol.DiscoveryMatch{{Entity: e, Score: 1, Pinned: true}}, nil
			}
			got, err := (&Service{Store: store}).Discover(ctx, graphprotocol.DiscoverRequest{Scope: entityTestScope(), Query: "entry"})
			want := ErrGenerationChanged
			if mode == "cancel" {
				want = context.Canceled
			}
			if mode == "oversize" {
				want = ErrQuerySize
			}
			if mode == "isolated" {
				if err != nil || got.Status != "no_entry_point" || len(got.Matches) > 0 {
					t.Fatalf("weak=%+v %v", got, err)
				}
				return
			}
			if mode == "exact_isolated" {
				if err != nil || got.Status != "candidates" || got.Confidence != "low" || len(got.Matches) != 1 {
					t.Fatalf("exact weak name=%+v %v", got, err)
				}
				return
			}
			if !errors.Is(err, want) || len(got.Matches) > 0 || len(got.Generations) > 0 {
				t.Fatalf("leaked=%+v %v", got, err)
			}
		})
	}
}

func TestDiscoveryLookaheadCannotLeakScope(t *testing.T) {
	store := &discoveryTestStore{discover: func(context.Context, DiscoverySearch) ([]graphprotocol.DiscoveryMatch, error) {
		visible, hidden := testEntity("visible"), testEntity("hidden")
		hidden.RepositoryID = 99
		return []graphprotocol.DiscoveryMatch{{Entity: visible}, {Entity: hidden}}, nil
	}}
	got, err := (&Service{Store: store}).Discover(t.Context(), graphprotocol.DiscoverRequest{Scope: entityTestScope(), Query: "entry", Limit: 1, CandidateLimit: 1})
	if !errors.Is(err, ErrGenerationChanged) || got.CandidateTruncated || len(got.Generations) > 0 {
		t.Fatalf("lookahead leaked: %+v %v", got, err)
	}
}

func TestDiscoveryMultitermConfiguration(t *testing.T) {
	store := &discoveryTestStore{discover: func(context.Context, DiscoverySearch) ([]graphprotocol.DiscoveryMatch, error) {
		a, b := testEntity("broad"), testEntity("corroborated")
		a.Fact.Kind, b.Fact.Kind = "function", "function"
		return []graphprotocol.DiscoveryMatch{{Entity: a, Score: 100, MatchedTerms: 1}, {Entity: b, Score: 10, MatchedTerms: 2}}, nil
	}}
	for _, disabled := range []bool{false, true} {
		got, err := (&Service{Store: store}).Discover(t.Context(), graphprotocol.DiscoverRequest{Scope: entityTestScope(), Query: "cache service", Limit: 1, Config: graphprotocol.DiscoveryConfig{NoMultiterm: disabled}})
		want := "corroborated"
		if disabled {
			want = "broad"
		}
		if err != nil || len(got.Matches) != 1 || got.Matches[0].Entity.Fact.Occurrence != want {
			t.Fatalf("disabled=%v matches=%+v err=%v", disabled, got.Matches, err)
		}
	}
}
