# ADR-0007: Start with One Zoekt Node

- Status: Accepted
- Date: 2026-07-18

## Decision

Place many repositories on one Zoekt node for the pilot and keep placement
behind a `ShardRouter` boundary.

## Rationale

One node is sufficient to measure the initial corpus and avoids speculative
fan-out, rebalancing, and distributed coordination.

## Consequences

No one-pod-per-repository layout and no multi-node implementation are included.
Sharding requires a separate design after measured capacity data exists.
