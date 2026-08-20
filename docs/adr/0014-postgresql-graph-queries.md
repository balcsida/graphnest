# ADR-0014: Query the Authoritative PostgreSQL Graph

- Status: Accepted
- Date: 2026-08-18
- Supersedes: ADR-0012

## Decision

The graph service implements context, impact, trace, selector resolution, and
readiness directly from the existing PostgreSQL graph uploads, nodes, edges,
and manifests. Every query is scoped by authorized repositories and exact
commits, uses bounded parameterized operations, and returns stable ordering.

After normalized parity is established, GrepNest removes the LadybugDB replica,
its synchronization/runtime/client topology, and the raw Cypher API. PostgreSQL
remains the sole graph authority and query store.

## Rationale

LadybugDB duplicates data already stored transactionally in PostgreSQL while
adding a single-writer service, native library, CGO, persistence, synchronization,
health, and deployment modes. The supported public operations are narrow and
bounded; they do not require a general graph database interface.

## Consequences

Graph traversal batches frontiers and enforces existing depth, fanout, node,
edge, path, result, cancellation, and timeout limits. Representative fixtures
must demonstrate acceptable query plans before speculative indexes are added.
The administrator-only raw Cypher endpoint, MCP tool, and UI surface are a
documented pre-1.0 removal; no generic SQL replacement is introduced.
