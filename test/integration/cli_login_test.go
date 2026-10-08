//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/cli"
	"github.com/balcsida/graphnest/internal/client"
	"github.com/balcsida/graphnest/internal/graphingest"
	"github.com/balcsida/graphnest/internal/httpapi"
	"github.com/balcsida/graphnest/internal/oauthas"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
)

var consentRequestID = regexp.MustCompile(`name="request_id" value="([^"]+)"`)

// consentFlow signs the session's user in at authorizationURL as a browser would: it reads the
// consent page, allows it and returns the callback URL the server redirects to.
func consentFlow(ctx context.Context, browser *http.Client, origin, authorizationURL string) (string, error) {
	response, err := browser.Get(authorizationURL)
	if err != nil {
		return "", err
	}
	page, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || strings.Contains(authorizationURL, "graph%3Awrite") != strings.Contains(string(page), "publish code graphs") {
		return "", errors.New("consent page: HTTP " + response.Status)
	}
	match := consentRequestID.FindSubmatch(page)
	if match == nil {
		return "", errors.New("consent page has no request_id")
	}
	form := url.Values{"request_id": {string(match[1])}, "decision": {"allow"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/oauth/authorize", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", origin)
	response, err = browser.Do(request)
	if err != nil {
		return "", err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		return "", errors.New("consent decision: HTTP " + response.Status)
	}
	return response.Header.Get("Location"), nil
}

// TestCLILoginPublishesWithGraphWriteScope signs in through the real authorization server, publishes
// with the resulting OAuth token, refreshes it, and signs out.
func TestCLILoginPublishesWithGraphWriteScope(t *testing.T) {
	h := newPostgresHarness(t)
	internalID := h.seedRepository(t, 10, cliPublishGitHub)
	setGraphCommit(t, h, internalID, cliPublishSHA)
	if err := h.store.UpsertSearchNode(t.Context(), "node-a", "http://search.invalid"); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := h.pool.QueryRow(t.Context(), `insert into users (external_id, user_name, source) values ($1, $1, 'scim') returning id`, "cli-login-user").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(t.Context(), `insert into user_repository_grants (user_id, repository_id) values ($1, $2)`, userID, cliPublishGitHub); err != nil {
		t.Fatal(err)
	}
	subject := strconv.FormatInt(userID, 10)
	if err := h.store.SetGraphPublicationGrant(t.Context(), internalID, subject, "admin", true); err != nil {
		t.Fatal(err)
	}
	sessions := &authn.SessionManager{Store: h.store, IdleTTL: time.Hour, TTL: 2 * time.Hour}
	sessionToken, _, err := sessions.CreateForUser(t.Context(), userID, authn.ProviderOIDC, false)
	if err != nil {
		t.Fatal(err)
	}

	var handler http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { handler.ServeHTTP(writer, request) }))
	server.StartTLS()
	t.Cleanup(server.Close)
	bearer := authn.BearerRouter{APITokens: authn.TokenManager{Store: h.store}, OAuth: authn.OAuthTokenAuthenticator{Store: h.store}}
	mux := http.NewServeMux()
	(&oauthas.Server{Origin: server.URL, Store: h.store, Sessions: sessions, Limiter: h.store, LoginPath: "/login"}).Register(mux)
	httpapi.RegisterRepositoryInventory(mux, authn.RequestAuthenticator{Bearer: bearer, Session: sessions, PublicOrigin: server.URL}, &repository.Service{Store: h.store, SCIP: h.store, Graph: h.store}, 100, 256<<10)
	httpapi.RegisterGraphIngestion(mux, bearer, &graphingest.Service{Store: h.store, MaxUploadBytes: 8 << 20}, nil, 8<<20, 1<<20)
	handler = mux

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	serverURL, _ := url.Parse(server.URL)
	jar.SetCookies(serverURL, []*http.Cookie{{Name: authn.SessionCookieName, Value: sessionToken, Path: "/", Secure: true}})
	browser := &http.Client{Transport: server.Client().Transport, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	configDir, caFile := t.TempDir(), filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	browserDone := make(chan error, 1)
	env := cli.OSEnvironment()
	env.Getenv = func(key string) string {
		return map[string]string{"GRAPHNEST_SERVER_URL": server.URL, "GRAPHNEST_CA_FILE": caFile}[key]
	}
	env.Repository = cliFixtureRepository{}
	env.Sleep = func(context.Context, time.Duration) error { return nil }
	env.ConfigDir = func() (string, error) { return configDir, nil }
	env.OpenBrowser = func(authorizationURL string) error {
		go func() {
			callback, err := consentFlow(t.Context(), browser, server.URL, authorizationURL)
			if err == nil {
				var response *http.Response
				if response, err = http.Get(callback); err == nil {
					response.Body.Close()
					if response.StatusCode != http.StatusOK {
						err = errors.New("callback: HTTP " + response.Status)
					}
				}
			}
			browserDone <- err
		}()
		return nil
	}
	run := func(args ...string) (int, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := cli.Run(t.Context(), args, env, &stdout, &stderr)
		return code, stdout.String() + "\x00" + stderr.String()
	}
	loginPath := filepath.Join(configDir, "graphnest", "credentials", func() string { sum := sha256.Sum256([]byte(server.URL)); return hex.EncodeToString(sum[:]) }()+".json")
	readLogin := func() client.Login {
		t.Helper()
		data, err := os.ReadFile(loginPath)
		if err != nil {
			t.Fatal(err)
		}
		var login client.Login
		if err := json.Unmarshal(data, &login); err != nil {
			t.Fatal(err)
		}
		return login
	}
	noSecrets := func(output string, login client.Login) {
		t.Helper()
		for _, secret := range []string{login.AccessToken, login.RefreshToken} {
			if secret == "" || strings.Contains(output, secret) {
				t.Fatalf("a token is empty or was printed: %q", output)
			}
		}
	}

	// 1. login
	code, output := run("login")
	if code != 0 {
		t.Fatalf("login code=%d %q", code, output)
	}
	if err := <-browserDone; err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(loginPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("login file: %v %v", info, err)
	}
	first := readLogin()
	noSecrets(output, first)

	// 2. graph status
	code, output = run("graph", "status", "--repository-id", cliPublishRepo)
	if code != 0 {
		t.Fatalf("status code=%d %q", code, output)
	}
	var status statusReportForTest
	if err := json.Unmarshal([]byte(strings.SplitN(output, "\x00", 2)[0]), &status); err != nil || status.Graph.Publication == nil || !status.Graph.Publication.Permitted {
		t.Fatalf("status=%q err=%v", output, err)
	}
	noSecrets(output, first)

	// 3. publish
	code, output = run(cliImportArgs(cliPublishSHA)...)
	if code != 0 {
		t.Fatalf("import code=%d %q", code, output)
	}
	if outcome := decodeOutcome(t, strings.SplitN(output, "\x00", 2)[0]); !outcome.Published {
		t.Fatalf("outcome=%q", output)
	}
	noSecrets(output, first)
	var publisher string
	if err := h.pool.QueryRow(t.Context(), `select publisher from graph_uploads where repository_id=$1 order by id desc limit 1`, internalID).Scan(&publisher); err != nil || publisher != "oauth_token:"+subject {
		t.Fatalf("publisher=%q err=%v", publisher, err)
	}

	// 4. an expired access token is refreshed
	expired := first
	expired.ExpiresAt = time.Now().Add(-time.Hour)
	data, _ := json.Marshal(expired)
	if err := os.WriteFile(loginPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	code, output = run("graph", "status", "--repository-id", cliPublishRepo)
	if code != 0 {
		t.Fatalf("refreshed status code=%d %q", code, output)
	}
	refreshed := readLogin()
	if refreshed.RefreshToken == first.RefreshToken || refreshed.AccessToken == first.AccessToken {
		t.Fatal("the login was not rotated")
	}
	noSecrets(output, refreshed)
	noSecrets(output, first)

	// 5. a login without scope reads but cannot publish
	narrow := narrowToken(t, h, browser, server)
	if code, body := apiCall(t, server, narrow, http.MethodGet, "/v1/repositories/101"); code != http.StatusOK {
		t.Fatalf("narrow read=%d %s", code, body)
	}
	var narrowStatus statusReportForTest
	if code, body := apiCall(t, server, narrow, http.MethodGet, "/v1/graph/repositories/101/status"); code != http.StatusOK || json.Unmarshal([]byte(`{"graph":`+body+`}`), &narrowStatus) != nil || narrowStatus.Graph.Publication == nil || narrowStatus.Graph.Publication.Permitted {
		t.Fatalf("narrow status=%d %s", code, body)
	}
	if code, body := apiCall(t, server, narrow, http.MethodPost, "/v1/graph/uploads?repository_id=101&commit="+cliPublishSHA+"&expected_generation=0"); code != http.StatusForbidden {
		t.Fatalf("narrow upload=%d %s", code, body)
	}

	// 6. logout
	code, output = run("logout")
	if code != 0 {
		t.Fatalf("logout code=%d %q", code, output)
	}
	if _, err := os.Stat(loginPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("login file remains: %v", err)
	}
	if code, body := apiCall(t, server, refreshed.AccessToken, http.MethodGet, "/v1/repositories/101"); code != http.StatusUnauthorized {
		t.Fatalf("revoked token read=%d %s", code, body)
	}
}

type statusReportForTest struct {
	Graph struct {
		Publication *struct {
			Permitted bool `json:"permitted"`
		} `json:"publication"`
	} `json:"graph"`
}

func apiCall(t *testing.T, server *httptest.Server, token, method, path string) (int, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", api.GraphArtifactV2ContentType)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body)
}

// narrowToken drives register, consent and exchange by hand without a scope and returns the access token.
func narrowToken(t *testing.T, h *postgresHarness, browser *http.Client, server *httptest.Server) string {
	t.Helper()
	redirect := "http://127.0.0.1:1/callback"
	registration, _ := json.Marshal(map[string]any{"client_name": "narrow", "redirect_uris": []string{redirect}, "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none"})
	response, err := server.Client().Post(server.URL+"/oauth/register", "application/json", bytes.NewReader(registration))
	if err != nil {
		t.Fatal(err)
	}
	var registered struct {
		ClientID string `json:"client_id"`
	}
	err = json.NewDecoder(response.Body).Decode(&registered)
	response.Body.Close()
	if err != nil || registered.ClientID == "" {
		t.Fatalf("register: %d %v", response.StatusCode, err)
	}
	verifier := client.NewSecret()
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	authorizationURL := server.URL + "/oauth/authorize?" + url.Values{"response_type": {"code"}, "client_id": {registered.ClientID}, "redirect_uri": {redirect}, "state": {"s"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}.Encode()
	callback, err := consentFlow(t.Context(), browser, server.URL, authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	location, err := url.Parse(callback)
	if err != nil || location.Query().Get("code") == "" {
		t.Fatalf("callback %q: %v", callback, err)
	}
	response, err = server.Client().PostForm(server.URL+"/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {location.Query().Get("code")}, "redirect_uri": {redirect}, "client_id": {registered.ClientID}, "code_verifier": {verifier}})
	if err != nil {
		t.Fatal(err)
	}
	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	err = json.NewDecoder(response.Body).Decode(&tokens)
	response.Body.Close()
	if err != nil || tokens.AccessToken == "" {
		t.Fatalf("exchange: %d %v", response.StatusCode, err)
	}
	return tokens.AccessToken
}
