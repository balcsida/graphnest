// Renders the embedded web console in a real Chromium against a deterministic
// stub of the REST API and saves PNGs: 1440x900 in both themes and 390x844 in
// light. Run after `make ui`:
//   node test/smoke/console-screenshots.mjs <out-dir>
// The stubs follow docs/openapi.yaml; this is a rendering check of the real
// build, not a verified GitHub integration. The script exits non-zero when a
// page logs a console error or a /v1 request returns 404 (a missing stub).
import { chromium } from "@playwright/test";
import crypto from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import path from "node:path";

const outDir = process.argv[2];
if (!outDir) {
  console.error("usage: node test/smoke/console-screenshots.mjs <out-dir>");
  process.exit(2);
}
const distDir = path.resolve("internal/webui/dist");
if (!fs.existsSync(path.join(distDir, "index.html"))) {
  console.error("internal/webui/dist/index.html is missing; run make ui first");
  process.exit(2);
}
const spdx = JSON.parse(fs.readFileSync(path.resolve("test/fixtures/supplychain/ghes-spdx-2.3.json"), "utf8"));

const STREAM = "github:source";
const collected = "2026-09-21T10:00:00Z";
const earlier = "2026-09-14T10:00:00Z";
const assessedAt = "2026-09-21T10:05:00Z";
const sha = (seed) => crypto.createHash("sha1").update(seed).digest("hex");
const sha256 = (seed) => crypto.createHash("sha256").update(seed).digest("hex");

// ---- repositories -------------------------------------------------------
const repositoryNames = ["acme/storefront-web", "acme/orders-service", "acme/billing-api", "acme/inventory-sync", "acme/platform-libs", "acme/data-pipeline"];
const repositoryStatuses = ["ready", "ready", "ready", "pending", "ready", "failed"];
const repositories = repositoryNames.map((name, index) => ({
  id: 101 + index,
  github_id: 101 + index,
  name,
  branch: index === 4 ? "trunk" : "main",
  desired_sha: sha(`${name}:desired`),
  indexed_sha: sha(`${name}:indexed`),
  web_url: `https://github.example.com/${name}`,
  status: repositoryStatuses[index],
  error_code: repositoryStatuses[index] === "failed" ? "index_failed" : "",
  search_node: `zoekt-${index % 2}`,
  last_indexed_at: `2026-09-2${index}T0${index + 1}:30:00Z`,
  scip_status: ["current", "stale", "current", "absent", "unknown", "absent"][index],
  scip_commit: index % 2 === 0 ? sha(`${name}:indexed`) : undefined,
}));
const byId = (id) => repositories.find((repository) => repository.id === Number(id));

// ---- search, file and navigation ----------------------------------------
const snippets = [
  [0, "src/api/client.ts", 12, ["export function createClient(baseUrl: string) {", "  const http = new HttpClient({ baseUrl, timeout: 5000 });", "  return { get: http.get, post: http.post };"]],
  [0, "src/api/client.ts", 48, ["export class HttpClient {", "  constructor(private readonly options: HttpClientOptions) {}", "  async get<T>(path: string): Promise<T> {"]],
  [0, "src/hooks/useOrders.ts", 7, ["const http = createClient(config.apiUrl);", "export const useOrders = () => useQuery(['orders'], () => http.get('/orders'));"]],
  [1, "internal/client/http.go", 21, ["func NewHTTPClient(timeout time.Duration) *http.Client {", "\treturn &http.Client{Timeout: timeout}", "}"]],
  [1, "internal/billing/gateway.go", 64, ["client := NewHTTPClient(cfg.Timeout)", "resp, err := client.Post(cfg.URL, \"application/json\", body)"]],
  [1, "internal/billing/gateway_test.go", 33, ["func TestGatewayRetriesHTTPClientErrors(t *testing.T) {", "\tclient := NewHTTPClient(time.Second)"]],
  [2, "src/Inventory.Sync/HttpClientFactory.cs", 18, ["public static HttpClient Create(Uri baseAddress) =>", "    new HttpClient { BaseAddress = baseAddress, Timeout = TimeSpan.FromSeconds(5) };"]],
  [2, "src/Inventory.Sync/StockPuller.cs", 41, ["using var http = HttpClientFactory.Create(options.Endpoint);", "var stock = await http.GetFromJsonAsync<Stock[]>(\"stock\");"]],
  [2, "README.md", 9, ["The puller reuses one HTTP client per endpoint and retries 5xx responses."]],
];
const searchMatches = snippets.map(([repo, file, line, preview], index) => ({
  repository: { id: repositories[repo].id, name: repositories[repo].name, branch: repositories[repo].branch, indexed_sha: repositories[repo].indexed_sha, web_url: repositories[repo].web_url },
  path: file,
  sha: repositories[repo].indexed_sha,
  line_number: line,
  line_start: line,
  line_end: line + preview.length - 1,
  preview: preview.join("\n"),
  score: Number((9.2 - index * 0.6).toFixed(2)),
}));
const fileLines = `package client

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Config holds the settings shared by every outbound HTTP client.
type Config struct {
	BaseURL string
	Timeout time.Duration
	Retries int
}

// Client wraps http.Client with retries for transient failures.
type Client struct {
	config Config
	http   *http.Client
}

// NewHTTPClient builds a Client from the configuration.
func NewHTTPClient(config Config) *Client {
	return &Client{config: config, http: &http.Client{Timeout: config.Timeout}}
}

// Get fetches path relative to BaseURL and retries 5xx responses.
func (c *Client) Get(ctx context.Context, path string) (*http.Response, error) {
	var last error
	for attempt := 0; attempt <= c.config.Retries; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.BaseURL+path, nil)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		response, err := c.http.Do(request)
		if err == nil && response.StatusCode < 500 {
			return response, nil
		}
		last = err
	}
	return nil, last
}`.split("\n").slice(0, 40).join("\n");
const navigationLocations = [
  { repository_id: 102, name: "acme/orders-service", path: "internal/client/http.go", symbol: "scip-go gomod github.com/acme/orders-service v1.4.0 internal/client/`NewHTTPClient().", start: 21, approximate: false },
  { repository_id: 102, name: "acme/orders-service", path: "internal/client/http_test.go", symbol: "scip-go gomod github.com/acme/orders-service v1.4.0 internal/client/`NewHTTPClient().", start: 12, approximate: true },
  { repository_id: 104, name: "acme/platform-libs", path: "httpx/client.go", symbol: "scip-go gomod github.com/acme/platform-libs v0.9.2 httpx/`NewHTTPClient().", start: 37, approximate: false },
].map((item) => ({
  repository_id: item.repository_id, repository_name: item.name, branch: byId(item.repository_id).branch, web_url: byId(item.repository_id).web_url,
  commit: byId(item.repository_id).indexed_sha, path: item.path, symbol: item.symbol, start_line: item.start, start_character: 5, end_line: item.start, end_character: 18,
  position_encoding: "UTF16CodeUnitOffsetFromLineStart", roles: 1, approximate: item.approximate,
}));

