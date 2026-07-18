# ADR-0011: Use the Official MCP Go SDK

- Status: Accepted
- Date: 2026-07-18

## Decision

Use `github.com/modelcontextprotocol/go-sdk` v1.6.1 for Streamable HTTP and
stdio MCP transports.

## Rationale

The official SDK supplies protocol negotiation, schema handling, Streamable
HTTP, and stdio without GrepNest maintaining a second protocol implementation.
Version 1.6.1 is stable; 1.7 tags available at this decision date are
prereleases.

## Consequences

Hosted tools call the shared application service. The stdio executable proxies
the server's tools over authenticated Streamable HTTP and never calls Zoekt.
