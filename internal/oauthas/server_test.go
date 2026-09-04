package oauthas

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/jackc/pgx/v5"
)

// ---- in-memory store -----------------------------------------------------------

type memoryStore struct {
	mu       sync.Mutex
	clients  map[string]authn.OAuthClient
	requests map[[32]byte]authn.OAuthAuthorizationRequest
	grants   map[int64]*authn.OAuthGrant
	nextID   int64
	github   map[int64][]int64
	fail     error
}

func newMemoryStore() *memoryStore {
	return &memoryStore{clients: map[string]authn.OAuthClient{}, requests: map[[32]byte]authn.OAuthAuthorizationRequest{}, grants: map[int64]*authn.OAuthGrant{}, github: map[int64][]int64{}}
}

func (m *memoryStore) CreateOAuthClient(_ context.Context, client authn.OAuthClient) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	m.clients[client.ID] = client
	return nil
}

func (m *memoryStore) OAuthClient(_ context.Context, id string, _ time.Time) (authn.OAuthClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	client, ok := m.clients[id]
	if !ok {
		return authn.OAuthClient{}, pgx.ErrNoRows
	}
	return client, nil
}

func (m *memoryStore) CreateOAuthAuthorizationRequest(_ context.Context, request authn.OAuthAuthorizationRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[request.ID] = request
	return nil
}

func (m *memoryStore) OAuthAuthorizationRequest(_ context.Context, id [32]byte, phase string, now time.Time) (authn.OAuthAuthorizationRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	request, ok := m.requests[id]
	if !ok || request.Phase != phase || !request.ExpiresAt.After(now) {
		return authn.OAuthAuthorizationRequest{}, pgx.ErrNoRows
	}
	return request, nil
}

func (m *memoryStore) IssueOAuthCode(_ context.Context, pendingID, codeID [32]byte, userID int64, expiresAt, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	request, ok := m.requests[pendingID]
	if !ok || request.Phase != "pending" || !request.ExpiresAt.After(now) {
		return pgx.ErrNoRows
	}
	delete(m.requests, pendingID)
	request.ID, request.Phase, request.UserID, request.CreatedAt, request.ExpiresAt = codeID, "code", userID, now, expiresAt
	m.requests[codeID] = request
	return nil
}

func (m *memoryStore) DeleteOAuthAuthorizationRequest(_ context.Context, id [32]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.requests, id)
	return nil
}

func (m *memoryStore) ConsumeOAuthCode(_ context.Context, codeID [32]byte, now time.Time) (authn.OAuthAuthorizationRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	request, ok := m.requests[codeID]
	if !ok || request.Phase != "code" || !request.ExpiresAt.After(now) {
		return authn.OAuthAuthorizationRequest{}, pgx.ErrNoRows
	}
	delete(m.requests, codeID)
	return request, nil
}

func (m *memoryStore) CreateOAuthGrant(_ context.Context, grant authn.OAuthGrant) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	grant.ID = m.nextID
	grant.LastUsedAt = grant.CreatedAt
	m.grants[grant.ID] = &grant
	return grant.ID, nil
}

func (m *memoryStore) live(grant *authn.OAuthGrant, now time.Time) bool {
	return grant.RevokedAt == nil && grant.ExpiresAt.After(now)
}

func (m *memoryStore) OAuthPrincipal(_ context.Context, accessHash [32]byte, now time.Time) (authn.Principal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, grant := range m.grants {
		if grant.AccessHash == accessHash && m.live(grant, now) && grant.AccessExpiresAt.After(now) {
			grant.LastUsedAt = now
			return authn.Principal{Subject: "11", Method: authn.ProviderOAuthToken, RepositoryIDs: append([]int64(nil), m.github[grant.UserID]...)}, nil
		}
	}
	return authn.Principal{}, pgx.ErrNoRows
}

func (m *memoryStore) OAuthGrantByRefresh(_ context.Context, refreshHash [32]byte, now time.Time) (authn.OAuthGrant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, grant := range m.grants {
		if grant.RefreshHash == refreshHash && m.live(grant, now) {
			return *grant, nil
		}
	}
	return authn.OAuthGrant{}, pgx.ErrNoRows
}

func (m *memoryStore) RotateOAuthGrant(_ context.Context, refreshHash [32]byte, rotation authn.OAuthRotation) (authn.OAuthGrant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, grant := range m.grants {
		if grant.RefreshHash == refreshHash && m.live(grant, rotation.Now) {
			previous := grant.RefreshHash
			grant.PreviousRefreshHash = &previous
			grant.RefreshHash, grant.AccessHash, grant.AccessExpiresAt, grant.LastUsedAt = rotation.RefreshHash, rotation.AccessHash, rotation.AccessExpiresAt, rotation.Now
			return *grant, nil
		}
	}
	for _, grant := range m.grants {
		if grant.PreviousRefreshHash != nil && *grant.PreviousRefreshHash == refreshHash && !grant.LastUsedAt.After(rotation.Now.Add(-rotation.Grace)) {
			revoked := rotation.Now
			grant.RevokedAt = &revoked
			return authn.OAuthGrant{}, authn.ErrOAuthReplay
		}
	}
	return authn.OAuthGrant{}, pgx.ErrNoRows
}

