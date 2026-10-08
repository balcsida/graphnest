# CLI OAuth Login Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `graphnest login` signs in through the browser with authorization code + PKCE over a loopback redirect (RFC 8252), and the stored login drives every server command, including publication gated by a new `graph:write` scope.

**Architecture:** The server already is the OAuth authorization server (ADR-0016). Tasks 1–2 let its access tokens reach the CLI's REST routes and gate publication on `graph:write`. Tasks 3–4 add the OAuth client, the stored-login file and the `login`/`logout` commands. Task 5 proves the whole path against PostgreSQL and the real authorization server.

**Tech Stack:** Go 1.27.1 standard library only (`net`, `net/http`, `crypto/rand`, `crypto/sha256`, `crypto/subtle`, `encoding/json`, `os`), existing `internal/httpclient`, PostgreSQL integration tests.

**Spec:** `docs/superpowers/specs/2026-10-08-cli-oauth-login-design.md`

## Global Constraints

- No new module dependencies; `go.mod`/`go.sum` unchanged. `gofmt` clean, `go vet ./...` clean.
- Commits: Conventional Commits, imperative subject under 72 characters, body ends with `Co-Authored-By: <your model name> <noreply@anthropic.com>`. Never commit to `main`.
- A token, refresh token, code or verifier never appears in an error, stdout, stderr, log line or command argument. Tests assert this where they print.
- Scope value: `graph:write`. Client name: `graphnest CLI`. Callback path: `/callback`. Stored login: `<os.UserConfigDir()>/graphnest/credentials/<hex sha256(origin)>.json`, directory 0700, file 0600. Origin: server URL scheme + `://` + lower-cased host (port kept, path dropped).
- `login --timeout` default `5m`, bounding only the wait for the browser. Refresh when less than the request timeout plus `30s` remain.
- Exact copy (verbatim):
  - consent item: `publish code graphs to repositories where you hold a publication grant`
  - consent sentence with the scope: `It will not be able to change anything else or create further credentials.` (without the scope the existing sentence stays)
  - stderr before the browser: `Sign in to <origin> in your browser. If it does not open, visit:` then the URL on its own line
  - stderr on success: `Signed in to <origin>.`; logout: `Signed out of <origin>.`; nothing stored: `Not signed in to <origin>.`
  - precedence note: `note: GRAPHNEST_TOKEN or GRAPHNEST_TOKEN_FILE is set and takes precedence over this login`
  - callback pages (text/plain): success `Signed in to GraphNest. You can close this tab and return to the terminal.`; wrong state (400) `This response does not belong to the running graphnest login.`; error (400) `Sign-in failed. Return to the terminal for details.`
  - errors: `server does not offer OAuth sign-in (GRAPHNEST_MCP_OAUTH is off); set GRAPHNEST_TOKEN instead`; `authorization server issuer "<got>" does not match "<origin>"`; `authorization server does not support PKCE S256`; `authorization failed: <error>: <error_description>`; `timed out waiting for the browser sign-in`; `GRAPHNEST_TOKEN or GRAPHNEST_TOKEN_FILE is required, or run graphnest login`; `the stored login for <origin> has expired or was revoked; run graphnest login`; logout revocation failure `signed out locally, but the server did not revoke the grant: <err>; disconnect "graphnest CLI" under Account → Connected MCP clients`
  - 401 hint appended to server errors: `; check the token or run graphnest login`
- Integration tests: `GRAPHNEST_TEST_POSTGRES_DSN='postgres://graphnest:graphnest@192.168.107.2:5432/graphnest?sslmode=disable'` (the compose PostgreSQL is already running; never start or stop containers).

## Review Focus

