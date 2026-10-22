"use strict";

const { test } = require("node:test");
const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const vm = require("node:vm");

const source = readFileSync(join(__dirname, "main.js"), "utf8");
const fixture = (name) => JSON.parse(readFileSync(join(__dirname, "..", "fixtures", `${name}.json`), "utf8"));
const plain = (value) => JSON.parse(JSON.stringify(value));

class Element {
  constructor(tag) {
    this.tagName = tag;
    this.children = [];
    this.listeners = {};
    this.attributes = {};
    this.style = { setProperty: (key, value) => { this.style[key] = value; } };
    this.value = "";
    this._text = "";
  }
  appendChild(child) {
    this.children.push(child);
    if (this.tagName === "select" && this.children.length === 1) this.value = child.value;
    return child;
  }
  replaceChildren(...children) { this.children = children; this._text = ""; }
  set textContent(value) { this._text = value; this.children = []; }
  get textContent() { return this._text + this.children.map((child) => child.textContent).join(" "); }
  setAttribute(key, value) { this.attributes[key] = value; }
  addEventListener(key, callback) { this.listeners[key] = callback; }
  dispatch(key) {
    let prevented = false;
    this.listeners[key]({ preventDefault: () => { prevented = true; } });
    return prevented;
  }
}

function harness({ bridge = true, mount = true } = {}) {
  const forbidden = () => { throw new Error("Forbidden network, persistence or lifecycle side effect"); };
  const root = new Element("main");
  const document = {
    getElementById: (id) => id === "root" ? root : null,
    createElement: (tag) => new Element(tag),
    body: { scrollHeight: 500 },
    documentElement: new Element("html"),
  };
  const messages = [];
  class Port {
    start() { this.started = true; }
    postMessage(message) {
      assert.equal(message.kind, "ui.resize", "UI attempted a host action");
      messages.push(message);
    }
  }
  const port = new Port();
  const context = vm.createContext({
    ...(mount ? { document } : {}),
    ...(bridge ? { __multicaPluginBridgePortV2: port } : {}),
    MessagePort: Port,
    fetch: forbidden, XMLHttpRequest: forbidden, WebSocket: forbidden,
    localStorage: { getItem: forbidden, setItem: forbidden },
    sessionStorage: { getItem: forbidden, setItem: forbidden },
    navigator: { sendBeacon: forbidden, clipboard: { writeText: forbidden } },
    process: { exit: forbidden }, require: forbidden,
    setTimeout: forbidden, setInterval: forbidden,
  });
  vm.runInContext(source, context, { timeout: 1000 });
  const all = (node = root) => [node, ...node.children.flatMap((child) => all(child))];
  const find = (predicate) => {
    const result = all().find(predicate);
    assert.ok(result, "Expected accessible fixture control");
    return result;
  };
  return { root, document, port, context, messages, find, all };
}

test("shipped surface shows missing services and every incomplete capability", () => {
  const h = harness();
  for (const text of ["Missing", "Unknown", "Unsupported", "Incomplete", "Disabled", "No approved service endpoint"]) {
    assert.match(h.root.textContent, new RegExp(text));
  }
  assert.equal(h.port.started, true);
  assert.equal(h.context.__multicaPluginBridgePortV2, undefined);
  assert.ok(h.messages.length > 0);
  assert.ok(h.messages.every((message) => message.kind === "ui.resize"));
  const notice = h.find((node) => node.attributes.role === "status");
  assert.equal(notice.attributes["aria-live"], "polite");
  assert.equal(h.find((node) => node.readOnly === true).value, "");
});

test("fixture matrix keeps manager/executor absence and remote coverage explicit", () => {
  const h = harness({ mount: false });
  for (const name of ["missing-manager", "missing-executor", "unavailable"]) {
    const status = fixture(name);
    assert.deepEqual(plain(h.context.parsePreviewStatus(status)), status);
    const view = h.context.mountPreview(h.document, h.port);
    view.renderStatus(status);
    assert.match(h.root.textContent, /Supervisor launch Unsupported/);
    assert.match(h.root.textContent, /Complete drain Incomplete/);
    assert.match(h.root.textContent, /Production transactions Disabled/);
    h.root.replaceChildren();
  }
});

test("preview parser rejects capability-only, privileged and malformed observations", async (t) => {
  const h = harness({ mount: false });
  const base = fixture("missing-manager");
  const cases = {
    null: null, array: [], empty: {},
    "core-only": { contract: "multica-lifecycle/v1", backend_admission: "supported", daemon_admission: "unknown", complete_drain: false },
    "wrong-major": { ...base, contract: "runtime-manager-preview/v2" },
    ready: { ...base, manager: "ready" },
    launch: { ...base, supervisor_launch: "supported" },
    drain: { ...base, complete_drain: true },
    transaction: { ...base, production_transactions: true },
    coverage: { ...base, remote_coverage: "supported" },
    "truthy-string": { ...base, complete_drain: "false" },
    credentials: { ...base, token: "fixture-only" },
    actor: { ...base, actor: { type: "member", approved: true } },
  };
  for (const [name, input] of Object.entries(cases)) {
    await t.test(name, () => assert.throws(() => h.context.parsePreviewStatus(input), /Unsupported preview status/));
  }
});