// ---- supply chain -------------------------------------------------------
// eco|namespace|name|license|status|version:repoIndex,repoIndex;version:... (repo index into `repositories`).
const catalog = `
npm||react|MIT|resolved|18.3.1:0,1;19.1.0:2
npm||react-dom|MIT|resolved|18.3.1:0,1;19.1.0:2
npm||lodash|MIT|resolved|4.17.21:0,1,2,4
npm||axios|MIT|resolved|1.7.9:0,1,3
npm||express|MIT|resolved|4.21.2:1,3
npm||typescript|Apache-2.0|resolved|5.6.3:0,1,2,4
npm||zod|MIT|declared|3.24.1:0,2
npm||date-fns|MIT|resolved|3.6.0:0,2
npm||uuid|MIT|resolved|11.0.3:1,3
npm||chalk|MIT|resolved|5.3.0:2,4
npm||commander|MIT|resolved|12.1.0:2,4
npm||debug|MIT|resolved|4.3.7:0,1,2,3,4
npm||semver|ISC|resolved|7.6.3:0,1,3,4
npm||minimist||conflict|1.2.8:1,3
npm|@types|node|MIT|resolved|22.10.2:0,1,2
npm|@babel|core|MIT|resolved|7.26.0:0,2
npm||eslint|MIT|resolved|9.17.0:0,2,4
npm||jest|MIT|resolved|29.7.0:1,3
npm||webpack|MIT|declared|5.97.1:0,4
npm||tailwindcss|MIT|resolved|4.0.0:0
npm||left-pad||unlicensed|1.3.0:3
npm||moment||conflict|2.30.1:0,2
npm||colors||unlicensed|1.4.0:4
maven|org.springframework|spring-core|Apache-2.0|resolved|6.1.14:1,3
maven|org.springframework.boot|spring-boot-starter-web|Apache-2.0|resolved|3.3.5:1,3
maven|com.fasterxml.jackson.core|jackson-databind||conflict|2.17.2:1,3
maven|org.slf4j|slf4j-api|MIT|resolved|2.0.16:1,3,4
maven|ch.qos.logback|logback-classic|LGPL-2.1-only|resolved|1.5.12:1,3
maven|org.apache.commons|commons-lang3|Apache-2.0|resolved|3.17.0:1,3,4
maven|com.google.guava|guava|Apache-2.0|resolved|33.3.1:1,3
maven|org.postgresql|postgresql|BSD-3-Clause|resolved|42.7.4:1,3
maven|org.hibernate.orm|hibernate-core|LGPL-2.1-only|declared|6.5.3:1
maven|com.mysql|mysql-connector-j|GPL-3.0-only|declared|9.1.0:3
maven|org.mozilla|rhino|MPL-2.0|resolved|1.7.15:3
maven|io.netty|netty-handler|Apache-2.0|resolved|4.1.114:1,3
maven|org.junit.jupiter|junit-jupiter|NOASSERTION|unknown|5.11.3:1,3
maven|org.bouncycastle|bcprov-jdk18on|MIT|resolved|1.79:1
golang|github.com/spf13|cobra|Apache-2.0|resolved|v1.8.1:2,4
golang|github.com/gin-gonic|gin|MIT|resolved|v1.10.0:2,4
golang|github.com/stretchr|testify|MIT|resolved|v1.9.0:1,2,4
golang|golang.org/x|net|BSD-3-Clause|resolved|v0.31.0:2,3,4
golang|google.golang.org|grpc|Apache-2.0|resolved|v1.68.0:2,4
golang|go.uber.org|zap|MIT|resolved|v1.27.0:2,4
golang|github.com/gorilla|mux|BSD-3-Clause|resolved|v1.8.1:2
golang|github.com/hashicorp|vault-api|MPL-2.0|resolved|v1.15.0:2,4
golang|github.com/hashicorp|consul-api|MPL-2.0|declared|v1.30.0:4
golang|github.com/sirupsen|logrus|MIT|resolved|v1.9.3:2
golang|github.com/jackc|pgx|MIT|resolved|v5.7.1:2,4
golang|github.com/go-sql-driver|mysql|MPL-2.0|resolved|v1.8.1:2
nuget||Newtonsoft.Json|MIT|resolved|13.0.3:1,3,4
nuget||Serilog|Apache-2.0|resolved|4.1.0:3,4
nuget||Dapper|Apache-2.0|declared|2.1.35:3
nuget||AutoMapper|MIT|resolved|13.0.1:3
nuget||Polly|BSD-3-Clause|resolved|8.5.0:3,4
nuget||xunit|Apache-2.0|resolved|2.9.2:3,4
nuget||Moq|BSD-3-Clause|resolved|4.20.72:3
nuget||Npgsql||unlicensed|9.0.1:3
nuget||StackExchange.Redis|MIT|resolved|2.8.16:3,4
nuget||Humanizer|MIT|resolved|2.14.1:3
`;
const inventoryRepositories = new Set([101, 102, 103, 104, 105]); // data-pipeline has never been collected
const encodeSegment = (value) => encodeURIComponent(value).replace(/%2F/g, "/");
const coordinates = catalog.trim().split("\n").flatMap((line) => {
  const [ecosystem, namespace, name, license, status, versions] = line.split("|");
  return versions.split(";").map((entry) => {
    const [version, repos] = entry.split(":");
    const purlNamespace = namespace ? (ecosystem === "npm" ? `${encodeURIComponent(namespace)}/` : `${namespace}/`) : "";
    const repoIds = repos.split(",").map((index) => repositories[Number(index)].id);
    return {
      key: Buffer.from([ecosystem, namespace, name, version].join("\u0001")).toString("hex"),
      ecosystem, namespace, name, version, status, expression: license,
      purl: `pkg:${ecosystem}/${purlNamespace}${encodeSegment(name)}@${version}`,
      repoIds,
    };
  });
}).sort((a, b) => b.repoIds.length - a.repoIds.length || a.name.localeCompare(b.name) || a.version.localeCompare(b.version));

