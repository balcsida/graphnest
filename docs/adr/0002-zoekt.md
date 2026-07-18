# ADR-0002: Use Zoekt as the Initial Search Backend

- Status: Accepted
- Date: 2026-07-18

## Decision

Use Zoekt commit `3c8b39b1ef4f8194cb912d7e6581cff9db224aa7`
(`v0.0.0-20260717095332-3c8b39b1ef4f`) for indexing and text search behind
GrepNest's `SearchBackend` interface. Pin the development image to
`ghcr.io/sourcegraph/zoekt@sha256:ac76391662c77d02f5be73b64272304415dbc42cac70633ef89d28747edff4cd`.

## Rationale

Zoekt already implements fast trigram-based code search. Reimplementing it
would add risk without improving the Milestone 1 outcome.

## Consequences

Zoekt remains internal and is never exposed directly. Its JSON and index
formats are adapter details, and GrepNest returns only canonical models.
