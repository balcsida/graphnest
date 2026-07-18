# ADR-0004: Use PostgreSQL for Metadata and the Durable Queue

- Status: Accepted for Milestone 2
- Date: 2026-07-18

## Decision

Use PostgreSQL as the future metadata store and durable index-job queue. Do not
use it in the static Milestone 1 application path.

## Rationale

One transactional system can enforce repository identity, webhook
deduplication, job coalescing, and lease-based claims without Redis.

## Consequences

Development Compose includes PostgreSQL now. Schema, migrations, `pgx`, and
runtime readiness dependency begin only in Milestone 2.
