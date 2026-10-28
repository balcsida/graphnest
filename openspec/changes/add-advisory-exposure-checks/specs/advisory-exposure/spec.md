# Spec Delta

## Purpose

Show, for every advisory, which repositories are exposed, how urgently, where
the evidence is, and who owns it, kept current as repositories change, and
readable by people, agents, and exports.

## ADDED Requirements

### Requirement: Matching spans every inventory stream
An advisory SHALL be matched against the latest snapshot of every stream of each repository: the GitHub dependency graph, SBOM imports, and the runtime inventory. A component matches a product when its package URL type is equal and its namespace and name match one of the product's patterns.

#### Scenario: Library from the dependency graph
- **WHEN** a repository's `github:source` snapshot contains `pkg:pypi/pyarrow@21.0.0` and an advisory product matches `pkg:pypi/pyarrow` with `fixed: [22.0.0]`
- **THEN** the repository has a vulnerable occurrence from the dependency graph

#### Scenario: Image from an imported SBOM
- **WHEN** an `import:artifact:syft-image` snapshot contains a matching component
- **THEN** that occurrence is evaluated like any other, with the stream named in its evidence

### Requirement: Package URL patterns
Pattern names and namespaces SHALL support `*` within a segment. PyPI names SHALL compare after PEP 503 normalization. A Docker pattern SHALL match an image whose path equals the pattern path or ends with it after a `/`, so registry proxies match. A `library/<name>` pattern SHALL also match a single-segment image `<name>` on any registry. A pattern's `repository_url` restricts the registry.

#### Scenario: Registry proxy
- **WHEN** a pattern is `pkg:docker/library/node` and a Dockerfile uses `artifactory.example.com/docker-remote/library/node:22.18.0`
- **THEN** the image matches

#### Scenario: Different publisher
- **WHEN** a pattern is `pkg:docker/library/node` and a Dockerfile uses `bitnami/node:22`
- **THEN** the image does not match

#### Scenario: Wildcard
- **WHEN** a pattern is `pkg:docker/dotnet/*?repository_url=mcr.microsoft.com`
- **THEN** both `mcr.microsoft.com/dotnet/aspnet:8.0` and `mcr.microsoft.com/dotnet/runtime:8.0` match

### Requirement: Exact versions
An occurrence with a complete version SHALL be `fixed` when the version is not affected because a fixed version of its line is at or below it, `not_affected` when it is below `introduced`, `eol` when it is affected and its line is listed as EOL, and otherwise `vulnerable`.

#### Scenario: Pinned and vulnerable
- **WHEN** an advisory fixes Node.js in `22.21.1` and a Dockerfile pins `node:22.18.0-slim`
- **THEN** the occurrence is `vulnerable` with the reason `Node.js 22.18.0, fixed in 22.21.1`

#### Scenario: Pinned and fixed
- **WHEN** a Dockerfile pins `node:22.21.1-slim`
- **THEN** the occurrence is `fixed`

#### Scenario: EOL line
- **WHEN** the advisory lists `eol: ["<20"]` and a workflow uses `node-version: 18.20.4`
- **THEN** the occurrence is `eol`

### Requirement: Floating versions
A partial version, range, `latest`, or alias SHALL be treated as floating over the versions it allows. It SHALL be `needs_rebuild` when a fixed version lies within that span and nothing affected follows it there, `fixed` when no version in the span is affected, `eol` when its line is EOL, and otherwise `vulnerable`.

#### Scenario: Major float
- **WHEN** the advisory fixes Node.js in `22.21.1` and a Dockerfile uses `node:22-slim`
- **THEN** the occurrence is `needs_rebuild`

#### Scenario: Minor float without a fix
- **WHEN** the advisory fixes Node.js in `22.21.1` and `.nvmrc` contains `22.18`
- **THEN** the occurrence is `vulnerable` because 22.18.x has no fixed release

#### Scenario: Engines range
- **WHEN** `package.json` declares `"engines": {"node": "^18 || ^20 || ^22"}`
- **THEN** the occurrence is evaluated as its newest alternative, `^22`

### Requirement: Image tags and aliases
An image tag's version SHALL be its first numeric token, with an optional `v` prefix; a tag containing `latest` floats at that token's precision. Tags made only of variant words such as `slim` or `alpine` float over all versions. Other words SHALL be looked up in the product's `tag_aliases`; unknown words, unresolved variables, and digests without a tag SHALL make the occurrence `unresolved`.

#### Scenario: Envoy floating tag
- **WHEN** an advisory fixes Envoy in `1.38.1` and a Dockerfile uses `envoyproxy/envoy:distroless-v1.38-latest`
- **THEN** the occurrence is `needs_rebuild`

#### Scenario: Codename tag
- **WHEN** a Dockerfile uses `node:krypton-slim` and the product has `tag_aliases: {krypton: 24}` with `24.11.1` fixed
- **THEN** the occurrence is `needs_rebuild`

#### Scenario: Unknown codename
- **WHEN** a Dockerfile uses `node:gallium-slim` and `gallium` has no alias
- **THEN** the occurrence is `unresolved` with the reason `tag gallium-slim has no alias`

### Requirement: Path classes
Every occurrence with a file path SHALL be classified as `ci` (CI configuration such as `.github/workflows/` or `.gitlab-ci.yml`), `test` (test, fixture, spec, or mock paths), `docs` (documentation, examples, samples, plans, and experiments), or otherwise `prod`. Dependency-graph occurrences without a path SHALL count as `prod`.

#### Scenario: Test fixture
- **WHEN** an occurrence is in `test/fixtures/legacy/Dockerfile`
- **THEN** its class is `test`

#### Scenario: Workflow
- **WHEN** an occurrence is in `.github/workflows/ci.yml`
- **THEN** its class is `ci`

