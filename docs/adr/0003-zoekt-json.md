# ADR-0003: Start with the Zoekt JSON HTTP Adapter

- Status: Accepted
- Date: 2026-07-18

## Decision

Call Zoekt's pinned `POST /api/search` endpoint through a bounded `http.Client`.
Send the canonical `Q`, `RepoIDs`, and `Opts` fields and decode the `Result` or
`Error` envelope. Keep the protocol isolated in `internal/zoekt`.

## Rationale

JSON HTTP provides the smallest observable process boundary for the first
vertical slice and avoids coupling GrepNest to Zoekt's internal Go packages.

## Consequences

Evaluate gRPC only after an identical-query benchmark shows JSON serialization
or HTTP handling materially affects end-to-end latency or CPU. A useful switch
threshold is at least 10% lower p95 latency or CPU at representative load.
