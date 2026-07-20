# Compatibility

Milestones 0-2 are pre-pilot and make no release compatibility promise.
Milestone 2 targets GitHub Enterprise Server 3.17 with REST API version
`2022-11-28` by default and remains compatible with configurable HTTPS web,
API, upload, and Git endpoints plus a custom CA bundle. GitHub Enterprise Cloud
uses the same configurable contract. Only default branches are indexed.

The local GHES-compatible fixture is not a certification against a live GHES
instance. Kubernetes, OpenShift, published images, upgrades, backup/restore,
and production-scale compatibility remain unverified.