func (m *memoryStore) UpdateOAuthGrantGitHubToken(_ context.Context, grantID int64, ciphertext []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if grant, ok := m.grants[grantID]; ok {
		grant.GitHubTokenCiphertext = ciphertext
	}
	return nil
}

func (m *memoryStore) RevokeOAuthGrant(_ context.Context, grantID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if grant, ok := m.grants[grantID]; ok {
		now := time.Now()
		grant.RevokedAt, grant.GitHubTokenCiphertext = &now, nil
	}
	return nil
}

func (m *memoryStore) RevokeOAuthGrantByToken(_ context.Context, hash [32]byte, clientID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, grant := range m.grants {
		if grant.ClientID == clientID && (grant.AccessHash == hash || grant.RefreshHash == hash) {
			now := time.Now()
			grant.RevokedAt = &now
		}
	}
	return nil
}

func (m *memoryStore) ListOAuthGrants(context.Context, int64) ([]authn.OAuthGrantMetadata, error) {
	return nil, nil
}
func (m *memoryStore) RevokeUserOAuthGrant(context.Context, int64, int64) error { return nil }

func (m *memoryStore) ReplaceGitHubGrants(_ context.Context, userID int64, repositoryIDs []int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.github[userID] = append([]int64(nil), repositoryIDs...)
	return nil
}

// ---- fixtures ----------------------------------------------------------------------

type staticSessions struct{ principal authn.Principal }

func (s staticSessions) Authenticate(_ context.Context, token string) (authn.Principal, error) {
	if token != sessionTokenValue {
		return authn.Principal{}, authn.ErrUnauthenticated
	}
	return s.principal, nil
}

type recordingAudit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recordingAudit) Record(_ context.Context, event audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *recordingAudit) operations() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var names []string
	for _, event := range r.events {
		names = append(names, event.Operation)
	}
	return names
}

type githubStub struct {
	repositories []int64
	err          error
	tokens       []string
}

type unauthorizedError struct{}

func (unauthorizedError) Error() string      { return "401" }
func (unauthorizedError) Unauthorized() bool { return true }

func (g *githubStub) AccessibleRepositories(_ context.Context, token string) ([]int64, error) {
	g.tokens = append(g.tokens, token)
	return g.repositories, g.err
}

// sessionTokenValue is the fixture's browser session cookie: 32 bytes base64url,
// as the real session manager issues.
var sessionTokenValue = base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 32)))

const origin = "https://graphnest.example"

type harness struct {
	server *Server
	store  *memoryStore
	audit  *recordingAudit
	github *githubStub
	mux    *http.ServeMux
	clock  time.Time
	sealer *Sealer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store := newMemoryStore()
	recorder := &recordingAudit{}
	github := &githubStub{repositories: []int64{101, 102}}
	sealer, err := NewSealer(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{store: store, audit: recorder, github: github, clock: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC), sealer: sealer}
	tokens := NewProviderTokens(func() time.Time { return h.clock })
	// The login that preceded consent deposited the GitHub token for this session.
	tokens.StoreProviderToken(context.Background(), sessionTokenValue, "gho_user")
	h.server = &Server{
		Origin: origin, Store: store, Sessions: staticSessions{authn.Principal{Subject: "11", Method: authn.ProviderOAuth, RepositoryIDs: []int64{101}}},
		Sealer: sealer, GitHub: github, GitHubTokens: tokens, Audit: recorder,
		Now:      func() time.Time { return h.clock },
		UserName: func(context.Context, authn.Principal) string { return "Ada Lovelace" },
	}
	h.mux = http.NewServeMux()
	h.server.Register(h.mux)
	return h
}

