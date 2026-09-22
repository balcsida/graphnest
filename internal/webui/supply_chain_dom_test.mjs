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
  querySelectorAll(selector) {
    if (selector === "[data-screen]") return all.filter(node => node.dataset.screen);
    if (selector === "[data-nav]") return all.filter(node => node.dataset.nav);
    return [];
  },
  addEventListener(name, listener) { documentListeners[name] = listener; },
};
globalThis.document = document;
globalThis.Node = FakeNode;
globalThis.location = {hash: "#repo=101&stream=github:source", origin: "https://graphnest.example"};
globalThis.window = {confirm: () => true};
globalThis.URL = class extends URL {
  static createObjectURL() { return "blob:graphnest"; }
  static revokeObjectURL() {}
};
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
  "sc-export", "sc-compare", "sc-compare-base", "sc-compare-body",
  "pf-overview-cards", "pf-component-cards", "pf-assessments", "pf-ecosystems",
  "pf-window", "pf-denominators", "pf-overview-state", "pf-ecosystem",
  "pf-assessment", "pf-license", "pf-search", "pf-scope", "pf-rows", "pf-state",
  "pf-more", "pf-detail", "pf-detail-title", "pf-detail-body", "pf-detail-close",
]) {
  const node = document.createElement(id === "token-form" ? "form" : "div");
  node.hidden = ["sc-shell", "sc-notice", "sc-more", "sc-detail", "sc-compare", "pf-more", "pf-detail", "pf-state", "pf-overview-state"].includes(id);
  ids.set(id, node);
}

