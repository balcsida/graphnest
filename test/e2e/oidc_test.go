//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/authz"
	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/httpapi"
	"github.com/grepnest/grepnest/internal/mcpserver"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/scipgraph"
	"github.com/grepnest/grepnest/internal/search"
	"github.com/grepnest/grepnest/internal/sso"
	oidcclient "github.com/grepnest/grepnest/internal/sso/oidc"
	"github.com/grepnest/grepnest/internal/webui"
	"github.com/grepnest/grepnest/pkg/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

const (
	oidcClientID       = "grepnest-e2e"
	oidcClientSecret   = "oidc-e2e-secret"
	oidcInstallationID = int64(71)
	oidcAllowedRepoID  = int64(7101)
	oidcDeniedRepoID   = int64(7102)
	oidcUserToken      = "oidc-user-token"
	oidcAdminToken     = "oidc-admin-token"
	oidcIndexedSHA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestOIDCCrossReplicaSessions(t *testing.T) {
	database := newMilestoneDatabase(t)
	allowed, denied := seedOIDCRepositories(t, database)
	idp := newOIDCIdP(t)
	now := time.Now().UTC().Truncate(time.Second)
	clock := func() time.Time { return now }
	public := newOIDCPublicOrigin(t)
	a := newOIDCReplica(t, database, idp, public.url(), clock, allowed.ZoektID)
	b := newOIDCReplica(t, database, idp, public.url(), clock, allowed.ZoektID)
	public.set(a, b)
	browser := oidcBrowser(t, public.server)

	t.Run("cross replica login and authorization", func(t *testing.T) {
		callback, loginCookie := beginOIDCLogin(t, browser, public, "A", idp)
		response := callbackOIDC(t, browser, public, "B", callback)
		assertRedirect(t, response, "/")
		sessionCookie := namedResponseCookie(t, response, authn.SessionCookieName)
		if !sessionCookie.Secure || !sessionCookie.HttpOnly || sessionCookie.Path != "/" ||
			sessionCookie.Domain != "" || sessionCookie.SameSite != http.SameSiteStrictMode {
			t.Fatalf("session cookie = %#v", sessionCookie)
		}
		if loginCookie.Value == sessionCookie.Value {
			t.Fatal("browser binding cookie reused as session token")
		}

		var listed struct {
			Repositories []api.RepositorySummary `json:"repositories"`
		}
		response = requestOIDC(t, browser, public, "A", http.MethodGet, "/v1/repositories", nil, nil)
		decodeOIDCResponse(t, response, http.StatusOK, &listed)
		if len(listed.Repositories) != 1 || listed.Repositories[0].ID != oidcAllowedRepoID {
			t.Fatalf("repositories = %#v", listed.Repositories)
		}

		response = requestOIDC(t, browser, public, "B", http.MethodPost, "/v1/search",
			api.SearchRequest{Query: "needle"}, map[string]string{"Origin": public.url()})
		var searched api.SearchResponse
		decodeOIDCResponse(t, response, http.StatusOK, &searched)
		if len(searched.Matches) != 1 || searched.Matches[0].Repository.ID != oidcAllowedRepoID ||
			!slices.Equal(b.backend.repositoryIDs(), []uint32{allowed.ZoektID}) {
			t.Fatalf("search = %#v, backend repositories = %v", searched, b.backend.repositoryIDs())
		}
		if slices.Contains(b.backend.repositoryIDs(), denied.ZoektID) {
			t.Fatal("search reached repository outside the durable user scope")
		}

		response = requestOIDC(t, browser, public, "A", http.MethodPut, "/v1/scip/dependencies",
			map[string]any{}, map[string]string{"Origin": public.url()})
		drainClose(response)
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("SCIP status = %d, want 403", response.StatusCode)
		}
		response = requestOIDC(t, browser, public, "B", http.MethodPost, "/mcp", nil, nil)
		drainClose(response)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("MCP cookie status = %d, want 401", response.StatusCode)
		}
		response = requestOIDC(t, browser, public, "A", http.MethodGet, "/v1/repositories", nil,
			map[string]string{"Authorization": "Bearer " + oidcUserToken})
		drainClose(response)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("mixed credentials status = %d, want 401", response.StatusCode)
		}
		response = callbackOIDC(t, browser, public, "A", callback)
		assertAuthFailure(t, response)

		response = requestOIDC(t, browser, public, "A", http.MethodPost, "/auth/logout", nil,
			map[string]string{"Origin": public.url()})
		drainClose(response)
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("logout status = %d", response.StatusCode)
		}
		response = requestOIDC(t, browser, public, "B", http.MethodGet, "/v1/repositories", nil, nil)
		drainClose(response)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("revoked session status = %d, want 401", response.StatusCode)
		}
		assertBearerStillWorks(t, public, oidcUserToken, http.StatusOK)
		assertBearerStillWorks(t, public, oidcAdminToken, http.StatusOK)
	})

	t.Run("invalid tokens and denied groups", func(t *testing.T) {
		for _, mode := range []string{"wrong-nonce", "wrong-audience", "denied-group", "token-failure"} {
			t.Run(mode, func(t *testing.T) {
				idp.setMode(mode)
				callback, _ := beginOIDCLogin(t, browser, public, "A", idp)
				assertAuthFailure(t, callbackOIDC(t, browser, public, "B", callback))
			})
		}
		idp.setMode("valid")
	})

	t.Run("JWKS rotation", func(t *testing.T) {
		idp.rotate(t)
		callback, _ := beginOIDCLogin(t, browser, public, "A", idp)
		assertRedirect(t, callbackOIDC(t, browser, public, "B", callback), "/")
		logoutOIDC(t, browser, public)
	})

	t.Run("duplicate callback race", func(t *testing.T) {
		callback, binding := beginOIDCLogin(t, browser, public, "A", idp)
		client := oidcClientWithCookie(t, public.server, binding)
		type callbackResult struct {
			status   int
			location string
		}
		results := make(chan callbackResult, 2)
		var wait sync.WaitGroup
		for _, replica := range []string{"A", "B"} {
			wait.Add(1)
			go func(replica string) {
				defer wait.Done()
				response := callbackOIDC(t, client, public, replica, callback)
				results <- callbackResult{response.StatusCode, response.Header.Get("Location")}
				drainClose(response)
			}(replica)
		}
		wait.Wait()
		close(results)
		statuses, locations := []int{}, []string{}
		for result := range results {
			statuses = append(statuses, result.status)
			locations = append(locations, result.location)
		}
		slices.Sort(statuses)
		slices.Sort(locations)
		if !slices.Equal(statuses, []int{http.StatusSeeOther, http.StatusSeeOther}) ||
			!slices.Equal(locations, []string{"/", "/?auth_error=authentication_failed"}) {
			t.Fatalf("callback statuses = %v, locations = %v", statuses, locations)
		}
		if got := countSessions(t, database); got != 1 {
			t.Fatalf("sessions = %d, want 1", got)
		}
		clearAuthRows(t, database)
	})

	t.Run("stolen callback lacks browser binding", func(t *testing.T) {
		callback, _ := beginOIDCLogin(t, browser, public, "A", idp)
		thief := oidcBrowser(t, public.server)
		assertAuthFailure(t, callbackOIDC(t, thief, public, "B", callback))
		assertRedirect(t, callbackOIDC(t, browser, public, "B", callback), "/")
		logoutOIDC(t, browser, public)
	})

	t.Run("expired flow session and cleanup", func(t *testing.T) {
		callback, _ := beginOIDCLogin(t, browser, public, "A", idp)
		now = now.Add(2 * time.Minute)
		assertAuthFailure(t, callbackOIDC(t, browser, public, "B", callback))
		flows, sessions, err := database.store.DeleteExpiredAuth(t.Context(), now)
		if err != nil || flows != 1 || sessions != 0 {
			t.Fatalf("flow cleanup = %d/%d, err=%v", flows, sessions, err)
		}

		callback, _ = beginOIDCLogin(t, browser, public, "A", idp)
		assertRedirect(t, callbackOIDC(t, browser, public, "B", callback), "/")
		now = now.Add(2 * time.Hour)
		response := requestOIDC(t, browser, public, "A", http.MethodGet, "/v1/repositories", nil, nil)
		drainClose(response)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expired session status = %d, want 401", response.StatusCode)
		}
		flows, sessions, err = database.store.DeleteExpiredAuth(t.Context(), now)
		if err != nil || flows != 0 || sessions != 1 {
			t.Fatalf("session cleanup = %d/%d, err=%v", flows, sessions, err)
		}
	})

	t.Run("unsafe Origin is exact", func(t *testing.T) {
		now = time.Now().UTC().Truncate(time.Second)
		callback, _ := beginOIDCLogin(t, browser, public, "A", idp)
		assertRedirect(t, callbackOIDC(t, browser, public, "B", callback), "/")
		for _, origin := range []string{"", "https://evil.example"} {
			response := requestOIDC(t, browser, public, "A", http.MethodPost, "/v1/search",
				api.SearchRequest{Query: "needle"}, map[string]string{"Origin": origin})
			drainClose(response)
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("Origin %q status = %d, want 401", origin, response.StatusCode)
			}
		}
		logoutOIDC(t, browser, public)
	})

	t.Run("disabled remains bearer only", func(t *testing.T) {
		disabled := newOIDCDisabledReplica(database, allowed.ZoektID)
		public.set(disabled, disabled)
		response := requestOIDC(t, oidcBrowser(t, public.server), public, "A", http.MethodGet, "/v1/auth/config", nil, nil)
		var cfg struct {
			TokenLogin bool           `json:"token_login"`
			Providers  []sso.Metadata `json:"providers"`
		}
		decodeOIDCResponse(t, response, http.StatusOK, &cfg)
		if !cfg.TokenLogin || len(cfg.Providers) != 0 {
			t.Fatalf("auth config = %#v", cfg)
		}
		assertBearerStillWorks(t, public, oidcUserToken, http.StatusOK)
		response = requestOIDC(t, oidcBrowser(t, public.server), public, "A", http.MethodGet, "/", nil, nil)
		drainClose(response)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("disabled UI status = %d, want 200", response.StatusCode)
		}
		response = requestOIDC(t, oidcBrowser(t, public.server), public, "A", http.MethodGet, "/auth/oidc/login", nil, nil)
		drainClose(response)
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("disabled login status = %d, want 404", response.StatusCode)
		}
	})
}

