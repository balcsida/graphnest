//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/account"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/authz"
	"github.com/balcsida/graphnest/internal/githubapp"
	"github.com/balcsida/graphnest/internal/httpapi"
	"github.com/balcsida/graphnest/internal/mcpserver"
	"github.com/balcsida/graphnest/internal/oauthas"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/internal/search"
	"github.com/balcsida/graphnest/internal/sso"
	"github.com/balcsida/graphnest/internal/sso/githuboauth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPOAuthAuthorizationCodeFlow acts as an MCP client against the real HTTP
// stack, Postgres and a fake GitHub identity provider: discovery, dynamic
// registration, browser sign-in, consent, code exchange, an authenticated MCP
// call, refresh with rotation, replay detection and account-side revocation.
func TestMCPOAuthAuthorizationCodeFlow(t *testing.T) {
	database := newMilestoneDatabase(t)
	idp := newGitHubOAuthTestProvider(t)
	seedGitHubOAuthAuthorization(t, database, idp.linkID())
	public := newOIDCPublicServer(t)
	public.Config.Handler = newMCPOAuthServer(t, database, idp, public.URL)
	base := public.URL

	// 1. An unauthenticated MCP request advertises the authorization server.
	response, err := public.Client().Post(base+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || response.Header.Get("WWW-Authenticate") != `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource"` {
		t.Fatalf("mcp status=%d WWW-Authenticate=%q", response.StatusCode, response.Header.Get("WWW-Authenticate"))
	}
	var resource struct {
		AuthorizationServers []string `json:"authorization_servers"`
	}
	getJSON(t, public.Client(), base+"/.well-known/oauth-protected-resource", &resource)
	var metadata struct {
		Authorization string `json:"authorization_endpoint"`
		Token         string `json:"token_endpoint"`
		Registration  string `json:"registration_endpoint"`
	}
	getJSON(t, public.Client(), resource.AuthorizationServers[0]+"/.well-known/oauth-authorization-server", &metadata)

	// 2. Dynamic registration as a public loopback client.
	registration := postJSON(t, public.Client(), metadata.Registration, map[string]any{
		"client_name": "OpenCode", "redirect_uris": []string{"http://127.0.0.1:53000/mcp/oauth/callback"},
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none",
	}, http.StatusCreated)
	clientID := registration["client_id"].(string)

	// 3. Authorization request from a browser without a GraphNest session.
	jar, _ := cookiejar.New(nil)
	browser := public.Client()
	browser.Jar = jar
	browser.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	verifier := strings.Repeat("v", 64)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	authorize := metadata.Authorization + "?" + url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:61000/mcp/oauth/callback"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"e2e-state"}, "resource": {base + "/mcp"},
	}.Encode()
	response = browserRequest(t, browser, authorize, "A")
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || !strings.HasPrefix(response.Header.Get("Location"), "/auth/oauth/github/login?return_to=") {
		t.Fatalf("authorize without session: status=%d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
	// Follow the GitHub login through the fake IdP back to the resume page.
	login := browserRequest(t, browser, base+response.Header.Get("Location"), "A")
	login.Body.Close()
	callback := completeGitHubOAuthLogin(t, browser, login.Header.Get("Location"), "A")
	_ = callback
	consent := browserRequest(t, browser, base+"/oauth/authorize/resume", "A")
	page, _ := io.ReadAll(consent.Body)
	consent.Body.Close()
	if consent.StatusCode != http.StatusOK || !bytes.Contains(page, []byte("OpenCode")) || !bytes.Contains(page, []byte("http://127.0.0.1:61000")) {
		t.Fatalf("consent status=%d page=%s", consent.StatusCode, page)
	}
	requestID := regexp.MustCompile(`name="request_id" value="([^"]+)"`).FindSubmatch(page)
	if requestID == nil {
		t.Fatal("consent page has no request_id")
	}

	// 4. Allow.
	form := url.Values{"request_id": {string(requestID[1])}, "decision": {"allow"}}
	decision, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/oauth/authorize", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	decision.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	decision.Header.Set("Origin", base)
	decision.Header.Set("X-Replica", "A")
	response, err = browser.Do(decision)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	redirect, err := url.Parse(response.Header.Get("Location"))
	if err != nil || response.StatusCode != http.StatusSeeOther || redirect.Host != "127.0.0.1:61000" || redirect.Query().Get("state") != "e2e-state" || redirect.Query().Get("code") == "" {
		t.Fatalf("consent status=%d location=%q", response.StatusCode, response.Header.Get("Location"))
	}

	// 5. Exchange the code.
	tokens := postForm(t, public.Client(), metadata.Token, url.Values{
		"grant_type": {"authorization_code"}, "code": {redirect.Query().Get("code")}, "client_id": {clientID},
		"redirect_uri": {"http://127.0.0.1:61000/mcp/oauth/callback"}, "code_verifier": {verifier},
	}, http.StatusOK)
	access := tokens["access_token"].(string)
	refresh := tokens["refresh_token"].(string)
	if !strings.HasPrefix(access, "gno_") || !strings.HasPrefix(refresh, "gnr_") {
		t.Fatalf("tokens=%v", tokens)
	}

	// 6. The access token drives MCP as the user (repository 101 only).
	assertMCPRepositoryAccess(t, public, access)
	assertBearerStatus(t, public.Client(), base, access, "/v1/repositories/101", http.StatusOK)
	// ...but cannot manage credentials.
	assertBearerStatus(t, public.Client(), base, access, "/v1/account/api-tokens", http.StatusForbidden)

	// 7. Refresh rotates; the old access token dies; replay after the grace window revokes.
	rotated := postForm(t, public.Client(), metadata.Token, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}}, http.StatusOK)
	newAccess := rotated["access_token"].(string)
	assertBearerStatus(t, public.Client(), base, newAccess, "/v1/repositories/101", http.StatusOK)
	assertBearerStatus(t, public.Client(), base, access, "/v1/repositories/101", http.StatusUnauthorized)
	if _, err := database.pool.Exec(t.Context(), `update oauth_grants set last_used_at = last_used_at - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	postForm(t, public.Client(), metadata.Token, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}}, http.StatusBadRequest)
	assertBearerStatus(t, public.Client(), base, newAccess, "/v1/repositories/101", http.StatusUnauthorized)

	// 8. A second grant appears in the account and can be revoked from the browser.
	grantsBefore := countGrants(t, browser, base)
	consent = browserRequest(t, browser, authorize, "A")
	page, _ = io.ReadAll(consent.Body)
	consent.Body.Close()
	requestID = regexp.MustCompile(`name="request_id" value="([^"]+)"`).FindSubmatch(page)
	if consent.StatusCode != http.StatusOK || requestID == nil {
		t.Fatalf("second authorize with session: status=%d", consent.StatusCode)
	}
	decision, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/oauth/authorize", strings.NewReader(url.Values{"request_id": {string(requestID[1])}, "decision": {"allow"}}.Encode()))
	decision.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	decision.Header.Set("Origin", base)
	decision.Header.Set("X-Replica", "A")
	response, err = browser.Do(decision)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	redirect, _ = url.Parse(response.Header.Get("Location"))
	tokens = postForm(t, public.Client(), metadata.Token, url.Values{
		"grant_type": {"authorization_code"}, "code": {redirect.Query().Get("code")}, "client_id": {clientID},
		"redirect_uri": {"http://127.0.0.1:61000/mcp/oauth/callback"}, "code_verifier": {verifier},
	}, http.StatusOK)
	access = tokens["access_token"].(string)
	assertBearerStatus(t, public.Client(), base, access, "/v1/repositories/101", http.StatusOK)
	if countGrants(t, browser, base) != grantsBefore+1 {
		t.Fatal("new grant not listed in the account")
	}
	var listed struct {
		Grants []struct {
			ID         int64  `json:"id"`
			ClientName string `json:"client_name"`
		} `json:"grants"`
	}
	getJSONReplica(t, browser, base+"/v1/account/oauth-grants", &listed)
	last := listed.Grants[len(listed.Grants)-1]
	if last.ClientName != "OpenCode" {
		t.Fatalf("grant=%+v", last)
	}
	revoke, _ := http.NewRequestWithContext(t.Context(), http.MethodDelete, base+"/v1/account/oauth-grants/"+strconv.FormatInt(last.ID, 10), nil)
	revoke.Header.Set("Origin", base)
	revoke.Header.Set("X-Replica", "A")
	response, err = browser.Do(revoke)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke status=%d", response.StatusCode)
	}
	assertBearerStatus(t, public.Client(), base, access, "/v1/repositories/101", http.StatusUnauthorized)
}