// The three screens and their nav buttons mirror the markup's data attributes.
const screens = new Map(), navs = new Map();
for (const name of ["overview", "components", "repository"]) {
  const screen = document.createElement("section"); screen.dataset.screen = name; screens.set(name, screen);
  const nav = document.createElement("button"); nav.dataset.nav = name; navs.set(name, nav);
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
const overview = {
  stream: "github:source", generated_at: "2026-02-02T10:00:00Z",
  repositories: {authorized: 12, with_inventory: 9, never_collected: 2, stale: 3, failed_last_attempt: 1, opted_out: 1},
  components: {occurrences: 410, unique_coordinates: 260, without_purl: 7, without_version: 4, unassessed: 4, assessments: {resolved: 3, conflict: 1}},
  warning_total: 5, oldest_collected_at: "2026-01-20T08:00:00Z", newest_collected_at: "2026-02-01T10:00:00Z",
  ecosystems: [{value: "npm", count: 180}, {value: "maven", count: 80}],
  denominators: [
    "Authorized repositories: repositories this token can read.",
    "With inventory: authorized repositories with at least one snapshot.",
    "Occurrences: component rows across the latest snapshot of each repository.",
    "Unique coordinates: distinct ecosystem, namespace, name and version tuples.",
    "Assessments describe collected evidence; they are not a compliance verdict.",
  ],
};
const facets = {
  stream: "github:source",
  ecosystems: [{value: "npm", count: 180}, {value: "maven", count: 80}],
  assessments: [{value: "resolved", count: 3}, {value: "conflict", count: 1}],
  licenses: [{value: "MIT", count: 120}, {value: "Apache-2.0 AND MIT", count: 4}],
};
const portfolioRow = (ordinal, overrides = {}) => ({
  key: "npm||pkg-" + ordinal + "|1." + ordinal + ".0", ecosystem: "npm", name: "pkg-" + ordinal,
  version: "1." + ordinal + ".0", purl: "pkg:npm/pkg-" + ordinal + "@1." + ordinal + ".0",
  repository_count: 2, occurrence_count: 3, assessment: "resolved", expression: "MIT",
  declared_raw: ["MIT"], newest_collected_at: "2026-02-01T10:00:00Z", oldest_collected_at: "2026-01-20T08:00:00Z",
  repositories: [{id: 101, name: "acme/widgets"}], ...overrides,
});
const portfolioFirst = [
  portfolioRow(1, {namespace: "@acme", name: "<i>x</i>", assessment: "mixed", expression: "", declared_raw: ["NOASSERTION", "MIT"]}),
  portfolioRow(2),
];
const portfolioSecond = [portfolioRow(3), portfolioRow(4)];
const portfolioDetail = {
  key: portfolioFirst[0].key, stream: "github:source", ecosystem: "npm", namespace: "@acme",
  name: "<i>x</i>", version: "1.1.0", truncated: true,
  notes: ["Occurrences are limited to the caller's authorized repositories."],
  occurrences: [
    {repository_id: 101, repository: "acme/widgets", snapshot_id: 11, collected_at: "2026-02-01T10:00:00Z", element_id: "SPDXRef-1", root: true, declared_raw: "NOASSERTION", assessment: "conflict", expression: "", detail_path: "/v1/supply-chain/repositories/101/component?element=SPDXRef-1"},
    {repository_id: 202, repository: "acme/gadgets", snapshot_id: 21, collected_at: "2026-01-30T10:00:00Z", element_id: "SPDXRef-9", root: false, declared_raw: "MIT", assessment: "resolved", expression: "MIT", detail_path: "/v1/supply-chain/repositories/202/component?element=SPDXRef-9"},
  ],
};
const comparison = {
  repository_id: 101,
  base: {id: 10, collected_at: "2026-01-25T10:00:00Z"}, head: {id: 11, collected_at: "2026-02-01T10:00:00Z"},
  added_components: ["npm:pkg-5@1.5.0"], removed_components: ["npm:pkg-0@1.0.0"],
  license_changes: [{component: "npm:pkg-1@1.1.0", from: "MIT", to: "Apache-2.0"}],
  edges_added: 4, edges_removed: 2, metadata_changes: ["producer tool changed"],
  notes: ["Comparison is by coordinate, not by SPDXID."],
};
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
  ["/v1/supply-chain/overview?" + STREAM]: overview,
  ["/v1/supply-chain/facets?" + STREAM]: facets,
  ["/v1/supply-chain/components?" + STREAM + "&limit=100"]: {stream: "github:source", repositories_in_scope: 9, components: portfolioFirst, truncated: true, next_cursor: "p2"},
  ["/v1/supply-chain/components?" + STREAM + "&limit=100&cursor=p2"]: {stream: "github:source", repositories_in_scope: 9, components: portfolioSecond, truncated: false},
  ["/v1/supply-chain/components?" + STREAM + "&limit=100&ecosystem=maven"]: {stream: "github:source", repositories_in_scope: 9, components: [portfolioRow(7)], truncated: false},
  ["/v1/supply-chain/components/" + encodeURIComponent(portfolioFirst[0].key) + "?" + STREAM]: portfolioDetail,
  ["/v1/supply-chain/repositories/101/snapshots?" + STREAM + "&limit=20"]: {snapshots: [
    {id: 11, collected_at: "2026-02-01T10:00:00Z"},
    {id: 10, collected_at: "2026-01-25T10:00:00Z"},
  ]},
  ["/v1/supply-chain/compare?repository_id=101&base=10&head=11"]: comparison,
  ["/v1/supply-chain/repositories/202/components?" + STREAM + "&limit=100"]: {snapshot_id: 21, components: [component(9)], truncated: false},
  ["/v1/supply-chain/repositories/202?" + STREAM]: {
    repository_id: 202, repository: "acme/gadgets", stream: "github:source", producer: "github",
    collection: "current", freshness_seconds: 60, latest_snapshot: {...snapshot, id: 21, repository_id: 202},
    enrichment: "not_configured", license_summary: {}, notes: [], documents: [],
  },
  ["/v1/supply-chain/repositories/202/component?element=SPDXRef-9&" + STREAM + "&snapshot_id=21"]: componentDetail,
  ["/v1/supply-chain/repositories/202/snapshots?" + STREAM + "&limit=20"]: {snapshots: [{id: 21, collected_at: "2026-01-30T10:00:00Z"}]},
  ["/v1/supply-chain/repositories/202/collections?" + STREAM]: {collections: [], truncated: false},
};
let cursorRejected = false;

