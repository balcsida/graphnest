package graphquery

import (
	"context"
	"encoding/hex"
	"slices"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/balcsida/graphnest/internal/graphprotocol"
)

type SegmentSearch struct {
	Snapshots       []QuerySnapshot
	Variants, Words []string
	Limit           int
}

type SegmentStore interface {
	QuerySegments(context.Context, SegmentSearch) ([]graphprotocol.SegmentMatch, error)
}

func (service *Service) SegmentMatches(ctx context.Context, req graphprotocol.SegmentRequest) (graphprotocol.SegmentResponse, error) {
	if service == nil || len(req.Words) > 32 || req.Limit < 0 || req.Limit > 100 {
		return graphprotocol.SegmentResponse{}, ErrInvalidRequest
	}
	ctx, cancel := service.entityContext(ctx)
	defer cancel()
	q := SegmentSearch{Limit: req.Limit}
	if q.Limit == 0 {
		q.Limit = 6
	}
	q.Limit = min(q.Limit, service.limits().MaxRows)
	for _, word := range req.Words {
		if len(word) > 16384 || !utf8.ValidString(word) || strings.ContainsRune(word, 0) {
			return graphprotocol.SegmentResponse{}, ErrInvalidRequest
		}
		for _, v := range SegmentWordVariants(word) {
			if !slices.Contains(q.Variants, v) {
				q.Variants = append(q.Variants, v)
				q.Words = append(q.Words, word)
			}
		}
	}
	ready, err := service.readyEntities(ctx, req.Scope)
	if err != nil {
		return graphprotocol.SegmentResponse{}, err
	}
	store, ok := ready.store.(SegmentStore)
	if !ok {
		return graphprotocol.SegmentResponse{}, ErrDiscoveryUnavailable
	}
	q.Snapshots = ready.selected
	limit := q.Limit
	q.Limit++
	matches, err := store.QuerySegments(ctx, q)
	if err != nil {
		return graphprotocol.SegmentResponse{}, err
	}
	for i := range matches {
		m := &matches[i]
		selected := false
		for _, s := range ready.selected {
			selected = selected || s.RepositoryID == m.Entity.RepositoryID
		}
		if !selected || m.Entity.Fact == nil {
			return graphprotocol.SegmentResponse{}, ErrGenerationChanged
		}
		m.Entity.RepositoryID = ready.publicIDs[m.Entity.RepositoryID]
		slices.Sort(m.MatchedWords)
	}
	result := graphprotocol.SegmentResponse{Matches: matches, Generations: ready.publicGenerations(), Truncated: len(matches) > limit}
	if len(matches) > limit {
		result.Matches = matches[:limit]
	}
	if err = entityResponseSize(result); err != nil {
		return graphprotocol.SegmentResponse{}, err
	}
	if err = ready.current(ctx); err != nil {
		return graphprotocol.SegmentResponse{}, err
	}
	return result, nil
}

// FoldName changes ASCII case only; selectors retain non-ASCII and NUL bytes.
func FoldName(value string) string {
	b := []byte(value)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// NameGrams uses hex bytes so GIN keys preserve every literal selector byte.
func NameGrams(value string) []string {
	b := []byte(FoldName(value))
	out := []string{}
	seen := map[string]bool{}
	for w := 1; w <= 3; w++ {
		for i := 0; i+w <= len(b); i++ {
			g := hex.EncodeToString(b[i : i+w])
			if !seen[g] {
				seen[g] = true
				out = append(out, g)
			}
		}
	}
	return out
}

// IdentifierSegments follows the pinned segment vocabulary, without discovery's
// diacritic folding or suffix stemming. Only twelve useful segments are retained.
func IdentifierSegments(value string) []string {
	out := []string{}
	add := func(word string) {
		word = strings.ToLower(word)
		n := len(utf16.Encode([]rune(word)))
		if n < 2 || n > 32 || len(out) >= 12 {
			return
		}
		letters := false
		for _, r := range word {
			letters = letters || !unicode.IsNumber(r)
		}
		if letters && !slices.Contains(out, word) {
			out = append(out, word)
		}
	}
	for _, word := range strings.FieldsFunc(value, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		r := []rune(word)
		start := 0
		for i := 1; i < len(r); i++ {
			if unicode.IsUpper(r[i]) && (unicode.IsLower(r[i-1]) || unicode.IsNumber(r[i-1]) || (unicode.IsUpper(r[i-1]) && i+1 < len(r) && unicode.IsLower(r[i+1]))) {
				add(string(r[start:i]))
				start = i
			}
		}
		add(string(r[start:]))
	}
	return out
}

func SegmentWordVariants(word string) []string {
	out := []string{word}
	n := len(utf16.Encode([]rune(word)))
	if strings.HasSuffix(word, "xes") || strings.HasSuffix(word, "shes") || strings.HasSuffix(word, "sses") || strings.HasSuffix(word, "zzes") {
		if n >= 6 {
			out = append(out, word[:len(word)-2])
		}
	} else if strings.HasSuffix(word, "ches") || strings.HasSuffix(word, "ses") || strings.HasSuffix(word, "zes") || strings.HasSuffix(word, "oes") {
		if n >= 6 {
			out = append(out, word[:len(word)-2])
		}
		if n >= 5 {
			out = append(out, word[:len(word)-1])
		}
	} else if strings.HasSuffix(word, "s") && !strings.HasSuffix(word, "ss") && n >= 5 {
		out = append(out, word[:len(word)-1])
	}
	return out
}
