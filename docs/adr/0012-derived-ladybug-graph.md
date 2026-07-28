# ADR-0012: Treat LadybugDB as Derived Graph Storage

- Status: Accepted
- Date: 2026-07-28

## Decision

PostgreSQL is authoritative for graph artifacts, jobs, repository state, and
indexed SHAs. LadybugDB is a derived local query store with one writable owner.
The default `embedded` mode runs that owner in the indexer; `separate` runs one
standalone graph process. Servers always use the authenticated internal graph
client.

## Rationale

One writer avoids divergent local copies and lets server replicas remain
stateless. A failed or incompatible LadybugDB database can be rebuilt from
PostgreSQL. The native runtime requires cgo and the pinned LadybugDB ABI, so
deployment images must carry the matching native library. Direct `.scip`
ingestion remains an independent code-navigation and compatibility-fallback
path, rather than a replacement native scanner.

## Rejected

Per-server LadybugDB copies were rejected: each replica would need its own
synchronization, recovery, and freshness behavior, while still requiring a
single authoritative source. Multiple graph writers were rejected for the same
reason.
