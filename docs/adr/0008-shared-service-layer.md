# ADR-0008: Share One Application Service Between REST and MCP

- Status: Accepted
- Date: 2026-07-18

## Decision

REST and MCP handlers call the same search application service. Authentication,
authorization, limits, and backend selection live below both transports.

## Rationale

One service prevents transport-specific authorization gaps and keeps handlers
small.

## Consequences

`graphnest-server` hosts REST and Streamable HTTP MCP. `graphnest-mcp` is a stdio
client of the server and cannot bypass it to contact Zoekt.
