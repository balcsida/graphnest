package client

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	testAccess   = "acc-s3cret"
	testRefresh  = "ref-s3cret"
	testCode     = "code-s3cret"
	testVerifier = "verifier-s3cret"
)

// noSecrets fails when an error message leaks a token, code or verifier.
func noSecrets(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, secret := range []string{testAccess, testRefresh, testCode, testVerifier, "rotated-s3cret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks a secret: %v", err)
		}
	}
}

func TestOriginNormalizes(t *testing.T) {
	got, err := Origin("https://Graph.Example:8443/x?y")
	if err != nil || got != "https://graph.example:8443" {
		t.Fatalf("%q %v", got, err)
	}
	for _, bad := range []string{"ftp://h", "http://", "", "h"} {
		if _, err := Origin(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestNewSecret(t *testing.T) {
	a, b := NewSecret(), NewSecret()
	raw, err := base64.RawURLEncoding.DecodeString(a)
	if len(a) != 43 || err != nil || len(raw) != 32 || a == b {
		t.Fatalf("%q %q %v", a, b, err)
	}
}

// metadataServer serves RFC 8414 metadata with the given issuer ("" means the server's own origin) and methods.
func metadataServer(t *testing.T, issuer string, methods []string, extra http.HandlerFunc) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server" {
			if extra != nil {
				extra(w, r)
				return
			}
			http.NotFound(w, r)
			return
		}
		iss := issuer
		if iss == "" {
			iss = server.URL
		}
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": iss, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token",
			"registration_endpoint": server.URL + "/register", "revocation_endpoint": server.URL + "/revoke",
			"code_challenge_methods_supported": methods,
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDiscoverOAuth(t *testing.T) {
	ok := metadataServer(t, "", []string{"S256"}, nil)
	if _, err := DiscoverOAuth(context.Background(), Config{ServerURL: ok.URL + "/some/path"}); err != nil {
		t.Fatal(err)
	}
	mismatch := metadataServer(t, "https://evil.example", []string{"S256"}, nil)
	_, err := DiscoverOAuth(context.Background(), Config{ServerURL: mismatch.URL})
	want := `authorization server issuer "https://evil.example" does not match "` + mismatch.URL + `"`
	if err == nil || err.Error() != want {
		t.Fatalf("%v, want %s", err, want)
	}
	noPKCE := metadataServer(t, "", []string{"plain"}, nil)
	if _, err = DiscoverOAuth(context.Background(), Config{ServerURL: noPKCE.URL}); err == nil || err.Error() != "authorization server does not support PKCE S256" {
		t.Fatal(err)
	}
	missing := httptest.NewServer(http.NotFoundHandler())
	defer missing.Close()
	_, err = DiscoverOAuth(context.Background(), Config{ServerURL: missing.URL})
	if err == nil || err.Error() != "server does not offer OAuth sign-in (GRAPHNEST_MCP_OAUTH is off); set GRAPHNEST_TOKEN instead" {
		t.Fatal(err)
	}
}

func discover(t *testing.T, server *httptest.Server) *OAuth {
	t.Helper()
	o, err := DiscoverOAuth(context.Background(), Config{ServerURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestRegisterSendsPublicClient(t *testing.T) {
	var got map[string]any
	server := metadataServer(t, "", []string{"S256"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/register" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s %s %s", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"client_id":"cid-1"}`))
	})
	id, err := discover(t, server).Register(context.Background(), "http://127.0.0.1:5555/callback")
	if err != nil || id != "cid-1" {
		t.Fatalf("%q %v", id, err)
	}
	want := map[string]any{
		"client_name": "graphnest CLI", "redirect_uris": []any{"http://127.0.0.1:5555/callback"},
		"grant_types": []any{"authorization_code", "refresh_token"}, "response_types": []any{"code"},
		"token_endpoint_auth_method": "none",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

func TestAuthorizationURL(t *testing.T) {
	server := metadataServer(t, "", []string{"S256"}, nil)
	raw := discover(t, server).AuthorizationURL("cid", "http://127.0.0.1:1/callback", "st", testVerifier)
	u, err := url.Parse(raw)
	if err != nil || !strings.HasPrefix(raw, server.URL+"/authorize?") {
		t.Fatalf("%s %v", raw, err)
	}
	sum := sha256.Sum256([]byte(testVerifier))
	want := url.Values{
		"response_type": {"code"}, "client_id": {"cid"}, "redirect_uri": {"http://127.0.0.1:1/callback"}, "state": {"st"},
		"scope": {"graph:write"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])},
	}
	if !reflect.DeepEqual(u.Query(), want) {
		t.Fatalf("%v", u.Query())
	}
}

func TestExchange(t *testing.T) {
	var form url.Values
	reply := `{"access_token":"` + testAccess + `","token_type":"bearer","expires_in":3600,"refresh_token":"` + testRefresh + `","scope":"graph:write"}`
	status := http.StatusOK
	server := metadataServer(t, "", []string{"S256"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("bad headers %v", r.Header)
		}
		r.ParseForm()
		form = r.PostForm
		w.WriteHeader(status)
		w.Write([]byte(reply))
	})
	o := discover(t, server)
	login, err := o.Exchange(context.Background(), "cid", "http://127.0.0.1:1/callback", testCode, testVerifier)
	if err != nil {
		t.Fatal(err)
	}
	wantForm := url.Values{"grant_type": {"authorization_code"}, "code": {testCode}, "redirect_uri": {"http://127.0.0.1:1/callback"}, "client_id": {"cid"}, "code_verifier": {testVerifier}}
	if !reflect.DeepEqual(form, wantForm) {
		t.Fatalf("%v", form)
	}
	if login.Server != server.URL || login.ClientID != "cid" || login.AccessToken != testAccess || login.RefreshToken != testRefresh ||
		login.TokenEndpoint != server.URL+"/token" || login.RevocationEndpoint != server.URL+"/revoke" ||
		time.Until(login.ExpiresAt) < time.Hour-time.Second || time.Until(login.ExpiresAt) > time.Hour+time.Second {
		t.Fatalf("%+v", login)
	}
	for name, body := range map[string]string{
		"no refresh token": `{"access_token":"` + testAccess + `","token_type":"Bearer","expires_in":60}`,
		"mac token type":   `{"access_token":"` + testAccess + `","token_type":"mac","expires_in":60,"refresh_token":"` + testRefresh + `"}`,
		"no expiry":        `{"access_token":"` + testAccess + `","token_type":"Bearer","refresh_token":"` + testRefresh + `"}`,
	} {
		reply = body
		if _, err = o.Exchange(context.Background(), "cid", "r", testCode, testVerifier); err == nil {
			t.Fatalf("%s accepted", name)
		}
		noSecrets(t, err)
	}
	status, reply = http.StatusBadRequest, `{"error":"invalid_grant","error_description":"bad code"}`
	_, err = o.Exchange(context.Background(), "cid", "r", testCode, testVerifier)
	noSecrets(t, err)
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatal(err)
	}
}

func TestRevokeLogin(t *testing.T) {
	var form url.Values
	status := http.StatusOK
	server := metadataServer(t, "", []string{"S256"}, func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		form = r.PostForm
		w.WriteHeader(status)
	})
	login := Login{Server: server.URL, ClientID: "cid", AccessToken: testAccess, RefreshToken: testRefresh, RevocationEndpoint: server.URL + "/revoke"}
	if err := RevokeLogin(context.Background(), Config{ServerURL: server.URL}, login); err != nil {
		t.Fatal(err)
	}
	if want := (url.Values{"token": {testRefresh}, "token_type_hint": {"refresh_token"}, "client_id": {"cid"}}); !reflect.DeepEqual(form, want) {
		t.Fatalf("%v", form)
	}
	status = http.StatusBadRequest
	noSecrets(t, RevokeLogin(context.Background(), Config{ServerURL: server.URL}, login))
	login.RevocationEndpoint = ""
	noSecrets(t, RevokeLogin(context.Background(), Config{ServerURL: server.URL}, login))
}

func TestDiscoverOAuthRequiresEndpointsOnOrigin(t *testing.T) {
	serve := func(overrides map[string]string) *httptest.Server {
		var server *httptest.Server
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			metadata := map[string]any{
				"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token",
				"registration_endpoint": server.URL + "/register", "revocation_endpoint": server.URL + "/revoke",
				"code_challenge_methods_supported": []string{"S256"},
			}
			for k, v := range overrides {
				if v == "" {
					delete(metadata, k)
				} else {
					metadata[k] = v
				}
			}
			json.NewEncoder(w).Encode(metadata)
		}))
		t.Cleanup(server.Close)
		return server
	}
	for key, foreign := range map[string]string{
		"authorization_endpoint": "file:///Applications/Calculator.app",
		"token_endpoint":         "https://evil.example/token",
		"revocation_endpoint":    "https://evil.example/revoke",
	} {
		server := serve(map[string]string{key: foreign})
		_, err := DiscoverOAuth(context.Background(), Config{ServerURL: server.URL})
		if want := `authorization server endpoint "` + foreign + `" is not on ` + server.URL; err == nil || err.Error() != want {
			t.Fatalf("%s: %v, want %s", key, err, want)
		}
	}
	server := serve(map[string]string{"revocation_endpoint": ""})
	if _, err := DiscoverOAuth(context.Background(), Config{ServerURL: server.URL}); err != nil {
		t.Fatal(err)
	}
}

func TestStripControlKeepsEscapeOutOfTokenErrors(t *testing.T) {
	if got := StripControl("a\x1b[31mb\n\x7fc"); got != "a[31mbc" {
		t.Fatalf("%q", got)
	}
	err := &oauthError{Code: "bad\x1b", Description: "evil \x1b[2J text"}
	if strings.ContainsRune(err.Error(), 0x1b) || err.Error() != "token endpoint: bad: evil [2J text" {
		t.Fatalf("%q", err)
	}
}
