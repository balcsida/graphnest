package cli

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/client"
)

const (
	issuedAccess  = "gnt_issued_access"
	issuedRefresh = "gnr_issued_refresh"
	issuedCode    = "gnc_issued_code"
)

// fakeAuth is an authorization server that approves (or denies) every request and records what it saw.
type fakeAuth struct {
	*httptest.Server
	mu          sync.Mutex
	deny        bool
	badIssuer   bool
	foreignAuth bool
	denyText    string // error_description sent when denying; empty means "user said no"
	revokeCode  int
	authorize   url.Values
	challenge   string
	revoked     []url.Values
	bearers     []string
	registerURI string
}

func newFakeAuth(t *testing.T) *fakeAuth {
	t.Helper()
	f := &fakeAuth{revokeCode: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		issuer := f.URL
		if f.badIssuer {
			issuer = "https://elsewhere.example"
		}
		authorize := f.URL + "/oauth/authorize"
		if f.foreignAuth {
			authorize = "https://evil.example/authorize"
		}
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer, "authorization_endpoint": authorize, "token_endpoint": f.URL + "/oauth/token",
			"registration_endpoint": f.URL + "/oauth/register", "revocation_endpoint": f.URL + "/oauth/revoke",
			"code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("/oauth/register", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RedirectURIs []string `json:"redirect_uris"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.registerURI = body.RedirectURIs[0]
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"client_id":"gnc_new"}`))
	})
	mux.HandleFunc("/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		f.authorize = q
		f.challenge = q.Get("code_challenge")
		f.mu.Unlock()
		if q.Get("client_id") != "gnc_new" || q.Get("redirect_uri") != f.registerURI || q.Get("code_challenge_method") != "S256" || q.Has("resource") {
			http.Error(w, "bad authorization request", http.StatusBadRequest)
			return
		}
		target, _ := url.Parse(q.Get("redirect_uri"))
		out := url.Values{"state": {q.Get("state")}}
		if f.deny {
			out.Set("error", "access_denied")
			out.Set("error_description", "user said no")
			if f.denyText != "" {
				out.Set("error_description", f.denyText)
			}
		} else {
			out.Set("code", issuedCode)
		}
		target.RawQuery = out.Encode()
		http.Redirect(w, r, target.String(), http.StatusFound)
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		f.mu.Lock()
		want := f.challenge
		f.mu.Unlock()
		if r.Form.Get("code") != issuedCode || r.Form.Get("client_id") != "gnc_new" || r.Form.Get("redirect_uri") != f.registerURI ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != want {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": issuedAccess, "token_type": "Bearer", "expires_in": 3600, "refresh_token": issuedRefresh})
	})
	mux.HandleFunc("/oauth/revoke", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		f.revoked = append(f.revoked, r.Form)
		f.mu.Unlock()
		w.WriteHeader(f.revokeCode)
	})
	api := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			f.bearers = append(f.bearers, r.Header.Get("Authorization"))
			f.mu.Unlock()
			if r.Header.Get("Authorization") != "Bearer "+issuedAccess {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(body))
		}
	}
	mux.HandleFunc("/v1/repositories/9", api(`{"id":9,"name":"o/n","status":"indexed"}`))
	mux.HandleFunc("/v1/graph/repositories/9/status", api(`{"repository_id":9,"state":"current"}`))
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// browser GETs the URL in a goroutine, following redirects to the loopback callback.
func browser(visited chan<- string) func(string) error {
	return func(u string) error {
		go func() {
			if visited != nil {
				visited <- u
			}
			if response, err := http.Get(u); err == nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
			}
		}()
		return nil
	}
}

type loginSetup struct {
	env    Environment
	config string
	auth   *fakeAuth
}

func (s loginSetup) logins() client.Logins {
	return client.Logins{Dir: filepath.Join(s.config, "graphnest", "credentials")}
}

func newLoginSetup(t *testing.T, vars map[string]string, open func(string) error) loginSetup {
	t.Helper()
	auth := newFakeAuth(t)
	config := t.TempDir()
	all := map[string]string{"GRAPHNEST_SERVER_URL": auth.URL}
	for k, v := range vars {
		all[k] = v
	}
	env := testEnv(all)
	env.ConfigDir = func() (string, error) { return config, nil }
	env.OpenBrowser = open
	return loginSetup{env, config, auth}
}

func assertNoSecrets(t *testing.T, outputs ...string) {
	t.Helper()
	for _, out := range outputs {
		for _, secret := range []string{issuedAccess, issuedRefresh, issuedCode, "gnr_old", token} {
			if strings.Contains(out, secret) {
				t.Errorf("output leaks %q: %q", secret, out)
			}
		}
	}
}

func TestLoginStoresTokensAndCommandsUseThem(t *testing.T) {
	s := newLoginSetup(t, nil, browser(nil))
	code, stdout, stderr := run(t, s.env, "login")
	if code != 0 || stdout != "" {
		t.Fatalf("code %d stdout %q stderr %q", code, stdout, stderr)
	}
	for _, want := range []string{"Sign in to " + s.auth.URL + " in your browser. If it does not open, visit:\n" + s.auth.URL + "/oauth/authorize?", "Signed in to " + s.auth.URL + "."} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q: %q", want, stderr)
		}
	}
	redirect := s.auth.authorize.Get("redirect_uri")
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") || !strings.HasSuffix(redirect, "/callback") || s.auth.authorize.Get("scope") != "graph:write" ||
		s.auth.authorize.Get("code_challenge_method") != "S256" || s.auth.authorize.Has("resource") {
		t.Errorf("authorize request %v", s.auth.authorize)
	}
	entries, err := os.ReadDir(s.logins().Dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("%v %v", err, entries)
	}
	if info, _ := entries[0].Info(); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode())
	}
	assertNoSecrets(t, stdout, stderr)

	code, stdout, stderr = run(t, s.env, "graph", "status", "--repository-id", "9")
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"id": 9`) {
		t.Fatalf("status: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	if got := s.auth.bearers; len(got) == 0 || got[0] != "Bearer "+issuedAccess {
		t.Errorf("bearers %v", got)
	}
}

