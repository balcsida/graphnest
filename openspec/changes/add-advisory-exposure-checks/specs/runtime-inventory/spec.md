# Spec Delta

## Purpose

Record the container base images and language runtimes a repository's default
branch pins, with file and line evidence, in the supply-chain inventory so
advisories about runtimes and images can be matched like package advisories.

## ADDED Requirements

### Requirement: Extraction follows lexical publication
When the supply-chain module is enabled, the indexer SHALL extract runtime and image pins from the exact tree it indexed, after the commit becomes searchable and before the index job completes. When the module is disabled, the indexer SHALL NOT read candidate files or write supply-chain rows.

#### Scenario: Module enabled
- **WHEN** `GRAPHNEST_SUPPLY_CHAIN=true` and an index job publishes commit `C` for a repository
- **THEN** a `graphnest:runtime` collection is recorded for that repository for commit `C` before the index job is marked complete

#### Scenario: Module disabled
- **WHEN** `GRAPHNEST_SUPPLY_CHAIN` is unset or `false` and an index job runs
- **THEN** no `graphnest:runtime` collection, snapshot, or owner row is written

### Requirement: Snapshots are bound to the indexed commit
A successful extraction SHALL publish an immutable snapshot on stream `graphnest:runtime` with producer `graphnest`, subject `source`, the indexed commit as subject revision, and subject assurance `verified`. The preserved original document SHALL be a CycloneDX 1.6 JSON document downloadable through the existing snapshot document route.

#### Scenario: Published snapshot metadata
- **WHEN** extraction succeeds for commit `C`
- **THEN** the stream's latest snapshot has `subject_revision` equal to `C`, `subject_assurance` equal to `verified`, and its document downloads as CycloneDX 1.6 JSON listing the same components

### Requirement: Extraction failure never fails indexing
An extraction error, timeout, or budget overrun SHALL be recorded as a failed `graphnest:runtime` collection with a safe diagnostic, SHALL leave the stream's previous latest snapshot in place, and SHALL NOT change the outcome of the index job.

#### Scenario: Budget exceeded
- **WHEN** a repository contains more candidate files than the extraction budget allows
- **THEN** the collection outcome is `too_large`, no partial snapshot is published, and the index job still completes successfully

### Requirement: Dockerfile base images
The extractor SHALL record every external base image named by a `FROM` instruction in files named `Dockerfile`, `Dockerfile.*`, `*.Dockerfile`, `*.dockerfile`, `Containerfile`, or `Containerfile.*`. It SHALL ignore `scratch`, references to earlier build stages, and `--platform` and other flags, and SHALL honour line continuations and comments.

#### Scenario: Multi-stage build
- **WHEN** a Dockerfile contains `FROM node:22.18.0-slim AS build` and later `FROM build` and `FROM gcr.io/distroless/nodejs22-debian12`
- **THEN** two image components are recorded, `node` and `gcr.io/distroless/nodejs22-debian12`, and the stage reference `build` is not

#### Scenario: Digest pin with tag
- **WHEN** a Dockerfile contains `FROM node:22.18.0-slim@sha256:<digest>`
- **THEN** the component version is `22.18.0-slim` and the digest is kept as a qualifier

### Requirement: Variable indirection is resolved only when unambiguous
A `$VAR`, `${VAR}`, or `${VAR:-default}` in an image reference or pin SHALL be resolved from an `ARG` default in the same Dockerfile, then from a single repository-wide definition in `*.env` files or `docker-bake.hcl` variable defaults. Anything else SHALL be recorded as unresolved with the raw text and any candidate values, never guessed.

#### Scenario: ARG default
- **WHEN** a Dockerfile has `ARG NODE_VERSION=22.18` before `FROM node:${NODE_VERSION}-slim`
- **THEN** the component version is `22.18-slim` and its evidence names both the `FROM` line and the `ARG` line

#### Scenario: Conflicting definitions
- **WHEN** `FROM node:${NODE_VERSION}` has no `ARG` default and `NODE_VERSION` is defined as `22` in `build.env` and as `20` in `docker-bake.hcl`
- **THEN** the component is unresolved with raw version `${NODE_VERSION}` and both candidates listed in its evidence

### Requirement: Runtime pin files
The extractor SHALL record Node.js pins from `.nvmrc` and `.node-version`; Node.js, Bun, and .NET pins from `.tool-versions`; Bun pins from `.bun-version`; the .NET SDK from `global.json` `sdk.version`; and .NET runtimes from `TargetFramework` and `TargetFrameworks` in `*.csproj`, `*.fsproj`, `*.vbproj`, and `Directory.Build.props`.

#### Scenario: nvmrc alias
- **WHEN** `.nvmrc` contains `lts/jod`
- **THEN** a Node.js component is recorded with version `lts/jod`

#### Scenario: Multiple target frameworks
- **WHEN** a project file contains `<TargetFrameworks>net8.0;net9.0;netstandard2.0</TargetFrameworks>`
- **THEN** .NET runtime components `8.0` and `9.0` are recorded and `netstandard2.0` is ignored

