package zoekt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/search"
)

func TestSearchUsesPinnedJSONContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/search" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		var body any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{
			"Q":       "needle",
			"RepoIDs": []any{float64(7)},
			"Opts": map[string]any{
				"NumContextLines":    float64(3),
				"MaxDocDisplayCount": float64(20),
				"MaxWallTime":        float64(time.Second),
			},
		}
		if !equalJSON(body, want) {
			t.Fatalf("body = %#v, want %#v", body, want)
		}
		_, _ = writer.Write([]byte(`{"Result":{"Files":[{"FileName":"main.go","Repository":"acme/one","Version":"abc123","Branches":["main"],"RepositoryID":7,"Score":4.5,"LineMatches":[{"Line":"ZnVuYyBtYWluKCkge30K","LineNumber":3,"LineStart":0,"LineEnd":15,"Score":4.5}]}]}}`))
	}))
	defer server.Close()

	client, err := New(server.URL, server.Client(), 1024, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Search(t.Context(), search.BackendRequest{Query: "needle", RepositoryIDs: []uint32{7}, Limit: 20, ContextLines: 3, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Matches) != 1 {
		t.Fatalf("matches = %#v", response.Matches)
	}
	match := response.Matches[0]
	if match.Path != "main.go" || match.ZoektID != 7 || match.SHA != "abc123" || match.LineNumber != 3 || match.Preview != "func main() {}\n" || match.Score != 4.5 {
		t.Fatalf("match = %#v", match)
	}
}

func equalJSON(got, want any) bool {
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	return string(gotJSON) == string(wantJSON)
}

func TestSearchSendsEmptyRepoIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			RepoIDs []uint32 `json:"RepoIDs"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.RepoIDs == nil || len(body.RepoIDs) != 0 {
			t.Fatalf("RepoIDs = %#v", body.RepoIDs)
		}
		_, _ = writer.Write([]byte(`{"Result":{"Files":[]}}`))
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client(), 1024, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Search(t.Context(), search.BackendRequest{Query: "needle"}); err != nil {
		t.Fatal(err)
	}
}

func TestSearchClassifiesFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"bad request", http.StatusBadRequest, `{}`, ErrInvalidQuery},
		{"server error", http.StatusInternalServerError, `{}`, ErrUnavailable},
		{"invalid JSON", http.StatusOK, `{`, ErrUnavailable},
		{"zoekt error", http.StatusOK, `{"Error":"bad query"}`, ErrInvalidQuery},
		{"trailing JSON", http.StatusOK, `{} {}`, ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			client, err := New(server.URL, server.Client(), 1024, observability.New())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Search(t.Context(), search.BackendRequest{Query: "needle"})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestSearchRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"Result":{"Files":[]}}`)
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client(), 4, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Search(t.Context(), search.BackendRequest{Query: "needle"})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func TestNewClampsResponseLimitToCanonicalCeiling(t *testing.T) {
	client, err := New("http://example.test", http.DefaultClient, 512<<10, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	if client.maxBytes != 256<<10 {
		t.Fatalf("maxBytes = %d, want %d", client.maxBytes, 256<<10)
	}
}

func TestSearchAppliesCanonicalCeilingToUpstreamBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, strings.Repeat("x", 256<<10+1))
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client(), 512<<10, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Search(t.Context(), search.BackendRequest{Query: "needle"})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func TestSearchRejectsRedirectWithoutMutatingCallerClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "/other", http.StatusFound)
	}))
	defer server.Close()
	httpClient := server.Client()
	if httpClient.CheckRedirect != nil {
		t.Fatal("unexpected redirect policy")
	}
	client, err := New(server.URL, httpClient, 1024, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Search(t.Context(), search.BackendRequest{Query: "needle"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if httpClient.CheckRedirect != nil {
		t.Fatal("caller redirect policy changed")
	}
}

func TestSearchPreservesCancellation(t *testing.T) {
	client, err := New("http://127.0.0.1:1", http.DefaultClient, 1024, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.Search(ctx, search.BackendRequest{Query: "needle"})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestSearchHonorsRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = writer.Write([]byte(`{"Result":{"Files":[]}}`))
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client(), 1024, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Search(t.Context(), search.BackendRequest{Query: "needle", Timeout: time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestNormalizeLimitsPreviewBytes(t *testing.T) {
	response := normalize([]wireFile{{RepositoryID: 7, LineMatches: []wireMatch{{Line: []byte("abcdef")}}}}, 4)
	if got := response.Matches[0].Preview; got != "abcd" {
		t.Fatalf("preview = %q", got)
	}
}

func TestNormalizeClampsDirectCallersToCanonicalCeiling(t *testing.T) {
	line := []byte(strings.Repeat("x", 256<<10+1))
	response := normalize([]wireFile{{LineMatches: []wireMatch{{Line: line}}}}, 512<<10)
	if got := len(response.Matches[0].Preview); got != 256<<10 {
		t.Fatalf("preview bytes = %d, want %d", got, 256<<10)
	}
}

func TestNormalizePreservesUTF8AtPreviewBoundary(t *testing.T) {
	response := normalize([]wireFile{{LineMatches: []wireMatch{{Line: []byte("a€")}}}}, 2)
	preview := response.Matches[0].Preview
	if !utf8.ValidString(preview) || len(preview) > 2 || preview != "a" {
		t.Fatalf("preview = %q (%d bytes)", preview, len(preview))
	}
}

func TestHealthUsesEmptyRepoIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			RepoIDs []uint32 `json:"RepoIDs"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.RepoIDs == nil || len(body.RepoIDs) != 0 {
			t.Fatalf("RepoIDs = %#v", body.RepoIDs)
		}
		_, _ = writer.Write([]byte(`{"Result":{"Files":[]}}`))
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client(), 1024, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Health(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSearchRecordsBackendFailure(t *testing.T) {
	metrics := observability.New()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusInternalServerError) }))
	defer server.Close()
	client, err := New(server.URL, server.Client(), 1024, metrics)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Search(t.Context(), search.BackendRequest{Query: "needle"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(recorder.Body.String(), `grepnest_search_backend_calls_total{result="error"} 1`) {
		t.Fatalf("metrics = %s", recorder.Body.String())
	}
}
