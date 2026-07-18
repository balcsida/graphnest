# Milestone 2 Design

## Scope

Milestone 2 replaces the static runtime repository registry with PostgreSQL,
ingests GitHub Enterprise App events, and indexes each repository's default
branch through a durable queue. REST and MCP keep the shared authorization and
search service built in Milestones 0-1.

This milestone does not add OpenShift, Helm, public ingress, multi-node Zoekt,
semantic search, a UI, Redis, an ORM, or a general workflow engine.

## Architecture

Run two Go processes against one PostgreSQL database and one Zoekt node:

```text
GitHub Enterprise -> grepnest-server -> PostgreSQL <- grepnest-indexer
                          |                          |
                    REST and MCP               Git -> Zoekt
```

`grepnest-server` owns webhook verification, GitHub reconciliation, repository
and status APIs, indexed-SHA file reads, and the PostgreSQL-backed repository
registry. `grepnest-indexer` claims one leased job at a time, fetches one
default branch, invokes the pinned Zoekt indexer, and publishes completion only
after Zoekt exposes the requested revision.

Both processes may run embedded ordered migrations. A PostgreSQL advisory
transaction lock serializes migration application.

## GitHub Enterprise App

Use `net/http`, `crypto/rsa`, `crypto/sha256`, and `crypto/x509`; do not add a
GitHub SDK. Configuration supplies independent HTTPS web, REST API, upload, and
Git remote bases, the App ID, private key, webhook secret, REST API version,
and optional custom CA bundle. The API version defaults to `2022-11-28`.

App JWTs use RS256 with `iat` 60 seconds in the past, expiration no more than
10 minutes later, and App ID as `iss`. Installation tokens are minted just in
time, kept only in process memory, and discarded shortly before expiry. Cache
keys include installation and optional repository restriction.

The Go and Git clients extend the system root pool with the same configured CA
bundle. They require HTTPS, reject redirects, and accept API and clone targets
only on their configured hosts. No code path may set `InsecureSkipVerify`.

Reconciliation uses numeric installation and repository IDs as durable
identity. It runs at startup, periodically, and after relevant webhook events.
Renames update display metadata without changing the internal repository or
Zoekt IDs. Removed, suspended, archived, or disabled repositories become
unavailable without recycling their IDs.

## Webhook Ingestion

`POST /v1/github/webhooks` requires `X-Hub-Signature-256`,
`X-GitHub-Event`, and `X-GitHub-Delivery`. Read a bounded raw body, verify the
SHA-256 HMAC in constant time, and only then decode JSON. Never persist the
body.

Handle these events:

- `installation` and `installation_repositories`: reconcile the installation;
- `repository`: reconcile rename, transfer, archive, and deletion state;
- `push`: enqueue only `refs/heads/<current-default-branch>` with a nonzero
  40-character hexadecimal commit SHA.

One transaction inserts the delivery ID, updates repository state and
`desired_sha`, and upserts the newest queued job. A duplicate delivery is a
successful no-op. Unknown event types are acknowledged without mutation.

## PostgreSQL Model and Queue

Use `github.com/jackc/pgx/v5` directly. Embedded SQL migrations create:

- `installations`: GitHub installation ID, account metadata, and availability;
- `repositories`: GitHub repository ID, installation, owner/name, default
  branch, desired and indexed SHAs, status, error, and stable Zoekt RepoID;
- `webhook_deliveries`: delivery ID, event name, and receipt time only;
- `index_jobs`: repository, target SHA, state, attempt, scheduling, lease, and
  bounded failure metadata;
- `search_nodes`: the single configured Zoekt node identity and health state.

Allocate Zoekt RepoIDs from a sequence constrained to unsigned 32-bit values;
never recycle them. Partial unique indexes allow at most one queued and one
running job per repository.

A worker claims with `FOR UPDATE SKIP LOCKED` in a short transaction and
commits before network or process work. A running job holds a renewable lease.
Renew, complete, and fail operations require the matching lease owner and an
unexpired lease. A reaper terminalizes expired attempts and queues the current
desired SHA when needed.

