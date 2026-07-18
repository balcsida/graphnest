# ADR-0006: Support OpenShift Arbitrary UIDs

- Status: Accepted for Milestone 3
- Date: 2026-07-18

## Decision

Build images that run as an arbitrary non-root UID with root group, without
`anyuid`, privileged mode, fixed `runAsUser`, or `hostPath`.

## Rationale

This matches OpenShift's restricted security model and remains valid on
standard Kubernetes.

## Consequences

Runtime images must use group-writable data paths, non-privileged ports,
dropped capabilities, and bounded writable mounts. Packaging starts only after
the local Milestones 0-1 slice passes.
