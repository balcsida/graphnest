package oauthas

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
)

const (
	// Lifetimes. Access tokens are short so revocation and GitHub-derived
	// access changes propagate within the hour; the grant itself is capped.
	AccessTokenTTL                  = time.Hour
	GrantTTL                        = 30 * 24 * time.Hour
	codeTTL                         = 60 * time.Second
	pendingTTL                      = 10 * time.Minute
	refreshGrace                    = 30 * time.Second
	syncTimeout                     = 10 * time.Second
	maxRedirectURIs                 = 8
	maxClientName                   = 64
	maxFormBytes                    = 16 << 10
	RequestCookie                   = "__Host-graphnest_oauth_req"
	ResumePath                      = "/oauth/authorize/resume"
	MCPPath                         = "/mcp"
	ProtectedResourceMetadataPath   = "/.well-known/oauth-protected-resource"
	authorizationServerMetadataPath = "/.well-known/oauth-authorization-server"
)

// Audit operations recorded by the authorization server.
const (
	OperationClientRegistered = audit.OperationOAuthClientRegistered
	OperationConsentGranted   = audit.OperationOAuthConsentGranted
	OperationConsentDenied    = audit.OperationOAuthConsentDenied
	OperationGrantCreated     = audit.OperationOAuthGrantCreated
	OperationGrantRefreshed   = audit.OperationOAuthGrantRefreshed
	OperationGrantRevoked     = audit.OperationOAuthGrantRevoked
	OperationGrantReplay      = audit.OperationOAuthGrantReplay
)

// SessionAuthenticator resolves the browser session cookie to a principal.
type SessionAuthenticator interface {
	Authenticate(context.Context, string) (authn.Principal, error)
}

// GitHubAccess re-derives a user's repository access from GitHub with the
// user's own token; it is nil when GitHub-derived access is not configured.
type GitHubAccess interface {
	AccessibleRepositories(ctx context.Context, accessToken string) ([]int64, error)
}

// GitHubTokenSource is the hand-off from the login that completed a pending
// authorization to the code exchange: consent moves the identity provider's
// token from the browser session to the authorization code, exchange takes it.
type GitHubTokenSource interface {
	Transfer(sessionToken string, codeHash [32]byte)
	TakeForCode(codeHash [32]byte) (string, bool)
}

// Server is the authorization server. All URLs derive from Origin.
type Server struct {
	Origin       string
	Store        authn.OAuthStore
	Sessions     SessionAuthenticator
	Sealer       *Sealer
	GitHub       GitHubAccess
	GitHubTokens GitHubTokenSource
	Audit        audit.Recorder
	Now          func() time.Time
	Rand         io.Reader
	// UserName renders the consent page; nil falls back to the subject.
	UserName func(context.Context, authn.Principal) string
	// Limiter is consulted for registration and token requests; nil allows all.
	Limiter interface{ Allow(remoteAddr string) bool }
}

func (server *Server) now() time.Time {
	if server.Now != nil {
		return server.Now()
	}
	return time.Now()
}

// Register mounts every endpoint. The /mcp handler itself stays where it is;
// use Challenge to decorate its 401 responses.
func (server *Server) Register(mux *http.ServeMux) {
	mux.Handle(ProtectedResourceMetadataPath, public(getOnly(http.HandlerFunc(server.protectedResourceMetadata))))
	mux.Handle(authorizationServerMetadataPath, public(getOnly(http.HandlerFunc(server.authorizationServerMetadata))))
	mux.Handle("/oauth/register", noStore(postOnly(http.HandlerFunc(server.register))))
	mux.Handle("/oauth/authorize", noStore(http.HandlerFunc(server.authorize)))
	mux.Handle(ResumePath, noStore(getOnly(http.HandlerFunc(server.resume))))
	mux.Handle("/oauth/token", noStore(postOnly(http.HandlerFunc(server.token))))
	mux.Handle("/oauth/revoke", noStore(postOnly(http.HandlerFunc(server.revoke))))
}

// Challenge writes the WWW-Authenticate header MCP clients use to discover
// this authorization server (RFC 9728 §5.1).
func (server *Server) Challenge(writer http.ResponseWriter, invalidToken bool) {
	value := `Bearer resource_metadata="` + server.Origin + ProtectedResourceMetadataPath + `"`
	if invalidToken {
		value = `Bearer error="invalid_token", resource_metadata="` + server.Origin + ProtectedResourceMetadataPath + `"`
	}
	writer.Header().Set("WWW-Authenticate", value)
}

