"use strict";

const previewContract = "runtime-manager-preview/v1";
const absentStatus = Object.freeze({
  contract: previewContract,
  manager: "missing",
  executor: "missing",
  remote_coverage: "unknown",
  supervisor_launch: "unsupported",
  complete_drain: false,
  production_transactions: false,
});
const planSteps = Object.freeze({
  enrollment: ["Review target and provisioning budget.", "Approve enrollment identity and transport.", "Authorize a separate provisioning window."],
  upgrade: ["Review artifact and rollback hashes.", "Qualify every admission and launch participant.", "Authorize a separate maintenance window."],
  rollback: ["Review the prior artifact and current transaction.", "Reconcile held admission and unknown children.", "Authorize a separate rollback window."],
  maintenance: ["Verify exact human owner across all registrations.", "Qualify backend, daemon and external launch fences.", "Authorize a separate maintenance window."],
});

function parsePreviewStatus(input) {
  if (!input || typeof input !== "object" || Array.isArray(input) ||
      Object.keys(input).length !== Object.keys(absentStatus).length ||
      Object.keys(input).some((key) => !Object.hasOwn(absentStatus, key)) ||
      input.contract !== previewContract ||
      !["missing", "unavailable"].includes(input.manager) ||
      !["missing", "unavailable"].includes(input.executor) ||
      !["unknown", "unsupported"].includes(input.remote_coverage) ||
      input.supervisor_launch !== "unsupported" ||
      input.complete_drain !== false || input.production_transactions !== false) {
    throw new Error("Unsupported preview status. Coverage remains unknown; no readiness or authority established.");
  }
  return { ...input };
}

function createProposal(operation, target, reason) {
  if (!Object.hasOwn(planSteps, operation)) throw new Error("Choose a supported proposal type.");
  if (typeof target !== "string" || !/^[A-Za-z0-9][A-Za-z0-9 _-]{0,63}$/.test(target)) {
    throw new Error("Use a non-secret target alias (1-64 letters, numbers, spaces, underscores or hyphens).");
  }
  if (typeof reason !== "string" || reason.trim().length < 10 || reason.length > 240 ||
      /[\x00-\x1f\x7f]|https?:|[\\/]|(?:bearer|token|password|secret)\s*[:=]|(?:mat_|mpi_|mpc_|whsec_)/i.test(reason)) {
    throw new Error("Use a 10-240 character reason without credentials, paths, URLs or control characters.");
  }
  return {
    contract: previewContract,
    kind: "change_request_proposal",
    operation,
    target_alias: target.trim(),
    reason: reason.trim(),
    authority: "none",
    submitted: false,
    complete_drain: false,
    production_transactions: false,
    prerequisites: [...planSteps[operation]],
  };
}

function mountPreview(document, port) {
  const root = document.getElementById("root");
  if (!root) throw new Error("Runtime manager surface root is missing.");
  const element = (tag, text, parent = root) => {
    const node = document.createElement(tag);
    if (text) node.textContent = text;
    parent.appendChild(node);
    return node;
  };
  root.style.cssText = "padding:14px;display:grid;gap:12px;color:var(--foreground);background:var(--background);font-size:var(--text-body,14px)";
  element("h2", "Runtime Manager Preview");
  element("p", "Offline preview. No approved service endpoint is connected.");
  const status = element("dl");
  status.style.cssText = "display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:6px;margin:0";
  function renderStatus(input) {
    const observation = parsePreviewStatus(input);
    status.replaceChildren();
    for (const [label, value] of [
      ["Manager", observation.manager === "missing" ? "Missing" : "Unavailable"],
      ["Executor", observation.executor === "missing" ? "Missing" : "Unavailable"],
      ["Remote coverage", observation.remote_coverage === "unknown" ? "Unknown" : "Unsupported"],
      ["Supervisor launch", "Unsupported"],
      ["Complete drain", "Incomplete"],
      ["Production transactions", "Disabled"],
    ]) {
      element("dt", label, status);
      element("dd", value, status);
    }
  }
  renderStatus(absentStatus);
  element("p", "Package installation and core-only capabilities do not establish readiness. Plugin, callback, tool and actor metadata grant no maintenance authority.");
  const form = element("form");
  form.style.cssText = "display:grid;gap:12px";
  const operationLabel = element("label", "Proposal type", form);
  const operation = element("select", "", operationLabel);
  operation.name = "operation";
  for (const [value, label] of [["enrollment", "Enrollment"], ["upgrade", "Upgrade"], ["rollback", "Rollback"], ["maintenance", "Maintenance"]]) {
    const option = element("option", label, operation);
    option.value = value;
  }
  operation.value = "enrollment";
  const targetLabel = element("label", "Target alias", form);
  const target = element("input", "", targetLabel);
  target.name = "target";
  target.maxLength = 64;
  target.required = true;
  const reasonLabel = element("label", "Reason", form);
  const reason = element("textarea", "", reasonLabel);
  reason.name = "reason";
  reason.maxLength = 240;
  reason.required = true;
  element("p", "Use non-secret aliases only. Do not enter credentials, private host details, paths or URLs.", form);
  const plan = element("ol", "", form);
  function renderPlan() {
    plan.replaceChildren();
    for (const step of planSteps[operation.value]) element("li", step, plan);
  }
  renderPlan();
  const button = element("button", "Create local draft", form);
  button.type = "submit";
  const notice = element("p");
  notice.setAttribute("role", "status");
  notice.setAttribute("aria-live", "polite");
  const draftLabel = element("label", "Change-request draft (not submitted)");
  const draft = element("textarea", "", draftLabel);
  draft.readOnly = true;
  draft.rows = 14;
  draft.style.width = "100%";
  for (const label of [operationLabel, targetLabel, reasonLabel, draftLabel]) {
    label.style.cssText = "display:grid;gap:4px;min-width:0";
  }
  element("p", "Copy manually for review. Drafts stay in this frame and are lost when it closes. No approval, submission, installation or execution occurs.");
  const resize = () => {
    if (port) port.postMessage({ id: "runtime-manager-size", kind: "ui.resize", height: document.body.scrollHeight + 16 });
  };
  const clearDraft = () => {
    draft.value = "";
    notice.textContent = "";
  };
  target.addEventListener("input", clearDraft);
  reason.addEventListener("input", clearDraft);
  operation.addEventListener("change", () => {
    clearDraft();
    renderPlan();
    resize();
  });
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    draft.value = "";
    try {
      draft.value = JSON.stringify(createProposal(operation.value, target.value, reason.value), null, 2);
      notice.textContent = "Local proposal only. Not submitted; no authority granted.";
    } catch (error) {
      notice.textContent = error.message;
    }
    resize();
  });
  if (port) {
    port.onmessage = (event) => {
      if (event.data?.kind !== "theme") return;
      for (const [key, value] of Object.entries(event.data.theme ?? {})) {
        if (key.startsWith("--") && typeof value === "string") document.documentElement.style.setProperty(key, value);
      }
    };
    port.start();
  } else {
    notice.textContent = "Host bridge unavailable. Offline draft preview only.";
  }
  resize();
  return { renderStatus };
}

if (typeof document !== "undefined") {
  const port = globalThis.__multicaPluginBridgePortV2;
  delete globalThis.__multicaPluginBridgePortV2;
  mountPreview(document, typeof MessagePort !== "undefined" && port instanceof MessagePort ? port : null);
}
