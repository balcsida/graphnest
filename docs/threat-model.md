# Threat Model

## Protected assets

- source, repository metadata, and indexed revisions;
- bearer tokens and future installation credentials;
- authorization scopes and server-selected Zoekt repository IDs;
- service and index availability.

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

## Known limits

This is a local development slice, not a production security boundary.
PostgreSQL has no application role yet. GitHub webhook verification,
installation-token handling, enterprise CAs, durable job leases, container
isolation, network policy, secret delivery, and production ingress are deferred
to Milestones 2 and 3. Do not claim production readiness from local or Compose
success.