// ---- discovery -------------------------------------------------------------

func (server *Server) protectedResourceMetadata(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"resource":                 server.Origin + MCPPath,
		"authorization_servers":    []string{server.Origin},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{},
	})
}

func (server *Server) authorizationServerMetadata(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"issuer":                                     server.Origin,
		"authorization_endpoint":                     server.Origin + "/oauth/authorize",
		"token_endpoint":                             server.Origin + "/oauth/token",
		"registration_endpoint":                      server.Origin + "/oauth/register",
		"revocation_endpoint":                        server.Origin + "/oauth/revoke",
		"response_types_supported":                   []string{"code"},
		"response_modes_supported":                   []string{"query"},
		"grant_types_supported":                      []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":           []string{"S256"},
		"token_endpoint_auth_methods_supported":      []string{"none"},
		"revocation_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                           []string{},
	})
}

// ---- dynamic client registration (RFC 7591) --------------------------------

type registrationRequest struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope"`
	ClientURI               string   `json:"client_uri"`
	LogoURI                 string   `json:"logo_uri"`
	SoftwareID              string   `json:"software_id"`
	SoftwareVersion         string   `json:"software_version"`
	Contacts                []string `json:"contacts"`
	TOSURI                  string   `json:"tos_uri"`
	PolicyURI               string   `json:"policy_uri"`
	JWKSURI                 string   `json:"jwks_uri"`
	ApplicationType         string   `json:"application_type"`
}