const facetOf = (items) => [...items.reduce((map, value) => map.set(value, (map.get(value) ?? 0) + 1), new Map())]
  .map(([value, count]) => ({ value, count })).sort((a, b) => b.count - a.count || a.value.localeCompare(b.value));
const licenseOf = (coordinate) => coordinate.expression || null;
/** One element per (coordinate, repository) occurrence inside the requested scope. */
function occurrences(scope) {
  const ids = scope?.length ? scope : null;
  return coordinates.flatMap((coordinate) => coordinate.repoIds.filter((id) => !ids || ids.includes(id)).map((id) => ({ coordinate, id })));
}
function facets(scope) {
  const items = occurrences(scope);
  return {
    stream: STREAM,
    ecosystems: facetOf(items.map(({ coordinate }) => coordinate.ecosystem)),
    assessments: facetOf(items.map(({ coordinate }) => coordinate.status)),
    licenses: facetOf(items.flatMap(({ coordinate }) => (licenseOf(coordinate) ? [licenseOf(coordinate)] : []))),
  };
}
function overview(scope) {
  const ids = scope?.length ? scope : repositories.map((repository) => repository.id);
  const items = occurrences(ids);
  const unique = new Set(items.map(({ coordinate }) => coordinate.key));
  const coordinatesInScope = coordinates.filter((coordinate) => unique.has(coordinate.key));
  const assessments = {};
  for (const { status } of coordinatesInScope) assessments[status] = (assessments[status] ?? 0) + 1;
  const withInventory = ids.filter((id) => inventoryRepositories.has(id)).length;
  return {
    stream: STREAM, generated_at: collected,
    repositories: {
      authorized: ids.length, with_inventory: withInventory, never_collected: ids.length - withInventory,
      stale: ids.includes(104) ? 1 : 0, failed_last_attempt: ids.includes(105) ? 1 : 0, opted_out: 0,
    },
    components: {
      occurrences: items.length, unique_coordinates: unique.size, without_purl: 0, without_version: 0,
      unassessed: 0, assessments,
    },
    warning_total: ids.filter((id) => inventoryRepositories.has(id)).length * 2,
    oldest_collected_at: "2026-09-01T08:00:00Z", newest_collected_at: collected,
    ecosystems: facetOf(items.map(({ coordinate }) => coordinate.ecosystem)),
    denominators: [
      "Authorized repositories: repositories this token can read.",
      "With inventory: authorized repositories with at least one collected snapshot.",
      "Occurrences: component rows across the latest snapshot of each repository.",
      "Unique coordinates: distinct ecosystem, namespace, name and version tuples.",
      "Assessments describe collected evidence; they are not a compliance verdict.",
    ],
  };
}
function portfolioComponent(coordinate, scope) {
  const ids = coordinate.repoIds.filter((id) => !scope || scope.includes(id));
  return {
    key: coordinate.key, ecosystem: coordinate.ecosystem, ...(coordinate.namespace ? { namespace: coordinate.namespace } : {}), name: coordinate.name,
    version: coordinate.version, purl: coordinate.purl, repository_count: ids.length, occurrence_count: ids.length,
    assessment: coordinate.status, assessment_statuses: [coordinate.status],
    ...(coordinate.status === "resolved" || coordinate.status === "declared" ? { expression: coordinate.expression } : {}),
    declared_raw: [coordinate.expression || "NOASSERTION"],
    newest_collected_at: collected, oldest_collected_at: "2026-09-01T08:00:00Z",
    repositories: ids.map((id) => ({ id, name: byId(id).name })),
  };
}
const PAGE_SIZE = 30;
function componentList(query) {
  const scope = query.get("repository_id") ? [Number(query.get("repository_id"))] : null;
  const text = (query.get("q") ?? "").toLowerCase();
  const matching = coordinates.filter((coordinate) =>
    (!scope || coordinate.repoIds.some((id) => scope.includes(id))) &&
    (!query.get("ecosystem") || coordinate.ecosystem === query.get("ecosystem")) &&
    (!query.get("assessment") || coordinate.status === query.get("assessment")) &&
    (!query.get("license") || (licenseOf(coordinate) ?? "NOASSERTION") === query.get("license") || coordinate.expression === query.get("license")) &&
    (!text || coordinate.name.toLowerCase().includes(text) || coordinate.purl.toLowerCase().includes(text)));
  const second = query.get("cursor") === "page-2";
  const page = second ? matching.slice(PAGE_SIZE) : matching.slice(0, PAGE_SIZE);
  return {
    stream: STREAM, repositories_in_scope: scope ? 1 : inventoryRepositories.size, truncated: false,
    components: page.map((coordinate) => portfolioComponent(coordinate, scope)),
    ...(!second && matching.length > PAGE_SIZE ? { next_cursor: "page-2" } : {}),
  };
}

