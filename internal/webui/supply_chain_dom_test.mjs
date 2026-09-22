import assert from "node:assert/strict";
import fs from "node:fs";
import vm from "node:vm";

process.env.TZ = "UTC";

class FakeNode {
  constructor(tag = "") {
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.dataset = {};
    this.attributes = {};
    this.listeners = {};
    this.className = "";
    this.hidden = false;
    this.disabled = false;
    this.value = "";
    this.style = {};
    this.textContent = "";
  }
  append(...children) { this.children.push(...children); }
  removeChild(child) { this.children.splice(this.children.indexOf(child), 1); }
  get firstChild() { return this.children[0] || null; }
  setAttribute(name, value) { this.attributes[name] = String(value); }
  addEventListener(name, listener) { this.listeners[name] = listener; }
  dispatch(name) { return this.listeners[name]?.({preventDefault() {}, target: this}); }
  focus() { document.activeElement = this; }
  click() { this.clicked = true; }
  get classList() {
    return {
      add: value => { this.className += " " + value; },
      toggle: value => { this.className = this.className.includes(value) ? this.className.replace(value, "") : this.className + " " + value; },
    };
  }
}

const all = [];
const ids = new Map();
const documentListeners = {};
const document = {
  activeElement: null,
  documentElement: new FakeNode("html"),
  createElement(tag) { const node = new FakeNode(tag); all.push(node); return node; },
  getElementById(id) { return ids.get(id); },
  querySelectorAll() { return []; },
  addEventListener(name, listener) { documentListeners[name] = listener; },
};
globalThis.document = document;
globalThis.Node = FakeNode;
globalThis.location = {hash: "#repo=101&stream=github:source", origin: "https://graphnest.example"};
globalThis.window = {confirm: () => true};
const storage = new Map([["graphnest_admin_token", "sc-token"]]);
globalThis.sessionStorage = {
  getItem: key => storage.get(key) || null,
  setItem: (key, value) => storage.set(key, value),
  removeItem: key => storage.delete(key),
};

for (const id of [
  "access-panel", "access-message", "provider-options", "token-form", "token",
  "sc-shell", "sc-status", "sc-theme", "sc-logout", "sc-repository", "sc-stream",
  "sc-refresh", "sc-download", "sc-notes", "sc-notice", "sc-cards", "sc-details",
  "sc-document", "sc-warnings", "sc-warning-count", "sc-search", "sc-shown",
  "sc-rows", "sc-loading", "sc-empty", "sc-error", "sc-more", "sc-collections",
  "sc-detail", "sc-detail-title", "sc-detail-body", "sc-detail-close",
]) {
  const node = document.createElement(id === "token-form" ? "form" : "div");
  node.hidden = id === "sc-shell" || id === "sc-notice" || id === "sc-more" || id === "sc-detail";
  ids.set(id, node);
}

