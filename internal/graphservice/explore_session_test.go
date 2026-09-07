package graphservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/repository"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/pkg/api"
	"google.golang.org/protobuf/proto"
)

// The larger block is eligible for an eight-line back-reference; the short
// neighbor keeps the repeated response useful without activating restoration.
func sessionFixture() (*Service, *exploreBackend, *fakeRepositoryStore) {
	s, b, store := exploreFixture()
	b.file.Size = 100
	b.entity.Fact.Location = &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(0), Character: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(0), Character: proto.Int32(5)}}
	b.matches[0].Entity = b.entity
	other := b.entity
	other.ID = "neighbor"
	other.Fact = proto.Clone(other.Fact).(*graphv2.Node)
	other.Fact.Path = proto.String("short.ts")
	other.Fact.Occurrence = "neighbor"
	b.neighbors = []graphprotocol.Entity{other}
	b.filesByPath = map[string]*graphv2.File{"unicode.ts": b.file, "short.ts": {Path: "short.ts", Size: 6}}
	s.Files = inspectionReader(func(_ context.Context, _ authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
		content := strings.TrimSuffix(strings.Repeat("hello\r\n", 16), "\n")
		if r.Path == "short.ts" {
			content = "short"
		}
		return api.ReadFileResponse{RepositoryID: r.RepositoryID, Path: r.Path, IndexedSHA: sha, BlobSHA: "blob-" + r.Path, StartLine: 1, EndLine: strings.Count(content, "\n") + 1, Content: content}, nil
	})
	return s, b, store
}
func sessionRequest(id string) ExploreRequest {
	r := ExploreRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "hello", MaxFiles: 4, SourceUnits: 13000}
	r.SessionID = id
	return r
}
func sessionPrincipal() authn.Principal {
	p := principalFor(101)
	p.Subject = "session-user"
	p.Method = "local"
	return p
}
func sessionSource(r ExploreResponse, path string) string {
	var out strings.Builder
	for _, f := range r.Files {
		if f.Path == path {
			for _, seg := range f.Segments {
				out.WriteString(seg.Content)
			}
		}
	}
	return out.String()
}
func TestExploreSessionRepeatedControls(t *testing.T) {
	for _, mode := range []string{"enabled", "disabled", "no_session", "isolated_session", "isolated_principal", "restore"} {
		t.Run(mode, func(t *testing.T) {
			s, b, _ := sessionFixture()
			r := sessionRequest("conversation")
			p := sessionPrincipal()
			if mode == "disabled" {
				if err := json.Unmarshal([]byte(`{"dedup":false}`), &r.Config); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "no_session" {
				r = sessionRequest("")
			}
			if mode == "restore" {
				b.neighbors = nil
			}
			first, err := s.Explore(t.Context(), p, r)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "isolated_session" {
				r = sessionRequest("another")
			}
			if mode == "isolated_principal" {
				p.Subject = "another"
			}
			second, err := s.Explore(t.Context(), p, r)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "enabled" {
				if sessionSource(second, "unicode.ts") != "" || sessionSource(second, "short.ts") != "short" {
					t.Fatalf("repeated eligible source was resent: units=%d content=%q", second.Usage.SourceUnits, sessionSource(second, "unicode.ts"))
				}
			} else if sessionSource(second, "unicode.ts") != sessionSource(first, "unicode.ts") {
				t.Fatal("lost exact source in control/restoration")
			}
		})
	}
}

func TestExploreSessionPreciseCoverage(t *testing.T) {
	s, _, _ := sessionFixture()
	reader := s.Files
	expanded := false
	s.Files = inspectionReader(func(ctx context.Context, p authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
		v, e := reader.ReadFileAt(ctx, p, r, sha)
		if r.Path == "unicode.ts" {
			v.Content = strings.TrimSuffix(strings.Repeat("hello\r\n", 8), "\n") + "\npartial"
			if expanded {
				v.Content += " unseen bytes\r\n" + strings.TrimSuffix(strings.Repeat("new\r\n", 7), "\n")
			}
			v.EndLine = strings.Count(v.Content, "\n") + 1
		}
		return v, e
	})
	r := sessionRequest("partial")
	if _, e := s.Explore(t.Context(), sessionPrincipal(), r); e != nil {
		t.Fatal(e)
	}
	expanded = true
	got, e := s.Explore(t.Context(), sessionPrincipal(), r)
	if e != nil {
		t.Fatal(e)
	}
	f := got.Files[0]
	if len(f.References) != 1 || f.References[0].StartLine != 1 || f.References[0].EndLine != 8 || len(f.Segments) != 1 || f.Segments[0].StartLine != 9 || !strings.HasPrefix(f.Segments[0].Content, "partial unseen bytes") {
		t.Fatalf("partial prefix hid unseen bytes: %+v", f)
	}
	if len(f.Selections) != 1 || f.Selections[0].Status != "already_seen" || f.Selections[0].Reference == nil || f.Selections[0].Segment != -1 {
		t.Fatalf("lost exact referenced selection: %+v", f.Selections)
	}
	// The newly emitted eight-line span is admitted only after this result succeeds.
	again, e := s.Explore(t.Context(), sessionPrincipal(), r)
	if e != nil || sessionSource(again, "unicode.ts") != "" {
		t.Fatalf("overlapping range was not merged: %v %+v", e, again)
	}
}
func TestExploreSessionDeliveryFailures(t *testing.T) {
	for _, mode := range []string{"size", "cancel", "grant", "generation", "sha", "unreadable", "oversized", "unauthenticated", "rotation"} {
		t.Run(mode, func(t *testing.T) {
			s, b, store := sessionFixture()
			r := sessionRequest("delivery")
			p := sessionPrincipal()
			reader := s.Files
			fail := true
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			repositories := append([]repository.Repository(nil), store.repositories...)
			s.Files = inspectionReader(func(c context.Context, p authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
				v, e := reader.ReadFileAt(c, p, r, sha)
				if fail {
					switch mode {
					case "cancel":
						cancel()
					case "grant":
						store.repositories = nil
					case "generation":
						b.changed = true
					case "sha":
						store.repositories[0].IndexedSHA = strings.Repeat("b", 40)
					case "unreadable":
						return api.ReadFileResponse{}, errors.New("private source failure")
					case "oversized":
						return api.ReadFileResponse{}, repository.ErrFileTooLarge
					}
				}
				return v, e
			})
			if mode == "size" {
				s.Limits.MaxResponseBytes = 1
			}
			if mode == "unauthenticated" {
				p = authn.Principal{}
			}
			if mode == "rotation" {
				p.ForceRotation = true
			}
			got, e := s.Explore(ctx, p, r)
			if mode == "unreadable" || mode == "oversized" {
				if e != nil || got.Complete {
					t.Fatalf("source refusal: %+v %v", got, e)
				}
			} else if e == nil || !reflect.DeepEqual(got, ExploreResponse{}) {
				t.Fatalf("failed domain leaked: %+v %v", got, e)
			}
			fail = false
			s.Limits.MaxResponseBytes = 0
			b.changed = false
			store.repositories = repositories
			got, e = s.Explore(t.Context(), sessionPrincipal(), r)
			if e != nil || sessionSource(got, "unicode.ts") == "" {
				t.Fatalf("failed result advanced history: %+v %v", got, e)
			}
			next, e := s.Explore(t.Context(), sessionPrincipal(), r)
			if e != nil || sessionSource(next, "unicode.ts") != "" {
				t.Fatalf("successful retry not admitted: %+v %v", next, e)
			}
		})
	}
}
func TestExploreSessionReplacementAndConfig(t *testing.T) {
	for _, mode := range []string{"generation", "sha", "blob", "repository", "method", "installation", "disabled_then_enabled"} {
		t.Run(mode, func(t *testing.T) {
			s, b, store := sessionFixture()
			r := sessionRequest("isolation")
			p := sessionPrincipal()
			if mode == "disabled_then_enabled" {
				r.Config.Dedup = proto.Bool(false)
			}
			if _, e := s.Explore(t.Context(), p, r); e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "generation":
				b.generation.UploadID++
			case "sha":
				b.generation.Commit = strings.Repeat("b", 40)
				store.repositories[0].IndexedSHA = b.generation.Commit
			case "blob":
				reader := s.Files
				s.Files = inspectionReader(func(c context.Context, p authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
					v, e := reader.ReadFileAt(c, p, r, sha)
					v.BlobSHA += "changed"
					return v, e
				})
			case "repository":
				store.repositories[0].ID = 2
			case "method":
				p.Method = "oidc"
			case "installation":
				p.InstallationID = 2
			case "disabled_then_enabled":
				r.Config.Dedup = proto.Bool(true)
			}
			got, e := s.Explore(t.Context(), p, r)
			if e != nil {
				t.Fatal(e)
			}
			if (sessionSource(got, "unicode.ts") == "") != (mode == "disabled_then_enabled") {
				t.Fatalf("scope/config reuse: %s %+v", mode, got)
			}
		})
	}
}
func TestExploreSessionFinalSizeIncludesReferences(t *testing.T) {
	s, _, _ := sessionFixture()
	r := sessionRequest("size")
	p := sessionPrincipal()
	first, e := s.Explore(t.Context(), p, r)
	if e != nil {
		t.Fatal(e)
	}
	second, e := s.Explore(t.Context(), p, r)
	if e != nil {
		t.Fatal(e)
	}
	data, _ := json.Marshal(second)
	s.Limits.MaxResponseBytes = len(data) - 1
	got, e := s.Explore(t.Context(), p, r)
	if !errors.Is(e, graphquery.ErrQuerySize) || !reflect.DeepEqual(got, ExploreResponse{}) {
		t.Fatalf("reference bypassed final envelope ceiling: %+v %v (first units=%d)", got, e, first.Usage.SourceUnits)
	}
}
func TestExploreHistoryBoundsExpiryAndConcurrency(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	key := func(n int) [32]byte { return [32]byte{byte(n), byte(n >> 8)} }
	lines := map[exploreLine][32]byte{{Number: 1}: key(100)}
	var h exploreHistory
	for n := 0; n < 5; n++ {
		if e := h.record(t.Context(), key(n), key(99), lines, now.Add(time.Duration(n)*time.Second)); e != nil {
			t.Fatal(e)
		}
	}
	if h.snapshot(key(0), now.Add(5*time.Second)) != nil || len(h.snapshot(key(4), now.Add(5*time.Second))) != 1 {
		t.Fatal("principal LRU did not evict oldest")
	}
	if h.snapshot(key(4), now.Add(exploreHistoryTTL+4*time.Second)) != nil {
		t.Fatal("fixed lifetime became sliding")
	}
	h = exploreHistory{}
	for n := 0; n < 65; n++ {
		if e := h.record(t.Context(), key(n), key(n), lines, now.Add(time.Duration(n)*time.Second)); e != nil {
			t.Fatal(e)
		}
	}
	if h.snapshot(key(0), now.Add(time.Minute)) != nil || len(h.entries) != 64 {
		t.Fatal("entry admission unbounded")
	}
	large := make(map[exploreLine][32]byte)
	for n := 0; n < exploreHistoryLines+100; n++ {
		large[exploreLine{Number: n}] = key(n)
	}
	h = exploreHistory{}
	for n := 0; n < 20; n++ {
		if e := h.record(t.Context(), key(n), key(n), large, now.Add(time.Duration(n)*time.Second)); e != nil {
			t.Fatal(e)
		}
	}
	bytes := 0
	for _, entry := range h.entries {
		if len(entry.lines) > exploreHistoryLines {
			t.Fatal("entry line admission unbounded")
		}
		bytes += exploreHistoryBase + len(entry.lines)*exploreHistoryLineBytes
	}
	if bytes > exploreHistoryBytes || h.snapshot(key(0), now.Add(time.Minute)) != nil {
		t.Fatal("global byte admission failed")
	}
	// A caller snapshot stays isolated while concurrent successful results merge.
	h = exploreHistory{}
	if e := h.record(t.Context(), key(1), key(1), lines, now); e != nil {
		t.Fatal(e)
	}
	snapshot := h.snapshot(key(1), now)
	var wg sync.WaitGroup
	for n := 2; n < 30; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			delta := map[exploreLine][32]byte{{Number: n}: key(n)}
			if e := h.record(t.Context(), key(1), key(1), delta, now); e != nil {
				t.Error(e)
			}
			h.snapshot(key(1), now)
		}(n)
	}
	wg.Wait()
	if len(snapshot) != 1 || len(h.snapshot(key(1), now)) != 29 {
		t.Fatal("history snapshots mutated or concurrent commits lost")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if e := h.record(canceled, key(2), key(2), lines, now); !errors.Is(e, context.Canceled) || h.snapshot(key(2), now) != nil {
		t.Fatal("canceled admission advanced history")
	}
	t.Logf("bounds: entries=%d per-principal=%d entry bytes=%d total bytes=%d lifetime=%s", exploreHistoryEntries, exploreHistoryPerPrincipal, exploreHistoryEntryBytes, exploreHistoryBytes, exploreHistoryTTL)
}