const snapshotId = (repositoryId, newest = true) => (repositoryId - 100) * 10 + (newest ? 2 : 1);
function snapshot(repositoryId, newest = true) {
  const count = occurrences([repositoryId]).length;
  const hash = sha256(`${repositoryId}:${newest}`);
  return {
    id: snapshotId(repositoryId, newest), repository_id: repositoryId, stream: STREAM, producer: "github", subject: "source",
    collected_at: newest ? collected : earlier, created_at_claimed: spdx.creationInfo.created, producer_tool: "GitHub.com-Dependency-Graph",
    document_name: `com.github.${byId(repositoryId).name}`, document_namespace: `${spdx.documentNamespace}-${repositoryId}`,
    spdx_version: spdx.spdxVersion, data_license: spdx.dataLicense, subject_assurance: "unknown",
    root_element_ids: [`SPDXRef-root-${repositoryId}`], parser_version: 1, component_count: newest ? count : count - 2, edge_count: count + 3, warning_count: 2,
    warnings: [
      { code: "package_purl_missing", element: "SPDXRef-vendored-legacy", detail: "package has no purl external reference" },
      { code: "package_version_missing", element: "SPDXRef-vendored-legacy", detail: "package has no versionInfo" },
    ],
    published_at: newest ? collected : earlier, document_sha256: hash, document_format: "spdx-2.3-json", document_bytes: 4650 + count * 180,
  };
}
const repositoryComponents = (repositoryId) => occurrences([repositoryId]).map(({ coordinate }, ordinal) => ({
  element_id: `SPDXRef-${coordinate.ecosystem}-${ordinal}`, ordinal, name: coordinate.name, version: coordinate.version, purl: coordinate.purl,
  ecosystem: coordinate.ecosystem, ...(coordinate.namespace ? { namespace: coordinate.namespace } : {}), package_name: coordinate.name,
  license_declared_raw: coordinate.expression || "NOASSERTION", license_concluded_raw: "NOASSERTION", is_root: false, scope: ordinal % 3 ? "direct" : "transitive",
  license: {
    status: coordinate.status, ...(coordinate.expression && coordinate.status !== "unknown" ? { expression: coordinate.expression } : {}),
    ...(coordinate.status === "conflict" ? { conflict_detail: "producer_declared: MIT | registry: Apache-2.0" } : {}),
    evidence_count: 1, assessed_at: assessedAt, evidence_fingerprint: sha256(coordinate.key),
  },
}));
const evidenceFor = (coordinate) => ({
  id: 20, source: "registry_" + (["npm", "maven", "nuget"].includes(coordinate.ecosystem) ? coordinate.ecosystem : "npm"), route: `${coordinate.ecosystem}:registry.example.com`,
  ecosystem: coordinate.ecosystem, ...(coordinate.namespace ? { namespace: coordinate.namespace } : {}), name: coordinate.name, version: coordinate.version,
  raw_value: coordinate.expression || "", raw_kind: coordinate.expression ? "expression" : "missing", parse_status: coordinate.expression ? "parsed" : "not_applicable",
  ...(coordinate.expression ? { expression: coordinate.expression } : {}), resolver_version: 1, license_list_version: "3.27.0", fetched_at: assessedAt,
  outcome: coordinate.expression ? "resolved" : "no_license_metadata",
});
function repositoryComponentDetail(repositoryId, element) {
  const component = repositoryComponents(repositoryId).find((item) => item.element_id === element);
  if (!component) return null;
  const coordinate = coordinates.find((item) => item.purl === component.purl);
  return {
    component, snapshot: snapshot(repositoryId),
    declarations: [{ ...evidenceFor(coordinate), id: 0, source: "producer_declared", route: undefined, raw_value: component.license_declared_raw, fetched_at: collected }],
    evidence: [evidenceFor(coordinate)],
    relationships: [{ from: `SPDXRef-root-${repositoryId}`, type: "DEPENDS_ON", to: component.element_id, resolved: true }],
    notes: ["Publisher declarations and registry metadata are evidence, not approval; a human conclusion or policy decision is recorded separately."],
    truncated: false,
  };
}
const collectionRecord = (repositoryId) => ({
  id: repositoryId * 4, job_id: repositoryId * 2, producer: "github", stream: STREAM, started_at: "2026-09-22T11:59:58Z", finished_at: "2026-09-22T12:00:00Z",
  outcome: repositoryId === 105 ? "forbidden" : "published", http_status: repositoryId === 105 ? 403 : 200, snapshot_id: repositoryId === 105 ? null : snapshotId(repositoryId),
  ...(repositoryId === 105 ? { error_code: "github_forbidden", message: "GitHub returned 403 for the SBOM export: the dependency graph may be disabled or the installation may lack Contents read access." } : {}),
});
function repositoryStatus(repositoryId) {
  const never = !inventoryRepositories.has(repositoryId);
  const latest = never ? null : snapshot(repositoryId);
  const summary = {};
  for (const { coordinate } of occurrences([repositoryId])) summary[coordinate.status] = (summary[coordinate.status] ?? 0) + 1;
  return {
    repository_id: repositoryId, repository: byId(repositoryId).name, stream: STREAM, producer: "github", subject: "source",
    collection: never ? "never" : repositoryId === 104 ? "stale" : repositoryId === 105 ? "failed" : "current",
    freshness_seconds: never ? null : repositoryId === 104 ? 604800 : 93600, latest_snapshot: latest,
    last_collection: never ? null : collectionRecord(repositoryId), active_job: null,
    enrichment: "configured", enrichment_ecosystems: ["npm", "nuget", "maven"], license_summary: never ? {} : summary, opt_out: false,
    notes: never ? ["No inventory has been collected for this repository yet."] : ["GitHub dependency-graph exports are timestamped observations of the default branch; they are not bound to a commit."],
    documents: latest ? [{ snapshot_id: latest.id, sha256: latest.document_sha256, format: "spdx-2.3-json", bytes: latest.document_bytes, path: `/v1/supply-chain/snapshots/${latest.id}/document` }] : [],
    streams: [{ key: STREAM, producer: "github", subject: "source", has_inventory: !never, last_outcome: never ? undefined : collectionRecord(repositoryId).outcome }],
  };
}
function comparison(repositoryId) {
  return {
    repository_id: repositoryId, base: snapshot(repositoryId, false), head: snapshot(repositoryId),
    added_components: ["npm:react-dom@18.3.1", "npm:zod@3.24.1"], removed_components: ["npm:moment@2.29.4"],
    license_changes: [{ component: "npm:colors@1.4.0", from: "MIT", to: "NOASSERTION" }],
    edges_added: 4, edges_removed: 1, metadata_changes: ["producer tool version changed"],
    notes: ["Comparison is by coordinate, not by SPDXID."],
  };
}

