# Authentication, Authorization, User Management, and SCIM Design

## Scope

GraphNest will replace production static bearer identities with one
deployment-wide OIDC provider, PostgreSQL-backed users and groups, fixed
role/repository grants, revocable API tokens, SCIM 2.0 provisioning, and a
disabled-by-default local break-glass administrator login.

Static fixture authentication remains available only for local and test mode.
The durable server uses the new identity system. Existing REST, MCP, search,
repository, SCIP, and administrative services continue to authorize through
`authn.Principal`; authorization remains server-side.

Delivery is split into four independently testable slices:

1. OIDC, durable users, live session authorization, and the browser login;
2. group/direct grants, user administration, and user API tokens;
3. SCIM users and groups; and
4. break-glass recovery and durable audit events.

## Explicit Non-Goals

This design does not add SAML, multiple OIDC providers, multiple SCIM
directories, public registration, ordinary local-password users, email
password reset, refresh-token storage, IdP token storage, custom roles, a
permission expression language, delegated administration, impersonation,
token exchange, SCIM Bulk, SCIM sorting, SCIM ETags, custom SCIM schemas, or
SCIM password provisioning.

These features should be added only for a concrete deployment requirement.

## Existing Code to Reuse

The implementation starts from current `origin/main`. It ports the tested OIDC
work on `feat/oidc-sso` instead of recreating it:

- strict OIDC discovery and Authorization Code with PKCE;
- state, browser binding, nonce, issuer, audience, signature, and expiry checks;
- opaque PostgreSQL login flows and browser sessions;
- cookie-or-bearer REST authentication with mixed-credential rejection;
- bearer-only MCP authentication;
- OIDC UI, OpenAPI, Compose, Helm, NetworkPolicy, and cross-replica tests.

The old branch's migration becomes migration 007 because current main already
uses migration 006. Its session schema is changed before integration: sessions
store a user ID, not a snapshot of administrator or repository grants.

The following current boundaries remain authoritative:

- `internal/authn.Principal` is the request identity passed to services;
- HTTP authentication writes that principal into the request context;
- `internal/authz` selects authorized repositories;
- `internal/search`, `internal/repository`, `internal/scipgraph`, and
  `internal/admin` enforce access below REST and MCP adapters;
- PostgreSQL migrations remain embedded ordered SQL executed with pgx;
- the web and admin consoles remain embedded HTML without a frontend toolchain.

No ORM, authentication framework, permissions framework, or new frontend
framework is introduced.

## Identity and Data Model

PostgreSQL stores:

- `users`: internal identity, SCIM `external_id`, case-insensitive unique
  `user_name`, profile fields, SCIM active state, administrator suspension,
  source (`scim` or `local`), creation/update/deletion timestamps;
- `user_identities`: unique OIDC `(issuer, subject)` bound to one user;
- `groups`: stable SCIM identity, `external_id`, case-insensitive unique display
  name, and lifecycle timestamps;
- `group_memberships`: unique user/group membership pairs;
- `group_roles` and `user_roles`: only the fixed `administrator` role;
- `group_repository_grants` and `user_repository_grants`: installation and
  repository grants using existing durable GitHub repository IDs;
- `login_flows`: short-lived, one-time OIDC state, browser, nonce, and PKCE
  records;
- `sessions`: hashes of opaque browser credentials, user ID, idle/absolute
  expiry, and revocation timestamps;
- `api_tokens`: hashes of opaque credentials, non-secret token ID/prefix, user
  ID, optional expiry, last-use time, and revocation time;
- `password_credentials`: Argon2id parameters, salt, hash, forced-rotation
  state, and update time for local break-glass administrators only; and
- `audit_events`: actor, target, authentication method, operation, coarse
  outcome, request ID, and timestamp.

Database constraints enforce identity, membership, and grant uniqueness.
Deletes use tombstones where audit history needs the target to remain
referentially valid; deleted users and groups are absent from normal and SCIM
reads.

Email and display name are mutable profile attributes and never identify an
account. OIDC identity is always `(issuer, subject)`. SCIM group mappings use
stable group IDs, never display names.

## OIDC Linking and Login

One OIDC issuer and client are configured per deployment. GraphNest uses
Authorization Code with PKCE S256 and validates:

- HTTPS discovery and endpoints;
- exact configured issuer;
- asymmetric ID-token signature and allowed algorithm;
- client audience and authorized party where present;
- nonce, expiry, issued-at bounds, one-time state, and browser binding.

GraphNest does not persist access, ID, or refresh tokens.

The deployment config selects one immutable OIDC claim used for provisioning
linkage. Its value must exactly match the SCIM user's `externalId`. The default
claim is `sub`; an explicit alternative supports providers whose SCIM object ID
is exposed under another immutable claim. Username or email fallback is not
allowed.

On first successful login, GraphNest atomically binds `(issuer, subject)` to the
active SCIM user with that external ID. Later logins use only the immutable
binding. Unknown, deleted, SCIM-inactive, or administrator-suspended users are
denied with a generic response.

