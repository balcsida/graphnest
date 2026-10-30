# Tasks

Each group is one reviewable layer (a stacked PR) and lands its own tests and
documentation. Group 2 is independent of the rest and can ship first.

## 1. Decision record and contracts

- [x] 1.1 Write `docs/adr/0020-runtime-inventory-and-advisories.md` (indexer-produced `graphnest:runtime` stream, operator-supplied advisories, not a vulnerability database) and add it to `docs/adr/README.md`; verify links render and ADR-0017 references it as amended
- [x] 1.2 Add the advisory routes, schemas, and the more specific search error text to `docs/openapi.yaml`; verify with `make openapi-check`

## 2. Search query errors

- [x] 2.1 Make `internal/zoekt` read at most 4 KiB of an HTTP 400 body and return `search.QueryError{Reason}` wrapping `ErrInvalidQuery`; verify with a client test whose server answers 400 with a JSON `Error` reporting the missing `]`
- [x] 2.2 Refuse unescaped flag groups that set or clear `i` (`(?i)`, `(?-i)`, `(?i:`) in `search.Service.Search` and add the hint table; verify with table tests, including `\(\?i\)` and `(?:22|20)` passing through
- [x] 2.3 Render `search query is invalid: <reason>[; hint: …]` in `writeSearchError` and MCP `runSearch` with control characters stripped and length bounded; update the `internal/mcpserver` "invalid query" expectations and verify REST and MCP tests
- [x] 2.4 Map GitHub code search `422` to `QueryError` with GitHub's bounded `message`; verify with an `httptest` GitHub server test
- [x] 2.5 Document `case:no` and term quoting in the README search section and add a CHANGELOG `Changed` entry (inline flags now return 400); verify `make test`

## 3. Storage

- [ ] 3.1 Add migration `040_supply_chain_advisories.sql`: advisory tables, `repository_owners`, `supply_chain_components.locations`, and producer `graphnest` in the check constraints; verify the migration integration test applies it on a populated database
- [ ] 3.2 Persist and read `Component.Locations` in `insertSupplyChainSnapshot` and component reads; verify with a `postgres` integration test
- [ ] 3.3 Add store methods for owners, advisories, versions, results, events, and exceptions, all scoped by repository IDs passed in; verify integration tests including cascade on repository deletion

## 4. Runtime inventory

- [ ] 4.1 Implement the image reference parser and purl mapping in `internal/supplychain/runtime`; verify table tests (official, user, registry with port, `localhost`, mcr, digest, tag+digest)
- [ ] 4.2 Implement the Dockerfile parser (continuations, comments, flags, `AS` stages, stage references, `scratch`, global and stage `ARG`); verify table tests including a multi-stage file with `ARG NODE_VERSION=22.18` and `FROM node:${NODE_VERSION}`
- [ ] 4.3 Implement `.nvmrc`, `.node-version`, `.bun-version`, `.tool-versions`, `global.json`, and MSBuild `TargetFramework(s)` parsers; verify table tests including `lts/jod` and `net8.0;net9.0;netstandard2.0`
- [ ] 4.4 Implement the GitHub Actions extractor (setup inputs, matrix expansion, `node-version-file` ignored, `container:`, `services.*.image`); verify table tests with a matrix workflow
- [ ] 4.5 Implement YAML `image:` and Helm `repository`/`tag` extraction with template skipping, and `package.json` `engines`; verify table tests
- [ ] 4.6 Implement `*.env` and `docker-bake.hcl` variable sources with the key allowlist and the resolution pass (same-file `ARG`, single repository-wide definition, else unresolved with candidates); verify table tests, including `build.env` with a secret that must not appear and the conflicting-definitions case
- [ ] 4.7 Implement the CODEOWNERS parser and matcher (anchors, directories, `*`, `**`, last match wins); verify table tests
- [ ] 4.8 Implement the walker and budgets (skip `.git`, `node_modules`, non-regular files; caps on files, bytes, time, and components → `too_large`, no partial publication); verify tests with temporary trees including a symlink escaping the root
- [ ] 4.9 Generate the deterministic CycloneDX 1.6 document and extend `NormalizeCycloneDX16` to carry `evidence.occurrences` as locations; verify a round-trip test and that `evidence_not_carried` remains for other evidence
- [ ] 4.10 Add the runtime publisher (publish with `JobID` nil, failure collections, CODEOWNERS upsert, no projection, no enrichment) and the `Worker.Runtime` hook after `PublishIndex`; add `config.Indexer.SupplyChain` and wire `cmd/graphnest-indexer`; verify worker tests that a failing or timed-out hook leaves the index job succeeded and that a disabled module never calls the hook
- [ ] 4.11 Accept `graphnest:runtime` in `ValidStream`/`StreamProducer`, reject it in `Refresh` with `invalid_request`, keep the scheduler on `github:source`, and add it to the web stream picker; verify service, REST, and UI tests
- [ ] 4.12 Pass `GRAPHNEST_SUPPLY_CHAIN` to the indexer in the Helm chart (when `server.supplyChain.enabled`) and Compose; verify `make helm-lint helm-test compose-test`
- [ ] 4.13 Add the extraction metric and document runtime inventory (sources, budgets, backfill reindex loop, coverage) in `docs/operations.md`, `docs/architecture.md`, and the extraction trust boundary in `docs/threat-model.md`; add a CHANGELOG `Added` entry; verify the docs build links

