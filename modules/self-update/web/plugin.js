// The web face of self-update: one "update" button in the top bar plus the panel it opens.
//
// This file belongs to modules/self-update, not to the UI: the UI (web-dashboard) only
// gives one top-bar slot and a host (the contract is the "front-end plugin" section in
// web-dashboard/api.go); what gets drawn here, what gets asked and what gets said is this
// module's business. All copy comes from this module's extension endpoint (translated on
// the backend), and the styles use only the UI's design tokens (a skin change carries
// over).
//
// No framework, no build step: one ES module, embedded in the binary and shipped as is.

const STYLE = `
.su { position: relative; }
.su-dot { display: inline-block; width: 7px; height: 7px; border-radius: 50%;
  background: var(--warn); margin-left: 6px; vertical-align: middle; }
.su-panel { position: absolute; right: 0; top: calc(100% + 6px); z-index: 50;
  width: min(380px, 90vw); padding: 12px 14px; background: var(--panel);
  color: var(--ink); border: 1px solid var(--line); border-radius: var(--radius-sm);
  box-shadow: 0 8px 24px rgb(0 0 0 / 25%); display: grid; gap: 8px; }
.su-panel[hidden] { display: none; }
.su-version { font-family: var(--mono); font-size: 13px; word-break: break-all; }
.su-dim { color: var(--dim); font-size: 12px; }
.su-result { font-size: 13px; line-height: 1.4; }
.su-result[data-tone="ok"] { color: var(--ok); }
.su-result[data-tone="warn"] { color: var(--warn); }
.su-result[data-tone="bad"] { color: var(--danger); }
.su-row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.su-row a { color: var(--accent); }
`;

function h(tag, props = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v);
  }
  el.append(...children);
  return el;
}

export function mount(el, host) {
  const style = h("style", {}, STYLE);
  const dot = h("span", { class: "su-dot", hidden: "" });
  const button = h("button", { type: "button" });
  const label = document.createTextNode("…");
  button.append(label, dot);

  const title = h("div", { class: "su-dim" });
  const version = h("div", { class: "su-version" });
  const source = h("div", { class: "su-dim" });
  const result = h("div", { class: "su-result" });
  const when = h("div", { class: "su-dim" });
  const check = h("button", { type: "button" });
  const releases = h("a", { target: "_blank", rel: "noopener noreferrer" });
  const install = h("div", { class: "su-dim" });
  const panel = h("div", { class: "su-panel", hidden: "" },
    title, version, source, result, when, h("div", { class: "su-row" }, check, releases), install);
  const root = h("div", { class: "su" }, style, button, panel);
  el.append(root);

  let doc = null;
  let busy = false;

  function render() {
    if (!doc) return;
    const L = doc.labels;
    label.textContent = L.button;
    title.textContent = L.title;
    version.textContent = doc.version;
    source.textContent = doc.source;
    check.textContent = busy ? L.checking : L.check;
    check.disabled = busy;
    releases.hidden = !doc.releases_url;
    if (doc.releases_url) {
      releases.href = doc.releases_url;
      releases.textContent = `${L.releases} ↗`;
    }
    const last = doc.last;
    dot.hidden = !(last && last.available);
    install.hidden = !(last && last.available);
    install.textContent = L.install;
    if (!last) {
      result.textContent = L.never;
      result.dataset.tone = "";
      when.textContent = "";
    } else if (last.error) {
      result.textContent = `${L.failed}: ${last.error}`;
      result.dataset.tone = "bad";
      when.textContent = last.ago;
    } else {
      result.textContent = `${last.available ? L.available : L.current} — ${last.message}`;
      result.dataset.tone = last.available ? "warn" : "ok";
      when.textContent = last.ago;
    }
  }

  async function call(action) {
    try {
      doc = await host.ext("self-update", { action });
    } catch (err) {
      // The endpoint itself is unreachable (the daemon was swapped for a build without
      // this module): say it in the panel, don't pop up a dialog.
      if (doc) doc = { ...doc, last: { error: String(err.message ?? err), ago: "" } };
    }
    render();
  }

  async function runCheck() {
    busy = true;
    render();
    await call("check");
    busy = false;
    render();
  }

  button.addEventListener("click", () => {
    panel.hidden = !panel.hidden;
  });
  check.addEventListener("click", () => void runCheck());
  const outside = (e) => {
    if (!root.contains(e.target)) panel.hidden = true;
  };
  document.addEventListener("mousedown", outside);

  void call("status").then(() => {
    // If it hasn't been checked for a long time, check once on its own (the interval is
    // decided by the backend, see autoCheckEvery in web.go).
    if (doc && doc.check_due) void runCheck();
  });

  return () => {
    document.removeEventListener("mousedown", outside);
    root.remove();
  };
}
