# Repository Guidelines

## Project Structure & Module Organization

GrepNest is a Go code-search service. Executables live in `cmd/` (`grepnest-server`, `grepnest-indexer`, `grepnest-mcp`, and `grepnest-migrate`). Core implementation belongs in `internal/`; reusable API models are in `pkg/api/`. Put unit tests beside their packages. Cross-component tests live in `test/integration/` and `test/e2e/`, with deterministic inputs under `test/fixtures/`. Deployment resources are under `deploy/compose/` and `deploy/helm/grepnest/`; architecture, operations, ADRs, and the OpenAPI contract are under `docs/`.

## Build, Test, and Development Commands

- `make build` compiles every command under `cmd/`.
- `make server` runs the HTTP server locally; configure required `GREPNEST_*` variables as documented in `README.md`.
- `make test` runs the standard Go test suite.
- `make test-race` runs unit tests with the race detector and is part of CI.
- `make integration` starts PostgreSQL with Docker Compose and runs integration-tagged tests.
- `make e2e` installs pinned Zoekt tools, starts PostgreSQL, and runs end-to-end tests.
- `make fmt lint staticcheck govulncheck` checks formatting, `go vet`, static analysis, and known vulnerabilities.
- `make helm-lint helm-test` validates and renders the Helm chart.

Go 1.26, Git, and Docker Compose are required.

## Coding Style & Naming Conventions

Run `gofmt` on Go changes. Follow standard Go naming: lowercase packages, exported identifiers in `PascalCase`, and unexported identifiers in `camelCase`. Keep command wiring in `cmd/` and business logic in focused `internal/` packages. Preserve service boundaries: Zoekt is an implementation detail, not a public API.

## Testing Guidelines

Name tests `TestXxx` in `*_test.go`. Add tests for behavior changes and prefer package-level unit tests before integration coverage. Use the existing `integration` and `e2e` build tags for tests requiring PostgreSQL or Zoekt. No numeric coverage threshold is enforced; CI requires race, integration, end-to-end, security, and Helm checks to pass.

## Commit & Pull Request Guidelines

Use the repository's conventional, imperative subjects, such as `fix(api): reject impossible list budgets`; keep each commit atomic and under 72 characters. Never commit directly to `main` or `master`. Pull requests should stay within an accepted milestone, explain user-visible behavior, link the relevant issue or design, and list verification commands. Include screenshots only for UI or rendered-chart changes, and call out configuration, schema, or security implications explicitly.

## Security & Configuration

Never commit tokens, private keys, webhook secrets, or local Codex settings. Use distinct development user/admin tokens and secret-file environment variables described in `README.md`. Report vulnerabilities through `SECURITY.md` rather than a public issue.
