# Threat Model

## Protected assets

- source, repository metadata, and indexed revisions;
- bearer tokens and GitHub App installation credentials;
- authorization scopes and server-selected Zoekt repository IDs;
- service and index availability;
- OIDC login transactions, browser sessions, and the identity-to-scope mapping.

## Milestones 0-1 controls

- `/v1/search` and `/mcp` require exactly one bearer credential; malformed,
  missing, duplicate, or unknown credentials receive a generic 401 response;
- static tokens are compared in constant time and must be distinct at startup;
- the server intersects requested repository names with the authenticated
  principal's scope, then sends only the resulting Zoekt `RepoIDs`;
- Zoekt is internal to the application; Compose publishes it only on loopback;
- JSON bodies, result counts, context, timeout, backend response, and outbound
  response all have server-side bounds;
- errors are generic and structured; token values and Authorization headers are
  not logged;
- fixture indexing uses pinned binaries with argument arrays and never runs
  fixture repository code.

## Milestone 2 controls

- webhook HMAC is checked over bounded untouched bytes before JSON decoding;
- App and installation credentials stay in memory and are redacted from logs;
- Go and Git extend system trust with the same custom CA, require HTTPS, and
  reject redirects and unconfigured hosts;
- persisted Git remotes contain no credentials, and child processes receive an
  allowlisted environment through a fixed askpass helper;
- indexing never runs repository hooks, code, submodules, LFS smudge filters,
  build tools, or repository-supplied ctags configuration;
- numeric IDs determine database and disk identity; untrusted names and paths
  never determine filesystem locations;
- bearer authorization binds to explicit numeric GitHub repository-ID subsets,
  excludes disabled state before selecting RepoIDs, and never treats a mutable
  name as identity;
- PostgreSQL transactions deduplicate deliveries and coalesce pushes, leases
  prevent concurrent indexing, and indexed SHA is published only after exact
  Zoekt visibility;
- search suppresses Zoekt revisions that do not match committed repository
  metadata, and file reads authorize before fetching the committed indexed SHA;
- HTTP bodies, decoded files, child output, command duration, and free-space
  admission are bounded.

## Web UI controls

- the bearer token is held only in session storage or memory and is cleared on
  authentication failure;
- a strict hash-based CSP permits only same-origin connections and the exact
  embedded style and script blocks;
- API-controlled text is rendered through DOM text nodes, never HTML sinks;
- outbound repository links require HTTPS, encode SHA and path components, and
  use opener isolation; and
- the client selects repository names for usability, while the server still
  intersects them with the authenticated principal's numeric authorization
  scope.

## Optional OIDC session controls

- OIDC uses Authorization Code with S256 PKCE, a one-time stored state and
  browser binding, and an ID-token nonce; callback inputs are consumed before
  exchange to prevent login CSRF, authorization-code replay, and nonce replay;
- the callback accepts the configured HTTPS issuer and client audience only;
  multi-audience tokens require the matching authorized party, preventing
  issuer or audience confusion;
- sessions use fresh opaque random tokens, a `__Host-` Secure HttpOnly strict
  cookie, and stored token hashes. A read-only database dump cannot directly
  yield a live bearer token, but a database writer can insert the hash of a
  chosen session token with arbitrary otherwise-valid installation and
  repository scope, then authenticate as that scope;
- a stolen live session cookie is replayable until session expiry or server-side
  revocation. TTL and revocation bound that residual impact; Secure, HttpOnly,
  SameSite, and hash-only storage do not prevent either case. Raw IdP issuers,
  subjects, and tokens are not persisted;
- bearer and session credentials are mutually exclusive per REST request;
  session-authenticated unsafe methods require the exact configured public
  `Origin`, preventing mixed-credential confusion and cross-site request use;
- login and callback redirect only to fixed provider and `/` destinations, so
  request parameters cannot create an open redirect;
- an OIDC identity is mapped once to the current non-admin user repository
  scope. Exact, case-sensitive allowed-group checks happen at login; membership
  changes can remain effective until the bounded session TTL expires or the
  session is revoked;
- login-flow consumption and session creation are database transactions, so
  multiple server replicas cannot complete the same flow twice; and
- provider discovery, token exchange, and JWKS retrieval use configured HTTPS
  trust. An IdP outage or failed JWKS rotation denies new logins but does not
  invalidate already-issued GrepNest sessions before their expiry.

## Known limits

This remains a local development slice, not a production security boundary.
Git pack expansion and Zoekt shards require container and volume quotas.
Container isolation, network policy, secret delivery, backup/restore, and
production ingress remain Milestone 3 work. Do not claim production readiness
from local or Compose success. The Helm Ingress is structural support, not
proof of a production ingress deployment.