type oidcReplica struct {
	handler http.Handler
	backend *oidcSearchBackend
}

func newOIDCReplica(t *testing.T, database milestoneDatabase, idp *oidcIdP, publicURL string, now func() time.Time, zoektID uint32) *oidcReplica {
	t.Helper()
	public, _ := url.Parse(publicURL)
	client, err := oidcclient.New(t.Context(), config.OIDC{
		IssuerURL: idp.server.URL, ClientID: oidcClientID, Scopes: []string{"openid", "profile"},
		GroupsClaim: "groups", DisplayNameClaim: "name",
	}, public, []byte(oidcClientSecret), idp.caPEM())
	if err != nil {
		t.Fatal(err)
	}
	sessions := &authn.SessionManager{Store: database.store, TTL: time.Hour, Now: now}
	bearer := oidcBearer()
	requestAuth := authn.RequestAuthenticator{Bearer: bearer, Session: sessions, PublicOrigin: publicURL}
	provider := &oidcclient.Provider{
		Client: client, Store: database.store,
		Mapper: authn.ScopeMapper{
			InstallationID: oidcInstallationID, RepositoryIDs: []int64{oidcAllowedRepoID},
			AllowedGroups: []string{"engineering"},
		},
		Sessions: sessions, LoginTTL: time.Minute, Now: now,
	}
	return assembleOIDCReplica(database, requestAuth, []sso.Provider{provider}, sessions, zoektID)
}