### Requirement: CI and package manifest pins
The extractor SHALL record `node-version`, `bun-version`, and `dotnet-version` inputs of GitHub Actions workflow steps, expanding `${{ matrix.<key> }}` from the same job's matrix list, and container images from `container:` and `services.*.image`. It SHALL record `engines.node` and `engines.bun` from `package.json` outside `node_modules`.

#### Scenario: Workflow matrix
- **WHEN** a workflow job has `strategy.matrix.node: [20, 22]` and a step `with: {node-version: "${{ matrix.node }}"}`
- **THEN** two Node.js components are recorded with versions `20` and `22`, each pointing at the step's `node-version` line

#### Scenario: Version read from a file
- **WHEN** a workflow step uses `node-version-file: .nvmrc`
- **THEN** no component is recorded for that step, and `.nvmrc` is recorded from the file itself

### Requirement: Image references in YAML and build files
The extractor SHALL record image references from `image:` scalars in YAML files, from Helm-style maps that have both `repository` and `tag` keys, from `*.env` keys ending in `IMAGE`, and from `docker-bake.hcl` arguments. It SHALL skip values containing template expressions such as `{{ }}`.

#### Scenario: Helm values
- **WHEN** `values.yaml` contains `image: {repository: envoyproxy/envoy, tag: v1.37.2}`
- **THEN** an image component `envoyproxy/envoy` version `v1.37.2` is recorded with the line of `tag`

#### Scenario: Templated manifest
- **WHEN** a chart template contains `image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"`
- **THEN** no component is recorded for that line

### Requirement: Evidence on every occurrence
Every recorded component SHALL carry each occurrence's repository-relative path, one-based line number, and the line text, plus the location of any definition used to resolve a variable. Occurrences SHALL be capped per component and line text SHALL be length-bounded.

#### Scenario: Same image in two files
- **WHEN** `node:22-slim` appears in `Dockerfile` line 1 and `worker/Dockerfile` line 3
- **THEN** one component is recorded with two locations, `Dockerfile:1` and `worker/Dockerfile:3`

### Requirement: Package identifiers
Images SHALL be identified as `pkg:docker/<path>@<tag>` with `repository_url` set when the registry is not Docker Hub, and Docker Hub official images under namespace `library`. Node.js and Bun SHALL be `pkg:generic/node` and `pkg:generic/bun`; .NET runtimes `pkg:nuget/Microsoft.NETCore.App`; the .NET SDK `pkg:nuget/Microsoft.NET.Sdk`.

#### Scenario: Registry image
- **WHEN** a Dockerfile contains `FROM mcr.microsoft.com/dotnet/aspnet:8.0`
- **THEN** the component purl is `pkg:docker/dotnet/aspnet@8.0?repository_url=mcr.microsoft.com`

#### Scenario: Official image
- **WHEN** a Dockerfile contains `FROM node:22.21.0-slim`
- **THEN** the component purl is `pkg:docker/library/node@22.21.0-slim`

### Requirement: Bounded and inert reading
Extraction SHALL read only regular files within the indexed tree, SHALL NOT follow symbolic links, execute anything, or make network calls, SHALL skip `.git` and `node_modules`, and SHALL skip files larger than its per-file limit. From `*.env` files it SHALL record only values of allowlisted version and image keys.

#### Scenario: Secret in an env file
- **WHEN** `build.env` contains `NODE_VERSION=22.18.0` and `API_TOKEN=abc`
- **THEN** only the `NODE_VERSION` value appears in any snapshot, document, or evidence

#### Scenario: Symbolic link
- **WHEN** `Dockerfile` is a symbolic link to a file outside the repository
- **THEN** it is not read and no component is recorded from it

### Requirement: CODEOWNERS captured with the inventory
When the indexed tree contains a CODEOWNERS file in `.github/`, the root, or `docs/` (first found in that order), the extraction SHALL store its rules for the repository with the indexed commit so owners can be resolved per evidence path.

#### Scenario: CODEOWNERS present
- **WHEN** `.github/CODEOWNERS` contains `* @acme/payments`
- **THEN** the repository's stored CODEOWNERS rules for commit `C` include that rule

### Requirement: Runtime stream is visible in inventory views
The `graphnest:runtime` stream SHALL be selectable wherever a stream can be chosen in inventory REST routes, the web inventory views, and MCP inventory tools. It SHALL NOT be scheduled for GitHub collection or refreshed through the manual refresh route, and its components SHALL NOT be sent to license enrichment.

#### Scenario: Manual refresh refused
- **WHEN** an administrator requests a manual refresh of stream `graphnest:runtime`
- **THEN** the request fails with `400 invalid_request` explaining that runtime inventory refreshes when the repository is indexed

#### Scenario: Stream selection
- **WHEN** a user lists components of a repository with `stream=graphnest:runtime`
- **THEN** the runtime components are returned with their locations
