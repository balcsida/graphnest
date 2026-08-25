# SCIP Graph Support Design

## Purpose and success criteria

GraphNest will keep Zoekt as its lexical search engine and add precise code
navigation from standard SCIP protobuf indexes. A client or CI job can upload a
`.scip` file for an authorized repository and its exact indexed SHA, then ask
for definitions, references, or implementations from a source position.
Results may cross repositories, but only include repositories visible to the
caller. Administrators can declare package metadata manually and can request an
optional refresh from GitHub's dependency-graph SBOM endpoint. GitHub metadata
enriches package ownership; it never creates semantic edges that SCIP did not
emit.

Success means an upload is atomically replaceable, stale-SHA uploads cannot be
queried, a symbol can resolve across two authorized repositories, unauthorized
locations never leak, and unavailable GitHub dependency data leaves navigation
usable.

## Chosen approach

Accept pre-generated SCIP artifacts rather than running language toolchains in
GraphNest. This follows Sourcegraph's upload boundary and avoids coupling the Go
indexer image to every supported compiler and build system. Use the official
SCIP Go protobuf binding and PostgreSQL; do not add another graph database or
background service.

The alternatives were Sourcebot-style Ctags plus text heuristics, which cannot
provide the requested precise graph, and managed language indexers, which add
runtime images, build credentials, scheduling, and language-specific failure
modes before artifact ingestion is proven.

## Storage and ingestion

Migration `003` adds:

- `scip_uploads`: repository, commit, project root, indexer identity, and time;
- `scip_occurrences`: document path, range, symbol, and SCIP symbol roles;
- `scip_relationships`: source symbol, target symbol, and SCIP relationship
  flags;
- `repository_packages`: normalized package URL plus derived SCIP package
  manager/name/version, relationship (`provides` or `depends_on`), and source
  (`manual` or `github`).

The upload endpoint accepts `application/vnd.scip+protobuf`, bounded by a new
`GRAPHNEST_SCIP_MAX_UPLOAD_BYTES` setting. It requires an administrator bearer
token whose repository scope includes the target repository. The service
rejects malformed protobuf, invalid paths/ranges, duplicate documents, and
commits other than the
repository's current `indexed_sha`. It parses before opening a transaction,
then replaces the repository's previous SCIP rows and inserts the new upload
atomically. Local SCIP symbols are stored but scoped to their upload; only
global symbols can match across repositories. Occurrence indexes cover
`(upload_id, path)` for position lookup and `symbol` for graph traversal.

## Navigation API and authorization

`POST /v1/scip/navigation` accepts repository ID, path, one-based line,
zero-based character, and operation (`definitions`, `references`, or
`implementations`). It first authorizes the origin repository, finds the
smallest occurrence containing the position, then resolves matching occurrences
or relationship targets across only the caller's authorized repositories at
their current indexed SHAs. Responses contain repository ID/name, indexed SHA,
path, range, symbol, and role, with the existing result and response-byte caps.

The MCP server exposes one `navigate_symbol` tool with the same operation field
instead of three duplicate tools. REST and MCP share the service, authorization,
and limits.

## Dependency metadata

`PUT /v1/scip/dependencies` lets an administrator replace manual `provides` and
`depends_on` package URLs for one repository. Package URLs are parsed and
normalized with a small in-repository parser supporting the purl fields needed
for identity matching; no general package-management framework is added.

`POST /v1/scip/dependencies/github` requests the installation-authenticated
GitHub endpoint `GET /repos/{owner}/{repo}/dependency-graph/sbom`. The bounded
SPDX response is reduced to package URLs and `DEPENDS_ON` relationships and
atomically replaces only `source='github'` rows. A missing endpoint or missing
permission returns an explicit `available: false` response without deleting
manual metadata. Manual `provides` rows take precedence when the same package
has conflicting owners.

Dependency metadata maps packages to indexed repositories and helps select an
external symbol's provider when exact versions are unavailable. Exact SCIP
package identity remains the first choice; version-relaxed fallback is allowed
only along a declared dependency and is reported as approximate.

## Failure handling and verification

All write endpoints require administrator status and repository scope. Uploads,
manual metadata, and GitHub refreshes are transactional. Input and GitHub
responses are size-bounded; errors contain no tokens or artifact content.
Deleting a repository cascades to its graph rows; disabling one excludes it
through the existing authorization queries.

Focused tests cover protobuf validation, atomic replacement, exact and
dependency-assisted cross-repository resolution, repository authorization,
GitHub SBOM availability and parsing, REST limits, and the MCP adapter. The full
gate remains `make test-race`, with the PostgreSQL integration suite required
for the new persistence behavior.