func (server *Server) register(writer http.ResponseWriter, request *http.Request) {
	if !server.allow(request) {
		writeOAuthError(writer, http.StatusTooManyRequests, "invalid_request", "too many requests")
		return
	}
	if !strings.HasPrefix(request.Header.Get("Content-Type"), "application/json") {
		writeOAuthError(writer, http.StatusBadRequest, "invalid_client_metadata", "request body must be JSON")
		return
	}
	var input registrationRequest
	if err := json.NewDecoder(io.LimitReader(request.Body, maxFormBytes)).Decode(&input); err != nil {
		writeOAuthError(writer, http.StatusBadRequest, "invalid_client_metadata", "request body is not valid JSON")
		return
	}
	if len(input.RedirectURIs) == 0 || len(input.RedirectURIs) > maxRedirectURIs {
		writeOAuthError(writer, http.StatusBadRequest, "invalid_redirect_uri", "between 1 and 8 redirect_uris are required")
		return
	}
	for _, redirect := range input.RedirectURIs {
		if !validRedirectURI(redirect) {
			writeOAuthError(writer, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must be loopback http URIs or https URIs")
			return
		}
	}
	if input.TokenEndpointAuthMethod != "" && input.TokenEndpointAuthMethod != "none" {
		writeOAuthError(writer, http.StatusBadRequest, "invalid_client_metadata", "only public clients (token_endpoint_auth_method none) are supported")
		return
	}
	for _, grant := range input.GrantTypes {
		if grant != "authorization_code" && grant != "refresh_token" {
			writeOAuthError(writer, http.StatusBadRequest, "invalid_client_metadata", "unsupported grant_type "+grant)
			return
		}
	}
	for _, responseType := range input.ResponseTypes {
		if responseType != "code" {
			writeOAuthError(writer, http.StatusBadRequest, "invalid_client_metadata", "unsupported response_type "+responseType)
			return
		}
	}
	name := sanitizeClientName(input.ClientName)
	id, _, err := newSecret(server.Rand, ClientIDPrefix)
	if err != nil {
		writeOAuthError(writer, http.StatusInternalServerError, "server_error", "could not register client")
		return
	}
	now := server.now()
	client := authn.OAuthClient{ID: id, Name: name, RedirectURIs: input.RedirectURIs, CreatedAt: now}
	if err := server.Store.CreateOAuthClient(request.Context(), client); err != nil {
		writeOAuthError(writer, http.StatusServiceUnavailable, "temporarily_unavailable", "could not register client")
		return
	}
	server.record(request.Context(), audit.Event{ActorType: "anonymous", TargetType: "oauth_client", TargetID: id, Operation: OperationClientRegistered, Outcome: "success"})
	writeJSON(writer, http.StatusCreated, map[string]any{
		"client_id":                  id,
		"client_id_issued_at":        now.Unix(),
		"client_name":                name,
		"redirect_uris":              input.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
}

// validRedirectURI accepts loopback http(s) on any port (RFC 8252 §7.3) and
// https on a real host; everything else, including plain http to a non-loopback
// host and URIs with fragments, is rejected.
func validRedirectURI(raw string) bool {
	if len(raw) > 2048 {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Fragment != "" || parsed.Host == "" || parsed.User != nil {
		return false
	}
	switch parsed.Scheme {
	case "https":
		return !isLoopbackHost(parsed.Hostname())
	case "http":
		return isLoopbackHost(parsed.Hostname())
	}
	return false
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// resolveRedirect matches a requested redirect against a registered one and
// returns the URI the browser will actually be sent to. That URI is rebuilt
// from the *registered* value: only the port is taken from the request, and
// only for loopback clients, which bind an ephemeral port per run (RFC 8252
// §7.3). Nothing else user-supplied ever becomes a redirect target.
func resolveRedirect(registered, requested string) (string, bool) {
	if registered == requested {
		return registered, true
	}
	a, errA := url.Parse(registered)
	b, errB := url.Parse(requested)
	if errA != nil || errB != nil || a.Scheme != "http" || b.Scheme != "http" || !isLoopbackHost(a.Hostname()) || !isLoopbackHost(b.Hostname()) {
		return "", false
	}
	if a.Hostname() != b.Hostname() || a.Path != b.Path || a.RawQuery != b.RawQuery {
		return "", false
	}
	port, err := strconv.Atoi(b.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", false
	}
	resolved := *a
	resolved.Host = net.JoinHostPort(a.Hostname(), strconv.Itoa(port))
	return resolved.String(), true
}

// redirectMatches reports whether a requested redirect is acceptable for a
// registration; see resolveRedirect for the URI that is actually used.
func redirectMatches(registered, requested string) bool {
	_, ok := resolveRedirect(registered, requested)
	return ok
}

func sanitizeClientName(name string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(name) {
		if unicode.IsPrint(r) && r != '<' && r != '>' {
			builder.WriteRune(r)
		}
		if builder.Len() >= maxClientName {
			break
		}
	}
	if builder.Len() == 0 {
		return "MCP client"
	}
	return builder.String()
}

// ---- authorization endpoint ----------------------------------------------------

func (server *Server) authorize(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		server.startAuthorization(writer, request)
	case http.MethodPost:
		server.decide(writer, request)
	default:
		writer.Header().Set("Allow", "GET, POST")
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (server *Server) startAuthorization(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	now := server.now()
	clientID, ok := single(query, "client_id")
	if !ok {
		server.authorizeErrorPage(writer, "The authorization request is missing a client_id.")
		return
	}
	client, err := server.Store.OAuthClient(request.Context(), clientID, now)
	if err != nil {
		server.authorizeErrorPage(writer, "Unknown client. Register the client first.")
		return
	}
	requestedRedirect, ok := single(query, "redirect_uri")
	if !ok {
		server.authorizeErrorPage(writer, "The authorization request is missing a redirect_uri.")
		return
	}
	// Never redirect to an unregistered URI (RFC 6749 §4.1.2.1). From here on
	// `redirect` is derived from the registration, not from the request.
	redirect, ok := registeredRedirect(client.RedirectURIs, requestedRedirect)
	if !ok {
		server.authorizeErrorPage(writer, "The redirect_uri does not match the client registration.")
		return
	}
	state, _ := single(query, "state")
	fail := func(code, description string) {
		redirectError(writer, request, redirect, state, code, description)
	}
	for key, values := range query {
		if len(values) > 1 {
			fail("invalid_request", "parameter "+key+" is repeated")
			return
		}
	}
	if responseType, ok := single(query, "response_type"); !ok || responseType != "code" {
		fail("unsupported_response_type", "response_type must be code")
		return
	}
	challenge, ok := single(query, "code_challenge")
	if !ok || !validChallenge(challenge) {
		fail("invalid_request", "code_challenge (S256) is required")
		return
	}
	if method, ok := single(query, "code_challenge_method"); !ok || method != "S256" {
		fail("invalid_request", "code_challenge_method must be S256")
		return
	}
	resource, _ := single(query, "resource")
	if resource != "" && resource != server.Origin+MCPPath {
		fail("invalid_target", "resource must be "+server.Origin+MCPPath)
		return
	}
	scope, _ := single(query, "scope")
	if len(scope) > 256 || len(state) > 1024 {
		fail("invalid_request", "scope or state is too long")
		return
	}
	handle, handleHash, err := newSecret(server.Rand, "")
	if err != nil {
		fail("server_error", "could not start authorization")
		return
	}
	pending := authn.OAuthAuthorizationRequest{
		ID: handleHash, Phase: "pending", ClientID: client.ID, RedirectURI: redirect, CodeChallenge: challenge,
		State: state, Scope: scope, Resource: resource, CreatedAt: now, ExpiresAt: now.Add(pendingTTL),
	}
	if err := server.Store.CreateOAuthAuthorizationRequest(request.Context(), pending); err != nil {
		fail("temporarily_unavailable", "could not start authorization")
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name: RequestCookie, Value: handle, Path: "/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Expires: now.Add(pendingTTL), MaxAge: int(pendingTTL / time.Second),
	})
	if _, ok := server.sessionPrincipal(request); !ok {
		http.Redirect(writer, request, "/auth/oauth/github/login?return_to="+url.QueryEscape(ResumePath), http.StatusSeeOther)
		return
	}
	server.consent(writer, request, handle, pending, client)
}

// resume is where the login flow sends the browser back once a session exists.
func (server *Server) resume(writer http.ResponseWriter, request *http.Request) {
	handle, pending, client, ok := server.pendingFromCookie(writer, request)
	if !ok {
		return
	}
	if _, ok := server.sessionPrincipal(request); !ok {
		server.authorizeErrorPage(writer, "Sign-in did not complete. Start the authorization again from your MCP client.")
		return
	}
	server.consent(writer, request, handle, pending, client)
}

func (server *Server) pendingFromCookie(writer http.ResponseWriter, request *http.Request) (string, authn.OAuthAuthorizationRequest, authn.OAuthClient, bool) {
	cookie, count := namedCookie(request, RequestCookie)
	if count != 1 {
		server.authorizeErrorPage(writer, "This authorization request has expired. Start again from your MCP client.")
		return "", authn.OAuthAuthorizationRequest{}, authn.OAuthClient{}, false
	}
	hash, ok := hashSecret(cookie, "")
	if !ok {
		server.authorizeErrorPage(writer, "This authorization request is invalid. Start again from your MCP client.")
		return "", authn.OAuthAuthorizationRequest{}, authn.OAuthClient{}, false
	}
	now := server.now()
	pending, err := server.Store.OAuthAuthorizationRequest(request.Context(), hash, "pending", now)
	if err != nil {
		server.authorizeErrorPage(writer, "This authorization request has expired. Start again from your MCP client.")
		return "", authn.OAuthAuthorizationRequest{}, authn.OAuthClient{}, false
	}
	client, err := server.Store.OAuthClient(request.Context(), pending.ClientID, now)
	if err != nil {
		server.authorizeErrorPage(writer, "Unknown client.")
		return "", authn.OAuthAuthorizationRequest{}, authn.OAuthClient{}, false
	}
	return cookie, pending, client, true
}

func (server *Server) sessionPrincipal(request *http.Request) (authn.Principal, bool) {
	if server.Sessions == nil {
		return authn.Principal{}, false
	}
	token, count := namedCookie(request, authn.SessionCookieName)
	if count != 1 {
		return authn.Principal{}, false
	}
	principal, err := server.Sessions.Authenticate(request.Context(), token)
	if err != nil || principal.ForceRotation || !authn.IsInteractiveMethod(principal.Method) {
		return authn.Principal{}, false
	}
	return principal, true
}

// decide handles the consent form.
func (server *Server) decide(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Origin") != server.Origin {
		server.authorizeErrorPage(writer, "The consent form was submitted from an unexpected origin.")
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxFormBytes)
	if err := request.ParseForm(); err != nil {
		server.authorizeErrorPage(writer, "The consent form is invalid.")
		return
	}
	handle, pending, client, ok := server.pendingFromCookie(writer, request)
	if !ok {
		return
	}
	if request.PostForm.Get("request_id") != handle {
		server.authorizeErrorPage(writer, "The consent form does not match this authorization request.")
		return
	}
	principal, ok := server.sessionPrincipal(request)
	if !ok {
		server.authorizeErrorPage(writer, "Your session has expired. Start again from your MCP client.")
		return
	}
	userID, err := strconv.ParseInt(principal.Subject, 10, 64)
	if err != nil || userID < 1 {
		server.authorizeErrorPage(writer, "Your session cannot authorize clients.")
		return
	}
	http.SetCookie(writer, &http.Cookie{Name: RequestCookie, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	now := server.now()
	if request.PostForm.Get("decision") != "allow" {
		_ = server.Store.DeleteOAuthAuthorizationRequest(request.Context(), pending.ID)
		server.record(request.Context(), audit.Event{ActorType: "user", ActorID: principal.Subject, TargetType: "oauth_client", TargetID: client.ID, AuthenticationMethod: principal.Method, Operation: OperationConsentDenied, Outcome: "denied"})
		redirectError(writer, request, pending.RedirectURI, pending.State, "access_denied", "the user denied the request")
		return
	}
	code, codeHash, err := newSecret(server.Rand, CodePrefix)
	if err != nil || server.Store.IssueOAuthCode(request.Context(), pending.ID, codeHash, userID, now.Add(codeTTL), now) != nil {
		redirectError(writer, request, pending.RedirectURI, pending.State, "server_error", "could not issue an authorization code")
		return
	}
	if server.GitHubTokens != nil {
		sessionToken, _ := namedCookie(request, authn.SessionCookieName)
		server.GitHubTokens.Transfer(sessionToken, codeHash)
	}
	server.record(request.Context(), audit.Event{ActorType: "user", ActorID: principal.Subject, TargetType: "oauth_client", TargetID: client.ID, AuthenticationMethod: principal.Method, Operation: OperationConsentGranted, Outcome: "success"})
	target, _ := url.Parse(pending.RedirectURI)
	values := target.Query()
	values.Set("code", code)
	if pending.State != "" {
		values.Set("state", pending.State)
	}
	target.RawQuery = values.Encode()
	http.Redirect(writer, request, target.String(), http.StatusSeeOther)
}

// ---- token endpoint -------------------------------------------------------------

func (server *Server) token(writer http.ResponseWriter, request *http.Request) {
	if !server.allow(request) {
		writeOAuthError(writer, http.StatusTooManyRequests, "invalid_request", "too many requests")
		return
	}
	if !strings.HasPrefix(request.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		writeOAuthError(writer, http.StatusBadRequest, "invalid_request", "request body must be application/x-www-form-urlencoded")
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxFormBytes)
	if err := request.ParseForm(); err != nil {
		writeOAuthError(writer, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	if len(request.Header.Values("Authorization")) > 0 {
		writeOAuthError(writer, http.StatusUnauthorized, "invalid_client", "public clients must not authenticate")
		return
	}
	switch request.PostForm.Get("grant_type") {
	case "authorization_code":
		server.exchangeCode(writer, request)
	case "refresh_token":
		server.refresh(writer, request)
	default:
		writeOAuthError(writer, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
}

func (server *Server) exchangeCode(writer http.ResponseWriter, request *http.Request) {
	form := request.PostForm
	code, redirect, clientID, verifier := form.Get("code"), form.Get("redirect_uri"), form.Get("client_id"), form.Get("code_verifier")
	codeHash, ok := hashSecret(code, CodePrefix)
	if !ok || clientID == "" || verifier == "" {
		writeOAuthError(writer, http.StatusBadRequest, "invalid_request", "code, client_id and code_verifier are required")
		return
	}
	now := server.now()
	pending, err := server.Store.ConsumeOAuthCode(request.Context(), codeHash, now)
	if err != nil {
		writeOAuthError(writer, http.StatusBadRequest, "invalid_grant", "authorization code is invalid, expired or already used")
		return
	}
	if pending.ClientID != clientID || (redirect != "" && !redirectMatches(pending.RedirectURI, redirect)) || !verifyPKCE(verifier, pending.CodeChallenge) {
		writeOAuthError(writer, http.StatusBadRequest, "invalid_grant", "authorization code does not match this client")
		return
	}
	access, accessHash, err := newSecret(server.Rand, AccessTokenPrefix)
	if err != nil {
		writeOAuthError(writer, http.StatusInternalServerError, "server_error", "could not issue tokens")
		return
	}
	refresh, refreshHash, err := newSecret(server.Rand, RefreshTokenPrefix)
	if err != nil {
		writeOAuthError(writer, http.StatusInternalServerError, "server_error", "could not issue tokens")
		return
	}
	grant := authn.OAuthGrant{
		ClientID: pending.ClientID, UserID: pending.UserID, Scope: pending.Scope,
		AccessHash: accessHash, AccessExpiresAt: now.Add(AccessTokenTTL), RefreshHash: refreshHash,
		CreatedAt: now, ExpiresAt: now.Add(GrantTTL),
	}
	grantID, err := server.Store.CreateOAuthGrant(request.Context(), grant)
	if err != nil {
		writeOAuthError(writer, http.StatusServiceUnavailable, "temporarily_unavailable", "could not issue tokens")
		return
	}
	if server.GitHubTokens != nil && server.Sealer != nil {
		if githubToken, ok := server.GitHubTokens.TakeForCode(codeHash); ok {
			if ciphertext, err := server.Sealer.Seal(server.Rand, grantID, githubToken); err == nil {
				_ = server.Store.UpdateOAuthGrantGitHubToken(request.Context(), grantID, ciphertext)
			}
		}
	}
	server.record(request.Context(), audit.Event{ActorType: "user", ActorID: strconv.FormatInt(pending.UserID, 10), TargetType: "oauth_grant", TargetID: strconv.FormatInt(grantID, 10), AuthenticationMethod: authn.ProviderOAuthToken, Operation: OperationGrantCreated, Outcome: "success"})
	writeTokens(writer, access, refresh, pending.Scope, grant.AccessExpiresAt.Sub(server.now()))
}

func (server *Server) refresh(writer http.ResponseWriter, request *http.Request) {
	form := request.PostForm
	refreshHash, ok := hashSecret(form.Get("refresh_token"), RefreshTokenPrefix)
	clientID := form.Get("client_id")
	if !ok || clientID == "" {
		writeOAuthError(writer, http.StatusBadRequest, "invalid_request", "refresh_token and client_id are required")
		return
	}
	now := server.now()
	// The client binding is checked before rotating so a wrong client cannot
	// burn another client's refresh token; the lookup is read-only.
	current, err := server.Store.OAuthGrantByRefresh(request.Context(), refreshHash, now)
	if err == nil && current.ClientID != clientID {
		writeOAuthError(writer, http.StatusBadRequest, "invalid_grant", "refresh token does not belong to this client")
		return
	}
	access, accessHash, err := newSecret(server.Rand, AccessTokenPrefix)
	if err != nil {
		writeOAuthError(writer, http.StatusInternalServerError, "server_error", "could not issue tokens")
		return
	}
	refresh, newRefreshHash, err := newSecret(server.Rand, RefreshTokenPrefix)
	if err != nil {
		writeOAuthError(writer, http.StatusInternalServerError, "server_error", "could not issue tokens")
		return
	}
	// Access tokens never outlive the grant.
	accessExpires := now.Add(AccessTokenTTL)
	if current.ID != 0 && accessExpires.After(current.ExpiresAt) {
		accessExpires = current.ExpiresAt
	}
	grant, err := server.Store.RotateOAuthGrant(request.Context(), refreshHash, authn.OAuthRotation{AccessHash: accessHash, AccessExpiresAt: accessExpires, RefreshHash: newRefreshHash, Now: now, Grace: refreshGrace})
	if errors.Is(err, authn.ErrOAuthReplay) {
		server.record(request.Context(), audit.Event{ActorType: "anonymous", TargetType: "oauth_client", TargetID: clientID, AuthenticationMethod: authn.ProviderOAuthToken, Operation: OperationGrantReplay, Outcome: "denied"})
		writeOAuthError(writer, http.StatusBadRequest, "invalid_grant", "refresh token was already used; the grant has been revoked")
		return
	}
	if err != nil {
		writeOAuthError(writer, http.StatusBadRequest, "invalid_grant", "refresh token is invalid or expired")
		return
	}
	server.syncGitHubAccess(request.Context(), grant)
	server.record(request.Context(), audit.Event{ActorType: "user", ActorID: strconv.FormatInt(grant.UserID, 10), TargetType: "oauth_grant", TargetID: strconv.FormatInt(grant.ID, 10), AuthenticationMethod: authn.ProviderOAuthToken, Operation: OperationGrantRefreshed, Outcome: "success"})
	writeTokens(writer, access, refresh, grant.Scope, grant.AccessExpiresAt.Sub(server.now()))
}

// syncGitHubAccess re-derives the user's GitHub grants with the stored GitHub
// token. GitHub rejecting the token drops it (a later browser login re-seeds
// it); any other failure changes nothing. Access can narrow here, never widen
// without GitHub confirming it, and the token response never waits on GitHub
// for longer than syncTimeout.
func (server *Server) syncGitHubAccess(ctx context.Context, grant authn.OAuthGrant) {
	if server.GitHub == nil || server.Sealer == nil || len(grant.GitHubTokenCiphertext) == 0 {
		return
	}
	githubToken, err := server.Sealer.Open(grant.ID, grant.GitHubTokenCiphertext)
	if err != nil {
		_ = server.Store.UpdateOAuthGrantGitHubToken(ctx, grant.ID, nil)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	repositories, err := server.GitHub.AccessibleRepositories(ctx, githubToken)
	if err != nil {
		var denied interface{ Unauthorized() bool }
		if errors.As(err, &denied) && denied.Unauthorized() {
			_ = server.Store.UpdateOAuthGrantGitHubToken(ctx, grant.ID, nil)
		}
		return
	}
	_ = server.Store.ReplaceGitHubGrants(ctx, grant.UserID, repositories)
}

// ---- revocation (RFC 7009) ------------------------------------------------------

func (server *Server) revoke(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, maxFormBytes)
	if err := request.ParseForm(); err != nil {
		writeOAuthError(writer, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	clientID := request.PostForm.Get("client_id")
	token := request.PostForm.Get("token")
	if clientID == "" {
		writeOAuthError(writer, http.StatusBadRequest, "invalid_request", "client_id is required")
		return
	}
	for _, prefix := range []string{AccessTokenPrefix, RefreshTokenPrefix} {
		if hash, ok := hashSecret(token, prefix); ok {
			if err := server.Store.RevokeOAuthGrantByToken(request.Context(), hash, clientID); err != nil {
				writeOAuthError(writer, http.StatusServiceUnavailable, "temporarily_unavailable", "could not revoke")
				return
			}
			server.record(request.Context(), audit.Event{ActorType: "anonymous", TargetType: "oauth_client", TargetID: clientID, Operation: OperationGrantRevoked, Outcome: "success"})
		}
	}
	writer.WriteHeader(http.StatusOK)
}

// ---- consent page ----------------------------------------------------------------

var consentTemplate = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Authorize {{.ClientName}} · GraphNest</title>
<style>
:root{color-scheme:dark;--bg:#0b0e14;--surface:#141821;--border:#262c38;--text:#e6e9ef;--muted:#9aa3b2;--accent:#7c8cff;--danger:#ff6b6b}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:var(--bg);color:var(--text);font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Inter,sans-serif}
main{width:min(460px,calc(100vw - 32px));padding:28px;background:var(--surface);border:1px solid var(--border);border-radius:14px}
h1{margin:0 0 6px;font-size:20px}p{margin:8px 0;color:var(--muted)}strong{color:var(--text)}code{font:13px ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--text);word-break:break-all}
ul{margin:10px 0 18px;padding-left:20px;color:var(--muted)}.actions{display:flex;gap:10px;justify-content:flex-end;margin-top:20px}
button{font:inherit;padding:9px 16px;border-radius:8px;border:1px solid var(--border);background:transparent;color:var(--text);cursor:pointer}
button.allow{background:var(--accent);border-color:var(--accent);color:#0b0e14;font-weight:600}
</style></head><body><main>
<p style="margin:0 0 14px;font-size:12px;letter-spacing:.08em;text-transform:uppercase">GraphNest</p>
<h1>Authorize <strong>{{.ClientName}}</strong>?</h1>
<p><strong>{{.ClientName}}</strong> wants to use GraphNest as <strong>{{.UserName}}</strong>.</p>
<p>It will be able to:</p>
<ul><li>search code, read files and navigate symbols in every repository you can access in GraphNest</li><li>keep that access for up to 30 days, or until you revoke it from your account</li></ul>
<p>It will not be able to change anything or create further credentials.</p>
<p>After you allow, your browser is sent to <code>{{.RedirectTarget}}</code>. Deny if you did not start this from a tool you trust.</p>
<form method="post" action="/oauth/authorize">
<input type="hidden" name="request_id" value="{{.RequestID}}">
<div class="actions"><button type="submit" name="decision" value="deny">Deny</button><button class="allow" type="submit" name="decision" value="allow">Allow</button></div>
</form></main></body></html>`))

func (server *Server) consent(writer http.ResponseWriter, request *http.Request, handle string, pending authn.OAuthAuthorizationRequest, client authn.OAuthClient) {
	principal, _ := server.sessionPrincipal(request)
	name := principal.Subject
	if server.UserName != nil {
		if rendered := server.UserName(request.Context(), principal); rendered != "" {
			name = rendered
		}
	}
	target, _ := url.Parse(pending.RedirectURI)
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	writer.Header().Set("X-Frame-Options", "DENY")
	writer.WriteHeader(http.StatusOK)
	_ = consentTemplate.Execute(writer, map[string]string{
		"ClientName": client.Name, "UserName": name, "RequestID": handle,
		"RedirectTarget": target.Scheme + "://" + target.Host,
	})
}

var errorTemplate = template.Must(template.New("error").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Authorization failed · GraphNest</title>
<style>body{margin:0;min-height:100vh;display:grid;place-items:center;background:#0b0e14;color:#e6e9ef;font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Inter,sans-serif}main{width:min(460px,calc(100vw - 32px));padding:28px;background:#141821;border:1px solid #262c38;border-radius:14px}h1{margin:0 0 8px;font-size:20px}p{color:#9aa3b2}</style>
</head><body><main><h1>Authorization failed</h1><p>{{.}}</p></main></body></html>`))

func (server *Server) authorizeErrorPage(writer http.ResponseWriter, message string) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(http.StatusBadRequest)
	_ = errorTemplate.Execute(writer, message)
}

// ---- helpers ------------------------------------------------------------------------

func (server *Server) allow(request *http.Request) bool {
	return server.Limiter == nil || server.Limiter.Allow(request.RemoteAddr)
}

func (server *Server) record(ctx context.Context, event audit.Event) {
	if server.Audit == nil {
		return
	}
	event.RequestID = audit.RequestID(ctx)
	_ = server.Audit.Record(ctx, event)
}

// registeredRedirect resolves the request's redirect_uri against every
// registered URI and returns the registration-derived target.
func registeredRedirect(registered []string, requested string) (string, bool) {
	for _, candidate := range registered {
		if resolved, ok := resolveRedirect(candidate, requested); ok {
			return resolved, true
		}
	}
	return "", false
}

func single(values url.Values, key string) (string, bool) {
	items := values[key]
	if len(items) != 1 || items[0] == "" || len(items[0]) > 2048 {
		return "", false
	}
	return items[0], true
}

func redirectError(writer http.ResponseWriter, request *http.Request, redirect, state, code, description string) {
	target, err := url.Parse(redirect)
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	values := target.Query()
	values.Set("error", code)
	values.Set("error_description", description)
	if state != "" {
		values.Set("state", state)
	}
	target.RawQuery = values.Encode()
	http.Redirect(writer, request, target.String(), http.StatusSeeOther)
}

func writeTokens(writer http.ResponseWriter, access, refresh, scope string, lifetime time.Duration) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": max(0, int(lifetime / time.Second)),
		"refresh_token": refresh, "scope": scope,
	})
}

func writeOAuthError(writer http.ResponseWriter, status int, code, description string) {
	writeJSON(writer, status, map[string]string{"error": code, "error_description": description})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func namedCookie(request *http.Request, name string) (string, int) {
	var value string
	count := 0
	for _, cookie := range request.Cookies() {
		if cookie.Name == name {
			value = cookie.Value
			count++
		}
	}
	return value, count
}

func public(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "public, max-age=300")
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		next.ServeHTTP(writer, request)
	})
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Pragma", "no-cache")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(writer, request)
	})
}

func getOnly(next http.Handler) http.Handler { return methodOnly(http.MethodGet, next) }

func postOnly(next http.Handler) http.Handler { return methodOnly(http.MethodPost, next) }

func methodOnly(method string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != method {
			writer.Header().Set("Allow", method)
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(writer, request)
	})
}