func newOIDCDisabledReplica(database milestoneDatabase, zoektID uint32) *oidcReplica {
	bearer := oidcBearer()
	return assembleOIDCReplica(database, authn.RequestAuthenticator{Bearer: bearer}, nil, nil, zoektID)
}

func assembleOIDCReplica(database milestoneDatabase, authenticator authn.RequestAuthenticator, providers []sso.Provider, sessions *authn.SessionManager, zoektID uint32) *oidcReplica {
	backend := &oidcSearchBackend{zoektID: zoektID}
	searchService := search.NewService(backend, authz.NewPostgres(database.store), search.Limits{MaxResults: 10, MaxResponseBytes: 64 << 10})
	repositories := &repository.Service{Store: database.store}
	scip := &scipgraph.Service{Store: database.store, MaxResults: 10}
	mux := http.NewServeMux()
	webui.Register(mux)
	httpapi.RegisterAuth(mux, true, providers, authenticator, sessions, nil)
	httpapi.RegisterRepositories(mux, authenticator, repositories, 64<<10, 10, 64<<10)
	httpapi.RegisterSearch(mux, authenticator, searchService, 64<<10, 64<<10)
	httpapi.RegisterSCIP(mux, authenticator, scip, 64<<10, 64<<10, 64<<10)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcpserver.New(searchService, repositories)
	}, nil)
	mux.Handle("/mcp", httpapi.AuthenticateBearer(authenticator.Bearer, mcpHandler))
	return &oidcReplica{handler: mux, backend: backend}
}