// ---- admin and account --------------------------------------------------
const jobStates = ["succeeded", "running", "queued", "failed", "succeeded", "failed", "succeeded", "queued"];
const adminRepositories = repositories.map((repository, index) => ({
  id: repository.id, github_id: repository.github_id, installation_id: 7001, name: repository.name, default_branch: repository.branch, desired_sha: repository.desired_sha,
  indexed_sha: repository.indexed_sha, status: repository.status, error_code: repository.error_code, web_url: repository.web_url, enabled: true,
  private: index % 2 === 1, archived: index === 5, last_indexed_at: repository.last_indexed_at,
}));
const adminJobs = jobStates.map((state, index) => ({
  id: 900 - index, repository_id: repositories[index % 6].id, repository: repositories[index % 6].name, target_sha: repositories[index % 6].desired_sha, target_ref: "refs/heads/main",
  reason: index % 3 === 0 ? "webhook" : index % 3 === 1 ? "reconcile" : "manual", state, error_code: state === "failed" ? "index_failed" : "", attempt: state === "failed" ? 3 : 1, max_attempts: 5,
  priority: 0, run_after: `2026-09-22T1${index}:00:00Z`, created_at: `2026-09-22T1${index}:00:00Z`, updated_at: `2026-09-22T1${index}:05:00Z`,
}));
const deliveryStates = ["processed", "processed", "queued", "failed", "processed", "ignored"];
const deliveries = deliveryStates.map((state, index) => ({
  id: 500 - index, delivery_id: sha(`delivery:${index}`).replace(/^(.{8})(.{4})(.{4})(.{4})(.{12}).*/, "$1-$2-$3-$4-$5"), event: index % 2 ? "installation_repositories" : "push", state,
  error_code: state === "failed" ? "signature_mismatch" : "", installation_id: 7001, received_at: `2026-09-22T0${index}:10:00Z`, ...(state === "queued" ? {} : { processed_at: `2026-09-22T0${index}:10:02Z` }),
}));
const adminOverview = {
  repositories: { ready: 4, pending: 1, failed: 1 }, jobs: { queued: 2, running: 1, succeeded: 3, failed: 2 }, deliveries: { processed: 3, queued: 1, failed: 1, ignored: 1 },
  scip_uploads: 3, dependencies: 6, installations: 2,
};
const scipUploads = [102, 101, 103].map((repositoryId, index) => ({
  id: 30 - index, repository_id: repositoryId, repository: byId(repositoryId).name, commit: byId(repositoryId).indexed_sha, project_root: index === 1 ? "web" : ".",
  indexer_name: index === 1 ? "scip-typescript" : "scip-go", indexer_version: index === 1 ? "0.3.15" : "0.1.26", uploaded_at: `2026-09-2${index}T09:00:00Z`,
}));
const scipDependencies = [
  [102, "github", "depends_on", "pkg:golang/github.com/spf13/cobra@v1.8.1", "golang", "github.com/spf13/cobra", "v1.8.1"],
  [102, "github", "depends_on", "pkg:golang/golang.org/x/net@v0.31.0", "golang", "golang.org/x/net", "v0.31.0"],
  [104, "manual", "provides", "pkg:golang/github.com/acme/platform-libs@v0.9.2", "golang", "github.com/acme/platform-libs", "v0.9.2"],
  [101, "github", "depends_on", "pkg:npm/react@18.3.1", "npm", "react", "18.3.1"],
  [103, "github", "depends_on", "pkg:nuget/Serilog@4.1.0", "nuget", "Serilog", "4.1.0"],
  [101, "manual", "provides", "pkg:npm/%40acme/storefront-web@2.4.0", "npm", "@acme/storefront-web", "2.4.0"],
].map(([repositoryId, source, relation, purl, manager, name, version]) => ({ repository_id: repositoryId, repository: byId(repositoryId).name, source, relation, purl, manager, name, version }));
const adminUsers = [
  { id: 1, external_id: "u-1001", user_name: "mira.chen", display_name: "Mira Chen", source: "scim", scim_active: true, suspended: false, administrator: true, repository_ids: [101, 102, 103, 104, 105], direct_administrator: false, direct_repository_ids: [], github_repository_ids: [101, 102] },
  { id: 2, external_id: "u-1002", user_name: "tomas.novak", display_name: "Tomas Novak", source: "scim", scim_active: true, suspended: false, administrator: false, repository_ids: [101, 102], direct_administrator: false, direct_repository_ids: [101, 102], github_repository_ids: [] },
  { id: 3, external_id: "u-1003", user_name: "ayesha.khan", display_name: "Ayesha Khan", source: "github", scim_active: true, suspended: true, administrator: false, repository_ids: [103], direct_administrator: false, direct_repository_ids: [], github_repository_ids: [103] },
  { id: 4, external_id: "break-glass", user_name: "recovery", display_name: "Recovery administrator", source: "local", scim_active: true, suspended: false, administrator: true, repository_ids: [], direct_administrator: true, direct_repository_ids: [], github_repository_ids: [] },
];
const adminGroups = [
  { id: 1, external_id: "g-platform", display_name: "Platform engineering", administrator: true, repository_ids: [104, 105], member_count: 6 },
  { id: 2, external_id: "g-commerce", display_name: "Commerce", administrator: false, repository_ids: [101, 102, 103], member_count: 14 },
];
const auditEvents = [
  ["user", "mira.chen", "session", "1", "oidc", "auth.login", "success"],
  ["scim", "scim-bot", "user", "u-1003", "scim_token", "scim.user.suspend", "success"],
  ["user", "tomas.novak", "api_token", "12", "oidc", "api_token.create", "success"],
  ["anonymous", "", "authentication", "-", "", "auth.login", "denied"],
  ["operator", "recovery", "authentication", "local", "local", "auth.local.login", "success"],
].map(([actorType, actorId, targetType, targetId, method, operation, outcome], index) => ({
  actor_type: actorType, actor_id: actorId, target_type: targetType, target_id: targetId, authentication_method: method, operation, outcome,
  request_id: sha(`audit:${index}`).slice(0, 16), created_at: `2026-09-22T0${index + 1}:00:00Z`,
}));
const github = {
  app_id: 41873, web_url: "https://github.example.com", api_url: "https://github.example.com/api/v3", upload_url: "https://github.example.com/api/uploads", git_url: "https://github.example.com",
  api_version: "2022-11-28", private_key_configured: true, webhook_secret_configured: true, ca_configured: false,
  installations: [
    { github_id: 7001, account_login: "acme", account_type: "Organization", status: "active" },
    { github_id: 7002, account_login: "acme-labs", account_type: "Organization", status: "suspended", suspended_at: "2026-09-10T08:00:00Z" },
  ],
  truncated: false,
};
const apiTokens = [
  { id: 12, prefix: "gnu_4f8a2c1e", repository_ids: [101, 102], created_at: "2026-09-01T09:00:00Z", last_used_at: "2026-09-21T16:20:00Z", expires_at: "2026-12-01T00:00:00Z" },
  { id: 13, prefix: "gnu_91be07d3", repository_ids: [103], created_at: "2026-09-10T11:30:00Z", expires_at: "2026-10-10T00:00:00Z" },
  { id: 14, prefix: "gnd_e5c37a90", delegation_only: true, created_at: "2026-09-15T08:00:00Z", last_used_at: "2026-09-22T06:00:00Z" },
];
const oauthGrants = [
  { id: 3, client_name: "Claude Code", scope: "search", created_at: "2026-09-05T10:00:00Z", last_used_at: "2026-09-22T09:12:00Z", expires_at: "2026-12-05T10:00:00Z" },
  { id: 4, client_name: "Cursor", scope: "search", created_at: "2026-09-12T14:00:00Z", last_used_at: "2026-09-20T17:45:00Z", expires_at: "2026-12-12T14:00:00Z" },
];