func TestLoginIgnoresForgedCallback(t *testing.T) {
	forgedCh := make(chan string, 1)
	open := func(u string) error {
		go func() {
			parsed, _ := url.Parse(u)
			redirect := parsed.Query().Get("redirect_uri")
			response, err := http.Get(redirect + "?code=evil&state=wrong")
			if err == nil {
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				forgedCh <- response.Status + " " + string(body)
			}
			if response, err = http.Get(u); err == nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
			}
		}()
		return nil
	}
	s := newLoginSetup(t, nil, open)
	if code, _, stderr := run(t, s.env, "login"); code != 0 {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	var forged string
	select {
	case forged = <-forgedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("the browser recorded no forged response")
	}
	if !strings.HasPrefix(forged, "400") || !strings.Contains(forged, "This response does not belong to the running graphnest login.") {
		t.Errorf("forged response %q", forged)
	}
}

func TestLoginServesOnlyGetCallback(t *testing.T) {
	statusCh := make(chan int, 2)
	open := func(u string) error {
		go func() {
			redirect, _ := url.Parse(u)
			callback := redirect.Query().Get("redirect_uri")
			state := redirect.Query().Get("state")
			post, err := http.Post(callback+"?code=evil&state="+state, "text/plain", nil)
			if err == nil {
				post.Body.Close()
				statusCh <- post.StatusCode
			}
			base, _ := url.Parse(callback)
			base.Path = "/other"
			other, err := http.Get(base.String())
			if err == nil {
				other.Body.Close()
				statusCh <- other.StatusCode
			}
			if response, err := http.Get(u); err == nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
			}
		}()
		return nil
	}
	s := newLoginSetup(t, nil, open)
	if code, _, stderr := run(t, s.env, "login"); code != 0 {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	for range 2 {
		select {
		case status := <-statusCh:
			if status != 404 {
				t.Errorf("status %d", status)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the browser recorded too few statuses")
		}
	}
}

func TestLoginReportsDeniedAuthorization(t *testing.T) {
	s := newLoginSetup(t, nil, browser(nil))
	s.auth.deny = true
	code, stdout, stderr := run(t, s.env, "login")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "authorization failed: access_denied: user said no") {
		t.Fatalf("code %d stdout %q stderr %q", code, stdout, stderr)
	}
	if _, err := os.Stat(s.logins().Dir); err == nil {
		if entries, _ := os.ReadDir(s.logins().Dir); len(entries) != 0 {
			t.Errorf("login stored: %v", entries)
		}
	}
}

func TestLoginTimesOut(t *testing.T) {
	s := newLoginSetup(t, nil, func(string) error { return nil })
	code, _, stderr := run(t, s.env, "login", "--timeout", "200ms")
	if code != 1 || !strings.Contains(stderr, "timed out waiting for the browser sign-in") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	redirect, _ := url.Parse(s.auth.registerURI)
	if conn, err := net.DialTimeout("tcp", redirect.Host, time.Second); err == nil {
		conn.Close()
		t.Errorf("%s still accepts connections", redirect.Host)
	}
}

func TestLoginRejectsIssuerMismatch(t *testing.T) {
	opened := false
	s := newLoginSetup(t, nil, func(string) error { opened = true; return nil })
	s.auth.badIssuer = true
	code, _, stderr := run(t, s.env, "login")
	if code != 1 || opened || !strings.Contains(stderr, "issuer") {
		t.Fatalf("code %d opened %v stderr %q", code, opened, stderr)
	}
}

func TestLoginRejectsForeignAuthorizationEndpoint(t *testing.T) {
	opened := false
	s := newLoginSetup(t, nil, func(string) error { opened = true; return nil })
	s.auth.foreignAuth = true
	code, _, stderr := run(t, s.env, "login")
	if code != 1 || opened || !strings.Contains(stderr, `authorization server endpoint "https://evil.example/authorize" is not on `+s.auth.URL) {
		t.Fatalf("code %d opened %v stderr %q", code, opened, stderr)
	}
}

