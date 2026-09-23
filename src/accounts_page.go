package autonomy

// accountsPageHTML is the account-pool page served at GET /accounts (src/http_server.go).
//
// Same shape as the agent dashboard: one self-contained file — no build step, no bundler, no
// external asset — whose whole job is to call the pool's API (src/accounts_service.go) and
// render the answer. The one rule it enforces in the UI is the one the API enforces in the
// store: a key is written once and never read back, so an edit never carries the current
// secret, only a field that sets a new one (blank = leave it alone).
//
// An account without a key is normal for cline and codex: those harnesses fall back to the
// credentials their own CLI saved (`cline auth`, `codex auth`), which is what makes a machine
// that already authenticated usable without pasting a secret anywhere.
const accountsPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Autonomy · Harness Accounts</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 14px/1.4 -apple-system, Segoe UI, Roboto, sans-serif; margin: 0; padding: 20px; background: #f6f7f9; color: #1c1e21; }
  h1 { font-size: 18px; margin: 0 0 4px; }
  .sub { color: #667085; margin-bottom: 14px; }
  a { color: #1c4ed8; }
  table { border-collapse: collapse; width: 100%; background: #fff; box-shadow: 0 1px 2px rgba(16,24,40,.06); }
  th, td { text-align: left; padding: 8px 10px; border-bottom: 1px solid #eaecf0; vertical-align: middle; }
  th { background: #f9fafb; font-size: 12px; text-transform: uppercase; letter-spacing: .03em; color: #475467; }
  code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
  .pill { display: inline-block; padding: 1px 8px; border-radius: 999px; font-size: 12px; font-weight: 600; }
  .on { background: #e6f4ea; color: #1a7f37; }
  .off { background: #eef0f2; color: #475467; }
  .def { background: #e8f0fe; color: #1c4ed8; }
  .harness { background: #f2f4f7; color: #344054; }
  button { font: inherit; padding: 3px 9px; border: 1px solid #d0d5dd; background: #fff; border-radius: 6px; cursor: pointer; }
  button:hover { background: #f9fafb; }
  button.primary { background: #1c4ed8; border-color: #1c4ed8; color: #fff; }
  button.danger { color: #b42318; border-color: #fda29b; }
  form { background: #fff; padding: 14px; margin-bottom: 16px; box-shadow: 0 1px 2px rgba(16,24,40,.06); }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 10px; }
  label { display: block; font-size: 12px; color: #475467; margin-bottom: 3px; }
  input[type=text], input[type=password], select { width: 100%; padding: 5px 7px; border: 1px solid #d0d5dd; border-radius: 6px; }
  .row { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; margin-top: 10px; }
  .check { display: flex; gap: 6px; align-items: center; font-size: 13px; color: #344054; }
  .msg { margin: 10px 0; padding: 8px 10px; border-radius: 6px; display: none; white-space: pre-wrap; }
  .msg.ok { display: block; background: #e6f4ea; color: #1a7f37; }
  .msg.err { display: block; background: #fde8e8; color: #b42318; }
  .hint { color: #667085; font-size: 12px; }
  details { margin-top: 10px; }
</style>
</head>
<body>
<h1>Harness accounts</h1>
<div class="sub">
  One account = one harness (<code>cursor</code> / <code>cline</code> / <code>codex</code>) + one vendor + one credential.
  The runtime resolves an agent's credentials from this pool — never from the environment —
  and a task that names an account runs on it (<a href="/dashboard">agent status</a>).
</div>

<div id="msg" class="msg"></div>

<form id="editor" autocomplete="off">
  <div class="grid">
    <div>
      <label for="f-harness">harness</label>
      <select id="f-harness">
        <option value="cursor">cursor</option>
        <option value="cline">cline</option>
        <option value="codex">codex</option>
      </select>
    </div>
    <div>
      <label for="f-vendor">vendor <span class="hint" id="vendorhint">(the model supplier)</span></label>
      <select id="f-vendor"></select>
    </div>
    <div>
      <label for="f-label">label</label>
      <input type="text" id="f-label" placeholder="cursor main">
    </div>
    <div>
      <label for="f-key">api key <span class="hint" id="keyhint">(blank = use the CLI's own auth)</span></label>
      <input type="password" id="f-key" placeholder="sk-…">
    </div>
    <div>
      <label for="f-base">base url</label>
      <input type="text" id="f-base" placeholder="https://…（可省）">
    </div>
    <div>
      <label for="f-model">model <span class="hint">(optional: what the harness defaults to)</span></label>
      <input type="text" id="f-model" list="model-options" placeholder="">
      <datalist id="model-options"></datalist>
    </div>
    <div>
      <label for="f-root">agent root workspace <span class="hint">(exclusive: one account per root)</span></label>
      <input type="text" id="f-root" placeholder="每个账号独占一个根目录；留空 = 运行时默认根（也只能一个账号用）">
    </div>
  </div>
  <div class="row">
    <label class="check"><input type="checkbox" id="f-enabled" checked> enabled</label>
    <label class="check"><input type="checkbox" id="f-default"> default for its harness</label>
    <button class="primary" id="save" type="submit">Add account</button>
    <button type="button" id="cancel" style="display:none">Cancel edit</button>
    <span class="hint" id="mode">new account</span>
  </div>
</form>

<table>
  <thead>
    <tr><th>harness</th><th>vendor</th><th>label</th><th>key</th><th>model</th><th>agent root</th><th>state</th><th>actions</th></tr>
  </thead>
  <tbody id="rows"><tr><td colspan="7" class="hint">loading…</td></tr></tbody>
</table>

<div class="row">
  <label class="check"><input type="checkbox" id="verify-live"> live verify runs one short turn on that account (spends a little of its quota)</label>
</div>

<script>
const $ = (id) => document.getElementById(id);
let accounts = [];
let editing = null;
let lastVerify = null;

function say(text, kind) {
  const box = $("msg");
  box.className = "msg " + (kind || "ok");
  box.textContent = text;
}

async function api(method, path, body) {
  const res = await fetch(path, {
    method: method,
    headers: body ? { "content-type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let parsed = null;
  try { parsed = text ? JSON.parse(text) : null; } catch (e) { parsed = { raw: text }; }
  if (!res.ok) {
    throw new Error((parsed && (parsed.error || parsed.message)) || (res.status + " " + res.statusText));
  }
  return parsed;
}

function esc(value) {
  const map = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };
  return String(value == null ? "" : value).replace(/[&<>"']/g, (c) => map[c]);
}

function render() {
  const rows = $("rows");
  if (!accounts.length) {
    rows.innerHTML = '<tr><td colspan="8" class="hint">the pool is empty — add an account (nothing can run without one)</td></tr>';
    return;
  }
  rows.innerHTML = accounts.map((a) => {
    const state = [
      a.enabled ? '<span class="pill on">enabled</span>' : '<span class="pill off">disabled</span>',
      a.isDefault ? '<span class="pill def">default</span>' : "",
    ].join(" ");
    const verify = (lastVerify && lastVerify.accountId === a.accountId)
      ? '<tr><td colspan="8"><div class="msg ' + (lastVerify.ok ? "ok" : "err") + '">' +
        esc(lastVerify.detail) + (lastVerify.text ? " → " + esc(lastVerify.text) : "") + "</div></td></tr>"
      : "";
    return "<tr>" +
      '<td><span class="pill harness">' + esc(a.harness) + "</span></td>" +
      "<td><code>" + esc(a.vendor) + "</code></td>" +
      "<td>" + esc(a.label) + "</td>" +
      "<td><code>" + esc(a.apiKeyMasked || (a.hasKey ? "••••" : "—")) + "</code>" +
      (a.hasKey ? "" : ' <span class="hint">cli auth</span>') + "</td>" +
      "<td>" + esc(a.model || "—") + "</td>" +
      "<td><code>" + esc(a.agentRootWorkspace || "runtime default") + "</code></td>" +
      "<td>" + state + "</td>" +
      '<td style="white-space:nowrap">' +
        '<button data-act="edit" data-id="' + esc(a.accountId) + '">edit</button> ' +
        '<button data-act="toggle" data-id="' + esc(a.accountId) + '" data-on="' + (a.enabled ? "0" : "1") + '">' + (a.enabled ? "disable" : "enable") + "</button> " +
        '<button data-act="default" data-id="' + esc(a.accountId) + '">make default</button> ' +
        '<button data-act="verify" data-id="' + esc(a.accountId) + '">verify</button> ' +
        '<button class="danger" data-act="delete" data-id="' + esc(a.accountId) + '">delete</button>' +
      "</td></tr>" + verify;
  }).join("");
}

async function load() {
  try {
    const data = await api("GET", "/api/accounts");
    accounts = (data && data.accounts) || [];
    render();
  } catch (err) {
    say("could not read the pool: " + err.message, "err");
  }
}

// The vendor list comes from the runtime (GET /api/accounts/vendors): it asks the harness itself,
// so a human picks from what that harness can actually talk to instead of typing an identifier.
// The model list is the same idea one level down (GET /api/accounts/models) — and it may be
// empty, because some harnesses resolve the model by themselves, which is why the field stays
// typeable.
async function loadVendors(harness, accountId, keep) {
  const select = $("f-vendor");
  try {
    const query = "?harness=" + encodeURIComponent(harness) + (accountId ? "&accountId=" + encodeURIComponent(accountId) : "");
    const data = await api("GET", "/api/accounts/vendors" + query);
    let vendors = (data && data.vendors) || [];
    const current = keep || select.value;
    if (current && !vendors.includes(current)) vendors = [current].concat(vendors);
    select.innerHTML = vendors.map((v) => '<option value="' + esc(v) + '">' + esc(v) + "</option>").join("");
    select.value = current || (data && data.default) || (vendors[0] || "");
    $("vendorhint").textContent = "(" + vendors.length + " for " + harness + ")";
  } catch (err) {
    $("vendorhint").textContent = "(could not read the vendor list: " + err.message + ")";
  }
  await loadModels();
}

async function loadModels() {
  const harness = $("f-harness").value;
  const vendor = $("f-vendor").value;
  const list = $("model-options");
  list.innerHTML = "";
  if (!vendor) return;
  try {
    const data = await api("GET", "/api/accounts/models?harness=" + encodeURIComponent(harness) + "&vendor=" + encodeURIComponent(vendor));
    const models = (data && data.models) || [];
    list.innerHTML = models.map((m) => '<option value="' + esc(m) + '"></option>').join("");
  } catch (err) {
    /* suggestions are a convenience: a broken catalogue must not break the form */
  }
}

function fillForm(a) {
  $("f-harness").value = a ? a.harness : "cursor";
  loadVendors($("f-harness").value, a ? a.accountId : "", a ? (a.vendor || "") : "");
  $("f-label").value = a ? a.label : "";
  $("f-key").value = "";
  $("f-base").value = a ? (a.baseUrl || "") : "";
  $("f-model").value = a ? (a.model || "") : "";
  $("f-root").value = a ? (a.agentRootWorkspace || "") : "";
  $("f-enabled").checked = a ? !!a.enabled : true;
  $("f-default").checked = a ? !!a.isDefault : false;
  $("keyhint").textContent = a ? "(blank = keep the current key)" : "(blank = use the CLI's own auth)";
}

$("editor").addEventListener("submit", async (event) => {
  event.preventDefault();
  const body = {
    harness: $("f-harness").value,
    vendor: $("f-vendor").value.trim(),
    label: $("f-label").value.trim(),
    apiKey: $("f-key").value.trim(),
    baseUrl: $("f-base").value.trim(),
    model: $("f-model").value.trim(),
    agentRootWorkspace: $("f-root").value.trim(),
    enabled: $("f-enabled").checked,
    isDefault: $("f-default").checked,
  };
  try {
    if (editing) {
      await api("PATCH", "/api/accounts/" + encodeURIComponent(editing), body);
      say("updated " + body.label);
    } else {
      await api("POST", "/api/accounts", body);
      say("added " + body.label);
    }
    editing = null;
    $("save").textContent = "Add account";
    $("cancel").style.display = "none";
    $("mode").textContent = "new account";
    fillForm(null);
    await load();
  } catch (err) {
    say("save failed: " + err.message, "err");
  }
});

$("f-harness").addEventListener("change", () => {
  loadVendors($("f-harness").value, editing || "", "");
});

$("f-vendor").addEventListener("change", () => {
  loadModels();
});

$("cancel").addEventListener("click", () => {
  editing = null;
  $("save").textContent = "Add account";
  $("cancel").style.display = "none";
  $("mode").textContent = "new account";
  fillForm(null);
});

$("rows").addEventListener("click", async (event) => {
  const button = event.target.closest("button[data-act]");
  if (!button) return;
  const id = button.dataset.id;
  const account = accounts.find((a) => a.accountId === id);
  if (!account) return;
  try {
    if (button.dataset.act === "edit") {
      editing = id;
      fillForm(account);
      $("save").textContent = "Save changes";
      $("cancel").style.display = "";
      $("mode").textContent = "editing " + account.label;
      return;
    }
    if (button.dataset.act === "toggle") {
      await api("PATCH", "/api/accounts/" + encodeURIComponent(id), { enabled: button.dataset.on === "1" });
      say((button.dataset.on === "1" ? "enabled " : "disabled ") + account.label);
    }
    if (button.dataset.act === "default") {
      await api("PATCH", "/api/accounts/" + encodeURIComponent(id), { isDefault: true });
      say(account.label + " is now the default for " + account.harness);
    }
    if (button.dataset.act === "delete") {
      if (!confirm("delete " + account.label + "? tasks that named it fall back to the pool")) return;
      await api("DELETE", "/api/accounts/" + encodeURIComponent(id));
      say("deleted " + account.label);
    }
    if (button.dataset.act === "verify") {
      say("probing " + account.label + "…");
      const live = $("verify-live").checked ? "?live=1" : "";
      lastVerify = await api("POST", "/api/accounts/" + encodeURIComponent(id) + "/verify" + live);
      say((lastVerify.ok ? "verify ok: " : "verify failed: ") + lastVerify.detail +
        (lastVerify.text ? " → " + lastVerify.text : ""), lastVerify.ok ? "ok" : "err");
    }
    await load();
  } catch (err) {
    say("action failed: " + err.message, "err");
  }
});

loadVendors("cursor", "", "");
load();
</script>
</body>
</html>
`
