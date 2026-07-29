import assert from "node:assert/strict";
import fs from "node:fs";
import vm from "node:vm";

class FakeNode {
  constructor(tag = "") {
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.dataset = {};
    this.attributes = {};
    this.listeners = {};
    this.className = "";
    this.hidden = false;
    this.checked = false;
    this.value = "";
    this.files = [];
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
  get classList() {
    return {
      add: value => { this.className += " " + value; },
      toggle: value => { this.className = this.className.includes(value) ? this.className.replace(value, "") : this.className + " " + value; },
    };
  }
}

const all = [];
const ids = new Map();
const document = {
  activeElement: null,
  documentElement: new FakeNode("html"),
  createElement(tag) { const node = new FakeNode(tag); all.push(node); return node; },
  getElementById(id) { return ids.get(id); },
  querySelectorAll(selector) {
    if (selector === "[data-screen]") return all.filter(node => node.dataset.screen);
    if (selector === "[data-nav]") return all.filter(node => node.dataset.nav);
    if (selector === "[data-repo]") return all.filter(node => node.dataset.repo);
    if (selector === "[data-repo]:checked") return all.filter(node => node.dataset.repo && node.checked);
    return [];
  },
};
globalThis.document = document;
globalThis.Node = FakeNode;
globalThis.location = {hash: ""};
globalThis.window = {confirm: () => true};
globalThis.setInterval = () => 0;
const storage = new Map([["grepnest_admin_token", "admin"]]);
let storageUnavailable = true;
let storageRemovals = 0;
globalThis.sessionStorage = {
  getItem: key => {
    if (storageUnavailable) throw new Error("storage unavailable");
    return storage.get(key) || null;
  },
  setItem: (key, value) => {
    if (storageUnavailable) throw new Error("storage unavailable");
    storage.set(key, value);
  },
  removeItem: key => {
    storageRemovals++;
    if (storageUnavailable) throw new Error("storage unavailable");
    storage.delete(key);
  },
};

for (const id of [
  "access-panel", "admin-shell", "admin-status", "access-message", "token", "title", "subtitle",
  "overview-cards", "dependency-health", "activity", "repo-rows", "repo-empty", "repo-statuses",
  "job-rows", "job-empty", "queue-cards", "upload-list", "dependency-list", "delivery-rows",
  "delivery-empty", "github-cards", "github-config", "installations", "health-dot", "health-text",
  "ready-dot", "ready-text", "token-form", "logout", "theme", "repo-filter", "select-all",
  "reconcile", "github-reconcile", "reindex-selected", "scip-upload", "scip-file", "scip-repo",
  "scip-commit", "dependency-refresh", "dependency-repo", "inventory-notices",
]) {
  const node = document.createElement(id.includes("form") || id.includes("upload") || id.includes("refresh") ? "form" : "div");
  ids.set(id, node);
}
for (const name of ["overview", "repositories", "queue", "scip", "webhooks", "github"]) {
  const screen = document.createElement("section"); screen.dataset.screen = name;
  const nav = document.createElement("button"); nav.dataset.nav = name;
}

const responses = {
  "/v1/admin/overview": {repositories:{ready:1},jobs:{queued:1,running:1,succeeded:1,failed:1,superseded:1},deliveries:{succeeded:1},scip_uploads:1,dependencies:1,installations:1},
  "/v1/admin/repositories": {repositories:[
    {github_id:7,name:"acme/repo",default_branch:"main",status:"mystery",error_code:""},
    {github_id:8,name:"acme/failed",default_branch:"main",status:"failed",error_code:"clone_failed"},
  ],truncated:true},
  "/v1/admin/jobs": {jobs:["queued","running","succeeded","failed","superseded"].map((state,id)=>({id:id+1,repository:"acme/repo",target_ref:id === 0 ? "refs/heads/main" : "",target_sha:"a".repeat(40),state,error_code:id === 3 ? "index_failed" : "",attempt:1,max_attempts:3,updated_at:"2026-01-01T00:00:00Z"})),truncated:true},
  "/v1/admin/scip/uploads": {uploads:[],truncated:true},
  "/v1/admin/scip/dependencies": {dependencies:[],truncated:true},
  "/v1/admin/webhook-deliveries": {deliveries:[
    {delivery_id:"delivery-1",event:"push",state:"succeeded",installation_id:1,error_code:"",received_at:"2026-01-01T00:00:00Z",processed_at:"2026-01-02T00:00:00Z"},
    {delivery_id:"delivery-2",event:"push",state:"queued",installation_id:1,error_code:"",received_at:"2026-01-03T00:00:00Z",processed_at:null},
  ],truncated:true},
  "/v1/admin/github": {app_id:1,web_url:"https://example",api_url:"https://example/api",upload_url:"https://example/upload",git_url:"https://example",api_version:"1",private_key_configured:true,webhook_secret_configured:true,ca_configured:false,installations:[],truncated:true},
};
const requests = [];
let mutationDenial = 0;
let allowMutation = false;
let delayedOverview;
globalThis.fetch = async (path, options = {}) => {
  requests.push({path, options});
  if (path === "/healthz" || path === "/readyz") return {ok:true,status:200};
  if (path === "/v1/admin/overview" && delayedOverview) return delayedOverview;
  if (mutationDenial && options.method === "POST") return {ok:false,status:mutationDenial,json:async()=>({})};
  if (allowMutation && options.method === "POST") return {ok:true,status:200,json:async()=>({})};
  const body = responses[path];
  return {ok:!!body,status:body ? 200 : 404,json:async()=>body};
};

const source = fs.readFileSync(new URL("admin.html", import.meta.url), "utf8");
const script = source.match(/<script>([\s\S]+)<\/script>/i)[1];
vm.runInThisContext(script, {filename:"admin.html"});
assert.equal(ids.get("access-panel").hidden, false);
ids.get("token").value = "admin";
await ids.get("token-form").dispatch("submit");
await new Promise(resolve => setTimeout(resolve, 0));

const text = node => node.textContent + node.children.map(text).join(" ");
assert.match(text(ids.get("queue-cards")), /queued.*running.*succeeded.*failed.*superseded/s);
const mystery = all.find(node => node.textContent === "mystery");
assert.ok(mystery && !mystery.className.includes("ok"), "unknown status must not be green");
assert.match(text(ids.get("inventory-notices")), /partial/i);
const repositoryRows = ids.get("repo-rows").children;
assert.equal(repositoryRows[0].children[1].textContent, "7");
assert.equal(repositoryRows[0].children[5].textContent, "—");
assert.equal(repositoryRows[1].children[5].textContent, "clone_failed");
const jobRows = ids.get("job-rows").children;
assert.equal(jobRows[0].children[2].textContent, "refs/heads/main");
assert.equal(jobRows[1].children[2].textContent, "—");
assert.equal(jobRows[0].children[7].textContent, "—");
assert.equal(jobRows[3].children[7].textContent, "index_failed");
const deliveryRows = ids.get("delivery-rows").children;
const processedAt = new Intl.DateTimeFormat(undefined,{dateStyle:"medium",timeStyle:"short"}).format(new Date("2026-01-02T00:00:00Z"));
assert.equal(deliveryRows[0].children[6].textContent, processedAt);
assert.equal(deliveryRows[1].children[6].textContent, "—");
const failedRepositoryRow = ids.get("repo-rows").children.find(row => text(row).includes("acme/failed"));
const repositoryRetry = failedRepositoryRow?.children.at(-1).children[0];
assert.equal(repositoryRetry?.textContent, "Retry", "failed repositories must be labeled Retry");
await repositoryRetry.dispatch("click");
assert.equal(requests.at(-1).path, "/v1/admin/repositories/8/reindex");
assert.equal(ids.get("admin-shell").hidden, false, "action 404 must not lock the console");
assert.equal(ids.get("access-panel").hidden, true, "action 404 must not report static mode");
assert.equal(storageRemovals, 0, "action 404 must not remove the token");

ids.get("dependency-repo").value = "7";
await ids.get("dependency-refresh").dispatch("submit");
const dependency = requests.find(request => request.path === "/v1/scip/dependencies/github");
assert.deepEqual(JSON.parse(dependency.options.body), {repository_id:7});

const file = {name:"index.scip"};
ids.get("scip-repo").value = "7";
ids.get("scip-commit").value = "a".repeat(40);
ids.get("scip-file").files = [file];
await ids.get("scip-upload").dispatch("submit");
const upload = requests.find(request => request.path.startsWith("/v1/scip/uploads?"));
assert.equal(upload.path, "/v1/scip/uploads?repository_id=7&commit=" + "a".repeat(40));
assert.equal(upload.options.headers.get("Content-Type"), "application/vnd.scip+protobuf");
assert.equal(upload.options.body, file);

const retry = all.find(node => node.textContent === "Retry" && node !== repositoryRetry);
await retry.dispatch("click");
assert.ok(requests.some(request => request.path === "/v1/admin/jobs/4/retry" && request.options.method === "POST"));

mutationDenial = 403;
await ids.get("github-reconcile").dispatch("click");
await new Promise(resolve => setTimeout(resolve, 0));
assert.equal(ids.get("admin-shell").hidden, true);
assert.equal(ids.get("access-panel").hidden, false);
assert.equal(storageRemovals, 1);
assert.equal(ids.get("repo-rows").children.length, 0);

mutationDenial = 0;
ids.get("token").value = "admin";
await ids.get("token-form").dispatch("submit");
await new Promise(resolve => setTimeout(resolve, 0));
mutationDenial = 401;
await ids.get("github-reconcile").dispatch("click");
await new Promise(resolve => setTimeout(resolve, 0));
assert.equal(ids.get("admin-shell").hidden, true);
assert.match(ids.get("access-message").textContent, /required or expired/i);
assert.equal(storageRemovals, 2);

mutationDenial = 0;
ids.get("token").value = "admin";
await ids.get("token-form").dispatch("submit");
await new Promise(resolve => setTimeout(resolve, 0));
delete responses["/v1/admin/overview"];
allowMutation = true;
await ids.get("github-reconcile").dispatch("click");
await new Promise(resolve => setTimeout(resolve, 0));
assert.equal(ids.get("admin-shell").hidden, true);
assert.match(ids.get("access-message").textContent, /static mode/i);
assert.equal(storageRemovals, 3);

responses["/v1/admin/overview"] = {repositories:{ready:1},jobs:{queued:1,running:1,succeeded:1,failed:1,superseded:1},deliveries:{succeeded:1},scip_uploads:1,dependencies:1,installations:1};
let resolveOverview;
delayedOverview = new Promise(resolve => { resolveOverview = resolve; });
ids.get("token").value = "admin";
await ids.get("token-form").dispatch("submit");
await new Promise(resolve => setTimeout(resolve, 0));
await ids.get("logout").dispatch("click");
const pendingOverview = requests.findLast(request => request.path === "/v1/admin/overview");
assert.equal(pendingOverview.options.signal.aborted, true, "locking must abort the active admin load");
assert.equal(ids.get("token").value, "", "locking must clear the bearer-token input");
assert.equal(ids.get("admin-shell").hidden, true, "locking must hide privileged content");
resolveOverview({ok:true,status:200,json:async()=>responses["/v1/admin/overview"]});
await new Promise(resolve => setTimeout(resolve, 0));
assert.equal(ids.get("admin-shell").hidden, true, "a stale load must not reopen privileged content after lock");
assert.equal(ids.get("access-panel").hidden, false, "a stale load must not hide the access panel after lock");

assert.doesNotMatch(source, /\.aside-foot\{display:none/);