func TestLoginStripsControlCharactersFromAuthorizationError(t *testing.T) {
	s := newLoginSetup(t, nil, browser(nil))
	s.auth.deny, s.auth.denyText = true, "no\x1b[2Jway"
	code, _, stderr := run(t, s.env, "login")
	if code != 1 || strings.ContainsRune(stderr, 0x1b) || !strings.Contains(stderr, "authorization failed: access_denied: no[2Jway") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}

func TestLoginRevokesPreviousGrant(t *testing.T) {
	s := newLoginSetup(t, nil, browser(nil))
	old := client.Login{Server: s.auth.URL, ClientID: "gnc_old", AccessToken: "gnt_old", RefreshToken: "gnr_old",
		ExpiresAt: time.Now().Add(time.Hour), TokenEndpoint: s.auth.URL + "/oauth/token", RevocationEndpoint: s.auth.URL + "/oauth/revoke"}
	if err := s.logins().Save(old); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := run(t, s.env, "login")
	if code != 0 {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	if len(s.auth.revoked) != 1 || s.auth.revoked[0].Get("token") != "gnr_old" || s.auth.revoked[0].Get("client_id") != "gnc_old" {
		t.Errorf("revoked %v", s.auth.revoked)
	}
	stored, ok, err := s.logins().Load(s.auth.URL)
	if err != nil || !ok || stored.AccessToken != issuedAccess || stored.RefreshToken != issuedRefresh {
		t.Errorf("stored %+v %v %v", stored, ok, err)
	}
	assertNoSecrets(t, stderr)
}

func TestLoginNotesTokenPrecedence(t *testing.T) {
	s := newLoginSetup(t, map[string]string{"GRAPHNEST_TOKEN": token}, browser(nil))
	code, _, stderr := run(t, s.env, "login")
	if code != 0 || !strings.Contains(stderr, "note: GRAPHNEST_TOKEN or GRAPHNEST_TOKEN_FILE is set and takes precedence over this login") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	assertNoSecrets(t, stderr)
}

func TestLogout(t *testing.T) {
	s := newLoginSetup(t, nil, nil)
	login := client.Login{Server: s.auth.URL, ClientID: "gnc_old", AccessToken: "gnt_old", RefreshToken: "gnr_old",
		ExpiresAt: time.Now().Add(time.Hour), TokenEndpoint: s.auth.URL + "/oauth/token", RevocationEndpoint: s.auth.URL + "/oauth/revoke"}
	if err := s.logins().Save(login); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, s.env, "logout")
	if code != 0 || stdout != "" || !strings.Contains(stderr, "Signed out of "+s.auth.URL+".") {
		t.Fatalf("code %d stdout %q stderr %q", code, stdout, stderr)
	}
	if len(s.auth.revoked) != 1 || s.auth.revoked[0].Get("token") != "gnr_old" {
		t.Errorf("revoked %v", s.auth.revoked)
	}
	if _, ok, _ := s.logins().Load(s.auth.URL); ok {
		t.Error("login still stored")
	}
	code, _, stderr = run(t, s.env, "logout")
	if code != 0 || !strings.Contains(stderr, "Not signed in to "+s.auth.URL+".") {
		t.Fatalf("second logout: code %d stderr %q", code, stderr)
	}

	s.auth.revokeCode = http.StatusInternalServerError
	if err := s.logins().Save(login); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = run(t, s.env, "logout")
	if code != 1 || !strings.Contains(stderr, "signed out locally, but the server did not revoke the grant") || !strings.Contains(stderr, "Connected MCP clients") {
		t.Fatalf("failing revocation: code %d stderr %q", code, stderr)
	}
	if _, ok, _ := s.logins().Load(s.auth.URL); ok {
		t.Error("login still stored after failed revocation")
	}
	assertNoSecrets(t, stderr)
}

func TestLogoutDeletesUnreadableLogin(t *testing.T) {
	s := newLoginSetup(t, nil, nil)
	if err := os.MkdirAll(s.logins().Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.logins().Save(client.Login{Server: s.auth.URL, RefreshToken: "gnr_old"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(s.logins().Dir)
	file := filepath.Join(s.logins().Dir, entries[0].Name())
	if err := os.WriteFile(file, []byte("not json gnr_old"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := run(t, s.env, "logout")
	want := "the stored login for " + s.auth.URL + ` was unreadable and has been deleted; disconnect "graphnest CLI" under Account → Connected MCP clients`
	if code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("unreadable login not deleted: %v", err)
	}
	assertNoSecrets(t, stderr)
}

func TestLoginUsage(t *testing.T) {
	s := newLoginSetup(t, nil, nil)
	for _, args := range [][]string{{"login", "extra"}, {"login", "--timeout", "0s"}, {"logout", "extra"}} {
		if code, stdout, _ := run(t, s.env, args...); code != 2 || stdout != "" {
			t.Errorf("%v: code %d stdout %q", args, code, stdout)
		}
	}
}
