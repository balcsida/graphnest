# Architecture Decision Records

Accepted decisions are append-only. A newer record supersedes an older record
when the architecture changes.

The deployment implements ADR-0013 and ADR-0014 directly: archive workspaces
are ephemeral, PostgreSQL serves graph queries, and the superseded LadybugDB
topology is not rendered.

| ADR | Decision | Status |
| --- | --- | --- |
| [0001](0001-go.md) | Go | Accepted |
| [0002](0002-zoekt.md) | Zoekt | Accepted |
| [0003](0003-zoekt-json.md) | Zoekt JSON API | Accepted |
| [0004](0004-postgresql.md) | PostgreSQL durable state | Accepted |
| [0005](0005-github-app.md) | GitHub App access | Accepted |
| [0006](0006-openshift-uid.md) | OpenShift arbitrary UID | Accepted |
| [0007](0007-single-zoekt-node.md) | Single Zoekt node | Accepted |
| [0008](0008-shared-service-layer.md) | Shared service layer | Accepted |
| [0009](0009-indexed-sha-reads.md) | Exact indexed-SHA reads | Accepted |
| [0010](0010-no-jvm.md) | No JVM runtime | Accepted |
| [0011](0011-mcp-go-sdk.md) | MCP Go SDK | Accepted |
| [0012](0012-derived-ladybug-graph.md) | Derived LadybugDB graph | Superseded by 0014 |
| [0013](0013-ephemeral-exact-sha-archives.md) | Ephemeral exact-SHA archives | Accepted |
| [0014](0014-postgresql-graph-queries.md) | PostgreSQL graph queries | Accepted |
| [0015](0015-isolate-optional-enrichment.md) | Optional enrichment boundaries | Accepted |
| [0016](0016-mcp-oauth-authorization-server.md) | MCP OAuth authorization server | Accepted |
