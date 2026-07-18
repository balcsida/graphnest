# ADR-0009: Read Files at the Indexed SHA

- Status: Accepted for Milestone 2
- Date: 2026-07-18

## Decision

Future file reads default to each repository's `indexed_sha`, not its moving
default branch.

## Rationale

Agent-visible file content must correspond to search results and citations.

## Consequences

Search results carry indexed SHA metadata. File reads will use the authorized
GitHub content API because the public server does not mount the Zoekt PVC.