// ---- stub server --------------------------------------------------------
const apiError = (status, code, message) => [status, { error: { code, message, request_id: "stub", retryable: false } }];
const repositoryPath = /^\/v1\/supply-chain\/repositories\/(\d+)(?:\/(components|component|collections|snapshots))?$/;

/** Returns [status, body] for an API path, or null when the path is not stubbed. */
function api(method, url, bearer, requestBody) {
  const p = url.pathname;
  const q = url.searchParams;
  if (p === "/v1/auth/config") return [200, { token_login: true, break_glass: false, file_reads: true, providers: [] }];
  if (p === "/v1/auth/session") return bearer ? [200, { method: "bearer" }] : apiError(401, "unauthenticated", "authentication required");
  if (!bearer) return apiError(401, "unauthenticated", "authentication required");
  if (p === "/v1/repositories") return [200, { repositories, truncated: false }];
  if (p === "/v1/search") return [200, { matches: searchMatches, truncated: false, consistency: { backend: "github", exact: true, partial: false } }];
  if (p === "/v1/files/read") {
    const request = JSON.parse(requestBody || "{}");
    const repository = byId(request.repository_id) ?? repositories[0];
    return [200, { repository_id: repository.id, path: request.path, indexed_sha: repository.indexed_sha, blob_sha: sha(`${repository.id}:${request.path}`), content: fileLines, start_line: 1, end_line: 40, truncated: false }];
  }
  if (p === "/v1/scip/navigation") return [200, { locations: navigationLocations, truncated: false }];
  if (p === "/v1/scip/uploads" || p === "/v1/scip/dependencies/github") return [200, { available: true, packages: 3 }];
  if (p === "/v1/admin/overview") return [200, adminOverview];
  if (p === "/v1/admin/repositories") return [200, { repositories: adminRepositories, truncated: false }];
  if (p === "/v1/admin/jobs") return [200, { jobs: adminJobs, truncated: false }];
  if (p === "/v1/admin/webhook-deliveries") return [200, { deliveries, truncated: false }];
  if (p === "/v1/admin/github") return [200, github];
  if (p === "/v1/admin/scip/uploads") return [200, { uploads: scipUploads, truncated: false }];
  if (p === "/v1/admin/scip/dependencies") return [200, { dependencies: scipDependencies, truncated: false }];
  if (p === "/v1/admin/users") return [200, { users: adminUsers, truncated: false }];
  if (p === "/v1/admin/groups") return [200, { groups: adminGroups, truncated: false }];
  if (p === "/v1/admin/audit-events") return [200, { events: auditEvents, truncated: false }];
  if (p === "/v1/account/api-tokens") return [200, { tokens: apiTokens }];
  if (p === "/v1/account/oauth-grants") return [200, { grants: oauthGrants, truncated: false }];
  if (p === "/v1/supply-chain/overview") return [200, overview(q.getAll("repository_id").map(Number))];
  if (p === "/v1/supply-chain/facets") return [200, facets(q.get("repository_id") ? [Number(q.get("repository_id"))] : null)];
  if (p === "/v1/supply-chain/components") return [200, componentList(q)];
  if (p.startsWith("/v1/supply-chain/components/")) {
    const key = decodeURIComponent(p.slice("/v1/supply-chain/components/".length));
    const coordinate = coordinates.find((item) => item.key === key);
    if (!coordinate) return apiError(404, "not_found", "component not found");
    return [200, {
      key, stream: STREAM, ecosystem: coordinate.ecosystem, ...(coordinate.namespace ? { namespace: coordinate.namespace } : {}), name: coordinate.name, version: coordinate.version, truncated: false,
      notes: ["Occurrences are limited to the caller's authorized repositories."],
      occurrences: coordinate.repoIds.map((id, index) => ({
        repository_id: id, repository: byId(id).name, snapshot_id: snapshotId(id), collected_at: collected, element_id: `SPDXRef-${coordinate.ecosystem}-${index}`, root: false,
        declared_raw: coordinate.expression || "NOASSERTION", assessment: coordinate.status, ...(coordinate.expression ? { expression: coordinate.expression } : {}),
        detail_path: `/v1/supply-chain/repositories/${id}/component?element=SPDXRef-${coordinate.ecosystem}-${index}`,
      })),
    }];
  }
  if (p === "/v1/supply-chain/compare") return [200, comparison(Number(q.get("repository_id")))];
  const job = p.match(/^\/v1\/supply-chain\/jobs\/(\d+)$/);
  if (job) return [200, { id: Number(job[1]), repository_id: 101, stream: STREAM, reason: "manual", state: "succeeded", attempt: 1, max_attempts: 3, run_after: collected, created_at: collected, updated_at: assessedAt }];
  const repository = p.match(repositoryPath);
  if (repository) {
    const id = Number(repository[1]);
    if (!byId(id)) return apiError(404, "not_found", "repository not found");
    switch (repository[2]) {
      case undefined: return [200, repositoryStatus(id)];
      case "components": return [200, { snapshot_id: snapshotId(id), components: repositoryComponents(id), truncated: false }];
      case "component": {
        const detail = repositoryComponentDetail(id, q.get("element"));
        return detail ? [200, detail] : apiError(404, "not_found", "component not found");
      }
      case "collections": return [200, { collections: inventoryRepositories.has(id) ? [collectionRecord(id), { ...collectionRecord(id), id: id * 4 - 1, outcome: "published", http_status: 200, snapshot_id: snapshotId(id, false), error_code: undefined, message: undefined, finished_at: earlier }] : [], truncated: false }];
      case "snapshots": return [200, { snapshots: inventoryRepositories.has(id) ? [snapshot(id), snapshot(id, false)] : [] }];
    }
  }
  const refresh = p.match(/^\/v1\/supply-chain\/repositories\/(\d+)\/refresh$/);
  if (refresh && method === "POST") return [202, { job: { id: 77, repository_id: Number(refresh[1]), stream: STREAM, reason: "manual", state: "queued", attempt: 0, max_attempts: 3, run_after: collected, created_at: collected, updated_at: collected }, created: true }];
  return null;
}

