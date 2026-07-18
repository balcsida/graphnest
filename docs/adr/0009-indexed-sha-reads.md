# ADR-0009: Read Files at the Indexed SHA

- Status: Accepted for Milestone 2
- Date: 2026-07-18

## Decision

File reads use each repository's committed `indexed_sha`, not its moving
default branch. Search results are served only when Zoekt's branch version
matches that same committed SHA.

## Rationale

Agent-visible file content must correspond to search results and citations.

## Consequences

During the unavoidable Zoekt-filesystem/PostgreSQL publication gap, mismatched
search results are suppressed. File reads use the authorized GitHub Contents
API because the public server does not mount the Zoekt data volume. An empty
`indexed_sha` returns `not_indexed`; it never falls back to a branch head.
