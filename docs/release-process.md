# Release Process

Releases require a clean review, passing CI, pinned dependencies, a signed tag,
and release notes describing compatibility and security impact. This is a
pre-1.0 pilot compatible with Kubernetes 1.25 or newer.

Before tagging, set both `version` and `appVersion` in
`deploy/helm/graphnest/Chart.yaml` to the release version. The tag commit must
be reachable from `main`.

```sh
make fmt lint staticcheck govulncheck test test-race integration e2e build \
  compose-test openapi-check helm-lint helm-test image-test release-chart-test
git tag -s v0.1.0 -m 'GraphNest v0.1.0

Compatibility: pre-1.0 pilot; Kubernetes 1.25 or newer.
Security: images include SBOMs, provenance, and GitHub attestations.'
git push origin v0.1.0
```

The Release workflow publishes only the version and commit tags, never
floating tags. It publishes multi-architecture application and node images,
their SBOMs, provenance, and GitHub attestations, then packages an OCI chart
whose copied values contain both immutable image digests. It creates the
GitHub Release only after that work succeeds.

Rerun a tagged release only before its GitHub Release exists. Once it exists,
the workflow refuses to republish that tag.

After the GitHub Release exists, the `homebrew` job asks
[balcsida/homebrew-tap](https://github.com/balcsida/homebrew-tap) to move its
`graphnest` formula to the new version (`repository_dispatch` event
`update-graphnest`). The tap takes each binary's SHA-256 from the release's
checksums file and installs and tests the formula before publishing it. The job
needs the `TAP_DISPATCH_TOKEN` secret: a fine-grained token with Contents: write
on `balcsida/homebrew-tap` only. Without it the job leaves a notice and the
tap's daily check picks up the release; if the job fails, rerun only that job.