## 5. Advisory definitions and classification

- [ ] 5.1 Implement the definition parser in `internal/supplychain/advisory` (YAML/JSON, strict keys, alias rejection, field-path errors, count limits, supported package types with distribution types rejected); verify table tests for every validation scenario in `specs/advisory-definitions`
- [ ] 5.2 Implement OSV conversion (ecosystem → purl, `SEMVER`/`ECOSYSTEM` ranges, `versions`, skipped `GIT` and unknown ecosystems); verify with sanitized GHSA and Go fixture records under `test/fixtures/advisories/`
- [ ] 5.3 Implement version and selector parsing (exact, spans, npm ranges with `||` as highest alternative, aliases, Docker tags with variant words, .NET); verify table tests
- [ ] 5.4 Implement release-line inference, the unaffected set, occurrence statuses with reasons, path classes, repository aggregation, and owner resolution; verify table tests against the fixture advisory `test/fixtures/advisories/brotli.yaml` (Node.js, Envoy, Bun, pyarrow, OSV `brotli`)
- [ ] 5.5 Implement package URL pattern matching (wildcards, PEP 503, Docker path suffix for proxies, `library/` single segment, `repository_url`); verify table tests including the `bitnami/node` negative case
- [ ] 5.6 Document the advisory format with an annotated example in `docs/operations.md` and the advisory input trust boundary in `docs/threat-model.md`; verify the example parses in a test

## 6. Evaluation, exceptions, and history

- [ ] 6.1 Implement advisory create, edit (new version), close, and reopen (administrators only, unique key); verify service tests for `403`, `409`, and version preservation
- [ ] 6.2 Implement `EvaluatePending` (per-advisory `for update skip locked`, watermark, full pass on a new version, `status_changed` events only on real changes, with the commit) and run it in the server's one-minute loop; verify a `postgres` integration test where a newly published runtime snapshot moves a repository from `vulnerable` to `fixed`, and that two concurrent evaluators append one event
- [ ] 6.3 Implement exceptions (review grant or administrator, `basis` with `409 stale_basis`, expiry at most one year, `rebuilt` only for `needs_rebuild`, `withdrawn`, lapse on fingerprint change or expiry); verify service and integration tests
- [ ] 6.4 Implement history, burn-down (UTC days from the first check, publication and edits marked, excepted not open), and coverage reads scoped to the authorized repositories; verify tests including a principal who can read 40 of 120 repositories
- [ ] 6.5 Implement the dry-run check (stateless, cursor bound to a definition hash, 10 s budget, no writes); verify tests that nothing is persisted and that pages are complete
- [ ] 6.6 Add the evaluation metric and document the re-check cadence and exceptions in `docs/operations.md`; verify `make test-race`

