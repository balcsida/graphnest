// Renders the embedded /supply-chain page in a real Chromium against a
// stubbed same-origin inventory API and saves light and dark screenshots.
// Usage: node test/smoke/supply-chain-screenshots.mjs <html-path> <out-dir>
// The stubs mirror docs/openapi.yaml shapes; this is a rendering check of the
// real page, not a verified GitHub integration.
import { chromium } from "@playwright/test";
import fs from "node:fs";
import http from "node:http";
import path from "node:path";

const [htmlPath, outDir] = process.argv.slice(2);
const html = fs.readFileSync(htmlPath);
const fixture = fs.readFileSync(path.resolve("test/fixtures/supplychain/ghes-spdx-2.3.json"), "utf8");
const doc = JSON.parse(fixture);
const collected = "2026-09-21T10:00:00Z";
const assessed = "2026-09-21T10:05:00Z";
// Registry-derived assessments; the fixture declares NOASSERTION everywhere, so
// nothing here is inferred from the producer's declared value.
// Only ecosystems with a configured route (npm here) can be resolved; the
// root has no version, maven/nuget/golang/actions have no route, and the
// vendored component has no coordinates at all.
const ASSESSMENTS = [
  null,
  {status: "resolved", expression: "MIT", evidence_count: 1},
  {status: "conflict", conflict_detail: "producer_declared: Apache-2.0 | registry_maven@maven:internal: MIT", evidence_count: 1},
  null,
  null,
  null,
  {status: "not_applicable", evidence_count: 0},
];
function licenseFor(ordinal) {
  const assessment = ASSESSMENTS[ordinal];
  if (!assessment) return null;
  return {...assessment, assessed_at: assessed, evidence_fingerprint: "c3" + String(ordinal) + "9f41ad7b2e5c81d4a6027f3b9e5148dc6a0b7739ce21845f0db3c6a1e9f472"};
}
const components = doc.packages.map((pkg, ordinal) => {
  const purl = pkg.externalRefs?.find(ref => ref.referenceType === "purl")?.referenceLocator ?? null;
  const ecosystem = purl ? purl.slice(4, purl.indexOf("/")) : "";
  return {
    element_id: pkg.SPDXID, ordinal, name: pkg.name, version: pkg.versionInfo || null, purl,
    ecosystem, license_declared_raw: pkg.licenseDeclared ?? null, license_concluded_raw: pkg.licenseConcluded ?? null,
    is_root: ordinal === 0, scope: ordinal === 0 ? "root" : "direct",
    license: licenseFor(ordinal),
  };
});
const detailComponent = components[1];
const licenseSummary = {};
for (const item of components) if (item.license) licenseSummary[item.license.status] = (licenseSummary[item.license.status] || 0) + 1;
const snapshot = {
  id: 11, repository_id: 1, stream: "github:source", producer: "github", subject: "source", collected_at: collected,
  created_at_claimed: doc.creationInfo.created, producer_tool: "GitHub.com-Dependency-Graph", document_name: doc.name,
  document_namespace: doc.documentNamespace, spdx_version: "SPDX-2.3", data_license: "CC0-1.0", subject_assurance: "unknown",
  root_element_ids: [doc.packages[0].SPDXID], parser_version: 1, component_count: components.length, edge_count: doc.relationships.length,
  warning_count: 3, warnings: [
    {code: "relationship_unresolved", element: "SPDXRef-maven-org.example-core-2.1.0", detail: "DEPENDS_ON references an element that is not in the document"},
    {code: "package_purl_missing", element: "SPDXRef-vendored-legacy", detail: "package has no purl external reference"},
    {code: "package_version_missing", element: "SPDXRef-vendored-legacy", detail: "package has no versionInfo"},
  ],
  published_at: collected, document_sha256: "b0b4df8246d4f1c8873fc7c2ef1d9e423498de4d45331f058896b6ece51e970c", document_format: "spdx-2.3-json", document_bytes: 4650,
};
const responses = {
  "/v1/auth/config": {token_login: true, providers: []},
  "/v1/repositories": {repositories: [{id: 101, github_id: 101, name: "acme/widgets"}, {id: 102, github_id: 102, name: "acme/gadgets"}], truncated: false},
  "/v1/supply-chain/repositories/101": {
    repository_id: 101, repository: "acme/widgets", stream: "github:source", producer: "github", subject: "source", collection: "failed",
    freshness_seconds: 93600, latest_snapshot: snapshot,
    last_collection: {id: 40, job_id: 9, producer: "github", stream: "github:source", started_at: "2026-09-22T11:59:58Z", finished_at: "2026-09-22T12:00:00Z", outcome: "forbidden", http_status: 403, snapshot_id: null, error_code: "github_forbidden",
      message: "GitHub returned 403 for the SBOM export: the dependency graph may be disabled, the installation may lack Contents read access, or the endpoint may be unsupported on this GitHub version."},
    active_job: null, enrichment: "configured", enrichment_ecosystems: ["npm"], license_summary: licenseSummary, opt_out: false,
    notes: ["The latest refresh failed; the inventory shown is the last successful observation.", "GitHub dependency-graph exports are timestamped observations of the default branch; they are not bound to a commit and carry no license data.", "Normalization reported 3 coverage warning(s)."],
    documents: [{snapshot_id: 11, sha256: snapshot.document_sha256, format: "spdx-2.3-json", bytes: 4650, path: "/v1/supply-chain/snapshots/11/document"}],
  },
  "/v1/supply-chain/repositories/101/components": {snapshot_id: 11, components, truncated: false},
  "/v1/supply-chain/repositories/101/component": {
    component: detailComponent, snapshot,
    declarations: [
      {id: 0, source: "producer_concluded", ecosystem: "npm", namespace: "@scope", name: "left-pad", version: "1.3.0", raw_value: "NOASSERTION", raw_kind: "sentinel", parse_status: "no_assertion", resolver_version: 1, license_list_version: "3.27.0", fetched_at: collected, outcome: "resolved"},
      {id: 0, source: "producer_declared", ecosystem: "npm", namespace: "@scope", name: "left-pad", version: "1.3.0", raw_value: "NOASSERTION", raw_kind: "sentinel", parse_status: "no_assertion", resolver_version: 1, license_list_version: "3.27.0", fetched_at: collected, outcome: "resolved"},
    ],
    evidence: [
      {id: 21, source: "registry_npm", route: "npm:npm.example.internal", ecosystem: "npm", namespace: "@scope", name: "left-pad", version: "1.3.0", raw_value: "", raw_kind: "missing", parse_status: "not_applicable", resolver_version: 1, license_list_version: "3.27.0", fetched_at: "2026-09-22T09:00:00Z", expires_at: "2026-09-23T09:00:00Z", outcome: "unavailable", http_status: 503, message: "registry returned an unexpected status"},
      {id: 20, source: "registry_npm", route: "npm:npm.example.internal", ecosystem: "npm", namespace: "@scope", name: "left-pad", version: "1.3.0", raw_value: "MIT", raw_kind: "expression", parse_status: "parsed", expression: "MIT", detail: {integrity: "sha512-abc"}, resolver_version: 1, license_list_version: "3.27.0", content_sha256: "9f".repeat(32), fetched_at: assessed, outcome: "resolved"},
    ],
    relationships: [{from: doc.packages[0].SPDXID, type: "DEPENDS_ON", to: detailComponent.element_id, resolved: true}],
    notes: ["Publisher declarations and registry metadata are evidence, not approval; a human conclusion or policy decision is recorded separately."],
    truncated: false,
  },
  "/v1/supply-chain/repositories/101/collections": {collections: [
    {id: 40, job_id: 9, producer: "github", stream: "github:source", started_at: "2026-09-22T11:59:58Z", finished_at: "2026-09-22T12:00:00Z", outcome: "forbidden", http_status: 403, snapshot_id: null, error_code: "github_forbidden", message: "GitHub returned 403 for the SBOM export."},
    {id: 39, job_id: 8, producer: "github", stream: "github:source", started_at: "2026-09-21T09:59:57Z", finished_at: collected, outcome: "published", http_status: 200, snapshot_id: 11},
  ], truncated: false},
  "/v1/supply-chain/repositories/101/snapshots": {snapshots: [
    {id: 11, collected_at: collected}, {id: 10, collected_at: "2026-09-14T10:00:00Z"},
  ]},
  "/v1/supply-chain/overview": {
    stream: "github:source", generated_at: collected,
    repositories: {authorized: 12, with_inventory: 9, never_collected: 2, stale: 3, failed_last_attempt: 1, opted_out: 1},
    components: {occurrences: 410, unique_coordinates: 260, without_purl: 7, without_version: 4, unassessed: 231, assessments: licenseSummary},
    warning_total: 3, oldest_collected_at: "2026-09-01T08:00:00Z", newest_collected_at: collected,
    ecosystems: [{value: "npm", count: 180}, {value: "maven", count: 60}, {value: "nuget", count: 20}],
    denominators: [
      "Authorized repositories: repositories this token can read.",
      "With inventory: authorized repositories with at least one collected snapshot.",
      "Occurrences: component rows across the latest snapshot of each repository.",
      "Unique coordinates: distinct ecosystem, namespace, name and version tuples.",
      "Assessments describe collected evidence; they are not a compliance verdict.",
    ],
  },
  "/v1/supply-chain/facets": {
    stream: "github:source",
    ecosystems: [{value: "npm", count: 180}, {value: "maven", count: 60}, {value: "nuget", count: 20}],
    assessments: [{value: "resolved", count: 1}, {value: "conflict", count: 1}, {value: "not_applicable", count: 1}],
    licenses: [{value: "MIT", count: 120}, {value: "Apache-2.0", count: 40}, {value: "Apache-2.0 AND MIT", count: 4}],
  },
  "/v1/supply-chain/components": {
    stream: "github:source", repositories_in_scope: 9, truncated: false,
    components: components.filter(item => item.purl).map((item, ordinal) => ({
      key: Buffer.from([item.ecosystem, "", item.name, item.version || ""].join("\u0001")).toString("hex"), ecosystem: item.ecosystem, name: item.name,
      version: item.version || "", purl: item.purl, repository_count: 3 - (ordinal % 3), occurrence_count: 5 - ordinal,
      assessment: item.license?.status || "unassessed", expression: item.license?.expression || "",
      declared_raw: [item.license_declared_raw || "NOASSERTION"],
      newest_collected_at: collected, oldest_collected_at: "2026-09-01T08:00:00Z",
      repositories: [{id: 101, name: "acme/widgets"}],
    })),
  },
  "/v1/supply-chain/compare": {
    repository_id: 101, base: {id: 10, collected_at: "2026-09-14T10:00:00Z"}, head: {id: 11, collected_at: collected},
    added_components: ["npm:@scope/left-pad@1.3.0"], removed_components: ["npm:@scope/right-pad@1.0.0"],
    license_changes: [{component: "npm:@scope/left-pad@1.3.0", from: "NOASSERTION", to: "MIT"}],
    edges_added: 2, edges_removed: 1, metadata_changes: ["producer tool changed"],
    notes: ["Comparison is by coordinate, not by SPDXID."],
  },
};
const portfolioKey = responses["/v1/supply-chain/components"].components[0].key;
const portfolioDetail = {
  key: portfolioKey, stream: "github:source", ecosystem: "npm", namespace: "@scope", name: detailComponent.name,
  version: detailComponent.version || "", truncated: false,
  notes: ["Occurrences are limited to the caller's authorized repositories."],
  occurrences: [
    {repository_id: 101, repository: "acme/widgets", snapshot_id: 11, collected_at: collected, element_id: detailComponent.element_id, root: false, declared_raw: "NOASSERTION", assessment: "resolved", expression: "MIT", detail_path: "/v1/supply-chain/repositories/101/component?element=" + detailComponent.element_id},
    {repository_id: 102, repository: "acme/gadgets", snapshot_id: 21, collected_at: "2026-09-20T10:00:00Z", element_id: "SPDXRef-npm-left-pad", root: false, declared_raw: "MIT", assessment: "declared", expression: "MIT", detail_path: "/v1/supply-chain/repositories/102/component?element=SPDXRef-npm-left-pad"},
  ],
};
const server = http.createServer((request, response) => {
  const url = new URL(request.url, "http://127.0.0.1");
  if (url.pathname === "/supply-chain") {
    response.writeHead(200, {"Content-Type": "text/html; charset=utf-8"});
    return response.end(html);
  }
  if (url.pathname === "/v1/auth/session") {
    response.writeHead(401, {"Content-Type": "application/json"});
    return response.end(`{"error":{"code":"unauthenticated","message":"authentication required","request_id":"x","retryable":false}}`);
  }
  if (url.pathname === "/auth/logout") {
    response.writeHead(204);
    return response.end();
  }
  if (request.headers.authorization !== "Bearer demo") {
    response.writeHead(401, {"Content-Type": "application/json"});
    return response.end(`{"error":{"code":"unauthenticated","message":"authentication required","request_id":"x","retryable":false}}`);
  }
  const body = url.pathname.startsWith("/v1/supply-chain/components/") ? portfolioDetail : responses[url.pathname];
  if (!body) {
    response.writeHead(404, {"Content-Type": "application/json"});
    return response.end(`{"error":{"code":"not_found","message":"not found","request_id":"x","retryable":false}}`);
  }
  response.writeHead(200, {"Content-Type": "application/json"});
  response.end(JSON.stringify(body));
});
await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
const base = `http://127.0.0.1:${server.address().port}`;
fs.mkdirSync(outDir, {recursive: true});
const browser = await chromium.launch();
try {
  for (const theme of ["dark", "light"]) {
    const page = await browser.newPage({viewport: {width: 1440, height: 1100}});
    await page.goto(`${base}/supply-chain#repo=101&stream=github:source`);
    await page.getByLabel("Bearer token").fill("demo");
    await page.getByRole("button", {name: "Open inventory"}).click();
    await page.locator("#sc-rows tr").first().waitFor();
    await page.getByRole("button", {name: detailComponent.name, exact: true}).first().click();
    await page.locator("#sc-detail").waitFor({state: "visible"});
    await page.getByText("No registry evidence for these coordinates.").waitFor({state: "hidden"}).catch(() => {});
    if (theme === "light") await page.locator("#sc-theme").click();
    await page.waitForTimeout(150);
    await page.screenshot({path: path.join(outDir, `supply-chain-${theme}.png`), fullPage: true});
    const rows = await page.locator("#sc-rows tr").count();
    const notice = await page.locator("#sc-notice").textContent();
    console.log(`${theme}: ${rows} component rows; notice=${JSON.stringify(notice?.slice(0, 60))}`);
    if (rows !== components.length || !notice) throw new Error("page did not render the fixture inventory");

    await page.getByRole("button", {name: "Overview", exact: true}).click();
    await page.locator("#pf-denominators p").first().waitFor();
    await page.screenshot({path: path.join(outDir, `supply-chain-overview-${theme}.png`), fullPage: true});
    const denominators = await page.locator("#pf-denominators p").count();
    if (denominators !== responses["/v1/supply-chain/overview"].denominators.length) throw new Error("overview did not render every denominator");

    await page.getByRole("button", {name: "Components", exact: true}).click();
    await page.locator("#pf-rows tr").first().waitFor();
    await page.locator("#pf-rows tr").filter({hasText: "left-pad"}).first().getByRole("button").click();
    await page.locator("#pf-detail").waitFor({state: "visible"});
    await page.waitForTimeout(150);
    await page.screenshot({path: path.join(outDir, `supply-chain-components-${theme}.png`), fullPage: true});
    const portfolioRows = await page.locator("#pf-rows tr").count();
    const occurrences = await page.locator("#pf-detail tbody tr").count();
    console.log(`${theme}: ${denominators} denominators; ${portfolioRows} portfolio rows; ${occurrences} occurrences`);
    if (!portfolioRows || occurrences !== portfolioDetail.occurrences.length) throw new Error("portfolio screens did not render the fixture data");
    await page.close();
  }
} finally {
  await browser.close();
  server.close();
}
