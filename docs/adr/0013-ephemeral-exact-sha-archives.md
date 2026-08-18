# ADR-0013: Ingest Ephemeral Exact-SHA Archives

- Status: Accepted
- Date: 2026-08-18
- Extends: ADR-0002, ADR-0004, ADR-0005, ADR-0009

## Decision

The indexer acquires one job-scoped snapshot for the queued commit through a
source-neutral provider. The default provider downloads the repository archive
for that exact SHA with the repository-scoped GitHub App token, safely extracts
it into a bounded temporary workspace, and removes the workspace after every
terminal outcome. Zoekt and enabled enrichers consume that same directory.

The existing Git mirror/worktree implementation remains available as an
explicit rollback provider for one compatibility window. A job never mixes
providers. Persistent Zoekt shards remain separate from ephemeral source.

## Rationale

GrepNest needs file contents, not repository history. Exact-SHA archives remove
persistent clones and a second scanner checkout while preserving the existing
PostgreSQL desired/indexed SHA publication gates. One snapshot also prevents
indexing and enrichment from observing different revisions.

## Security and lifecycle

Archive redirects are bounded and HTTPS-only. GitHub.com may redirect only to
its documented archive host; GHES redirects require an explicit configured
origin allowlist. Authorization is stripped whenever a redirect leaves the API
origin, and errors/logs redact redirect targets. Credentials are never forwarded
to an untrusted host. Extraction rejects escaping paths, links, special files,
conflicting outputs, and configured resource limits.
Startup cleanup removes only inactive, conservatively old GrepNest workspaces;
active database jobs are never deleted.

## Consequences

Zoekt must index an ordinary directory with explicit repository metadata rather
than infer metadata from `.git`. Archive mode does not require Git at runtime.
Operators must provision bounded ephemeral space and may select the legacy Git
provider temporarily while migrating.