The browser receives a random opaque session token. PostgreSQL stores only its
SHA-256 hash. The cookie is `__Host-` prefixed, `Secure`, `HttpOnly`,
`SameSite=Lax`, `Path=/`, and has no `Domain`. Sessions have bounded idle and
absolute lifetimes and rotate after authentication or privilege-sensitive
credential changes.

Every authenticated request resolves the user's current active state, role,
and grants before constructing `authn.Principal`. No session contains durable
authorization claims. SCIM changes therefore take effect on the next request.

Unsafe cookie-authenticated requests require an exact match with the configured
public `Origin`. GraphNest rejects requests that present both a session cookie
and an `Authorization` header. MCP accepts bearer credentials only and never
browser sessions.

## Authorization Model

There are two effective roles:

- `user`: may use search, repository, file, and navigation functions within
  current repository grants;
- `administrator`: may use existing and new administrative operations and may
  access every enabled, non-archived repository in every active installation,
  preserving current main behavior.

A user is an administrator when any current direct or group role grants
`administrator`. Ordinary repository access is the union of current group and
direct grants, intersected with enabled, non-archived repositories in active
installations. Direct grants exist for exceptions; group grants are the normal
management path.

API token restrictions may narrow the owner's effective repository set but
never enlarge it. Administrator status is always resolved from current user and
group grants, never copied into a token.

All existing service-layer authorization checks remain in place. The REST,
MCP, browser, and SCIM adapters cannot bypass repository or administrator
checks.

## User and Group Management

The existing admin console gains bounded APIs and views for:

- listing and inspecting users and groups;
- suspending or restoring a user independently of SCIM active state;
- viewing memberships and effective roles/repository access;
- assigning and removing exceptional direct roles and repository grants;
- mapping groups to the administrator role and repository grants;
- revoking a user's sessions and API tokens; and
- viewing bounded audit events.

SCIM-owned username, external ID, profile, group name, and membership fields are
read-only in the admin API. This avoids two writers fighting over directory
state. Administrators manage GraphNest access mappings and emergency suspension;
SCIM manages directory identity and membership.

Users can list, create, and revoke their own API tokens. Creation accepts an
optional bounded expiry and optional repository restriction. A token is shown
exactly once with a recognizable `gnp_` prefix; only its hash and non-secret ID
remain stored. User deactivation, deletion, or administrator suspension causes
all of that user's sessions and tokens to fail immediately.

Administrative changes use exact methods, strict JSON, bounded bodies and
responses, generic safe errors, and the existing request-principal boundary.

## SCIM 2.0

SCIM is served at `/scim/v2` and uses a dedicated high-entropy provisioning
bearer secret loaded from a secret file. It cannot authenticate to search,
MCP, or administrative APIs. Discovery endpoints also require the SCIM token.
The configured token can be rotated or revoked by replacing the secret and
restarting the server; its plaintext is never stored or logged.

Supported discovery endpoints:

- `GET /ServiceProviderConfig`;
- `GET /ResourceTypes` and `/ResourceTypes/{User|Group}`; and
- `GET /Schemas` and `/Schemas/{core-user|core-group}`.

Supported resource endpoints:

- `GET|POST /Users`;
- `GET|PUT|PATCH|DELETE /Users/{id}`;
- `GET|POST /Groups`; and
- `GET|PUT|PATCH|DELETE /Groups/{id}`.

Users support `externalId`, required `userName`, `active`, `displayName`,
formatted/given/family name, and email values. SCIM passwords are rejected.
Groups support `externalId`, required `displayName`, and user members.
Server-generated `id` and `meta` fields are read-only.

Collection reads return SCIM `ListResponse` and support bounded,
deterministically ordered `startIndex`/`count` pagination, `attributes`,
`excludedAttributes`, and only these reconciliation filters:

- Users: `id`, `userName`, or `externalId` with `eq`;
- Groups: `id`, `displayName`, or `externalId` with `eq`.

Attribute and operator names are case-insensitive. Unsupported expressions
return `400 invalidFilter`; they are not silently ignored.

PUT replaces writable attributes. PATCH supports sequential, atomic operations
for user active/profile fields and group membership, including pathless
add/replace objects and `members[value eq "USER_ID"]`. Repeated membership
adds/removes and identical writes are no-ops. Nonexistent members, read-only
mutations, invalid paths, excessive operations, and malformed values are
rejected with SCIM errors.

Deactivating or deleting a user immediately removes effective access. Deleting
a group removes its memberships and GraphNest grants but never deletes users.
Duplicate usernames, group display names, or non-empty external IDs return
`409 uniqueness`.

Every SCIM response uses `application/scim+json`. Errors use the RFC 7644 error
schema and safe details. Unsupported methods return `405` with `Allow`; wrong
media types return `415`; oversized bodies return `413`; unknown resources
return `404`; missing or invalid provisioning credentials return `401` with
`WWW-Authenticate: Bearer`.

