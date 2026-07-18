# ADR-0004: Use PostgreSQL for Metadata and the Durable Queue

- Status: Accepted for Milestone 2
- Date: 2026-07-18

## Decision

Use PostgreSQL as the metadata store and durable index-job queue beginning in
Milestone 2. Use `pgx/v5` directly with embedded ordered SQL migrations; do not
add an ORM, Redis, or a migration framework.

## Rationale

One transactional system can enforce repository identity, webhook
deduplication, job coalescing, and lease-based claims without Redis.

## Consequences

The schema stores installations, repositories, webhook delivery IDs, index
jobs, and the single search node. Webhook deduplication, desired-SHA updates,
and queued-job coalescing share one transaction. Workers claim short
transactions with `FOR UPDATE SKIP LOCKED`, perform external work outside the
transaction, and publish `indexed_sha` only for the current desired SHA. Queue
states and database constraints enforce one running and one newest queued job
per repository.