// Go's "GET /admin/" style patterns match the whole subtree, so deep links such as /admin/jobs serve the shell too.
const htmlRoutes = new Set(["/", "/index.html", "/repositories", "/admin", "/account", "/supply-chain"]);
const isHtmlRoute = (p) => htmlRoutes.has(p) || ["/admin/", "/account/", "/supply-chain/"].some((prefix) => p.startsWith(prefix));
const mime = { ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".woff2": "font/woff2", ".woff": "font/woff", ".json": "application/json", ".map": "application/json" };
const notFoundApi = new Set();

function serveFile(response, file, cache, type) {
  try {
    const body = fs.readFileSync(file);
    response.writeHead(200, { "Content-Type": type, "Cache-Control": cache });
    response.end(body);
  } catch {
    response.writeHead(404).end();
  }
}

const server = http.createServer((request, response) => {
  const url = new URL(request.url, "http://127.0.0.1");
  const p = url.pathname;
  if (request.method === "GET" && isHtmlRoute(p)) return serveFile(response, path.join(distDir, "index.html"), "no-store", "text/html; charset=utf-8");
  if (request.method === "GET" && p === "/favicon.svg") return serveFile(response, path.join(distDir, "favicon.svg"), "no-store", "image/svg+xml");
  if (request.method === "GET" && p.startsWith("/assets/")) {
    const file = path.join(distDir, path.normalize(p));
    if (!file.startsWith(path.join(distDir, "assets") + path.sep)) return response.writeHead(404).end();
    return serveFile(response, file, "public, max-age=31536000, immutable", mime[path.extname(file)] ?? "application/octet-stream");
  }
  if (p === "/healthz" || p === "/readyz") return response.writeHead(200, { "Content-Type": "text/plain" }).end("ok");
  if (p === "/auth/logout") return response.writeHead(204).end();
  if (p.startsWith("/v1/")) {
    const chunks = [];
    request.on("data", (chunk) => chunks.push(chunk));
    request.on("end", () => {
      const bearer = request.headers.authorization === "Bearer demo";
      const result = api(request.method, url, bearer, Buffer.concat(chunks).toString("utf8"));
      const [status, body] = result ?? apiError(404, "not_found", "no such stub");
      if (status === 404) notFoundApi.add(`${request.method} ${p}${url.search}`);
      response.writeHead(status, { "Content-Type": "application/json" });
      response.end(JSON.stringify(body));
    });
    return;
  }
  response.writeHead(404).end();
});
await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
const base = `http://127.0.0.1:${server.address().port}`;

// ---- screenshots --------------------------------------------------------
const problems = [];
const generated = [];
fs.mkdirSync(outDir, { recursive: true });
const browser = await chromium.launch();

async function settle(page) {
  await page.waitForLoadState("networkidle");
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(500);
}

async function report(page, label, theme) {
  const title = await page.title();
  const heading = (await page.locator("h1").first().textContent({ timeout: 1000 }).catch(() => null)) ?? (await page.locator("h2").first().textContent({ timeout: 1000 }).catch(() => null)) ?? "(none)";
  const charts = await page.locator("svg.recharts-surface").count();
  const nodes = await page.locator(".react-flow__node").count();
  const rows = await page.locator("tbody tr").count();
  console.log(`${theme.padEnd(6)} ${label.padEnd(18)} title=${JSON.stringify(title)} heading=${JSON.stringify(heading.trim())} charts=${charts} graph_nodes=${nodes} table_rows=${rows}`);
  return { charts, nodes, rows };
}

