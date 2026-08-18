# ADR-0015: Isolate Optional Enrichment

- Status: Accepted
- Date: 2026-08-18

## Decision

Mandatory exact-SHA Zoekt indexing remains in the root module and default
image. Tree-sitter scanners live in a separate Go module and optional image;
generation-only Buf and protobuf tools live in a tools module. SCIP stays in
the root until its runtime coupling can be separated without duplicating graph
domain code.

Optional enrichment consumes the indexer's prepared snapshot through a narrow,
versioned, non-secret artifact contract. It is bounded by timeout, output size,
and exit status and cannot invalidate or indefinitely delay a valid Zoekt
publication.

## Rationale

Disabled extensions should not be resolved, compiled, linked, or shipped by the
default build. Module boundaries provide that property without a plugin system,
workflow engine, long-running scanner service, or language-toolchain farm.

## Consequences

Core, scanner, tools, and any separately justified SCIP module have independent tests,
vulnerability checks, dependency updates, and release targets. Development may
use a `go.work`; release builds name only the modules they require.
