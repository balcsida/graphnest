package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
		{"no token", map[string]string{"GRAPHNEST_SERVER_URL": "http://h"}, "graphnest login"},
		{"both tokens", map[string]string{"GRAPHNEST_SERVER_URL": "http://h", "GRAPHNEST_TOKEN": testToken, "GRAPHNEST_TOKEN_FILE": "tok"}, "GRAPHNEST_TOKEN_FILE"},
		{"unreadable token file", map[string]string{"GRAPHNEST_SERVER_URL": "http://h", "GRAPHNEST_TOKEN_FILE": "missing"}, "GRAPHNEST_TOKEN_FILE"},
		{"unreadable ca", map[string]string{"GRAPHNEST_SERVER_URL": "http://h", "GRAPHNEST_TOKEN": testToken, "GRAPHNEST_CA_FILE": "missing"}, "GRAPHNEST_CA_FILE"},
		{"token file", map[string]string{"GRAPHNEST_SERVER_URL": "http://h", "GRAPHNEST_TOKEN_FILE": "tok"}, ""},
	} {
		config, err := FromEnv(env(tc.env), read, Logins{})
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

func fromEnvMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnvPrecedence(t *testing.T) {
	logins := Logins{Dir: t.TempDir()}
	stored := Login{Server: "http://h", ClientID: "c", AccessToken: testAccess, RefreshToken: testRefresh, ExpiresAt: time.Now().Add(time.Hour), TokenEndpoint: "http://h/token"}
	if err := logins.Save(stored); err != nil {
		t.Fatal(err)
	}
	read := func(string) ([]byte, error) { return []byte("filetoken"), nil }
	config, err := FromEnv(fromEnvMap(map[string]string{"GRAPHNEST_SERVER_URL": "http://h"}), read, logins)
	if err != nil || config.Source != "stored login" || config.Login == nil || config.Login.RefreshToken != testRefresh || config.Token != "" {
		t.Fatalf("%+v %v", config, err)
	}
	config, err = FromEnv(fromEnvMap(map[string]string{"GRAPHNEST_SERVER_URL": "http://h", "GRAPHNEST_TOKEN": "envtoken"}), read, logins)
	if err != nil || config.Source != "GRAPHNEST_TOKEN" || config.Login != nil || config.Token != "envtoken" {
		t.Fatalf("%+v %v", config, err)
	}
	config, err = FromEnv(fromEnvMap(map[string]string{"GRAPHNEST_SERVER_URL": "http://h", "GRAPHNEST_TOKEN_FILE": "f"}), read, logins)
	if err != nil || config.Source != "GRAPHNEST_TOKEN_FILE" || config.Login != nil || config.Token != "filetoken" {
		t.Fatalf("%+v %v", config, err)
	}
	// A corrupt login file fails without echoing its contents.
	if err = os.WriteFile(filepath.Join(logins.Dir, loginFileName("http://h")), []byte("not json "+testRefresh), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = FromEnv(fromEnvMap(map[string]string{"GRAPHNEST_SERVER_URL": "http://h"}), read, logins)
	noSecrets(t, err)
}

// refreshFixture is a server that answers token-endpoint calls and records them.
type refreshFixture struct {
	server *httptest.Server
	forms  []url.Values
	auths  []string
	reply  func(form url.Values) (int, string)
	logins Logins
	stale  Login
}

func newRefreshFixture(t *testing.T, expiresIn time.Duration) *refreshFixture {
	t.Helper()
	f := &refreshFixture{logins: Logins{Dir: t.TempDir()}}
	f.reply = func(url.Values) (int, string) {
		return 200, `{"access_token":"rotated-s3cret","token_type":"Bearer","expires_in":3600,"refresh_token":"rotated-refresh","scope":"graph:write"}`
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			r.ParseForm()
			f.forms = append(f.forms, r.PostForm)
			status, body := f.reply(r.PostForm)
			w.WriteHeader(status)
			w.Write([]byte(body))
			return
		}
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		w.Write([]byte(`{"id":1,"name":"r"}`))
	}))
	t.Cleanup(f.server.Close)
	f.stale = Login{Server: f.server.URL, ClientID: "cid", AccessToken: testAccess, RefreshToken: testRefresh,
		ExpiresAt: time.Now().Add(expiresIn), TokenEndpoint: f.server.URL + "/token"}
	return f
}