async function shot(page, label, theme, name, full, expectation) {
  await settle(page);
  const viewport = page.viewportSize();
  if (full) {
    // Grow the viewport to the page so the fixed sidebar and the charts lay out for the full height.
    const height = await page.evaluate(() => document.documentElement.scrollHeight);
    await page.setViewportSize({ width: viewport.width, height: Math.max(viewport.height, height) });
    await settle(page);
  }
  const counts = await report(page, label, theme);
  for (const [what, ok] of Object.entries(expectation ?? {})) if (!ok(counts)) problems.push(`${theme} ${label}: expected ${what}, got ${JSON.stringify(counts)}`);
  const file = path.join(outDir, `console-${label}-${theme}.png`);
  await page.screenshot({ path: file });
  generated.push(file);
  if (full) await page.setViewportSize(viewport);
}

async function run(theme, viewport, suffix) {
  const context = await browser.newContext({ viewport, colorScheme: theme === "dark" ? "dark" : "light", reducedMotion: "reduce" });
  await context.addInitScript((value) => localStorage.setItem("graphnest-theme", value), theme === "dark" ? "dark" : "light");
  const page = await context.newPage();
  let where = "start";
  page.on("console", (message) => {
    if (message.type() !== "error") return;
    // The signed-out session probe is a deliberate 401; the browser logs it as a failed resource.
    if (message.location().url.endsWith("/v1/auth/session")) return;
    problems.push(`${suffix} ${where}: console error: ${message.text()} (${message.location().url})`);
  });
  page.on("pageerror", (error) => problems.push(`${suffix} ${where}: page error: ${error.message}`));
  const step = async (label, route, prepare, full = true, expectation) => {
    where = label;
    if (route) await page.goto(base + route);
    if (prepare) await prepare();
    await shot(page, label, suffix, label, full, expectation);
  };
  const hasRows = { rows: (c) => c.rows > 0 };

  await step("gate", "/", null, false);
  where = "sign-in";
  await page.getByLabel("Bearer token").fill("demo");
  await page.getByRole("button", { name: "Connect" }).click();
  await page.locator("#query").waitFor();

  await step("search", null, async () => {
    await page.locator("#query").fill("http client");
    await page.locator("#query").press("Enter");
    await page.getByRole("heading", { name: "9 matches" }).waitFor();
  }, false);
  if (suffix === "light") {
    const readme = path.join(outDir, "graphnest-ui.png");
    fs.copyFileSync(path.join(outDir, "console-search-light.png"), readme);
    generated.push(readme);
  }

  await step("file", null, async () => {
    await page.getByRole("button", { name: "internal/client/http.go" }).click();
    await page.getByRole("region", { name: "Indexed file contents" }).waitFor();
    await page.locator('[data-line="24"]').getByRole("button", { name: "NewHTTPClient", exact: true }).click();
    await page.getByRole("heading", { name: "Code navigation" }).waitFor();
    await page.getByText("internal/client/http_test.go:12").waitFor();
  }, false);

  await step("repositories", "/repositories", () => page.getByText("acme/data-pipeline").waitFor(), true, hasRows);
  await step("admin-overview", "/admin", () => page.getByText("Health OK").waitFor());
  await step("admin-repositories", "/admin/repositories", () => page.getByText("acme/data-pipeline").first().waitFor(), true, hasRows);
  await step("account", "/account", () => page.getByText("gnu_4f8a2c1e").waitFor());
  await step("supply-chain-overview", "/supply-chain", () => page.getByText("Authorized repositories").first().waitFor());
  await step("components", "/supply-chain/components", async () => {
    await page.getByRole("button", { name: "lodash", exact: true }).click();
    await page.getByRole("heading", { name: /Component occurrences · lodash/ }).waitFor();
    await page.getByRole("dialog").locator("tbody tr").first().waitFor();
  }, false, hasRows);
  await step("licenses", "/supply-chain/licenses", () => page.waitForFunction(() => document.querySelectorAll("svg.recharts-surface").length >= 3), true, { "at least 3 charts": (c) => c.charts >= 3 });
  // A chart container that lays out at zero size renders nothing; warn instead of shipping an empty card unnoticed.
  const empty = await page.locator("[data-slot=chart]").evaluateAll((items) => items.filter((item) => item.getBoundingClientRect().width === 0).length);
  if (empty) console.warn(`WARNING ${suffix} licenses: ${empty} chart container(s) have zero width and render nothing`);
  await step("graph", "/supply-chain/graph", async () => {
    await page.locator(".react-flow__node-dependency").first().waitFor();
    const stacked = await page.locator(".react-flow__node").evaluateAll((items) => items.length - new Set(items.map((item) => item.style.transform)).size);
    if (stacked) console.warn(`WARNING ${suffix} graph: ${stacked} node(s) share another node's position (overlapping layout)`);
    // The first fit ran before the layout settled; fit again, then select a dependency shared by four repositories (dispatched on the node so overlapping nodes cannot intercept it).
    await page.getByRole("button", { name: "fit view" }).click();
    await page.waitForTimeout(500);
    await page.locator(".react-flow__node-dependency", { hasText: "lodash" }).first().dispatchEvent("click");
    await page.getByRole("dialog").waitFor();
  }, false, { "graph nodes": (c) => c.nodes > 0 });
  await step("compare", "/supply-chain/compare?repo=101&base=11", () => page.getByText("Added components").waitFor());
  await context.close();
}

try {
  await run("light", { width: 1440, height: 900 }, "light");
  await run("dark", { width: 1440, height: 900 }, "dark");
  await run("light", { width: 390, height: 844 }, "mobile");
} catch (error) {
  problems.push(`script failure: ${error.stack ?? error}`);
} finally {
  await browser.close();
  server.close();
}

if (notFoundApi.size) problems.push(`404 API paths (missing stubs):\n  ${[...notFoundApi].join("\n  ")}`);
if (problems.length) {
  console.error(`\n${problems.length} problem(s):\n${[...new Set(problems)].join("\n")}`);
  process.exit(1);
}
console.log(`\n${generated.length} images written to ${outDir}`);