func TestExploreSessionInvalidID(t *testing.T) {
	s, _, _ := sessionFixture()
	for _, id := range []string{strings.Repeat("x", 129), "bad\x00id", "bad\nid", string([]byte{0xff})} {
		r := sessionRequest(id)
		if _, e := s.Explore(t.Context(), sessionPrincipal(), r); !errors.Is(e, ErrInvalidRequest) {
			t.Fatal(fmt.Sprintf("invalid session admitted: %q %v", id, e))
		}
	}
}

func TestExploreSessionEOFThreshold(t *testing.T) {
	s, _, _ := sessionFixture()
	reader := s.Files
	s.Files = inspectionReader(func(c context.Context, p authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
		v, e := reader.ReadFileAt(c, p, r, sha)
		if r.Path == "unicode.ts" {
			v.Content = strings.Repeat("hello\n", 7)
			v.EndLine = 8
		}
		return v, e
	})
	r := sessionRequest("eof")
	p := sessionPrincipal()
	if _, e := s.Explore(t.Context(), p, r); e != nil {
		t.Fatal(e)
	}
	got, e := s.Explore(t.Context(), p, r)
	if e != nil || sessionSource(got, "unicode.ts") != strings.Repeat("hello\n", 7) {
		t.Fatalf("EOF made seven source lines eligible: %+v %v", got, e)
	}
}