func (h *harness) do(request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

func (h *harness) registerClient(t *testing.T, redirect string) string {
	t.Helper()
	body := `{"client_name":"OpenCode","redirect_uris":["` + redirect + `"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none"}`
	request := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := h.do(request)
	if response.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", response.Code, response.Body.String())
	}
	var registered struct {
		ClientID string `json:"client_id"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &registered)
	return registered.ClientID
}

func pkce() (verifier, challenge string) {
	verifier = strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func authorizeURL(clientID, redirect, challenge string) string {
	values := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"st4te"}, "resource": {origin + "/mcp"}}
	return "/oauth/authorize?" + values.Encode()
}

func cookieNamed(response *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

// runConsent drives GET /oauth/authorize with a session and posts the decision.
func (h *harness) runConsent(t *testing.T, clientID, redirect, challenge, decision string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, authorizeURL(clientID, redirect, challenge), nil)
	request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: sessionTokenValue})
	response := h.do(request)
	if response.Code != http.StatusOK {
		t.Fatalf("authorize status=%d body=%s", response.Code, response.Body.String())
	}
	page := response.Body.String()
	for _, want := range []string{"OpenCode", "Ada Lovelace", "http://127.0.0.1:5000", `name="decision" value="allow"`, `name="decision" value="deny"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("consent page missing %q:\n%s", want, page)
		}
	}
	requestCookie := cookieNamed(response, RequestCookie)
	if requestCookie == nil || !requestCookie.HttpOnly || !requestCookie.Secure || requestCookie.Path != "/oauth" {
		t.Fatalf("request cookie = %+v", requestCookie)
	}
	form := url.Values{"request_id": {requestCookie.Value}, "decision": {decision}}
	post := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Origin", origin)
	post.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: sessionTokenValue})
	post.AddCookie(requestCookie)
	return h.do(post)
}

func (h *harness) exchange(t *testing.T, form url.Values) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := h.do(request)
	var body map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	return response, body
}

// ---- tests ---------------------------------------------------------------------------

func TestMetadataAdvertisesThisServer(t *testing.T) {
	h := newHarness(t)
	response := h.do(httptest.NewRequest(http.MethodGet, ProtectedResourceMetadataPath, nil))
	var resource map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &resource)
	if response.Code != http.StatusOK || resource["resource"] != origin+"/mcp" || resource["authorization_servers"].([]any)[0] != origin {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	response = h.do(httptest.NewRequest(http.MethodGet, authorizationServerMetadataPath, nil))
	var as map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &as)
	if as["issuer"] != origin || as["token_endpoint"] != origin+"/oauth/token" || as["registration_endpoint"] != origin+"/oauth/register" ||
		as["code_challenge_methods_supported"].([]any)[0] != "S256" || as["token_endpoint_auth_methods_supported"].([]any)[0] != "none" {
		t.Fatalf("metadata=%v", as)
	}
	if h.do(httptest.NewRequest(http.MethodPost, authorizationServerMetadataPath, nil)).Code != http.StatusMethodNotAllowed {
		t.Fatal("metadata must be GET only")
	}
	recorder := httptest.NewRecorder()
	h.server.Challenge(recorder, false)
	if got := recorder.Header().Get("WWW-Authenticate"); got != `Bearer resource_metadata="`+origin+ProtectedResourceMetadataPath+`"` {
		t.Fatalf("challenge=%q", got)
	}
	h.server.Challenge(recorder, true)
	if got := recorder.Header().Get("WWW-Authenticate"); !strings.Contains(got, `error="invalid_token"`) {
		t.Fatalf("invalid token challenge=%q", got)
	}
}