### Requirement: Repository status
A repository's status SHALL be the most urgent status among its `prod` and `ci` occurrences, ranked `vulnerable`, `eol`, `unresolved`, `needs_rebuild`, `fixed`, `not_affected`. A repository whose only matches are `test` or `docs` SHALL be `non_production` and SHALL NOT count as open. A current exception SHALL make an open repository `excepted`.

#### Scenario: Mixed evidence
- **WHEN** a repository has a `needs_rebuild` CI occurrence and a `vulnerable` prod occurrence
- **THEN** the repository is `vulnerable`

#### Scenario: Docs-only hit
- **WHEN** a repository's only match is a Dockerfile under `docs/examples/`
- **THEN** the repository is `non_production` and excluded from the open count

### Requirement: Continuous re-checking
While an advisory is open, a repository SHALL be re-checked within a few minutes of a new snapshot on any of its streams, a new advisory version, an exception being recorded or lapsing, or an owner change. Each status change SHALL record the time and the commit of the evidence.

#### Scenario: Fix pushed
- **WHEN** a team pushes a commit that pins `node:22.21.1-slim` and the repository is re-indexed
- **THEN** within a few minutes the repository moves from `vulnerable` to `fixed` and the change records that commit

### Requirement: Coverage is reported, not assumed
Reports SHALL count authorized repositories whose runtime inventory is missing or older than the indexed commit, and those without dependency inventory, as not checked, and SHALL let users list them. A repository SHALL never appear unaffected because its inventory is missing.

#### Scenario: Repository never reindexed
- **WHEN** a repository has an indexed commit but no `graphnest:runtime` snapshot for it
- **THEN** the advisory reports it under not checked for runtime inventory

### Requirement: Owners
Each listed repository SHALL show one owner and its source: the CODEOWNERS owners of its most urgent evidence path, else teams with the admin role on the repository, else directly added admin collaborators, else the repository's organization.

#### Scenario: CODEOWNERS
- **WHEN** CODEOWNERS has `* @acme/payments` and `/deploy/ @acme/platform`, and the most urgent evidence is `deploy/Dockerfile`
- **THEN** the owner is `@acme/platform` with source `codeowners`

#### Scenario: No owner found
- **WHEN** a repository has no CODEOWNERS and GitHub reports no admin team or direct admin
- **THEN** the owner is the organization with source `org`

### Requirement: Authorized results only
Every advisory read, count, history, export, and MCP result SHALL be computed over the live principal's authorized repositories only. Repository-specific routes for an unauthorized repository SHALL return `404 not_found`, indistinguishable from a missing repository.

#### Scenario: Partial access
- **WHEN** a user authorized for 40 of 120 affected repositories opens an advisory
- **THEN** every count, row, burn-down point, and export covers only those 40

### Requirement: Paginated results without a fixed cap
Repository rows SHALL be ordered by status urgency then repository name, filterable by status, path class, owner, organization, and name, and cursor-paginated with page sizes bounded by the server's result limit. Following cursors SHALL reach every matching repository.

#### Scenario: Large advisory
- **WHEN** an advisory matches 350 authorized repositories and pages of 100 are requested
- **THEN** four pages return all 350 repositories exactly once

### Requirement: Reports state their scope
Every advisory report, including REST responses, MCP results, the web view, and exports, SHALL state that it covers default branches only, the time of the newest evidence, and that running images and clusters are not scanned.

#### Scenario: Markdown export header
- **WHEN** a user exports an advisory as Markdown
- **THEN** the document begins with the advisory, its version, the generation time, the default-branch-only statement, and the coverage counts

### Requirement: REST interface
Advisories SHALL be served under `/v1/supply-chain/advisories` with routes to list, read, create, edit, close, and reopen advisories, list repository rows, read one repository's evidence and history, record exceptions, export CSV and Markdown, and run dry-run checks. The routes SHALL exist only when the supply-chain module is enabled.

#### Scenario: Module disabled
- **WHEN** `GRAPHNEST_SUPPLY_CHAIN` is not enabled
- **THEN** `/v1/supply-chain/advisories` returns `404`

### Requirement: MCP tools
MCP SHALL offer read-only `list_advisories` and `check_advisory`. `check_advisory` SHALL accept a stored advisory id or key, or an inline definition evaluated as a dry run, plus filters and a cursor, and SHALL return rows with evidence, owners, signals, coverage, and a note that evidence is untrusted content.

#### Scenario: Inline check by an agent
- **WHEN** an agent calls `check_advisory` with an inline definition and no saved advisory exists
- **THEN** it receives classified rows for the caller's repositories, a `next_cursor` when more remain, and nothing is saved

#### Scenario: Output budget
- **WHEN** a page would exceed the tool output budget
- **THEN** fewer rows are returned with `next_cursor` set, rather than an error

### Requirement: CSV and Markdown exports
Exports SHALL include every authorized repository matching the requested filters, one CSV row per evidence line or one Markdown table row per repository. Cell values starting with formula characters SHALL be neutralized in CSV, and Markdown table syntax in untrusted values SHALL be escaped.

#### Scenario: Formula in evidence
- **WHEN** an evidence line text begins with `=`
- **THEN** the CSV cell is prefixed so spreadsheet software treats it as text

### Requirement: Web view
The console SHALL list advisories under `/supply-chain/advisories`, show each advisory with status counters, a burn-down since publication, filters, and a repository table ordered by urgency, open a side sheet with evidence, history, signals, and the exception form, and give administrators a page to paste, check, publish, edit, and close advisories.

#### Scenario: Programme manager view
- **WHEN** a user opens an advisory
- **THEN** the page shows the scope banner, counters per status, the burn-down, and the repository table with CSV and Markdown export buttons
