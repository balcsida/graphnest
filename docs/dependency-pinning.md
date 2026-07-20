# Dependency Pinning

Pin Go modules, container images, and GitHub Actions to exact versions or
immutable revisions. Do not use floating tags such as `latest`.

CI installs `staticcheck` v0.7.0 and `govulncheck` v1.1.4 from the pinned Make
targets. Helm CI uses v3.18.4 through setup-helm v4.3.1 pinned to its immutable
commit.