func oidcBearer() authn.Authenticator {
	return authn.NewStatic(map[string]authn.Principal{
		oidcUserToken:  {Subject: "user", Method: "bearer", InstallationID: oidcInstallationID, RepositoryIDs: []int64{oidcAllowedRepoID}},
		oidcAdminToken: {Subject: "admin", Method: "bearer", Administrator: true, InstallationID: oidcInstallationID, RepositoryIDs: []int64{oidcAllowedRepoID, oidcDeniedRepoID}},
	})
}

type oidcSearchBackend struct {
	mu      sync.Mutex
	zoektID uint32
	lastIDs []uint32
}

func (backend *oidcSearchBackend) Search(_ context.Context, request search.BackendRequest) (api.SearchResponse, error) {
	backend.mu.Lock()
	backend.lastIDs = append([]uint32(nil), request.RepositoryIDs...)
	backend.mu.Unlock()
	return api.SearchResponse{Matches: []api.SearchMatch{{
		ZoektID: backend.zoektID, Path: "main.go", SHA: oidcIndexedSHA, Preview: "needle",
		Branches: []string{"main"},
	}}}, nil
}

func (*oidcSearchBackend) Health(context.Context) error { return nil }

func (backend *oidcSearchBackend) repositoryIDs() []uint32 {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return append([]uint32(nil), backend.lastIDs...)
}

type oidcPublicOrigin struct {
	server *httptest.Server
	mu     sync.RWMutex
	a, b   http.Handler
}

func newOIDCPublicOrigin(t *testing.T) *oidcPublicOrigin {
	t.Helper()
	origin := &oidcPublicOrigin{}
	origin.server = httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		origin.mu.RLock()
		handler := origin.a
		if request.Header.Get("X-GrepNest-Replica") == "B" {
			handler = origin.b
		}
		origin.mu.RUnlock()
		if handler == nil {
			http.Error(writer, "replica unavailable", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(writer, request)
	}))
	origin.server.StartTLS()
	t.Cleanup(origin.server.Close)
	return origin
}

func (origin *oidcPublicOrigin) url() string { return origin.server.URL }

func (origin *oidcPublicOrigin) set(a, b *oidcReplica) {
	origin.mu.Lock()
	defer origin.mu.Unlock()
	origin.a, origin.b = a.handler, b.handler
}

type oidcIdP struct {
	server *httptest.Server
	mu     sync.Mutex
	key    *rsa.PrivateKey
	kid    int
	mode   string
	nonce  string
	pkce   string
}

func newOIDCIdP(t *testing.T) *oidcIdP {
	t.Helper()
	idp := &oidcIdP{mode: "valid"}
	idp.rotate(t)
	idp.server = httptest.NewTLSServer(http.HandlerFunc(idp.serveHTTP))
	t.Cleanup(idp.server.Close)
	return idp
}

func (idp *oidcIdP) caPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: idp.server.Certificate().Raw})
}

func (idp *oidcIdP) setMode(mode string) {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	idp.mode = mode
}

func (idp *oidcIdP) rotate(t *testing.T) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp.mu.Lock()
	defer idp.mu.Unlock()
	idp.key, idp.kid = key, idp.kid+1
}

