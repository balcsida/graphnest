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
const components = doc.packages.map((pkg, ordinal) => {
  const purl = pkg.externalRefs?.find(ref => ref.referenceType === "purl")?.referenceLocator ?? null;
  const ecosystem = purl ? purl.slice(4, purl.indexOf("/")) : "";
  return {
    element_id: pkg.SPDXID, ordinal, name: pkg.name, version: pkg.versionInfo || null, purl,
    ecosystem, license_declared_raw: pkg.licenseDeclared ?? null, license_concluded_raw: pkg.licenseConcluded ?? null,
    is_root: ordinal === 0, scope: ordinal === 0 ? "root" : "direct",
  };
});
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
    active_job: null, enrichment: "not_configured", opt_out: false,
    notes: ["The latest refresh failed; the inventory shown is the last successful observation.", "GitHub dependency-graph exports are timestamped observations of the default branch; they are not bound to a commit and carry no license data.", "Normalization reported 3 coverage warning(s)."],
    documents: [{snapshot_id: 11, sha256: snapshot.document_sha256, format: "spdx-2.3-json", bytes: 4650, path: "/v1/supply-chain/snapshots/11/document"}],
  },
  "/v1/supply-chain/repositories/101/components": {snapshot_id: 11, components, truncated: false},
  "/v1/supply-chain/repositories/101/collections": {collections: [
    {id: 40, job_id: 9, producer: "github", stream: "github:source", started_at: "2026-09-22T11:59:58Z", finished_at: "2026-09-22T12:00:00Z", outcome: "forbidden", http_status: 403, snapshot_id: null, error_code: "github_forbidden", message: "GitHub returned 403 for the SBOM export."},
    {id: 39, job_id: 8, producer: "github", stream: "github:source", started_at: "2026-09-21T09:59:57Z", finished_at: collected, outcome: "published", http_status: 200, snapshot_id: 11},
  ], truncated: false},
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
  const body = responses[url.pathname];
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
    if (theme === "light") await page.locator("#sc-theme").click();
    await page.waitForTimeout(150);
    await page.screenshot({path: path.join(outDir, `supply-chain-${theme}.png`), fullPage: true});
    const rows = await page.locator("#sc-rows tr").count();
    const notice = await page.locator("#sc-notice").textContent();
    console.log(`${theme}: ${rows} component rows; notice=${JSON.stringify(notice?.slice(0, 60))}`);
    if (rows !== components.length || !notice) throw new Error("page did not render the fixture inventory");
    await page.close();
  }
} finally {
  await browser.close();
  server.close();
}