const snapshot = {
  id: 11, repository_id: 101, stream: "github:source", producer: "github",
  collected_at: "2026-02-01T10:00:00Z", created_at_claimed: "2026-02-01T09:00:00Z",
  producer_tool: "GitHub dependency graph", component_count: 7, warning_count: 3,
  warnings: [
    {code: "unresolved_relationship", element: "SPDXRef-1", detail: "target missing"},
    {code: "missing_version", element: "SPDXRef-2", detail: ""},
    {code: "unknown_license_expression", element: "SPDXRef-3", detail: "NOASSERTION"},
  ],
};
const component = (ordinal, overrides = {}) => ({
  element_id: "SPDXRef-" + ordinal, ordinal, name: "pkg-" + ordinal, version: "1." + ordinal + ".0",
  purl: "pkg:npm/pkg-" + ordinal + "@1." + ordinal + ".0", ecosystem: "npm",
  license_declared_raw: "MIT", is_root: false, scope: "transitive", ...overrides,
});
const assessment = (status, overrides = {}) => ({
  status, evidence_count: 2, assessed_at: "2026-02-01T11:00:00Z",
  evidence_fingerprint: "a1b2c3d4e5f60718293a4b5c6d7e8f90", ...overrides,
});
const firstPage = [
  component(1, {is_root: true, scope: "root", name: "<b>x</b>", license: assessment("resolved", {expression: "MIT"})}),
  component(2, {scope: "direct", license: assessment("conflict", {conflict_detail: "github says MIT, npm says Apache-2.0"})}),
  component(3, {version: null, purl: null, license_declared_raw: "NOASSERTION", scope: "unknown", license: null}),
  component(4),
];
const evidenceRow = (overrides = {}) => ({
  id: 1, source: "registry", route: "npm:test", ecosystem: "npm", name: "pkg-1", version: "1.1.0",
  raw_value: "MIT", raw_kind: "expression", parse_status: "parsed", expression: "MIT",
  resolver_version: 1, license_list_version: "3.27.0", fetched_at: "2026-02-01T10:30:00Z",
  outcome: "resolved", ...overrides,
});
const componentDetail = {
  component: firstPage[0], snapshot,
  declarations: [
    evidenceRow({id: 10, source: "producer", route: "", raw_value: "MIT", raw_kind: "spdx_declared"}),
    evidenceRow({id: 11, source: "producer", route: "", raw_value: "<b>MIT</b>", raw_kind: "spdx_declared", parse_status: "unparsed"}),
  ],
  evidence: [
    evidenceRow({id: 20, license_url: "https://registry.test/license"}),
    evidenceRow({id: 21, outcome: "unavailable", route: "npm:test", raw_value: "", expression: "", parse_status: "absent", message: "registry timed out"}),
  ],
  relationships: [{from: "SPDXRef-1", type: "DEPENDS_ON", to: "SPDXRef-2", resolved: false}],
  notes: ["Registry evidence is cached and may lag the registry.", "Declarations are shown verbatim."],
  truncated: false,
};
const secondPage = [component(5), component(6), component(7)];

const STREAM = "stream=github%3Asource";
const responses = {
  "/v1/repositories": {repositories: [
    {id: 1, github_id: 101, name: "acme/widgets"},
    {id: 2, github_id: 202, name: "acme/gadgets"},
  ], truncated: false},
  ["/v1/supply-chain/repositories/101?" + STREAM]: {
    repository_id: 101, repository: "acme/widgets", stream: "github:source", producer: "github",
    subject: "repository", collection: "failed", freshness_seconds: 7200,
    latest_snapshot: snapshot,
    last_collection: {id: 5, outcome: "forbidden", http_status: 403, message: "dependency graph is disabled", finished_at: "2026-02-02T10:00:00Z"},
    active_job: null, enrichment: "configured", enrichment_ecosystems: ["npm", "maven"],
    license_summary: {resolved: 3, conflict: 1, unknown: 2}, opt_out: false,
    notes: ["GitHub reports the dependency graph for the default branch.", "Subject assurance is unknown on GitHub Enterprise Server."],
    documents: [{snapshot_id: 11, sha256: "ab".repeat(32), format: "spdx-2.3-json", bytes: 1234, path: "/v1/supply-chain/snapshots/11/document"}],
  },
  ["/v1/supply-chain/repositories/101/components?" + STREAM + "&limit=100"]: {snapshot_id: 11, components: firstPage, truncated: true, next_cursor: "c2"},
  ["/v1/supply-chain/repositories/101/components?" + STREAM + "&limit=100&cursor=c2"]: {snapshot_id: 11, components: secondPage, truncated: false},
  ["/v1/supply-chain/repositories/101/component?element=SPDXRef-1&" + STREAM + "&snapshot_id=11"]: componentDetail,
  ["/v1/supply-chain/repositories/101/collections?" + STREAM]: {collections: [
    {id: 5, outcome: "forbidden", http_status: 403, error_code: "forbidden", message: "dependency graph is disabled", finished_at: "2026-02-02T10:00:00Z"},
    {id: 4, outcome: "published", http_status: 200, snapshot_id: 11, finished_at: "2026-02-01T10:00:00Z"},
  ], truncated: false},
};