func (idp *oidcIdP) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	switch request.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"issuer": idp.server.URL, "authorization_endpoint": idp.server.URL + "/authorize",
			"token_endpoint": idp.server.URL + "/token", "jwks_uri": idp.server.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	case "/keys":
		_ = json.NewEncoder(writer).Encode(map[string]any{"keys": []any{idp.jwk()}})
	case "/authorize":
		idp.nonce = request.URL.Query().Get("nonce")
		idp.pkce = request.URL.Query().Get("code_challenge")
		callback, _ := url.Parse(request.URL.Query().Get("redirect_uri"))
		query := callback.Query()
		query.Set("state", request.URL.Query().Get("state"))
		query.Set("code", idp.mode)
		callback.RawQuery = query.Encode()
		http.Redirect(writer, request, callback.String(), http.StatusSeeOther)
	case "/token":
		if idp.mode == "token-failure" {
			http.Error(writer, "failed", http.StatusBadGateway)
			return
		}
		_ = request.ParseForm()
		if request.Form.Get("client_id") != oidcClientID ||
			request.Form.Get("client_secret") != oidcClientSecret ||
			oauth2.S256ChallengeFromVerifier(request.Form.Get("code_verifier")) != idp.pkce {
			http.Error(writer, "invalid grant", http.StatusBadRequest)
			return
		}
		claims := map[string]any{
			"iss": idp.server.URL, "sub": "e2e-user", "aud": oidcClientID,
			"exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(),
			"nonce": idp.nonce, "name": "E2E User", "groups": []string{"engineering"},
		}
		switch request.Form.Get("code") {
		case "wrong-nonce":
			claims["nonce"] = "wrong"
		case "wrong-audience":
			claims["aud"] = "other"
		case "denied-group":
			claims["groups"] = []string{"sales"}
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"access_token": "not-persisted", "refresh_token": "also-not-persisted",
			"token_type": "Bearer", "expires_in": 60, "id_token": idp.sign(claims),
		})
	default:
		http.NotFound(writer, request)
	}
}

func (idp *oidcIdP) jwk() map[string]any {
	return map[string]any{
		"kty": "RSA", "kid": big.NewInt(int64(idp.kid)).String(), "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(idp.key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(idp.key.E)).Bytes()),
	}
}