func TestExploreSessionFailedExpansionDoesNotAdvance(t *testing.T) {
	s, _, _ := sessionFixture()
	r := sessionRequest("expansion")
	p := sessionPrincipal()
	if _, e := s.Explore(t.Context(), p, r); e != nil {
		t.Fatal(e)
	}
	reader := s.Files
	s.Files = inspectionReader(func(c context.Context, p authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
		v, e := reader.ReadFileAt(c, p, r, sha)
		if r.Path == "unicode.ts" {
			v.Content += "\n" + strings.TrimSuffix(strings.Repeat("unseen\r\n", 8), "\n")
			v.EndLine = 24
		}
		return v, e
	})
	s.Limits.MaxResponseBytes = 1
	if _, e := s.Explore(t.Context(), p, r); !errors.Is(e, graphquery.ErrQuerySize) {
		t.Fatal(e)
	}
	s.Limits.MaxResponseBytes = 0
	got, e := s.Explore(t.Context(), p, r)
	if e != nil {
		t.Fatal(e)
	}
	f := got.Files[0]
	// The first result ended at L16's CR: its newly selected LF must be
	// re-served with L16 rather than falsely attributed to prior delivery.
	if len(f.References) != 1 || f.References[0].EndLine != 15 || len(f.Segments) != 1 || f.Segments[0].StartLine != 16 || !strings.HasPrefix(f.Segments[0].Content, "hello\r\nunseen") {
		t.Fatalf("failed expansion advanced seen source: %+v", f)
	}
}