func newMCPOAuthServer(t *testing.T, database milestoneDatabase, github *githubOAuthTestProvider, publicURL string) http.Handler {
	t.Helper()
	public := mustURL(t, publicURL)
	endpoints := githubapp.Endpoints{Web: mustURL(t, github.server.URL), API: mustURL(t, github.server.URL), Upload: mustURL(t, github.server.URL), Git: mustURL(t, github.server.URL)}
	httpClient, err := githubapp.NewHTTPClient(github.caPEM(), endpoints, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client, err := githuboauth.NewClient(endpoints, public, "github-e2e", []byte(githubOAuthSecretCanary), "2022-11-28", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	sessions := &authn.SessionManager{Store: database.store, IdleTTL: time.Hour, TTL: 2 * time.Hour}
	apiTokens := authn.TokenManager{Store: database.store}
	bearer := authn.BearerRouter{APITokens: apiTokens, OAuth: authn.OAuthTokenAuthenticator{Store: database.store}}
	requestAuth := authn.RequestAuthenticator{Bearer: bearer, Session: sessions, PublicOrigin: publicURL}
	repositories := &repository.Service{Store: database.store}
	searchService := search.NewService(oidcSearchBackend{}, authz.NewPostgres(database.store), search.Limits{MaxResults: 10, MaxResponseBytes: 64 << 10})
	sealer, err := oauthas.NewSealer(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	provider := githuboauth.NewProvider(client, database.store, sessions, nil, time.Minute)
	authorizationServer := &oauthas.Server{Origin: publicURL, Store: database.store, Sessions: sessions, Sealer: sealer}
	mux := http.NewServeMux()
	httpapi.RegisterAuth(mux, false, false, true, []sso.Provider{provider}, requestAuth, sessions, nil)
	httpapi.RegisterRepositories(mux, requestAuth, repositories, 64<<10, 10, 64<<10)
	httpapi.RegisterSearch(mux, requestAuth, searchService, 64<<10, 64<<10)
	httpapi.RegisterAccount(mux, requestAuth, newAccountService(database), 64<<10, 64<<10)
	authorizationServer.Register(mux)
	mux.Handle("/mcp", httpapi.AuthenticateBearerWithChallenge(bearer, authorizationServer.Challenge, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcpserver.New(searchService, repositories)
	}, nil)))
	return mux
}

func getJSON(t *testing.T, client *http.Client, endpoint string, target any) {
	t.Helper()
	response, err := client.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s status=%d", endpoint, response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func getJSONReplica(t *testing.T, client *http.Client, endpoint string, target any) {
	t.Helper()
	response := browserRequest(t, client, endpoint, "A")
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s status=%d", endpoint, response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func postJSON(t *testing.T, client *http.Client, endpoint string, body any, wantStatus int) map[string]any {
	t.Helper()
	encoded, _ := json.Marshal(body)
	response, err := client.Post(endpoint, "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(response.Body).Decode(&decoded)
	if response.StatusCode != wantStatus {
		t.Fatalf("%s status=%d body=%v", endpoint, response.StatusCode, decoded)
	}
	return decoded
}

func postForm(t *testing.T, client *http.Client, endpoint string, form url.Values, wantStatus int) map[string]any {
	t.Helper()
	response, err := client.Post(endpoint, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(response.Body).Decode(&decoded)
	if response.StatusCode != wantStatus {
		t.Fatalf("%s status=%d body=%v", endpoint, response.StatusCode, decoded)
	}
	return decoded
}

func countGrants(t *testing.T, browser *http.Client, base string) int {
	t.Helper()
	var listed struct {
		Grants []json.RawMessage `json:"grants"`
	}
	getJSONReplica(t, browser, base+"/v1/account/oauth-grants", &listed)
	return len(listed.Grants)
}

func newAccountService(database milestoneDatabase) *account.Service {
	return &account.Service{Manager: authn.TokenManager{Store: database.store}, Authorizer: authz.NewPostgres(database.store)}
}