Completing a job and updating repository status occur in one transaction.
Publish `indexed_sha` only when the completed target still equals
`desired_sha`; otherwise preserve the previous indexed revision and leave the
newest work queued. Retain delivery IDs for 30 days and the newest 100 terminal
jobs per repository.

## Default-Branch Indexer

Use the system Git executable and the repository-pinned Zoekt binaries through
argument slices, never a shell or Git library. Store paths only by internal
numeric IDs:

```text
<data>/mirrors/<repository-id>.git
<data>/worktrees/<repository-id>/<job-id>
```

Resolve paths beneath their configured roots before use. One shared bare
mirror exists per repository and one temporary detached worktree per attempt.
Fetch only the configured default branch without tags, verify the requested
SHA as a commit, and recheck `desired_sha` before indexing.

Persist only a credential-free remote. A fixed askpass helper reads the
just-minted token from an allowlisted child environment. Set
`GIT_TERMINAL_PROMPT=0`, disable credential helpers, reject redirects and the
file protocol, disable hooks and LFS smudge, and never initialize submodules or
execute repository files.

Configure stable `zoekt.repoid`, repository name, web URL, and default branch
on the mirror. Run the pinned `zoekt-git-index` with one worker, the existing
2 MiB file limit, incremental indexing, submodules disabled, and ctags
disabled. Poll bounded Zoekt `/api/list` responses until the RepoID, branch,
and SHA are visible. An indexer exit alone is not completion.

Git and Zoekt commands have separate deadlines, bounded output, and Unix
process-group termination. On startup, prune abandoned worktrees that do not
belong to active leases. A configured free-space floor rejects new work before
Git can exhaust the data volume.

## Serving Consistency

PostgreSQL supplies the existing search service with authorized repositories
and server-selected Zoekt RepoIDs. Search returns a match only when Zoekt's
branch version equals that repository's committed `indexed_sha`. During the
filesystem/database publication gap, a mismatch therefore produces no result
rather than a citation to inconsistent content.

Repository list and status endpoints expose only repositories authorized for
the bearer principal. File reads authorize first, require a nonempty
`indexed_sha`, validate a clean slash-separated repository path, and call the
GitHub Contents API at that exact SHA. Accept only bounded UTF-8 regular-file
content; reject directories, symlinks, submodules, binary data, invalid base64,
and invalid line ranges. Responses include indexed and blob SHAs.

## Failure Handling

- Superseded work is cancelled and the newest queued revision proceeds.
- Network, rate-limit, Git timeout, Zoekt, and transient database failures use
  bounded exponential backoff with jitter.
- One 401 refreshes an installation token and retries once.
- Revoked installations and unavailable repositories are blocked until
  reconciliation.
- Invalid or missing target commits are terminal for that job; reconciliation
  determines the next desired SHA.
- Index or visibility failure retains the prior `indexed_sha`.
- Invalid CA, unsafe host/path, RepoID exhaustion, and malformed stored metadata
  are permanent configuration or data errors.

Secrets, authorization headers, token-bearing environments, webhook bodies,
and unredacted remote stderr never enter logs or PostgreSQL.

## Acceptance Tests

1. A fake TLS GHES verifies App JWT exchange, installation-token use, custom
   CA trust, pagination, redirect rejection, reconciliation, and redaction.
2. Webhook tests prove verification-before-decoding, body bounds, durable
   deduplication, default-branch filtering, and transactional job coalescing.
3. PostgreSQL integration tests prove concurrent claims, lease ownership,
   expiry recovery, push coalescing, and atomic indexed-SHA publication.
4. Temporary Git repositories prove credential-free remotes, default-branch
   fetches, missing and superseded SHA handling, rename identity, and cleanup.
5. The pinned Zoekt binaries prove webhook to queue to exact visible SHA to
   authorized REST and MCP search, including an empty repository.
6. Search and file-read tests prove unauthorized repositories, unindexed
   repositories, and SHA-mismatched shards return no source content.
7. Existing Milestones 0-1 unit, race, integration, end-to-end, and build gates
   remain green.

## Milestone Boundary

Milestone 2 is complete when the accepted tests pass locally and in CI. Image
hardening, Helm, OpenShift security contexts, network policies, PVC sizing,
backup/restore, and production ingress remain Milestone 3 work.