## 7. Owners and signals

- [ ] 7.1 Add `Installation.Permissions`, `RepositoryTeams`, `RepositoryAdmins`, `MatchingHeads`, and `SearchPullRequests` to `internal/githubapp`; verify `httptest` tests for paging, `403`, and rate limiting
- [ ] 7.2 Implement the owner refresher (24 h, newly listed repositories immediately, `permission_missing` recorded, one replica per tick via `pg_try_advisory_lock`); verify tests and that evaluation picks up refreshed owners
- [ ] 7.3 Implement the signal refresher (hourly, open rows only, permission-aware, rate-limit deferral, `signal_seen` events); verify tests for a found PR, a found branch, and the missing-permission message
- [ ] 7.4 Document the optional `Administration: read` and `Pull requests: read` permissions, what degrades without them, and the GitHub-call trust boundary in `docs/operations.md` and `docs/threat-model.md`; add the signal metric; verify `make test`

## 8. REST, exports, and MCP

- [ ] 8.1 Register the `/v1/supply-chain/advisories…` and `/v1/supply-chain/advisory-checks` routes behind the module gate with the existing error envelope; verify `internal/httpapi` tests for the authorization matrix (delegation-only refused, `404` for unauthorized repositories, administrator-only writes) and the OpenAPI wire tests
- [ ] 8.2 Implement CSV and Markdown exports (filters, scope header, coverage, `csvSafe`, Markdown escaping); verify tests including a formula-leading cell and a value containing `|`
- [ ] 8.3 Implement MCP `list_advisories` and `check_advisory` (stored or inline dry run, filters, cursor, output-budget trimming with `next_cursor`, untrusted-content note); verify `internal/mcpserver` tests
- [ ] 8.4 Document the REST routes and MCP tools in `README.md`; add a CHANGELOG `Added` entry; verify `make test`

## 9. Web console

- [ ] 9.1 Add the API client types and routes `/supply-chain/advisories`, `/:id`, `/new`, `/:id/edit`, and the advisory list view; verify Vitest tests
- [ ] 9.2 Build the detail view (scope banner, counters, burn-down, filters, repository table ordered by urgency, export buttons); verify Vitest tests
- [ ] 9.3 Build the repository side sheet (evidence, history, signals, exception form echoing `basis`, shown only when `can_record_exception`); verify Vitest tests
- [ ] 9.4 Build the administrator page to paste, check (warnings and inferred lines), publish, edit, and close advisories; verify Vitest tests and `make ui-check ui-screenshots`
- [ ] 9.5 Write `docs/responding-to-a-cve.md` (paste an advisory, read the table, record exceptions, export, use `check_advisory`, backfill, the default-branch caveat) and link it from the README; verify every command in it against a local durable Compose stack

## 10. Acceptance

- [ ] 10.1 Add fixture repositories under `test/fixtures/advisories/repos/` covering Node.js, Envoy, Bun, and a Python library (pinned vulnerable, floating, EOL, unresolved via `${VAR}` and an unknown codename, test-only hit, dependency-graph match) and an integration test that runs extraction, publication, and evaluation and asserts `check_advisory` returns the expected classified rows with evidence (golden file), including a partially authorized principal
- [ ] 10.2 Run `make fmt lint staticcheck govulncheck test-race integration e2e helm-lint helm-test ui-check` and record the results in the PR

## Workflow follow-up

- Publish the groups as a native stack (`gh stack`) of draft PRs referencing this change; merging stays the owner's decision.
- Archive the change with `openspec archive add-advisory-exposure-checks` after the stack merges.
- Follow-ups recorded in `design.md` Non-Goals: owner-triggered fix PRs, Python runtime pins, other CI systems, OS packages in images, search pagination.