const requests = [];
let statusDenied = false;
globalThis.fetch = async (path, options = {}) => {
  requests.push({path, options});
  if (path === "/auth/logout") return {ok: true, status: 204};
  if (path === "/v1/auth/config") return {ok: true, status: 200, json: async () => ({token_login: true, providers: []})};
  if (path === "/v1/auth/session") return {ok: false, status: 401};
  if (statusDenied && path.startsWith("/v1/supply-chain/")) return {ok: false, status: 401, json: async () => ({})};
  if (path === "/v1/supply-chain/repositories/101/refresh?" + STREAM && options.method === "POST") {
    return {ok: true, status: 202, json: async () => ({job: {id: 9, repository_id: 101, state: "queued", attempt: 0, max_attempts: 3}, created: true})};
  }
  const body = responses[path];
  return {ok: !!body, status: body ? 200 : 404, json: async () => body};
};

const source = fs.readFileSync(new URL("supply-chain.html", import.meta.url), "utf8");
const script = source.match(/<script>([\s\S]+)<\/script>/i)[1];
vm.runInThisContext(script, {filename: "supply-chain.html"});
const settle = () => new Promise(resolve => setTimeout(resolve, 5));
await settle();

const text = node => node.textContent + node.children.map(text).join(" ");

// The stored bearer token opens the shell after the same-origin session probe fails.
assert.equal(ids.get("sc-shell").hidden, false, "bearer fallback must open the inventory shell");
assert.ok(requests.some(({path}) => path === "/v1/auth/session"));
assert.ok(requests.some(({path}) => path === "/auth/logout"));
const authorized = requests.find(({path}) => path.startsWith("/v1/supply-chain/repositories/101?"));
assert.equal(authorized.options.headers.get("Authorization"), "Bearer sc-token", "bearer mode must send Authorization");

// Repository and stream selectors reflect the restored hash.
assert.deepEqual(ids.get("sc-repository").children.map(option => [option.value, option.textContent]), [["101", "acme/widgets"], ["202", "acme/gadgets"]]);
assert.equal(ids.get("sc-repository").value, "101");
assert.equal(ids.get("sc-stream").value, "github:source");
assert.match(location.hash, /repo=101&stream=github:source/);

// A failed refresh over a retained snapshot is called out literally.
assert.equal(ids.get("sc-notice").hidden, false, "failed collection must show the retained-inventory notice");
assert.match(text(ids.get("sc-notice")), /last successful observation/);
assert.match(text(ids.get("sc-notice")), /forbidden/);
assert.match(text(ids.get("sc-notice")), /403/);
const collectionPill = all.find(node => node.textContent === "failed" && node.className.includes("pill"));
assert.ok(collectionPill.className.includes("err"), "failed collection pill must use the error tone");
assert.match(text(ids.get("sc-cards")), /2 hours ago/);
assert.match(text(ids.get("sc-details")), /unknown — not bound to a commit/);
assert.match(text(ids.get("sc-details")), /Producer-claimed creation time/);
assert.match(text(ids.get("sc-notes")), /Subject assurance is unknown/);
assert.equal(ids.get("sc-warnings").children.length, 3);
assert.equal(ids.get("sc-collections").children.length, 2);

// Server pagination assembles the full snapshot inventory.
assert.equal(ids.get("sc-rows").children.length, 4);
assert.equal(ids.get("sc-more").hidden, false);
await ids.get("sc-more").dispatch("click");
await settle();
assert.equal(ids.get("sc-rows").children.length, 7, "Load more must append the second component page");
assert.equal(ids.get("sc-more").hidden, true);
assert.equal(ids.get("sc-shown").textContent, "Showing 7 of snapshot #11");