SCIM Bulk, root search, `/.search`, `/Me`, sorting, ETags, password operations,
enterprise extensions, and custom resources are advertised as unsupported.

## Break-Glass Authentication

Local passwords are disabled unless an operator explicitly enables the
break-glass route. Only local users with the administrator role may have a
password credential. There is no registration, reset-by-email, or local
non-administrator path.

An operator command using the PostgreSQL connection creates or rotates a named
break-glass administrator from a password read without terminal echo. It never
prints or accepts the password through arguments or environment variables.
The command can force rotation and records an audit event.

The local login endpoint has generic failures, bounded input, per-account and
per-source rate limiting, constant-shape work for unknown users, and Argon2id
password verification. Successful login issues the same opaque browser session
as OIDC. The login form is absent unless break-glass authentication is enabled.

OIDC outage does not automatically enable local login. Operators must make that
choice explicitly in deployment configuration.

## Audit and Secret Handling

Durable audit events cover:

- successful and failed OIDC/local login and logout;
- session and API-token creation, use rejection, and revocation;
- user suspension/restoration and credential rotation;
- SCIM create, replace, patch, deactivate, and delete;
- group membership, role, and repository-grant changes; and
- denied administrative operations.

Events include actor or provisioning-token ID, target type/ID, operation,
authentication method, safe result, request ID, and timestamp. They never
contain passwords, cookies, bearer values, authorization headers, OIDC tokens,
raw claims, SCIM request bodies, or unnecessary profile fields.

OIDC client secrets, SCIM credentials, break-glass controls, GitHub credentials,
and database credentials remain file-backed deployment secrets. Logs expose
coarse stable error codes only.

## Configuration and Deployment

Durable mode requires:

- public HTTPS origin;
- OIDC issuer, client ID, client-secret file, immutable link-claim name, CA
  file when needed, and bounded login/session lifetimes;
- SCIM token file when SCIM is enabled; and
- explicit break-glass enablement when local login is required.

Static fixture mode keeps its current distinct development tokens and does not
enable OIDC, SCIM, durable users, or local passwords.

Compose and Helm mount secret files read-only. Helm values and schema expose
only required identity endpoints, claim name, timeouts, secret references, and
optional custom CA. NetworkPolicy permits required OIDC egress without opening
unrelated destinations. Multi-replica login and sessions rely only on
PostgreSQL, not process memory.

The OpenAPI document covers GraphNest authentication, user, group, grant, token,
and audit APIs. SCIM schemas are documented separately by their standard
discovery endpoints rather than duplicated into the GraphNest OpenAPI document.

## Failure Handling

Authentication failures are generic and do not reveal account existence.
Authorization failures preserve the current `403`/non-enumerating `404`
behavior. Identity-provider outages fail login but do not invalidate existing
unexpired sessions. PostgreSQL failure fails authentication closed.

SCIM writes are transactional. An invalid operation rolls back the entire
request. A SCIM outage preserves the last committed directory state; an
explicit deactivate or delete takes effect immediately after commit.

Inputs have explicit byte, filter, page, PATCH-operation, and timeout limits.
Database uniqueness races are translated into stable `409` responses. Internal
SQL, identity, or secret details never reach clients.

## Verification

Each implementation slice uses test-first changes and an atomic commit. Focused
tests cover:

- OIDC discovery, PKCE, state/browser/nonce replay, signature, issuer, audience,
  expiry, immutable SCIM linking, cookie flags, Origin enforcement, logout,
  mixed credentials, and two-replica sessions;
- live grant resolution after membership, role, suspension, and repository
  changes;
- user/group administration, effective access, API-token one-time display,
  expiry, restriction, revocation, and bearer-only MCP;
- complete SCIM User and Group lifecycles, filters, pagination, projections,
  PUT, atomic PATCH, membership idempotency, uniqueness races, media types,
  errors, and bounds;
- immediate session/token denial after SCIM deactivation or deletion;
- break-glass bootstrap, Argon2id verification, forced rotation, rate limiting,
  generic failures, and disabled-by-default routing;
- audit completeness and secret/PII exclusion;
- OpenAPI consistency, embedded UI contracts, Compose, Helm schema/rendering,
  secret mounts, and NetworkPolicy; and
- an end-to-end mock OIDC plus SCIM flow that provisions a user/group, grants a
  repository, logs in, searches through REST and MCP token paths, then
  deprovisions the user and proves both paths fail immediately.

Final gates are the repository's existing formatting, unit, race, integration,
end-to-end, static-analysis, vulnerability, build, OpenAPI, Compose, and Helm
checks.

## Completion Criteria

The work is complete when a SCIM-provisioned user can authenticate through
OIDC, receives only current group/direct repository access, can create a
strictly narrower revocable API token for CLI/MCP, and immediately loses all
access when suspended or deprovisioned. Administrators can manage mappings,
exceptions, revocations, and audit records. A separately enabled break-glass
administrator can recover access without weakening normal SSO-first operation,
and all identity secrets and failures remain non-disclosing.