func TestRegistrationAcceptsOnlyPublicLoopbackOrHTTPSClients(t *testing.T) {
	h := newHarness(t)
	id := h.registerClient(t, "http://127.0.0.1:5000/mcp/oauth/callback")
	if !strings.HasPrefix(id, ClientIDPrefix) || h.store.clients[id].Name != "OpenCode" {
		t.Fatalf("client=%+v", h.store.clients[id])
	}
	h.registerClient(t, "http://localhost:8765/callback")
	h.registerClient(t, "http://[::1]:8765/callback")
	h.registerClient(t, "https://ide.example.com/oauth/callback")

	cases := map[string]string{
		"non-loopback http": `{"redirect_uris":["http://evil.example.com/cb"]}`,
		"loopback https":    `{"redirect_uris":["https://127.0.0.1:5000/cb"]}`,
		"fragment":          `{"redirect_uris":["http://127.0.0.1:5000/cb#frag"]}`,
		"userinfo":          `{"redirect_uris":["http://user@127.0.0.1:5000/cb"]}`,
		"no redirects":      `{"redirect_uris":[]}`,
		"too many":          `{"redirect_uris":["http://127.0.0.1:1/","http://127.0.0.1:2/","http://127.0.0.1:3/","http://127.0.0.1:4/","http://127.0.0.1:5/","http://127.0.0.1:6/","http://127.0.0.1:7/","http://127.0.0.1:8/","http://127.0.0.1:9/"]}`,
		"secret client":     `{"redirect_uris":["http://127.0.0.1:5000/cb"],"token_endpoint_auth_method":"client_secret_post"}`,
		"implicit grant":    `{"redirect_uris":["http://127.0.0.1:5000/cb"],"grant_types":["implicit"]}`,
		"token response":    `{"redirect_uris":["http://127.0.0.1:5000/cb"],"response_types":["token"]}`,
		"not json":          `redirect_uris=x`,
	}
	for name, body := range cases {
		request := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if response := h.do(request); response.Code != http.StatusBadRequest {
			t.Errorf("%s: status=%d body=%s", name, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(`{"client_name":"  <script>x</script> `+strings.Repeat("n", 100)+`","redirect_uris":["http://127.0.0.1:1/cb"]}`))
	request.Header.Set("Content-Type", "application/json")
	response := h.do(request)
	var registered struct {
		ClientName string `json:"client_name"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &registered)
	if strings.ContainsAny(registered.ClientName, "<>") || len(registered.ClientName) > maxClientName {
		t.Fatalf("client_name not sanitised: %q", registered.ClientName)
	}
	if !strings.Contains(strings.Join(h.audit.operations(), ","), OperationClientRegistered) {
		t.Fatal("registration not audited")
	}
}

func TestAuthorizeRejectsBadClientOrRedirectWithoutRedirecting(t *testing.T) {
	h := newHarness(t)
	clientID := h.registerClient(t, "http://127.0.0.1:5000/cb")
	_, challenge := pkce()
	for name, target := range map[string]string{
		"unknown client":   authorizeURL("gnc_nope", "http://127.0.0.1:5000/cb", challenge),
		"foreign redirect": authorizeURL(clientID, "http://127.0.0.1:5000/other", challenge),
		"evil redirect":    authorizeURL(clientID, "https://evil.example.com/cb", challenge),
		"missing client":   "/oauth/authorize?response_type=code",
	} {
		response := h.do(httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest || response.Header().Get("Location") != "" || !strings.Contains(response.Body.String(), "Authorization failed") {
			t.Errorf("%s: status=%d location=%q", name, response.Code, response.Header().Get("Location"))
		}
	}
}

func TestAuthorizeRedirectsProtocolErrorsToTheClient(t *testing.T) {
	h := newHarness(t)
	clientID := h.registerClient(t, "http://127.0.0.1:5000/cb")
	_, challenge := pkce()
	base := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:5000/cb"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"s"}}
	mutate := func(edit func(url.Values)) string {
		values := url.Values{}
		for key, value := range base {
			values[key] = value
		}
		edit(values)
		return "/oauth/authorize?" + values.Encode()
	}
	cases := map[string]struct {
		target, wantError string
	}{
		"plain pkce":       {mutate(func(v url.Values) { v.Set("code_challenge_method", "plain") }), "invalid_request"},
		"no pkce":          {mutate(func(v url.Values) { v.Del("code_challenge") }), "invalid_request"},
		"bad challenge":    {mutate(func(v url.Values) { v.Set("code_challenge", "short") }), "invalid_request"},
		"token response":   {mutate(func(v url.Values) { v.Set("response_type", "token") }), "unsupported_response_type"},
		"foreign resource": {mutate(func(v url.Values) { v.Set("resource", "https://other.example/mcp") }), "invalid_target"},
		"duplicate state":  {mutate(func(v url.Values) { v["state"] = []string{"a", "b"} }), "invalid_request"},
		"oversized scope":  {mutate(func(v url.Values) { v.Set("scope", strings.Repeat("s", 300)) }), "invalid_request"},
	}
	for name, tc := range cases {
		response := h.do(httptest.NewRequest(http.MethodGet, tc.target, nil))
		location, err := url.Parse(response.Header().Get("Location"))
		if response.Code != http.StatusSeeOther || err != nil || location.Host != "127.0.0.1:5000" || location.Query().Get("error") != tc.wantError {
			t.Errorf("%s: status=%d location=%q", name, response.Code, response.Header().Get("Location"))
		}
		if name != "duplicate state" && location.Query().Get("state") != "s" {
			t.Errorf("%s: state not echoed", name)
		}
	}
}

func TestAuthorizeWithoutSessionStartsLoginAndResumes(t *testing.T) {
	h := newHarness(t)
	clientID := h.registerClient(t, "http://127.0.0.1:5000/cb")
	_, challenge := pkce()
	response := h.do(httptest.NewRequest(http.MethodGet, authorizeURL(clientID, "http://127.0.0.1:5001/cb", challenge), nil))
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/auth/oauth/github/login?return_to=%2Foauth%2Fauthorize%2Fresume" {
		t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	requestCookie := cookieNamed(response, RequestCookie)
	if requestCookie == nil {
		t.Fatal("no request cookie")
	}
	// Coming back without a session shows an error page, not consent.
	resume := httptest.NewRequest(http.MethodGet, ResumePath, nil)
	resume.AddCookie(requestCookie)
	if response := h.do(resume); response.Code != http.StatusBadRequest {
		t.Fatalf("resume without session status=%d", response.Code)
	}
	// With a session the pending request (ephemeral port 5001) reaches consent.
	resume = httptest.NewRequest(http.MethodGet, ResumePath, nil)
	resume.AddCookie(requestCookie)
	resume.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: sessionTokenValue})
	response = h.do(resume)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "http://127.0.0.1:5001") {
		t.Fatalf("resume status=%d body=%s", response.Code, response.Body.String())
	}
	// No cookie at all: nothing to resume.
	if response := h.do(httptest.NewRequest(http.MethodGet, ResumePath, nil)); response.Code != http.StatusBadRequest {
		t.Fatalf("resume without cookie status=%d", response.Code)
	}
}

func TestConsentDenyRedirectsAccessDenied(t *testing.T) {
	h := newHarness(t)
	clientID := h.registerClient(t, "http://127.0.0.1:5000/cb")
	_, challenge := pkce()
	response := h.runConsent(t, clientID, "http://127.0.0.1:5000/cb", challenge, "deny")
	location, _ := url.Parse(response.Header().Get("Location"))
	if response.Code != http.StatusSeeOther || location.Query().Get("error") != "access_denied" || location.Query().Get("state") != "st4te" || location.Query().Get("code") != "" {
		t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	if len(h.store.requests) != 0 {
		t.Fatal("denied request must be deleted")
	}
	if ops := strings.Join(h.audit.operations(), ","); !strings.Contains(ops, OperationConsentDenied) {
		t.Fatalf("audit=%s", ops)
	}
}

func TestConsentRequiresSameOriginAndMatchingRequest(t *testing.T) {
	h := newHarness(t)
	clientID := h.registerClient(t, "http://127.0.0.1:5000/cb")
	_, challenge := pkce()
	request := httptest.NewRequest(http.MethodGet, authorizeURL(clientID, "http://127.0.0.1:5000/cb", challenge), nil)
	request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: sessionTokenValue})
	requestCookie := cookieNamed(h.do(request), RequestCookie)

	post := func(edit func(*http.Request, url.Values)) *httptest.ResponseRecorder {
		form := url.Values{"request_id": {requestCookie.Value}, "decision": {"allow"}}
		r := httptest.NewRequest(http.MethodPost, "/oauth/authorize", nil)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		r.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: sessionTokenValue})
		r.AddCookie(requestCookie)
		edit(r, form)
		r.Body = io.NopCloser(strings.NewReader(form.Encode()))
		return h.do(r)
	}
	if response := post(func(r *http.Request, _ url.Values) { r.Header.Set("Origin", "https://evil.example") }); response.Code != http.StatusBadRequest {
		t.Fatalf("cross-origin consent status=%d", response.Code)
	}
	if response := post(func(_ *http.Request, form url.Values) { form.Set("request_id", "other") }); response.Code != http.StatusBadRequest {
		t.Fatalf("mismatched request_id status=%d", response.Code)
	}
	if response := post(func(r *http.Request, _ url.Values) { r.Header.Del("Cookie"); r.AddCookie(requestCookie) }); response.Code != http.StatusBadRequest {
		t.Fatalf("consent without session status=%d", response.Code)
	}
	if len(h.store.requests) != 1 {
		t.Fatal("rejected consents must not consume the request")
	}
	if response := post(func(*http.Request, url.Values) {}); response.Code != http.StatusSeeOther {
		t.Fatalf("valid consent status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestFullCodeFlowIssuesRefreshesAndRevokes(t *testing.T) {
	h := newHarness(t)
	clientID := h.registerClient(t, "http://127.0.0.1:5000/cb")
	verifier, challenge := pkce()
	response := h.runConsent(t, clientID, "http://127.0.0.1:5000/cb", challenge, "allow")
	location, _ := url.Parse(response.Header().Get("Location"))
	code := location.Query().Get("code")
	if response.Code != http.StatusSeeOther || !strings.HasPrefix(code, CodePrefix) || location.Query().Get("state") != "st4te" || location.Host != "127.0.0.1:5000" {
		t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	if cleared := cookieNamed(response, RequestCookie); cleared == nil || cleared.MaxAge != -1 {
		t.Fatal("request cookie must be cleared after consent")
	}

	// Wrong verifier fails and burns the code.
	tokenResponse, body := h.exchange(t, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:5000/cb"}, "code_verifier": {strings.Repeat("x", 43)}})
	if tokenResponse.Code != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("bad verifier status=%d body=%v", tokenResponse.Code, body)
	}
	tokenResponse, body = h.exchange(t, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:5000/cb"}, "code_verifier": {verifier}})
	if tokenResponse.Code != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("burnt code status=%d body=%v", tokenResponse.Code, body)
	}

	// Fresh consent, correct exchange. The burnt code took the deposited
	// GitHub token with it; a real user re-authenticates, which re-deposits it.
	h.server.GitHubTokens.(*ProviderTokens).StoreProviderToken(context.Background(), sessionTokenValue, "gho_user")
	response = h.runConsent(t, clientID, "http://127.0.0.1:5000/cb", challenge, "allow")
	location, _ = url.Parse(response.Header().Get("Location"))
	code = location.Query().Get("code")
	tokenResponse, body = h.exchange(t, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:5000/cb"}, "code_verifier": {verifier}})
	if tokenResponse.Code != http.StatusOK || tokenResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("exchange status=%d body=%v", tokenResponse.Code, body)
	}
	access, _ := body["access_token"].(string)
	refresh, _ := body["refresh_token"].(string)
	if !strings.HasPrefix(access, AccessTokenPrefix) || !strings.HasPrefix(refresh, RefreshTokenPrefix) || body["token_type"] != "Bearer" || body["expires_in"] != float64(3600) {
		t.Fatalf("tokens=%v", body)
	}
	accessHash, _ := hashSecret(access, AccessTokenPrefix)
	principal, err := h.store.OAuthPrincipal(context.Background(), accessHash, h.clock)
	if err != nil || principal.Method != authn.ProviderOAuthToken {
		t.Fatalf("principal=%+v err=%v", principal, err)
	}
	var grant *authn.OAuthGrant
	for _, candidate := range h.store.grants {
		grant = candidate
	}
	if plaintext, err := h.sealer.Open(grant.ID, grant.GitHubTokenCiphertext); err != nil || plaintext != "gho_user" {
		t.Fatalf("stored GitHub token=%q err=%v", plaintext, err)
	}

	// Refresh: rotates, re-syncs GitHub grants, keeps the absolute expiry.
	h.clock = h.clock.Add(50 * time.Minute)
	h.github.repositories = []int64{101}
	tokenResponse, refreshed := h.exchange(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}})
	if tokenResponse.Code != http.StatusOK || refreshed["access_token"] == access || refreshed["refresh_token"] == refresh {
		t.Fatalf("refresh status=%d body=%v", tokenResponse.Code, refreshed)
	}
	if got := h.store.github[grant.UserID]; len(got) != 1 || got[0] != 101 {
		t.Fatalf("GitHub grants after refresh=%v", got)
	}
	if len(h.github.tokens) != 1 || h.github.tokens[0] != "gho_user" {
		t.Fatalf("GitHub called with %v", h.github.tokens)
	}
	if _, err := h.store.OAuthPrincipal(context.Background(), accessHash, h.clock); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("old access token must be dead after rotation")
	}
	if !grant.ExpiresAt.Equal(time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("absolute expiry moved to %v", grant.ExpiresAt)
	}

	// Wrong client cannot refresh; replaying the old refresh token revokes the grant.
	tokenResponse, body = h.exchange(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshed["refresh_token"].(string)}, "client_id": {"gnc_other"}})
	if tokenResponse.Code != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("foreign client refresh status=%d body=%v", tokenResponse.Code, body)
	}
	// Within the grace window a lost-response retry fails without revoking.
	tokenResponse, body = h.exchange(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}})
	if tokenResponse.Code != http.StatusBadRequest || body["error"] != "invalid_grant" || grant.RevokedAt != nil {
		t.Fatalf("retry within grace status=%d body=%v revoked=%v", tokenResponse.Code, body, grant.RevokedAt)
	}
	h.clock = h.clock.Add(refreshGrace + time.Second)
	tokenResponse, body = h.exchange(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}})
	if tokenResponse.Code != http.StatusBadRequest || body["error"] != "invalid_grant" || grant.RevokedAt == nil {
		t.Fatalf("replay status=%d body=%v revoked=%v", tokenResponse.Code, body, grant.RevokedAt)
	}
	ops := strings.Join(h.audit.operations(), ",")
	for _, want := range []string{OperationConsentGranted, OperationGrantCreated, OperationGrantRefreshed, OperationGrantReplay} {
		if !strings.Contains(ops, want) {
			t.Errorf("audit missing %s: %s", want, ops)
		}
	}
}

func TestRefreshHandlesGitHubOutcomes(t *testing.T) {
	newGrant := func(t *testing.T, h *harness) (string, *authn.OAuthGrant) {
		clientID := h.registerClient(t, "http://127.0.0.1:5000/cb")
		verifier, challenge := pkce()
		response := h.runConsent(t, clientID, "http://127.0.0.1:5000/cb", challenge, "allow")
		location, _ := url.Parse(response.Header().Get("Location"))
		_, body := h.exchange(t, url.Values{"grant_type": {"authorization_code"}, "code": {location.Query().Get("code")}, "client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:5000/cb"}, "code_verifier": {verifier}})
		var grant *authn.OAuthGrant
		for _, candidate := range h.store.grants {
			grant = candidate
		}
		h.store.github[grant.UserID] = []int64{101, 102}
		refresh, _ := body["refresh_token"].(string)
		_, refreshed := h.exchange(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}})
		if refreshed["access_token"] == nil {
			t.Fatalf("refresh failed: %v", refreshed)
		}
		return clientID, grant
	}
	t.Run("github rejects the token: grants kept, token dropped", func(t *testing.T) {
		h := newHarness(t)
		h.github.err = unauthorizedError{}
		_, grant := newGrant(t, h)
		if got := h.store.github[grant.UserID]; len(got) != 2 {
			t.Fatalf("grants changed on 401: %v", got)
		}
		if grant.GitHubTokenCiphertext != nil {
			t.Fatal("unusable GitHub token must be dropped")
		}
	})
	t.Run("github outage: everything kept", func(t *testing.T) {
		h := newHarness(t)
		h.github.err = errors.New("503")
		_, grant := newGrant(t, h)
		if got := h.store.github[grant.UserID]; len(got) != 2 || grant.GitHubTokenCiphertext == nil {
			t.Fatalf("outage must change nothing: grants=%v ct=%v", got, grant.GitHubTokenCiphertext != nil)
		}
	})
	t.Run("no github integration: refresh still works", func(t *testing.T) {
		h := newHarness(t)
		h.server.GitHub = nil
		h.server.GitHubTokens = nil
		newGrant(t, h)
	})
}

func TestTokenEndpointRejectsMalformedRequests(t *testing.T) {
	h := newHarness(t)
	cases := map[string]struct {
		contentType string
		form        url.Values
		authz       string
		wantStatus  int
		wantError   string
	}{
		"json body":            {"application/json", url.Values{"grant_type": {"authorization_code"}}, "", http.StatusBadRequest, "invalid_request"},
		"unknown grant":        {"application/x-www-form-urlencoded", url.Values{"grant_type": {"password"}}, "", http.StatusBadRequest, "unsupported_grant_type"},
		"missing code":         {"application/x-www-form-urlencoded", url.Values{"grant_type": {"authorization_code"}, "client_id": {"c"}, "code_verifier": {"v"}}, "", http.StatusBadRequest, "invalid_request"},
		"unknown code":         {"application/x-www-form-urlencoded", url.Values{"grant_type": {"authorization_code"}, "code": {CodePrefix + strings.Repeat("A", 43)}, "client_id": {"c"}, "code_verifier": {strings.Repeat("v", 43)}}, "", http.StatusBadRequest, "invalid_grant"},
		"unknown refresh":      {"application/x-www-form-urlencoded", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {RefreshTokenPrefix + strings.Repeat("A", 43)}, "client_id": {"c"}}, "", http.StatusBadRequest, "invalid_grant"},
		"client authenticates": {"application/x-www-form-urlencoded", url.Values{"grant_type": {"refresh_token"}}, "Basic abc", http.StatusUnauthorized, "invalid_client"},
	}
	for name, tc := range cases {
		request := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(tc.form.Encode()))
		request.Header.Set("Content-Type", tc.contentType)
		if tc.authz != "" {
			request.Header.Set("Authorization", tc.authz)
		}
		response := h.do(request)
		var body map[string]string
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		if response.Code != tc.wantStatus || body["error"] != tc.wantError {
			t.Errorf("%s: status=%d body=%v", name, response.Code, body)
		}
	}
	if h.do(httptest.NewRequest(http.MethodGet, "/oauth/token", nil)).Code != http.StatusMethodNotAllowed {
		t.Fatal("token endpoint must be POST only")
	}
}

func TestRevocationIsClientScopedAndIdempotent(t *testing.T) {
	h := newHarness(t)
	clientID := h.registerClient(t, "http://127.0.0.1:5000/cb")
	verifier, challenge := pkce()
	response := h.runConsent(t, clientID, "http://127.0.0.1:5000/cb", challenge, "allow")
	location, _ := url.Parse(response.Header().Get("Location"))
	_, body := h.exchange(t, url.Values{"grant_type": {"authorization_code"}, "code": {location.Query().Get("code")}, "client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:5000/cb"}, "code_verifier": {verifier}})
	access := body["access_token"].(string)
	accessHash, _ := hashSecret(access, AccessTokenPrefix)

	revoke := func(token, client string) int {
		request := httptest.NewRequest(http.MethodPost, "/oauth/revoke", strings.NewReader(url.Values{"token": {token}, "client_id": {client}}.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return h.do(request).Code
	}
	if revoke(access, "gnc_other") != http.StatusOK {
		t.Fatal("revocation must always answer 200")
	}
	if _, err := h.store.OAuthPrincipal(context.Background(), accessHash, h.clock); err != nil {
		t.Fatal("another client must not be able to revoke the grant")
	}
	if revoke("not-a-token", clientID) != http.StatusOK || revoke(access, clientID) != http.StatusOK {
		t.Fatal("revocation must always answer 200")
	}
	if _, err := h.store.OAuthPrincipal(context.Background(), accessHash, h.clock); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("grant must be revoked")
	}
	request := httptest.NewRequest(http.MethodPost, "/oauth/revoke", strings.NewReader(url.Values{"token": {access}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if h.do(request).Code != http.StatusBadRequest {
		t.Fatal("client_id is required")
	}
}

func TestLimiterGatesRegistrationAndTokens(t *testing.T) {
	h := newHarness(t)
	h.server.Limiter = denyAll{}
	request := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(`{"redirect_uris":["http://127.0.0.1:1/cb"]}`))
	request.Header.Set("Content-Type", "application/json")
	if h.do(request).Code != http.StatusTooManyRequests {
		t.Fatal("registration must honour the limiter")
	}
	request = httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader("grant_type=refresh_token"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if h.do(request).Code != http.StatusTooManyRequests {
		t.Fatal("token endpoint must honour the limiter")
	}
}

type denyAll struct{}

func (denyAll) Allow(string) bool { return false }

func TestRedirectMatchingAllowsEphemeralLoopbackPortsOnly(t *testing.T) {
	cases := []struct {
		registered, requested string
		want                  bool
	}{
		{"http://127.0.0.1:5000/cb", "http://127.0.0.1:61234/cb", true},
		{"http://localhost:5000/cb", "http://localhost:1/cb", true},
		{"http://[::1]:5000/cb", "http://[::1]:7/cb", true},
		{"http://127.0.0.1:5000/cb", "http://127.0.0.1:5000/other", false},
		{"http://127.0.0.1:5000/cb", "http://localhost:5000/cb", false},
		{"https://ide.example.com/cb", "https://ide.example.com:8443/cb", false},
		{"https://ide.example.com/cb", "https://ide.example.com/cb", true},
		{"http://127.0.0.1:5000/cb?x=1", "http://127.0.0.1:5001/cb?x=2", false},
	}
	for _, tc := range cases {
		if got := redirectMatches(tc.registered, tc.requested); got != tc.want {
			t.Errorf("redirectMatches(%q, %q)=%v want %v", tc.registered, tc.requested, got, tc.want)
		}
	}
}

func TestProviderTokensFollowTheFlowAndExpire(t *testing.T) {
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	tokens := NewProviderTokens(func() time.Time { return clock })
	tokens.StoreProviderToken(context.Background(), "not-a-session", "gho_x")
	tokens.StoreProviderToken(context.Background(), sessionTokenValue, "")
	if len(tokens.entries) != 0 {
		t.Fatal("malformed deposits must be ignored")
	}
	tokens.StoreProviderToken(context.Background(), sessionTokenValue, "gho_user")
	code := [32]byte{1}
	if _, ok := tokens.TakeForCode(code); ok {
		t.Fatal("nothing attached to the code yet")
	}
	tokens.Transfer(sessionTokenValue, code)
	tokens.Transfer(sessionTokenValue, [32]byte{2}) // second consent without re-login: nothing left to move
	if got, ok := tokens.TakeForCode(code); !ok || got != "gho_user" {
		t.Fatalf("token=%q ok=%v", got, ok)
	}
	if _, ok := tokens.TakeForCode(code); ok {
		t.Fatal("token must be taken exactly once")
	}
	tokens.StoreProviderToken(context.Background(), sessionTokenValue, "gho_user")
	clock = clock.Add(pendingTTL + time.Second)
	tokens.Transfer(sessionTokenValue, code)
	if _, ok := tokens.TakeForCode(code); ok {
		t.Fatal("expired deposits must not transfer")
	}
}

func TestResolveRedirectRebuildsTargetFromRegistration(t *testing.T) {
	cases := []struct {
		registered, requested, want string
	}{
		{"http://127.0.0.1:5000/cb", "http://127.0.0.1:61234/cb", "http://127.0.0.1:61234/cb"},
		{"http://localhost:5000/cb?x=1", "http://localhost:7/cb?x=1", "http://localhost:7/cb?x=1"},
		{"https://ide.example.com/cb", "https://ide.example.com/cb", "https://ide.example.com/cb"},
		// A userinfo or fragment smuggled into an otherwise matching loopback URI never survives.
		{"http://127.0.0.1:5000/cb", "http://evil@127.0.0.1:5000/cb", "http://127.0.0.1:5000/cb"},
		{"http://127.0.0.1:5000/cb", "http://127.0.0.1:70000/cb", ""},
		{"http://127.0.0.1:5000/cb", "http://127.0.0.1/cb", ""},
	}
	for _, tc := range cases {
		got, ok := resolveRedirect(tc.registered, tc.requested)
		if ok != (tc.want != "") || got != tc.want {
			t.Errorf("resolveRedirect(%q, %q)=%q,%v want %q", tc.registered, tc.requested, got, ok, tc.want)
		}
	}
}
