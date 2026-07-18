# ADR-0005: Use GitHub App Installation Authentication

- Status: Accepted for Milestone 2
- Date: 2026-07-18

## Decision

Access GitHub Enterprise Server and GitHub Enterprise Cloud with short-lived
GitHub App installation tokens and configurable web, API, and upload base URLs.

## Rationale

Installation tokens provide repository-scoped, revocable access without
persisting user credentials.

## Consequences

Tokens stay in memory, never enter persisted remotes, and are passed only to
the Git operation or API client that needs them. Milestone 1 uses fixtures and
does not implement this decision.
