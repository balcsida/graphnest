# Dependency Pinning

Pin Go modules, container images, and GitHub Actions to exact versions or
immutable revisions. Do not use floating tags such as `latest`.

CI installs `staticcheck` and `govulncheck` at the versions pinned in the
`Makefile`. Helm CI pins its version through setup-helm, itself pinned to an
immutable commit. Release Actions use full commit SHAs, and base images use
manifest-list digests.

The kind cluster test pins kind, its node image, and the registry image in the
`Makefile`. When raising `KIND_VERSION`, take the node image digest from that
kind release's notes.