test("all supported plans produce proposals, never grants or submissions", () => {
  const h = harness({ mount: false });
  for (const operation of ["enrollment", "upgrade", "rollback", "maintenance"]) {
    const result = plain(h.context.createProposal(operation, "worker alias", "Review this proposed change"));
    assert.equal(result.operation, operation);
    assert.equal(result.kind, "change_request_proposal");
    assert.equal(result.authority, "none");
    assert.equal(result.submitted, false);
    assert.equal(result.complete_drain, false);
    assert.equal(result.production_transactions, false);
    assert.equal(result.prerequisites.length, 3);
    for (const key of ["actor", "token", "holder", "generation", "command", "approved"]) {
      assert.equal(Object.hasOwn(result, key), false);
    }
  }
});

test("proposal validation rejects unsafe identifiers, secrets and unsupported operations", async (t) => {
  const h = harness({ mount: false });
  const cases = [
    ["release", "alias", "Review the proposal"],
    ["constructor", "alias", "Review the proposal"],
    ["upgrade", "", "Review the proposal"],
    ["upgrade", "a".repeat(65), "Review the proposal"],
    ["upgrade", "<script>", "Review the proposal"],
    ["upgrade", "alias", "short"],
    ["upgrade", "alias", "x".repeat(241)],
    ["upgrade", "alias", "Review\nthis proposal"],
    ["upgrade", "alias", "Use password=fixture"],
    ["upgrade", "alias", "Use bearer: fixture"],
    ["upgrade", "alias", "Use token=fixture"],
    ["upgrade", "alias", "Use mat_fixture value"],
    ["upgrade", "alias", "Visit https://example.test"],
    ["upgrade", "alias", "Review C:\\private\\path"],
  ];
  for (const [index, args] of cases.entries()) {
    await t.test(`invalid-${index}`, () => assert.throws(() => h.context.createProposal(...args)));
  }
});

test("proposal boundaries accept exact limits and reject non-string or trimmed-short input", () => {
  const h = harness({ mount: false });
  for (const length of [10, 240]) {
    const result = h.context.createProposal("upgrade", "a".repeat(64), "r".repeat(length));
    assert.equal(result.target_alias.length, 64);
    assert.equal(result.reason.length, length);
  }
  for (const value of [null, true, 42, {}, []]) {
    assert.throws(() => h.context.createProposal("upgrade", value, "Review the proposal"));
    assert.throws(() => h.context.createProposal("upgrade", "alias", value));
  }
  assert.throws(() => h.context.createProposal("upgrade", "alias", "r".repeat(9)));
  assert.throws(() => h.context.createProposal("upgrade", "alias", "    short    "));
});

test("form creates a local text-only draft and clears it on errors or plan changes", () => {
  const h = harness();
  const form = h.find((node) => node.tagName === "form");
  const target = h.find((node) => node.name === "target");
  const reason = h.find((node) => node.name === "reason");
  const operation = h.find((node) => node.name === "operation");
  const output = h.find((node) => node.readOnly);
  const notice = h.find((node) => node.attributes.role === "status");
  target.value = "worker alias";
  reason.value = "Review <img onerror=alert(1)> as text";
  assert.equal(form.dispatch("submit"), true);
  assert.equal(JSON.parse(output.value).reason, reason.value);
  assert.match(notice.textContent, /Not submitted; no authority granted/);
  assert.equal(h.all().some((node) => node.tagName === "img" || node.tagName === "script"), false);
  target.value = "another alias";
  target.dispatch("input");
  assert.equal(output.value, "");
  form.dispatch("submit");
  reason.dispatch("input");
  assert.equal(output.value, "");
  form.dispatch("submit");
  operation.value = "rollback";
  operation.dispatch("change");
  assert.equal(output.value, "");
  assert.match(h.root.textContent, /prior artifact/);
  reason.value = "Use secret=fixture";
  form.dispatch("submit");
  assert.equal(output.value, "");
  assert.match(notice.textContent, /without credentials/);
});

test("bridge metadata and core-only responses cannot change status or authorize actions", () => {
  const h = harness();
  const before = h.root.textContent;
  for (const message of [
    { kind: "action", ok: true, data: { approved: true } },
    { kind: "status", data: { contract: "multica-lifecycle/v1", backend_admission: "supported" } },
    { kind: "grant", actor: "owner", token: "mpc_fixture", operation: "release" },
    { kind: "installed", ready: true },
    { kind: "disabled" }, { kind: "uninstalled" },
  ]) h.port.onmessage({ data: message });
  assert.equal(h.root.textContent, before);
  h.port.onmessage({ data: { kind: "theme", theme: { "--foreground": "#fff" } } });
  assert.equal(h.document.documentElement.style["--foreground"], "#fff");
  assert.ok(h.messages.every((message) => message.kind === "ui.resize"));
});

test("mount, remove, disable and remount discard drafts without lifecycle or OS operations", () => {
  for (const state of ["installed", "disabled", "removed"]) {
    const h = harness();
    h.find((node) => node.name === "target").value = "alias";
    h.find((node) => node.name === "reason").value = "Review the proposed change";
    h.find((node) => node.tagName === "form").dispatch("submit");
    assert.ok(h.find((node) => node.readOnly).value);
    h.port.onmessage({ data: { kind: state } });
    h.root.replaceChildren();
    assert.ok(h.messages.every((message) => message.kind === "ui.resize"));
    const reopened = harness();
    assert.equal(reopened.find((node) => node.readOnly).value, "");
    assert.match(reopened.root.textContent, /Missing/);
  }
});

test("missing bridge stays explicitly offline and never falls back to a network call", () => {
  const h = harness({ bridge: false });
  assert.match(h.root.textContent, /Host bridge unavailable/);
  assert.equal(h.messages.length, 0);
});
