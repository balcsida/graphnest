# Local UI Repair Design

## Goal

Make the released two-image deployment and the documented static quick start
match what the two web consoles advertise, without weakening the distinction
between operational administrator tokens and durable identity sessions.

## Administrator console

`/v1/admin/overview` remains the sole access probe. A 401, or a 403/404 from
that probe, locks the console. Other 403 responses are ordinary capability
denials and must show an action error without discarding a valid token.

Bearer administrators load only operational inventory. Users, groups, account
API tokens, and audit events are session-only and their navigation and screens
remain hidden in bearer mode. OIDC and local administrator sessions keep the
complete console. The backend identity guards remain unchanged.

The existing DOM test will reproduce an administrator API token whose identity
requests would return 403, prove that those requests are not made, and prove
that operational data remains visible. It will also cover a denied operational
mutation without locking the console.

## Static search console

Static mode will register repository inventory and status routes using the
already-loaded static repository registry. File reads remain durable-only
because they require an authenticated GitHub App and indexed-SHA verification.
The HTTP registration is split into inventory routes and file-read routes so
the static server cannot accidentally expose an unusable handler.

The public UI will learn whether file reads are enabled from the existing auth
configuration response. When disabled, result paths are non-interactive and
the exact indexed GitHub link is the only open action. Repository filtering and
the repository table continue to work. No unauthenticated GitHub API client is
added.

Server and DOM tests will verify the static inventory route, the absent static
file-read route, and the non-interactive public-repository result path.

## Reproducible quick start

The Compose fixture commit will use fixed author and committer timestamps. The
checked-in static registry will contain that deterministic commit SHA instead
of an empty value. The existing Compose test will recreate the fixture commit
and fail if its SHA diverges from the registry, preserving the exact-SHA search
boundary when fixture contents change.

## Two-image durable deployment

The builder will compile `grepnest-scanner` and `grepnest-graph`, and the node
image will contain them alongside the indexer and Zoekt tools. Durable Compose
will run scanner replicas from `GREPNEST_NODE_IMAGE`; the separate graph mode
already uses that image. Documentation and Compose assertions will describe
only application and node images.

Image and Compose tests will assert the binaries and the shared node-image
contract. No third scanner image or release artifact is introduced.

## Verification

Run focused Go and Node DOM tests first, then `make test`, `make compose-test`,
`make image-test` when Docker is available, formatting and static checks, and a
browser smoke test against two public GitHub repositories with exact SHAs.