type sessionRepositoryStore []repository.Repository

func (s sessionRepositoryStore) GraphRepositories(context.Context, authn.Principal) ([]repository.Repository, error) {
	return append([]repository.Repository(nil), s...), nil
}

type concurrentExploreBackend struct {
	*exploreBackend
	mu sync.Mutex
}

func (b *concurrentExploreBackend) Discover(c context.Context, r graphprotocol.DiscoverRequest) (graphprotocol.DiscoverResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exploreBackend.Discover(c, r)
}
func (b *concurrentExploreBackend) Entities(c context.Context, r graphprotocol.EntitiesRequest) (graphprotocol.EntitiesResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exploreBackend.Entities(c, r)
}
func (b *concurrentExploreBackend) IndexedFiles(c context.Context, r graphprotocol.FilesRequest) (graphprotocol.FilesResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exploreBackend.IndexedFiles(c, r)
}
func (b *concurrentExploreBackend) Traverse(c context.Context, r graphprotocol.TraverseRequest) (graphprotocol.TraverseResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exploreBackend.Traverse(c, r)
}
func TestExploreSessionConcurrentCallsDoNotSeePendingResults(t *testing.T) {
	s, b, store := sessionFixture()
	s.Store = sessionRepositoryStore(store.repositories)
	s.Backend = &concurrentExploreBackend{exploreBackend: b}
	reader := s.Files
	arrived, release := make(chan struct{}, 2), make(chan struct{})
	s.Files = inspectionReader(func(c context.Context, p authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
		if r.Path == "unicode.ts" {
			arrived <- struct{}{}
			select {
			case <-release:
			case <-c.Done():
				return api.ReadFileResponse{}, c.Err()
			}
		}
		return reader.ReadFileAt(c, p, r, sha)
	})
	responses := make(chan ExploreResponse, 2)
	failures := make(chan error, 2)
	for n := 0; n < 2; n++ {
		go func() {
			r, e := s.Explore(t.Context(), sessionPrincipal(), sessionRequest("concurrent"))
			responses <- r
			failures <- e
		}()
	}
	for n := 0; n < 2; n++ {
		select {
		case <-arrived:
		case <-time.After(4 * time.Second):
			close(release)
			t.Fatal("concurrent request stalled before source")
		}
	}
	close(release)
	for n := 0; n < 2; n++ {
		r := <-responses
		e := <-failures
		if e != nil || sessionSource(r, "unicode.ts") == "" {
			t.Fatalf("consumed pending domain result: %+v %v", r, e)
		}
	}
	s.Files = reader
	got, e := s.Explore(t.Context(), sessionPrincipal(), sessionRequest("concurrent"))
	if e != nil || sessionSource(got, "unicode.ts") != "" {
		t.Fatalf("completed concurrent source not retained: %+v %v", got, e)
	}
}