func (f *refreshFixture) client(t *testing.T) *Client {
	t.Helper()
	login := f.stale
	c, err := New(Config{ServerURL: f.server.URL, Login: &login, Logins: f.logins})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClientRefreshesStoredLogin(t *testing.T) {
	f := newRefreshFixture(t, time.Minute)
	if _, err := f.client(t).Repository(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(f.forms) != 1 || f.forms[0].Get("grant_type") != "refresh_token" || f.forms[0].Get("refresh_token") != testRefresh || f.forms[0].Get("client_id") != "cid" {
		t.Fatalf("%v", f.forms)
	}
	if len(f.auths) != 1 || f.auths[0] != "Bearer rotated-s3cret" {
		t.Fatalf("%v", f.auths)
	}
	saved, ok, err := f.logins.Load(f.server.URL)
	if err != nil || !ok || saved.RefreshToken != "rotated-refresh" || saved.AccessToken != "rotated-s3cret" || saved.ClientID != "cid" {
		t.Fatalf("%+v %v %v", saved, ok, err)
	}

	f = newRefreshFixture(t, time.Hour)
	if _, err = f.client(t).Repository(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(f.forms) != 0 || len(f.auths) != 1 || f.auths[0] != "Bearer "+testAccess {
		t.Fatalf("%v %v", f.forms, f.auths)
	}
}

func TestClientAdoptsTokensRotatedByAnotherProcess(t *testing.T) {
	f := newRefreshFixture(t, time.Minute)
	f.reply = func(url.Values) (int, string) { return 400, `{"error":"invalid_grant"}` }
	rotated := f.stale
	rotated.AccessToken, rotated.RefreshToken, rotated.ExpiresAt = "rotated-s3cret", "rotated-refresh", time.Now().Add(time.Hour)
	if err := f.logins.Save(rotated); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client(t).Repository(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(f.forms) != 1 || len(f.auths) != 1 || f.auths[0] != "Bearer rotated-s3cret" {
		t.Fatalf("%v %v", f.forms, f.auths)
	}
}

func TestClientReportsExpiredLogin(t *testing.T) {
	f := newRefreshFixture(t, time.Minute)
	f.reply = func(url.Values) (int, string) { return 400, `{"error":"invalid_grant"}` }
	if err := f.logins.Save(f.stale); err != nil {
		t.Fatal(err)
	}
	_, err := f.client(t).Repository(context.Background(), 1)
	want := "the stored login for " + f.server.URL + " has expired or was revoked; run graphnest login"
	if err == nil || err.Error() != want || len(f.auths) != 0 {
		t.Fatalf("%v", err)
	}
	noSecrets(t, err)

	// A second invalid_grant after adopting a newer, soon-to-expire login is also expired.
	f = newRefreshFixture(t, time.Minute)
	f.reply = func(url.Values) (int, string) { return 400, `{"error":"invalid_grant"}` }
	newer := f.stale
	newer.RefreshToken, newer.ExpiresAt = "rotated-refresh", time.Now().Add(time.Minute)
	if err = f.logins.Save(newer); err != nil {
		t.Fatal(err)
	}
	_, err = f.client(t).Repository(context.Background(), 1)
	noSecrets(t, err)
	if len(f.forms) != 2 || err.Error() != "the stored login for "+f.server.URL+" has expired or was revoked; run graphnest login" {
		t.Fatalf("%v %v", f.forms, err)
	}
}
