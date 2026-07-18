# ADR-0005: Use GitHub App Installation Authentication

- Status: Accepted for Milestone 2
- Date: 2026-07-18

## Decision

Access GitHub Enterprise Server and GitHub Enterprise Cloud with short-lived
GitHub App installation tokens and independently configurable HTTPS web, API,
upload, and Git remote base URLs. Use the Go standard library rather than a
GitHub SDK.

## Rationale

Installation tokens provide repository-scoped, revocable access without
persisting user credentials.

## Consequences

App JWTs use RS256 and installation tokens stay in memory. Tokens never enter
persisted remotes and are passed only to the Git process or API request that
needs them. Go and Git share a configurable custom CA bundle, reject redirects,
and never disable TLS verification. Numeric GitHub installation and repository
IDs are durable identity; the App requires only Metadata read and Contents read.