const rows = ids.get("sc-rows").children.map(row => row.children.map(cell => text(cell)));
assert.deepEqual(rows[2].slice(1, 4), ["—", "npm", "—"], "missing version and PURL render as an em dash");
assert.equal(rows[2][5], "NOASSERTION", "declared license is shown verbatim");
assert.equal(rows[0][0], "<b>x</b>", "component names are rendered as literal text");
assert.equal(all.some(node => node.tagName === "B"), false, "no markup is built from component data");

// Assessed licenses are shown as a status pill plus the normalized expression.
assert.equal(rows[2][6], "—", "a component without an assessment shows an em dash");
assert.match(rows[0][6], /resolved/);
assert.match(rows[0][6], /MIT/, "a resolved assessment shows its expression");
const conflictPill = all.find(node => node.textContent === "conflict" && node.className.includes("pill"));
assert.ok(conflictPill.className.includes("err"), "a conflicting assessment uses the error tone");

// Enrichment state and the license summary are reported in the status area.
assert.match(text(ids.get("sc-details")), /License enrichment/);
assert.match(text(ids.get("sc-details")), /configured for npm, maven/);
assert.match(text(ids.get("sc-cards")), /3 resolved · 1 conflict · 2 unknown/);

// The component name opens the evidence detail panel for that element.
const nameButton = ids.get("sc-rows").children[0].children[0].children[0];
assert.equal(nameButton.tagName, "BUTTON", "component names open the detail panel");
await nameButton.dispatch("click");
await settle();
const detailRequest = requests.find(({path}) => path.includes("/component?"));
assert.equal(detailRequest.options.headers.get("Authorization"), "Bearer sc-token", "detail fetch must send Authorization");
assert.match(detailRequest.path, /snapshot_id=11/);
assert.equal(ids.get("sc-detail").hidden, false);
assert.equal(document.activeElement, ids.get("sc-detail-title"), "opening the panel moves focus to its heading");
const detailText = text(ids.get("sc-detail-body"));
const evidenceTable = ids.get("sc-detail-body").children.filter(node => node.className === "table-wrap");
assert.equal(evidenceTable.length, 2, "declarations and registry evidence each render a table");
assert.equal(evidenceTable[1].children[0].children[1].children.length, 2, "both registry evidence rows are rendered");
assert.match(detailText, /npm:test/);
assert.match(detailText, /resolver v1 · SPDX list 3\.27\.0/);
assert.match(detailText, /a1b2c3d4e5f6/, "the fingerprint is shortened");
assert.match(detailText, /registry timed out/);
assert.match(detailText, /Registry evidence is cached/, "detail notes are rendered");
assert.match(detailText, /\(unresolved\)/);
assert.ok(detailText.includes("<b>MIT</b>"), "raw declaration values stay literal text");
assert.equal(all.some(node => node.tagName === "B"), false, "no markup is built from evidence data");
const unavailablePill = all.find(node => node.textContent === "unavailable" && node.className.includes("pill"));
assert.ok(unavailablePill.className.includes("warn"), "an unavailable registry fetch uses the warning tone");
assert.match(location.hash, /element=SPDXRef-1/);

// Escape closes the panel and drops the element from the hash.
documentListeners.keydown({key: "Escape"});
assert.equal(ids.get("sc-detail").hidden, true, "Escape closes the evidence panel");
assert.equal(/element=/.test(location.hash), false);

// Refresh reports the queued job.
await ids.get("sc-refresh").dispatch("click");
await settle();
assert.match(ids.get("sc-status").textContent, /Refresh job #9 is queued/);

// A 401 on status locks the panel.
statusDenied = true;
ids.get("sc-repository").value = "202";
await ids.get("sc-repository").dispatch("change");
await settle();
assert.equal(ids.get("sc-shell").hidden, true, "a 401 must hide the shell");
assert.equal(ids.get("access-panel").hidden, false);
assert.equal(ids.get("sc-rows").children.length, 0);
