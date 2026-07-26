# Threat Model

## Protected assets

- source, repository metadata, and indexed revisions;
- bearer tokens and GitHub App installation credentials;
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

## Known limits

This remains a local development slice, not a production security boundary.
Git pack expansion and Zoekt shards require container and volume quotas.
Container isolation, network policy, secret delivery, backup/restore, and
production ingress remain Milestone 3 work. Do not claim production readiness
from local or Compose success. The Helm Ingress is structural support, not
proof of a production ingress deployment.