const requests = [];
let statusDenied = false;
globalThis.fetch = async (path, options = {}) => {
  requests.push({path, options});
  if (path === "/auth/logout") return {ok: true, status: 204};
  if (path === "/v1/auth/config") return {ok: true, status: 200, json: async () => ({token_login: true, providers: []})};
  if (path === "/v1/auth/session") return {ok: false, status: 401};
  if (statusDenied && path.startsWith("/v1/supply-chain/")) return {ok: false, status: 401, json: async () => ({})};
  if (path.includes("components.csv")) {
    return {
      ok: true, status: 200,
      headers: new Map([["Content-Disposition", 'attachment; filename="graphnest-components-11.csv"']]),
      blob: async () => ({type: "text/csv", body: "repository,snapshot_id\nacme/widgets,11\n"}),
    };
  }
  // The server rejects a cursor minted under a different filter set.
  if (cursorRejected && path.includes("/v1/supply-chain/components?") && path.includes("cursor=")) {
    return {ok: false, status: 400, json: async () => ({error: {code: "invalid_cursor", message: "cursor does not match the filters"}})};
  }
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

// Re-enter the shell with the stored token to exercise the portfolio screens.
statusDenied = false;
ids.get("token").value = "sc-token";
await ids.get("token-form").dispatch("submit");
await settle();
assert.equal(ids.get("sc-shell").hidden, false);
assert.match(location.hash, /view=repository/, "the hash carries the selected view");

// The overview screen names every denominator and each assessment count.
await navs.get("overview").dispatch("click");
await settle();
assert.equal(screens.get("overview").hidden, false);
assert.equal(screens.get("repository").hidden, true);
assert.equal(navs.get("overview").attributes["aria-current"], "page");
assert.match(location.hash, /view=overview/);
const denominators = text(ids.get("pf-denominators"));
for (const line of overview.denominators) assert.ok(denominators.includes(line), "every denominator is rendered: " + line);
assert.match(text(ids.get("pf-overview-cards")), /Authorized repositories/);
assert.match(text(ids.get("pf-overview-cards")), /Failed last attempt/);
assert.match(text(ids.get("pf-component-cards")), /Unique coordinates/);
assert.deepEqual(
  ids.get("pf-assessments").children.map(line => line.children.map(part => part.textContent)),
  [["resolved", "3"], ["conflict", "1"], ["unassessed", "4"]],
  "assessment counts keep the stable order and include unassessed",
);
assert.match(text(ids.get("pf-ecosystems")), /npm/);
assert.match(text(ids.get("pf-window")), /Oldest/);
assert.equal(/%|compliant/i.test(text(ids.get("pf-overview-cards")) + denominators.replace(/[^%]/g, "")), false, "the overview never claims a percentage or compliance");

// The components screen paginates through the portfolio with a server cursor.
await navs.get("components").dispatch("click");
await settle();
assert.equal(screens.get("components").hidden, false);
assert.equal(ids.get("pf-rows").children.length, 2);
assert.equal(ids.get("pf-scope").textContent, "9 repositories in scope");
assert.deepEqual(ids.get("pf-ecosystem").children.map(option => [option.value, option.textContent]), [["", "All ecosystems"], ["npm", "npm (180)"], ["maven", "maven (80)"]]);
assert.deepEqual(ids.get("pf-license").children.map(option => option.textContent), ["All license expressions", "MIT (120)", "Apache-2.0 AND MIT (4)"]);
assert.deepEqual(ids.get("pf-assessment").children.map(option => option.value), ["", "resolved", "conflict"]);
assert.equal(ids.get("pf-more").hidden, false);
await ids.get("pf-more").dispatch("click");
await settle();
assert.equal(ids.get("pf-rows").children.length, 4, "Load more appends the second portfolio page");
assert.equal(ids.get("pf-more").hidden, true);

const portfolioRows = ids.get("pf-rows").children.map(row => row.children.map(cell => text(cell)));
assert.equal(portfolioRows[0][0], "@acme/<i>x</i>", "namespace and name stay literal text");
assert.equal(all.some(node => node.tagName === "I"), false, "no markup is built from portfolio data");
assert.equal(portfolioRows[0][4], "2", "repository_count is shown");
assert.equal(portfolioRows[0][5], "3", "occurrence_count is shown");
assert.equal(portfolioRows[0][6], "NOASSERTION · MIT", "declared values are joined verbatim");
const mixedPill = all.find(node => node.textContent === "mixed" && node.className.includes("pill"));
assert.ok(mixedPill.className.includes("warn"), "a mixed assessment uses the warning tone");

// Changing a filter discards the cursor and refetches from the start.
ids.get("pf-ecosystem").value = "maven";
await ids.get("pf-ecosystem").dispatch("change");
await settle();
const filtered = requests[requests.length - 1].path;
assert.match(filtered, /ecosystem=maven/);
assert.equal(/cursor=/.test(filtered), false, "a filter change must drop the cursor");
assert.equal(ids.get("pf-rows").children.length, 1);
assert.match(location.hash, /eco=maven/);

// A rejected cursor resets the list instead of surfacing an error.
ids.get("pf-ecosystem").value = "";
await ids.get("pf-ecosystem").dispatch("change");
await settle();
cursorRejected = true;
await ids.get("pf-more").dispatch("click");
await settle();
assert.equal(ids.get("pf-state").hidden, true, "a rejected cursor must not leave an error message");
assert.equal(ids.get("pf-rows").children.length, 2, "a rejected cursor restarts from the first page");
cursorRejected = false;

// The package cell opens the portfolio detail with every authorized occurrence.
const packageButton = ids.get("pf-rows").children[0].children[0].children[0];
assert.equal(packageButton.tagName, "BUTTON");
await packageButton.dispatch("click");
await settle();
assert.equal(ids.get("pf-detail").hidden, false);
assert.equal(document.activeElement, ids.get("pf-detail-title"), "opening the panel moves focus to its heading");
const occurrenceTable = ids.get("pf-detail-body").children.find(node => node.className === "table-wrap");
const occurrenceRows = occurrenceTable.children[0].children[1].children;
assert.equal(occurrenceRows.length, 2, "both occurrences are rendered");
assert.match(text(ids.get("pf-detail-body")), /acme\/gadgets/);
assert.match(text(ids.get("pf-detail-body")), /NOASSERTION/);
assert.match(text(ids.get("pf-detail-body")), /truncated/);
assert.match(text(ids.get("pf-detail-body")), /Occurrences are limited/);
assert.match(location.hash, /pkey=/);

// The Evidence button hands the occurrence to the repository inventory view.
const evidenceButton = occurrenceRows[1].children[7].children[0];
assert.equal(evidenceButton.textContent, "Evidence");
await evidenceButton.dispatch("click");
await settle();
assert.equal(screens.get("repository").hidden, false, "Evidence switches to the repository inventory");
assert.match(location.hash, /view=repository/);
assert.match(location.hash, /repo=202/);
assert.match(location.hash, /element=SPDXRef-9/);
assert.ok(requests.some(({path}) => path.includes("/repositories/202/component?element=SPDXRef-9") && path.includes("snapshot_id=21")), "the occurrence snapshot is passed to the evidence fetch");

// The CSV export downloads the snapshot attachment under its server filename.
ids.get("sc-repository").value = "101";
await ids.get("sc-repository").dispatch("change");
await settle();
await ids.get("sc-export").dispatch("click");
await settle();
const exportRequest = requests.find(({path}) => path.includes("/v1/supply-chain/exports/101/components.csv"));
assert.match(exportRequest.path, /snapshot_id=11/);
const downloadLink = all.filter(node => node.tagName === "A").pop();
assert.equal(downloadLink.download, "graphnest-components-11.csv", "the filename comes from Content-Disposition");
assert.equal(downloadLink.clicked, true);

// Comparing against an older snapshot names each license change literally.
assert.deepEqual(ids.get("sc-compare-base").children.map(option => option.value), ["", "10"], "only older snapshots are offered as a base");
ids.get("sc-compare-base").value = "10";
await ids.get("sc-compare-base").dispatch("change");
await settle();
assert.equal(ids.get("sc-compare").hidden, false);
const compareText = text(ids.get("sc-compare-body"));
assert.match(compareText, /npm:pkg-1@1\.1\.0: MIT → Apache-2\.0/, "a license change reads from → to");
assert.match(compareText, /npm:pkg-5@1\.5\.0/);
assert.match(compareText, /npm:pkg-0@1\.0\.0/);
assert.match(compareText, /4 added · 2 removed/);
assert.match(compareText, /producer tool changed/);
assert.match(compareText, /Comparison is by coordinate/);

// A repository with a single snapshot offers no comparison base.
ids.get("sc-repository").value = "202";
await ids.get("sc-repository").dispatch("change");
await settle();
assert.deepEqual(ids.get("sc-compare-base").children.map(option => option.textContent), ["Only one snapshot has been collected"]);
assert.equal(ids.get("sc-compare-base").disabled, true);


