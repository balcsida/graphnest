# ADR-0010: No Java or JVM Runtime Dependency

- Status: Accepted
- Date: 2026-07-18

## Decision

GraphNest will contain no Java runtime, JVM, Maven, Gradle, or Java-based
sidecar, service, build step, or deployable dependency.

## Rationale

Zoekt indexes Java, Kotlin, and Gradle files as text without executing their
toolchains. GraphNest does not need a JVM to search them.

## Consequences

Container tests will assert the absence of Java executables and dependencies.
Repository indexing never executes repository code.
