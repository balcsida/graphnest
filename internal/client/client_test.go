package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/balcsida/graphnest/pkg/api"
)

const testToken = "s3cret-token"

func newClient(t *testing.T, url string) *Client {
	t.Helper()
	c, err := New(Config{ServerURL: url, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFromEnv(t *testing.T) {
	read := func(name string) ([]byte, error) {
		if name == "tok" {
			return []byte(" filetoken\n"), nil
		}
		return nil, errors.New("open " + name + ": nope")
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string // variable named in the error; empty means success
	}{
		{"missing url", map[string]string{"GRAPHNEST_TOKEN": testToken}, "GRAPHNEST_SERVER_URL"},
		{"no token", map[string]string{"GRAPHNEST_SERVER_URL": "http://h"}, "GRAPHNEST_TOKEN"},
		{"both tokens", map[string]string{"GRAPHNEST_SERVER_URL": "http://h", "GRAPHNEST_TOKEN": testToken, "GRAPHNEST_TOKEN_FILE": "tok"}, "GRAPHNEST_TOKEN_FILE"},
		{"unreadable token file", map[string]string{"GRAPHNEST_SERVER_URL": "http://h", "GRAPHNEST_TOKEN_FILE": "missing"}, "GRAPHNEST_TOKEN_FILE"},
		{"unreadable ca", map[string]string{"GRAPHNEST_SERVER_URL": "http://h", "GRAPHNEST_TOKEN": testToken, "GRAPHNEST_CA_FILE": "missing"}, "GRAPHNEST_CA_FILE"},
		{"token file", map[string]string{"GRAPHNEST_SERVER_URL": "http://h", "GRAPHNEST_TOKEN_FILE": "tok"}, ""},
	} {
		config, err := FromEnv(env(tc.env), read)
		if tc.want == "" {
			if err != nil || config.Token != "filetoken" {
				t.Fatalf("%s: %+v %v", tc.name, config, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), testToken) {
			t.Fatalf("%s: error %v", tc.name, err)
		}
	}
}

func TestNewRejectsNonHTTPURL(t *testing.T) {
	if _, err := New(Config{ServerURL: "ftp://h", Token: testToken}); err == nil {
		t.Fatal("ftp URL accepted")
	}
}

func TestPublishSendsExpectedRequest(t *testing.T) {
	var got struct {
		method, path, query, auth, contentType, body string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.method, got.path, got.query = r.Method, r.URL.Path, r.URL.RawQuery
		got.auth, got.contentType, got.body = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(body)
		w.Write([]byte(`{"repository_id":7,"commit":"abc","generation":3,"replaced_generation":2,"content_hash":"h","deduplicated":false}`))
	}))
	defer server.Close()
	c := newClient(t, server.URL+"/") // trailing slash tolerated
	result, err := c.Publish(context.Background(), 7, "abc", 2, true, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "POST" || got.path != "/v1/graph/uploads" || got.query != "commit=abc&expected_generation=2&replace_producer=true&repository_id=7" ||
		got.auth != "Bearer "+testToken || got.contentType != api.GraphArtifactV2ContentType || got.body != "payload" {
		t.Fatalf("request %+v", got)
	}
	if result.Generation != 3 || result.ReplacedGeneration != 2 {
		t.Fatalf("result %+v", result)
	}
	if _, err = c.Publish(context.Background(), 7, "abc", 0, false, nil); err != nil || got.query != "commit=abc&expected_generation=0&repository_id=7" {
		t.Fatalf("query %q err %v", got.query, err)
	}
}

func TestStatusAndRepository(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v1/repositories/5":
			w.Write([]byte(`{"id":5,"name":"o/n"}`))
		case "/v1/graph/repositories/5/status":
			w.Write([]byte(`{"repository_id":5,"state":"ready"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := newClient(t, server.URL)
	repository, err := c.Repository(context.Background(), 5)
	if err != nil || repository.Name != "o/n" {
		t.Fatalf("%+v %v", repository, err)
	}
	status, err := c.GraphStatus(context.Background(), 5)
	if err != nil || status.RepositoryID != 5 {
		t.Fatalf("%+v %v", status, err)
	}
}

func TestErrorEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("commit") == "plain" {
			w.WriteHeader(502)
			io.WriteString(w, "<html>"+r.Header.Get("Authorization")+"</html>")
			return
		}
		w.WriteHeader(409)
		io.WriteString(w, `{"error":{"code":"generation_conflict","message":"stale","request_id":"x","retryable":false}}`)
	}))
	defer server.Close()
	c := newClient(t, server.URL)
	_, err := c.Publish(context.Background(), 1, "abc", 1, false, nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || *apiErr != (Error{Status: 409, Code: "generation_conflict", Message: "stale"}) {
		t.Fatalf("got %#v", err)
	}
	_, err = c.Publish(context.Background(), 1, "plain", 1, false, nil)
	if !errors.As(err, &apiErr) || apiErr.Status != 502 || apiErr.Code != "" {
		t.Fatalf("got %#v", err)
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(apiErr.Message, testToken) {
		t.Fatal("token leaked into error")
	}
}

func TestTokenNeverInTransportError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	_, err := newClient(t, url).GraphStatus(context.Background(), 1)
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("err %v", err)
	}
}

func TestCrossOriginRedirectRefused(t *testing.T) {
	var hits atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer second.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL, http.StatusFound)
	}))
	defer first.Close()
	if _, err := newClient(t, first.URL).GraphStatus(context.Background(), 1); err == nil {
		t.Fatal("redirect followed")
	}
	if hits.Load() != 0 {
		t.Fatal("second server was contacted")
	}
}

func TestContextCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := newClient(t, server.URL).GraphStatus(ctx, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := newClient(t, server.URL).GraphStatus(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}