func (idp *oidcIdP) sign(claims map[string]any) string {
	encode := func(value any) string {
		data, _ := json.Marshal(value)
		return base64.RawURLEncoding.EncodeToString(data)
	}
	payload := encode(map[string]any{"alg": "RS256", "kid": big.NewInt(int64(idp.kid)).String(), "typ": "JWT"}) + "." + encode(claims)
	sum := sha256.Sum256([]byte(payload))
	signature, _ := rsa.SignPKCS1v15(rand.Reader, idp.key, crypto.SHA256, sum[:])
	return payload + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func seedOIDCRepositories(t *testing.T, database milestoneDatabase) (repository.Repository, repository.Repository) {
	t.Helper()
	if err := database.store.UpsertSearchNode(t.Context(), "oidc-e2e", "http://zoekt.invalid"); err != nil {
		t.Fatal(err)
	}
	if err := database.store.UpsertInstallation(t.Context(), postgres.InstallationUpdate{
		GitHubID: oidcInstallationID, AccountLogin: "acme", AccountType: "Organization", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	insert := func(id int64, name string) repository.Repository {
		repo, err := database.store.UpsertRepository(t.Context(), postgres.RepositoryUpdate{
			GitHubID: id, InstallationID: oidcInstallationID, Owner: "acme", Name: name,
			CloneURL: "https://example.invalid/" + name, WebURL: "https://example.invalid/" + name,
			DefaultBranch: "main", Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return repo
	}
	allowed, denied := insert(oidcAllowedRepoID, "allowed"), insert(oidcDeniedRepoID, "denied")
	if _, err := database.pool.Exec(t.Context(),
		"update repositories set desired_sha=$2, indexed_sha=$2, status='ready' where id=$1",
		allowed.ID, oidcIndexedSHA); err != nil {
		t.Fatal(err)
	}
	return allowed, denied
}

func oidcBrowser(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Transport: server.Client().Transport, Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func oidcClientWithCookie(t *testing.T, server *httptest.Server, cookie *http.Cookie) *http.Client {
	t.Helper()
	client := oidcBrowser(t, server)
	base, _ := url.Parse(server.URL)
	client.Jar.SetCookies(base, []*http.Cookie{cookie})
	return client
}

func beginOIDCLogin(t *testing.T, browser *http.Client, public *oidcPublicOrigin, replica string, idp *oidcIdP) (string, *http.Cookie) {
	t.Helper()
	response := requestOIDC(t, browser, public, replica, http.MethodGet, "/auth/oidc/login", nil, nil)
	if response.StatusCode != http.StatusSeeOther {
		drainClose(response)
		t.Fatalf("login status = %d", response.StatusCode)
	}
	location := response.Header.Get("Location")
	binding := namedResponseCookie(t, response, sso.OIDCLoginCookieName)
	authorization, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	query := authorization.Query()
	if query.Get("state") == "" || query.Get("nonce") == "" ||
		query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
		t.Fatalf("authorization redirect = %q", location)
	}
	if !binding.Secure || !binding.HttpOnly || binding.Path != "/" ||
		binding.Domain != "" || binding.SameSite != http.SameSiteLaxMode {
		t.Fatalf("login cookie = %#v", binding)
	}
	drainClose(response)
	idpClient := idp.server.Client()
	idpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err = idpClient.Get(location)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("authorization status = %d", response.StatusCode)
	}
	return response.Header.Get("Location"), binding
}

func callbackOIDC(t *testing.T, client *http.Client, public *oidcPublicOrigin, replica, callback string) *http.Response {
	t.Helper()
	parsed, err := url.Parse(callback)
	if err != nil {
		t.Fatal(err)
	}
	return requestOIDC(t, client, public, replica, http.MethodGet, parsed.RequestURI(), nil, nil)
}

func requestOIDC(t *testing.T, client *http.Client, public *oidcPublicOrigin, replica, method, path string, body any, headers map[string]string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, public.url()+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-GrepNest-Replica", replica)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func namedResponseCookie(t *testing.T, response *http.Response, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Cookies() {
		if cookie.Name == name {
			copy := *cookie
			return &copy
		}
	}
	t.Fatalf("response lacks cookie %q", name)
	return nil
}

func assertRedirect(t *testing.T, response *http.Response, location string) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != location {
		t.Fatalf("response = %d Location=%q, want 303 %q", response.StatusCode, response.Header.Get("Location"), location)
	}
}

func assertAuthFailure(t *testing.T, response *http.Response) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/?auth_error=authentication_failed" {
		t.Fatalf("auth failure = %d Location=%q", response.StatusCode, response.Header.Get("Location"))
	}
}

func decodeOIDCResponse(t *testing.T, response *http.Response, status int, target any) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != status {
		t.Fatalf("status = %d, want %d", response.StatusCode, status)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func drainClose(response *http.Response) {
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
}

func logoutOIDC(t *testing.T, browser *http.Client, public *oidcPublicOrigin) {
	t.Helper()
	response := requestOIDC(t, browser, public, "A", http.MethodPost, "/auth/logout", nil,
		map[string]string{"Origin": public.url()})
	drainClose(response)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status = %d", response.StatusCode)
	}
}

func assertBearerStillWorks(t *testing.T, public *oidcPublicOrigin, token string, want int) {
	t.Helper()
	response := requestOIDC(t, oidcBrowser(t, public.server), public, "B", http.MethodGet, "/v1/repositories", nil,
		map[string]string{"Authorization": "Bearer " + token})
	drainClose(response)
	if response.StatusCode != want {
		t.Fatalf("bearer status = %d, want %d", response.StatusCode, want)
	}
}

func countSessions(t *testing.T, database milestoneDatabase) int {
	t.Helper()
	var count int
	if err := database.pool.QueryRow(t.Context(), "select count(*) from auth_sessions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func clearAuthRows(t *testing.T, database milestoneDatabase) {
	t.Helper()
	if _, err := database.pool.Exec(t.Context(), "delete from auth_sessions; delete from auth_login_flows"); err != nil {
		t.Fatal(err)
	}
}