1. A token without `graph:write` (every MCP client's) never publishes, even with a publication grant — Task 1 unit test, Task 5 integration test.
2. OAuth tokens still fail on account, search and every other non-CLI route, so they cannot mint credentials — Task 2 handler test.
3. Two `graphnest` processes refreshing the same login at once do not lose it — Task 3 `TestClientAdoptsTokensRotatedByAnotherProcess`.
4. A request to the loopback callback with the wrong `state` neither ends nor hijacks the login — Task 4 `TestLoginIgnoresForgedCallback`.
5. A login against a server with `GRAPHNEST_MCP_OAUTH` off, or behind an unexpected issuer, fails before opening a browser — Task 3 discovery test, Task 4 issuer test.

---

### Task 1: Gate OAuth publication on the `graph:write` scope

**Files:**
- Modify: `internal/authn/static.go` (`Principal`), `internal/authn/oauth.go`
- Modify: `internal/postgres/oauth.go` (`OAuthPrincipal`)
- Modify: `internal/graphingest/service.go` (`mayPublish`)
- Modify: `internal/oauthas/server.go` (`consentTemplate`, `consent`)
- Test: `internal/authn/oauth_token_test.go`, `internal/graphingest/publish_test.go`, `internal/postgres/oauth_principal_test.go`, `internal/oauthas/server_test.go`

**Interfaces:**
- Produces: `authn.ScopeGraphWrite = "graph:write"`; field `authn.Principal.Scope string` (space-delimited OAuth grant scope, empty for every other credential); `func (p authn.Principal) HasScope(scope string) bool` (exact token match over `strings.Fields`, so `graph:writer` does not match).

- [ ] **Step 1: Write the failing tests**
  - `internal/authn/oauth_token_test.go` `TestPrincipalHasScope`: table over `Principal{Scope: s}.HasScope("graph:write")` — `"graph:write"`→true, `"openid graph:write"`→true, `"graph:writer"`→false, `""`→false.
  - `internal/graphingest/publish_test.go`: helper `oauthPrincipal(subject string, repositoryID int64, scope string) authn.Principal` = `readerPrincipal` with `Method: authn.ProviderOAuthToken, Scope: scope`. Extend `TestStatusReportsPublicationPreflight` with `{"OAuth grantee without graph:write", oauthPrincipal("42", 101, ""), map[string]bool{"42": true}, false}` and `{"OAuth grantee with graph:write", oauthPrincipal("42", 101, "graph:write"), map[string]bool{"42": true}, true}`. New `TestOAuthPublicationNeedsGraphWriteScope`: with `granted{"42": true}`, `Publish` as `oauthPrincipal("42", 101, "")` returns `ErrForbidden` with `store.grantCalls == 0`; as `oauthPrincipal("42", 101, "graph:write")` it succeeds and `store.publication.Publisher == "oauth_token:42"`.
  - `internal/postgres/oauth_principal_test.go` (build tag `integration`) `TestOAuthPrincipalCarriesGrantScope`: create one grant with `Scope: "graph:write"` and one with empty scope (pattern of `seedReplayGrant`: `migratedStore`, `seedOAuthClient`, `insertIdentityUser`, distinct access/refresh hashes); `OAuthPrincipal` returns `Scope == "graph:write"` and `Scope == ""` respectively.
  - `internal/oauthas/server_test.go` `TestConsentListsGraphWriteOnlyWhenRequested`: `h := newHarness(t)`; set `h.server.GitHub, h.server.GitHubTokens = nil, nil` so the session goes straight to consent; register a client with `h.registerClient`; GET `/oauth/authorize` (the `authorizeURL` parameters plus `scope`) with the session cookie. With `scope=graph:write` the 200 page contains the consent item and `change anything else or create further credentials`; with no scope it contains neither and still contains `It will not be able to change anything or create further credentials.`

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/authn ./internal/graphingest ./internal/oauthas -run 'HasScope|GraphWrite|PublicationPreflight' && GRAPHNEST_TEST_POSTGRES_DSN='postgres://graphnest:graphnest@192.168.107.2:5432/graphnest?sslmode=disable' go test -count=1 -tags=integration ./internal/postgres -run TestOAuthPrincipalCarriesGrantScope`
Expected: compile failure (`Scope`, `HasScope` undefined).

- [ ] **Step 3: Implement**
  - `Principal.Scope` with a comment; `ScopeGraphWrite` and `HasScope` in `internal/authn/oauth.go`.
  - `OAuthPrincipal`: `returning oauth_grants.user_id, oauth_grants.scope`, set `principal.Scope` next to `principal.Method`.
  - `mayPublish`: return `false, nil` when `principal.Method == authn.ProviderOAuthToken && !principal.HasScope(authn.ScopeGraphWrite)`, before the administrator and grant checks.
  - Consent: template data becomes `map[string]any` with `GraphWrite` = the pending request's scope contains `graph:write` (use `authn.Principal{Scope: pending.Scope}.HasScope(...)` or the same `strings.Fields` match); insert the item between the existing two list items and ` else` into the sentence under `{{if .GraphWrite}}`.

- [ ] **Step 4: Run the tests to see them pass**

Run: the Step 2 command, then `go test ./internal/authn ./internal/graphingest ./internal/oauthas ./internal/postgres`
Expected: PASS.

- [ ] **Step 5: Commit**

`feat(oauth): gate OAuth graph publication on graph:write`

### Task 2: Accept OAuth tokens on the CLI's REST routes

**Files:**
- Modify: `cmd/graphnest-server/main.go` (`newAPIHandlerWithMCP`)
- Modify: `cmd/graphnest-server/main_test.go` (`TestMCPOAuthBearerOnlyAuthenticatesMCP`)
- Modify: `test/e2e/mcp_oauth_test.go` (`newMCPOAuthServer`, step 6 assertions)
- Create: `docs/adr/0019-cli-oauth-login.md`; Modify: `docs/adr/README.md`, `docs/operations.md` (MCP OAuth section), `docs/openapi.yaml`

**Interfaces:**
- Consumes: Task 1's `graph:write` gating.
- Produces: with `mcpOAuth != nil`, `GET /v1/repositories`, `GET /v1/repositories/{id}` (via `RegisterRepositoryInventory`) and everything `RegisterGraphIngestion` mounts use the same `authn.BearerRouter{APITokens: authenticator.Bearer, OAuth: authn.OAuthTokenAuthenticator{Store: mcpOAuth.Store}}` as `/mcp`; the inventory gets a copy of `authenticator` with only `Bearer` replaced. File reads, search, account, admin, SCIP, graph queries and supply chain keep `authenticator`. `runtime.requestAuth.Bearer` stays the API-token authenticator (existing test asserts it).

- [ ] **Step 1: Write the failing test.** Rename `TestMCPOAuthBearerOnlyAuthenticatesMCP` to `TestOAuthBearerReachesOnlyMCPAndCLIRoutes`; build the handler with `&repository.Service{Store: repositoryStoreStub{}}` and `&graphingest.Service{}` so the CLI routes exist. For each of `GET /v1/repositories/42`, `GET /v1/graph/repositories/x/status` (400 before any store access) and `POST /v1/graph/uploads` without a content type (415 before any store access), the OAuth token gets the same status as the PAT and not 401. `GET /v1/auth/session` and `POST /v1/search` stay 401 for the OAuth token; `/mcp` keeps its current expectations.
- [ ] **Step 2:** `go test ./cmd/graphnest-server -run TestOAuthBearerReachesOnlyMCPAndCLIRoutes` fails with 401 on the CLI routes.
- [ ] **Step 3: Implement** the wiring in `newAPIHandlerWithMCP` (rename `mcpBearer` to `oauthBearer`, one comment citing ADR-0019).
- [ ] **Step 4: Mirror it in the e2e harness.** In `newMCPOAuthServer` register `RegisterRepositoryInventory` with a copy of `requestAuth` whose `Bearer` is the harness's `bearer`, and `RegisterFileReads` with `requestAuth`, instead of `RegisterRepositories`. In step 6 the OAuth token's `GET /v1/repositories/101` now expects 200; `/v1/account/api-tokens` stays 401.
- [ ] **Step 5: Docs.**
  - `docs/adr/0019-cli-oauth-login.md` in the ADR-0016 format (Status Accepted, Date 2026-10-08): Decision (`graphnest login` is an RFC 8252 native client: system browser, loopback `127.0.0.1` redirect, S256 PKCE, per-login dynamic registration; OAuth access tokens also authenticate the four CLI routes; publication by an OAuth principal needs `graph:write` plus the repository publication grant; the consent page lists it; `scopes_supported` stays empty), Rationale (no administrator or hand-copied credential on a developer laptop; a scope needs no migration and the read routes expose nothing MCP cannot already read; RFC 8707 audiences rejected for the migration and dual checks), Consequences (amends ADR-0016's "authenticates only `/mcp`"; existing MCP tokens can read those routes but never publish; CLI credentials live in a 0600 file per server; headless machines keep API tokens).
  - `docs/adr/README.md`: add the 0019 row; 0016's status becomes `Accepted; amended by 0019`.
  - `docs/operations.md` MCP OAuth section: replace "They authenticate only `/mcp` …" and the "`scope` is accepted … not yet enforced" sentence with the new routes and `graph:write` semantics (other scope values are still stored, echoed and ignored).
  - `docs/openapi.yaml`: `bearerAuth` gets a description naming the routes that also accept OAuth access tokens (`gno_…`) and that publication needs `graph:write`; the `/oauth/authorize` `scope` parameter gets a description of `graph:write`.
- [ ] **Step 6: Verify.** `go test ./cmd/graphnest-server ./internal/...`; `ruby scripts/check_openapi.rb docs/openapi.yaml` (or `make openapi-check`); `GRAPHNEST_TEST_POSTGRES_DSN='postgres://graphnest:graphnest@192.168.107.2:5432/graphnest?sslmode=disable' GRAPHNEST_REQUIRE_POSTGRES=1 go test -count=1 -tags=e2e ./test/e2e -run TestMCPOAuthAuthorizationCodeFlow`. All pass.
- [ ] **Step 7: Commit** `feat(oauth): accept OAuth tokens on the CLI's REST routes` (code and tests) and `docs(oauth): record CLI OAuth access in ADR-0019` (docs).

### Task 3: OAuth client, stored logins and refreshing REST client

**Files:**
- Create: `internal/client/oauth.go`, `internal/client/logins.go`
- Modify: `internal/client/client.go`
- Test: `internal/client/oauth_test.go`, `internal/client/logins_test.go`, `internal/client/client_test.go`
- Modify callers for the new `FromEnv` signature only: `internal/cli/status.go`, `internal/cli/publish.go`, `internal/cli/doctor.go` (pass `client.Logins{}` for now; Task 4 wires the real store)

**Interfaces:**
- Produces (package `client`):
  ```go
  // Login is a stored OAuth sign-in for one GraphNest server.
  type Login struct {
  	Server             string    `json:"server"`
  	ClientID           string    `json:"client_id"`
  	AccessToken        string    `json:"access_token"`
  	RefreshToken       string    `json:"refresh_token"`
  	ExpiresAt          time.Time `json:"expires_at"`
  	TokenEndpoint      string    `json:"token_endpoint"`
  	RevocationEndpoint string    `json:"revocation_endpoint,omitempty"`
  }
  type Logins struct{ Dir string } // one <hex sha256(origin)>.json per server; zero value stores nothing
  func (l Logins) Load(origin string) (Login, bool, error) // missing file or Dir "" → false, nil; file for another server → false, nil
  func (l Logins) Save(login Login) error                  // MkdirAll 0700, temp file 0600 in Dir, Sync, Rename; error when Dir is ""
  func (l Logins) Delete(origin string) error               // missing file is not an error
  func Origin(serverURL string) (string, error)             // http/https with host only
  func NewSecret() string                                    // 32 crypto/rand bytes, base64.RawURLEncoding (43 chars)
  type OAuth struct{ /* unexported: http client, issuer and endpoints */ }
  func DiscoverOAuth(ctx context.Context, config Config) (*OAuth, error)
  func (o *OAuth) Register(ctx context.Context, redirectURI string) (clientID string, err error)
  func (o *OAuth) AuthorizationURL(clientID, redirectURI, state, verifier string) string
  func (o *OAuth) Exchange(ctx context.Context, clientID, redirectURI, code, verifier string) (Login, error)
  func RevokeLogin(ctx context.Context, config Config, login Login) error
  func ServerFromEnv(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) // ServerURL and CAPEM only
  func FromEnv(getenv func(string) string, readFile func(string) ([]byte, error), logins Logins) (Config, error)
  ```
  `Config` gains `Login *Login`, `Logins Logins` and `Source string` (`"GRAPHNEST_TOKEN"`, `"GRAPHNEST_TOKEN_FILE"` or `"stored login"`). `New` accepts `Token` or `Login`; with a `Login` every request takes its bearer from a mutex-guarded source that refreshes first when `time.Until(ExpiresAt) < Timeout + 30s`.

- [ ] **Step 1: Write the failing tests** (fake authorization servers are `httptest` handlers inside each test; every error assertion also checks that no token value appears in the message):
  - `TestOriginNormalizes`: `https://Graph.Example:8443/x?y` → `https://graph.example:8443`; `ftp://h` and `http://` fail.
  - `TestNewSecret`: 43 characters, decodes to 32 bytes, two calls differ.
  - `TestDiscoverOAuth`: accepts metadata whose `issuer` equals the origin and lists `S256`; issuer mismatch fails naming both values; missing `S256` fails; 404 fails with the GRAPHNEST_MCP_OAUTH message.
  - `TestRegisterSendsPublicClient`: JSON body has `client_name` `graphnest CLI`, exactly the given `redirect_uris`, grant types `authorization_code`+`refresh_token`, response type `code`, `token_endpoint_auth_method` `none`; returns the `client_id` from a 201.
  - `TestAuthorizationURL`: query has `response_type=code`, `client_id`, `redirect_uri`, `state`, `scope=graph:write`, `code_challenge_method=S256`, `code_challenge` = base64url(sha256(verifier)), and no `resource`.
  - `TestExchange`: posts `grant_type=authorization_code`, `code`, `redirect_uri`, `client_id`, `code_verifier` as a form; returns a `Login` with `Server` = origin, both tokens, `ExpiresAt` within a second of now+`expires_in`, token and revocation endpoints. A response without `refresh_token`, with `token_type` other than Bearer (any case), or with `error=invalid_grant` fails.
  - `TestLoginsRoundTrip` (`logins_test.go`): `Save` then `Load` round-trips; directory mode 0700 and file mode 0600 (skip the mode checks on Windows); no other files remain in `Dir`; `Load` of another origin and of a file whose `server` differs return not found; `Delete` removes it and deleting again is nil; `Logins{}` loads nothing and refuses to save.
  - `TestFromEnv` keeps its cases with `Logins{}` (the no-token error now also contains `graphnest login`); new `TestFromEnvPrecedence`: a stored login is used only when both token variables are unset (`Source` `"stored login"`, `Login` set), `GRAPHNEST_TOKEN` and `GRAPHNEST_TOKEN_FILE` win over it with matching `Source`.
  - `TestClientRefreshesStoredLogin`: a login expiring in 1 minute (default 2-minute timeout) refreshes before the first request (`grant_type=refresh_token`, `refresh_token`, `client_id`), the request carries the new access token, and the store now holds the new refresh token; a login expiring in 1 hour sends its access token without refreshing.
  - `TestClientAdoptsTokensRotatedByAnotherProcess`: the store already holds a rotated login (fresh access token, new refresh token) while the client holds the stale one; the server answers the stale refresh with `invalid_grant`; the request uses the stored access token and no second refresh is sent.
  - `TestClientReportsExpiredLogin`: `invalid_grant` with an unchanged store fails with the expired-login message.
  - `TestRevokeLogin`: posts `token`=refresh token, `token_type_hint=refresh_token`, `client_id` to the revocation endpoint; a non-200 answer fails; an empty `RevocationEndpoint` fails.
- [ ] **Step 2:** `go test ./internal/client` fails to compile.
- [ ] **Step 3: Implement.** `oauth.go` holds the protocol (one shared token-endpoint helper decoding `access_token`, `token_type`, `expires_in`, `refresh_token`, `error`, `error_description`; an unexported error type so the refresh path can recognise `invalid_grant`); `logins.go` the file store; `client.go` the config and token source. Every HTTP client comes from `httpclient.New(config.CAPEM)` with `config.Timeout` (default 2 minutes), so CA handling, the response-size bound and the cross-origin redirect refusal apply to OAuth calls too. Refresh, in order: refresh with the held login; on `invalid_grant` load the stored login once and, if its refresh token differs, use its access token when fresh enough or refresh with it; on `invalid_grant` again (or no newer login) return the expired-login error; on success `Save` before using the new access token and return `Save`'s error if it fails. The scope constant is a local `const loginScope = "graph:write"` with a comment pointing at `authn.ScopeGraphWrite` (the client must not import server packages).
- [ ] **Step 4:** `go test ./internal/client ./internal/cli` passes; `go vet ./...` clean.
- [ ] **Step 5: Commit** `feat(client): add OAuth sign-in, stored logins and token refresh`.

### Task 4: `graphnest login` and `graphnest logout`

**Files:**
- Create: `internal/cli/login.go`, `internal/cli/login_test.go`
- Modify: `internal/cli/cli.go` (`Environment`, `OSEnvironment`, `dispatch`, `rootUsage`), `internal/cli/status.go` (`describeServerError`), `internal/cli/publish.go`, `internal/cli/doctor.go`, `internal/cli/doctor_test.go`
- Docs: `README.md` (command table row and the CLI paragraph), `docs/operations.md` (`graph status` credentials sentence, a new "Signing in" subsection before "Checking an import environment"), `docs/threat-model.md`, `CHANGELOG.md` (`## [Unreleased]`: Added `graphnest login`/`logout`; Changed OAuth tokens on the CLI routes and `graph:write`; Security notes)

**Interfaces:**
- Consumes: Task 3's `client` API.
- Produces: `Environment.ConfigDir func() (string, error)` (`os.UserConfigDir` in `OSEnvironment`) and `Environment.OpenBrowser func(url string) error` (starts `open URL` on darwin, `rundll32 url.dll,FileProtocolHandler URL` on windows, `xdg-open URL` elsewhere, reaping the process in a goroutine). `nil` `ConfigDir` means no stored logins for server commands and an error for `login`/`logout`; `nil` `OpenBrowser` only prints the URL. Server commands build `client.Logins{Dir: filepath.Join(configDir, "graphnest", "credentials")}`.

- [ ] **Step 1: Write the failing tests** in `login_test.go` with a fake authorization server (metadata, `/oauth/register`, an auto-approving `/oauth/authorize` that validates the request and 302s to `redirect_uri` with `code` and `state` or with `error`/`error_description`, `/oauth/token` checking the S256 verifier, redirect URI and client, `/oauth/revoke`, plus `/v1/repositories/9` and `/v1/graph/repositories/9/status` recording the bearer) and a test `OpenBrowser` that GETs the URL in a goroutine and follows the redirect to the loopback callback:
  - `TestLoginStoresTokensAndCommandsUseThem`: exit 0, stdout empty, stderr has the sign-in line, the URL and `Signed in to <origin>.`; the authorize request had `redirect_uri` `http://127.0.0.1:<port>/callback`, `scope=graph:write`, S256 and no `resource`; the login file exists with mode 0600; then `graph status --repository-id 9` without token variables succeeds with the issued access token as bearer; no output contains a token.
  - `TestLoginIgnoresForgedCallback`: the browser first GETs the callback with a wrong `state` (400, body is the wrong-state copy), then completes normally; login succeeds.
  - `TestLoginReportsDeniedAuthorization`: `error=access_denied` → exit 1, stderr contains `authorization failed: access_denied`, no login file.
  - `TestLoginTimesOut`: a browser that does nothing and `--timeout 200ms` → exit 1, stderr contains `timed out waiting for the browser sign-in`, and the registered redirect port refuses connections afterwards.
  - `TestLoginRejectsIssuerMismatch`: wrong `issuer` → exit 1 and the browser is never opened.
  - `TestLoginRevokesPreviousGrant`: with a stored login (`gnr_old`, `gnc_old`), a new login revokes `token=gnr_old` with `client_id=gnc_old` and stores the new tokens.
  - `TestLoginNotesTokenPrecedence`: with `GRAPHNEST_TOKEN` set, stderr has the precedence note and login still succeeds.
  - `TestLogout`: revokes and deletes, printing `Signed out of <origin>.`; a second run prints `Not signed in to <origin>.` and exits 0; a failing revocation still deletes the file, exits 1 and names `Connected MCP clients`.
  - `TestLoginUsage`: `login extra` and `login --timeout 0s` exit 2; `logout extra` exits 2.
  - `doctor_test.go` `TestDoctorServer`: the server detail contains `credentials: GRAPHNEST_TOKEN` with the token variable and `credentials: stored login` with a stored login.
  - `cli_test.go`: a 401 from `graph status` prints `check the token or run graphnest login`.
- [ ] **Step 2:** `go test ./internal/cli -run 'Login|Logout|Doctor|Status'` fails.
- [ ] **Step 3: Implement** `runLogin` and `runLogout` in `login.go`, following the spec's numbered login steps: listen on `127.0.0.1:0`, falling back to `[::1]:0`; redirect URI `"http://" + listener.Addr().String() + "/callback"`; register; `NewSecret()` for verifier and state; print, then `OpenBrowser`; serve only `GET /callback` from an `http.Server` (with `ReadHeaderTimeout`) on that listener, comparing `state` with `crypto/subtle.ConstantTimeCompare`, answering with `http.Error`/plain text, ignoring a wrong state, delivering the first result through a 1-buffered channel without blocking; the `--timeout` context bounds only this wait; shut the server down (bounded graceful shutdown) and close the listener before exchanging; exchange; load the previous login, `Save` the new one (on failure revoke the new grant best-effort and return the error), revoke the previous grant best-effort with a `warning:` line on failure. `describeServerError` appends the 401 hint for `Status == 401`; doctor prefixes the server detail with `credentials: <Source>; `.
- [ ] **Step 4: Docs** as listed under Files, in the repository's existing voice: the README row for `graphnest` mentions `graphnest login`; operations explain sign-in, where the login is stored, precedence, refresh, logout, the `graph:write` consent line, and that machines without a browser keep API tokens; the threat model adds the 0600 login file (readable by the same OS user), the loopback callback (IP literal, state, PKCE, single-use port) and `graph:write`; the CHANGELOG entry follows the existing release-note style.
- [ ] **Step 5:** `go test ./...` and `go vet ./...` pass.
- [ ] **Step 6: Commit** `feat(cli): sign in with graphnest login over a loopback redirect` (code and tests) and `docs(cli): document graphnest login and logout` (docs).

### Task 5: Prove the login against PostgreSQL and the real authorization server

**Files:**
- Create: `test/integration/cli_login_test.go` (build tag `integration`)

**Interfaces:**
- Consumes: everything above; the integration package's `newPostgresHarness`, `seedRepository`, `setGraphCommit`, `cliFixtureRepository`, `cliImportArgs`, `cliPublishSHA`, `cliPublishGitHub`, `cliPublishRepo`, `decodeOutcome`.

- [ ] **Step 1: Write `TestCLILoginPublishesWithGraphWriteScope`.** Fixture: harness; repository 101 at `cliPublishSHA` with a search node (as `newCLIPublishFixture` does); a user (`insert into users … source 'scim'`) with `user_repository_grants` for 101 and a publication grant for its numeric subject; a browser session from `authn.SessionManager{Store: h.store, IdleTTL: time.Hour, TTL: 2 * time.Hour}.CreateForUser(ctx, userID, authn.ProviderOIDC, false)`. Server: `httptest.NewUnstartedServer` + `StartTLS`, then a mux with `oauthas.Server{Origin: server.URL, Store: h.store, Sessions: sessions, Limiter: h.store, LoginPath: "/login"}` registered, `RegisterRepositoryInventory` and `RegisterGraphIngestion` wired with `authn.BearerRouter{APITokens: authn.TokenManager{Store: h.store}, OAuth: authn.OAuthTokenAuthenticator{Store: h.store}}` exactly as production wires them, and publication grants as in `newCLIPublishFixture`. The CLI trusts the server through `GRAPHNEST_CA_FILE` (PEM of `server.Certificate()`), stores logins in `t.TempDir()` through `ConfigDir`, and its `OpenBrowser` runs, in a goroutine reporting errors on a channel, a cookie-jar client from `server.Client()` that holds the session cookie: GET the authorization URL (200 consent page containing the `graph:write` item), POST `/oauth/authorize` with the page's `request_id`, `decision=allow` and `Origin: server.URL` (303), then GET the loopback `Location` (200). Assert, in order:
  1. `login` exits 0; the login file is mode 0600; no CLI output contains its access or refresh token.
  2. `graph status --repository-id 101` exits 0 with `graph.publication.permitted == true`.
  3. `graph import codegraph` (`cliImportArgs(cliPublishSHA)`) exits 0 with `published: true`, and the newest `graph_uploads.publisher` is `oauth_token:<user id>`.
  4. Rewriting the file with `expires_at` in the past, `graph status` still exits 0 and the file now holds a different refresh token.
  5. A token from a second, hand-driven authorization without `scope` (register, consent, exchange) reads `GET /v1/repositories/101` (200), sees `publication.permitted == false`, and gets 403 from `POST /v1/graph/uploads?repository_id=101&commit=<sha>&expected_generation=0`.
  6. `logout` exits 0, the file is gone, and the last stored access token now gets 401 from `GET /v1/repositories/101`.
- [ ] **Step 2: Run it.** `GRAPHNEST_TEST_POSTGRES_DSN='postgres://graphnest:graphnest@192.168.107.2:5432/graphnest?sslmode=disable' go test -count=1 -tags=integration ./test/integration -run TestCLILoginPublishesWithGraphWriteScope -v` passes, then the whole `./test/integration` package passes.
- [ ] **Step 3: Commit** `test(integration): sign in, publish and sign out with graphnest login`.