// Legal C2 segment shapes: line-oriented windows may end before a separator
// that a subsequent, larger window includes from the same immutable blob.
func sessionBoundaryResult(content string, start int, neighbor bool) ExploreResponse {
	result := ExploreResponse{Files: []ExploreFile{{Path: "boundary.ts", Status: "ok", Segments: []InspectionSource{{Path: "boundary.ts", IndexedSHA: "sha", BlobSHA: "blob", StartLine: start, EndLine: start + strings.Count(content, "\n"), Content: content}}}}, Usage: ExploreUsage{SourceUnits: sourceUnits(content), SourceBytes: len(content)}}
	if neighbor {
		result.Files = append(result.Files, ExploreFile{Path: "short.ts", Status: "ok", Segments: []InspectionSource{{Path: "short.ts", IndexedSHA: "sha", BlobSHA: "short", StartLine: 1, EndLine: 1, Content: "new"}}})
		result.Usage.SourceUnits += 3
		result.Usage.SourceBytes += 3
	}
	return result
}
func TestExploreSessionTerminatorCoverage(t *testing.T) {
	for _, test := range []struct{ newline, missing string }{{"\n", "\n"}, {"\r\n", "\n"}, {"\r\n", "\r\n"}} {
		newline := test.newline
		for _, count := range []int{8, 16} {
			for _, neighbor := range []bool{false, true} {
				t.Run(fmt.Sprintf("newline_%q_missing_%q_lines_%d_neighbor_%v", newline, test.missing, count, neighbor), func(t *testing.T) {
					content := strings.Repeat("hello"+newline, count)
					prior := exploreDelivered(sessionBoundaryResult(strings.TrimSuffix(content, test.missing), 1, neighbor))
					result := sessionBoundaryResult(content, 1, neighbor)
					result.Files[0].Entities = []graphprotocol.Entity{{ID: "first", Fact: &graphv2.Node{Location: &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(0), Character: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(0), Character: proto.Int32(5)}}}}}
					applyExploreHistory(&result, prior)
					want, saved := content, 0
					if count == 16 {
						want = "hello" + newline
						saved = 15 * (5 + len(newline))
					}
					if got := sessionSource(result, "boundary.ts"); got != want || result.Usage.DedupSavedUnits != saved {
						t.Fatalf("undelivered terminator lost: emitted=%q want=%q saved=%d wantSaved=%d", got, want, result.Usage.DedupSavedUnits, saved)
					}
					for _, segment := range result.Files[0].Segments {
						if segment.Content == "" {
							t.Fatal("empty segment misrepresented new source")
						}
					}
					if count == 16 {
						f := result.Files[0]
						if len(f.References) != 1 || f.References[0].StartLine != 1 || f.References[0].EndLine != 15 || len(f.Selections) != 1 || f.Selections[0].Status != "already_seen" || f.Selections[0].Selection == nil || f.Selections[0].Selection.StartByte != 0 || f.Selections[0].Selection.EndByte != 5 {
							t.Fatalf("terminator proof changed exact reference selection: %+v", f)
						}
					}
				})
			}
		}
	}
}
func TestExploreSessionSplitBoundaryCoverage(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("newline_%q", newline), func(t *testing.T) {
			// The new prefix's separator before L10 was never delivered. Re-serving
			// the first known line preserves that separator in an ordinary whole-line span.
			known := strings.TrimSuffix(strings.Repeat("seen"+newline, 9), "\n")
			prior := exploreDelivered(sessionBoundaryResult(known, 10, true))
			content := strings.Repeat("new"+newline, 9) + known
			result := sessionBoundaryResult(content, 1, true)
			applyExploreHistory(&result, prior)
			want := strings.Repeat("new"+newline, 9) + strings.TrimSuffix("seen"+newline, "\n")
			if got := sessionSource(result, "boundary.ts"); got != want {
				t.Fatalf("split dropped unseen separator: emitted=%q want=%q", got, want)
			}
			f := result.Files[0]
			if len(f.References) != 1 || f.References[0].StartLine != 11 || f.References[0].EndLine != 18 || result.Usage.DedupSavedUnits != sourceUnits(content)-sourceUnits(want) {
				t.Fatalf("split range/savings: %+v usage=%+v", f, result.Usage)
			}

			// Two separately delivered adjacent windows do not prove the separator
			// between them. Their union must not invent that missing byte.
			left := strings.TrimSuffix(strings.Repeat("left"+newline, 8), "\n")
			right := strings.TrimSuffix(strings.Repeat("right"+newline, 8), "\n")
			priorResult := sessionBoundaryResult(left, 1, true)
			priorResult.Files[0].Segments = append(priorResult.Files[0].Segments, sessionBoundaryResult(right, 9, false).Files[0].Segments[0])
			prior = exploreDelivered(priorResult)
			combined := left + "\n" + right
			result = sessionBoundaryResult(combined, 1, true)
			applyExploreHistory(&result, prior)
			if got := sessionSource(result, "boundary.ts"); got != combined || result.Usage.DedupSavedUnits != 0 {
				t.Fatalf("adjacent windows invented separator coverage: emitted=%q saved=%d", got, result.Usage.DedupSavedUnits)
			}
		})
	}
}
func TestExploreSessionEmptyRemainderRestores(t *testing.T) {
	content := strings.Repeat("hello\n", 8)
	prior := exploreDelivered(sessionBoundaryResult(content, 1, false))
	// Bounded admission may retain the eight actual lines but omit the EOF point.
	delete(prior, exploreLine{File: exploreSourceIdentity(sessionBoundaryResult(content, 1, false).Files[0].Segments[0]), Number: 9})
	result := sessionBoundaryResult(content, 1, false)
	applyExploreHistory(&result, prior)
	if sessionSource(result, "boundary.ts") != content || !result.SessionRestored || len(result.Files[0].References) != 0 || result.Usage.DedupSavedUnits != 0 {
		t.Fatalf("empty remainder defeated restoration: %+v", result)
	}
}
func TestExploreSessionMixedReadRefusalAdmission(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{"unreadable", errors.New("unreadable first range")},
		{"oversized", repository.ErrFileTooLarge},
		{"binary", repository.ErrBinaryFile},
		{"invalid_range", repository.ErrInvalidRange},
		{"partial_window", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, b, _ := sessionFixture()
			b.file.Size = 1000000
			first := b.entity
			first.Fact = proto.Clone(first.Fact).(*graphv2.Node)
			first.Fact.Location = &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(100), Character: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(100), Character: proto.Int32(5)}}
			second := first
			second.ID = "second"
			second.Fact = proto.Clone(first.Fact).(*graphv2.Node)
			second.Fact.Occurrence = "second"
			second.Fact.Name = "second"
			second.Fact.Location = &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(3100), Character: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(3100), Character: proto.Int32(5)}}
			b.entity = first
			b.matches = []graphprotocol.DiscoveryMatch{{Entity: first, File: b.file, Pinned: true, Score: 20}, {Entity: second, File: b.file, Pinned: true, Score: 20}}
			s.Files = inspectionReader(func(_ context.Context, _ authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
				if r.Path == "unicode.ts" && r.StartLine < 1000 && test.err != nil {
					return api.ReadFileResponse{}, test.err
				}
				content := strings.TrimSuffix(strings.Repeat("hello\n", 16), "\n")
				if r.Path == "short.ts" {
					content = "short"
				}
				return api.ReadFileResponse{RepositoryID: r.RepositoryID, Path: r.Path, IndexedSHA: sha, BlobSHA: "blob-" + r.Path, StartLine: r.StartLine, EndLine: r.StartLine + strings.Count(content, "\n"), Content: content}, nil
			})
			got, e := s.Explore(t.Context(), sessionPrincipal(), sessionRequest("mixed"))
			if e != nil {
				t.Fatal(e)
			}
			if got.Files[0].Status != "ok" || got.Complete {
				t.Fatalf("probe failed to reach later-success partial result: %+v", got)
			}
			if test.err != nil && !slices.Contains(got.Files[0].Boundaries, test.name) {
				t.Fatalf("missing retained refusal: %v", got.Files[0].Boundaries)
			}
			if (len(s.exploreHistory.entries) == 0) != (test.err != nil) {
				t.Fatalf("mixed-read admission: status=%s boundaries=%v history=%d", got.Files[0].Status, got.Files[0].Boundaries, len(s.exploreHistory.entries))
			}
		})
	}
}
func TestExploreSessionAdmissionChecksAllRefusalOutcomes(t *testing.T) {
	for _, status := range []string{"unreadable", "oversized", "binary", "invalid_range", "source_unavailable"} {
		for _, where := range []string{"status", "boundary"} {
			t.Run(status+"/"+where, func(t *testing.T) {
				result := sessionBoundaryResult(strings.Repeat("hello\n", 8), 1, true)
				if where == "status" {
					result.Files[0].Status = status
				} else {
					result.Files[0].Boundaries = []string{"read_window", status, "source_window"}
				}
				if got := exploreDelivered(result); len(got) != 0 {
					t.Fatalf("refused source admitted %d lines via %s", len(got), where)
				}
			})
		}
	}
	for _, boundary := range []string{"read_window", "source_window", "truncated", "budget_exhausted", "source_read_limit", "source_read_bytes", "entity_invalid_range"} {
		t.Run("valid/"+boundary, func(t *testing.T) {
			result := sessionBoundaryResult(strings.Repeat("hello\n", 8), 1, true)
			result.Complete = false
			result.Files[0].Boundaries = []string{boundary}
			if len(exploreDelivered(result)) == 0 {
				t.Fatal("valid partial window was rejected")
			}
		})
	}
}
