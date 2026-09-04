# ADR-0016: MCP OAuth Authorization Server

- Status: Accepted
- Date: 2026-09-04

## Decision

GraphNest is its own OAuth 2.1 authorization server for MCP clients, behind
`GRAPHNEST_MCP_OAUTH`. Clients discover it through RFC 9728 protected-resource
metadata on `/mcp`, register dynamically as public clients (RFC 7591), and run
the authorization-code flow with mandatory PKCE S256 and loopback redirects
(RFC 8252). Identity comes from the existing browser sign-in provider; every
authorization ends in an explicit consent page. Access tokens live one hour,
refresh tokens rotate with replay detection, and grants expire after 30 days.
An access token acts as the user with the user's own roles and GitHub-derived
grants but cannot mint or manage credentials.

Refresh re-derives GitHub access with the user's GitHub token, stored per grant
under AES-256-GCM with a deployment key, so access removed on GitHub reaches
agents within the hour. Scopes are persisted and echoed but not enforced.

## Rationale

MCP clients (pi, OpenCode, Claude Code, Cursor) implement the MCP authorization
specification with dynamic registration and PKCE; supporting it removes
long-lived hand-copied API tokens from agent configuration. An external
authorization server (Entra, Keycloak) would not know GitHub repository grants
and cannot register clients dynamically, breaking the "you see exactly what
GitHub lets you see" property and requiring per-client setup. Reusing the
session and token machinery keeps one authorization path.

Consent is required so an arbitrary local process cannot obtain the user's
GraphNest identity by triggering a login. Persisting `scope` now allows a later
`insufficient_scope` step-up without a migration.

## Consequences

Three tables (`oauth_clients`, `oauth_authorization_requests`, `oauth_grants`),
new `/oauth/*` and `/.well-known/*` routes, a `WWW-Authenticate` challenge on
`/mcp`, and account routes for connected clients. Confidential clients,
`client_credentials`, remembered consent and client-ID metadata documents are
deliberately absent; services continue to use API tokens and delegation. The
login-to-exchange hand-off of the GitHub token is process-local, which
constrains multi-replica deployments to route a browser session to one replica
during authorization.
