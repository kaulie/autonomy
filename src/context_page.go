package autonomy

// contextPageHTML is the Context Service page served at GET /context (src/http_server.go).
//
// Same shape as the accounts page: one self-contained file — no build step, no bundler, no
// external asset — whose whole job is to call the Context Service's operator API
// (src/context_write.go and its handlers in http_server.go) and render the answer: register a
// knowledge base by kind + provider (filesystem/local, source_code_repository/git-local),
// as one file or a recursive collection, sync it into the index, list a project's resources,
// search their sections, read one section, delete a resource. Agents still only read,
// through context.search / get / list.
//
// The Context Service needs the postgres store engine; without it every call answers 503
// INDEX_FAILED, and the page says that instead of showing an empty table as if it were the truth.
const contextPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Autonomy · Context</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 14px/1.4 -apple-system, Segoe UI, Roboto, sans-serif; margin: 0; padding: 20px; background: #f6f7f9; color: #1c1e21; }
  /* Embedded in the UI shell (GET /?embed=1): the shell owns the page title. */
  body.embedded h1, body.embedded .sub { display: none; }
  h1 { font-size: 18px; margin: 0 0 4px; }
  h2 { font-size: 14px; margin: 18px 0 8px; color: #344054; }
  .sub { color: #667085; margin-bottom: 14px; }
  table { border-collapse: collapse; width: 100%; background: #fff; box-shadow: 0 1px 2px rgba(16,24,40,.06); }
  th, td { text-align: left; padding: 8px 10px; border-bottom: 1px solid #eaecf0; vertical-align: middle; }
  th { background: #f9fafb; font-size: 12px; text-transform: uppercase; letter-spacing: .03em; color: #475467; }
  code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
  .pill { display: inline-block; padding: 1px 8px; border-radius: 999px; font-size: 12px; font-weight: 600; background: #f2f4f7; color: #344054; }
  button { font: inherit; padding: 3px 9px; border: 1px solid #d0d5dd; background: #fff; border-radius: 6px; cursor: pointer; }
  button:hover { background: #f9fafb; }
  button.primary { background: #1c4ed8; border-color: #1c4ed8; color: #fff; }
  button.danger { color: #b42318; border-color: #fda29b; }
  form, .panel { background: #fff; padding: 14px; margin-bottom: 16px; box-shadow: 0 1px 2px rgba(16,24,40,.06); }
  .grid > div { padding: 9px 0; border-bottom: 1px dashed #eaecf0; }
  .grid > div:last-child { border-bottom: none; }
  .formtitle { font-size: 13px; font-weight: 600; color: #344054; margin-bottom: 2px; }
  .actions { border-top: 1px solid #eaecf0; padding-top: 12px; margin-top: 4px; }
  label { display: block; font-size: 12px; color: #475467; margin-bottom: 3px; }
  input[type=text], select { width: 100%; padding: 5px 7px; border: 1px solid #d0d5dd; border-radius: 6px; box-sizing: border-box; }
  .row { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
  .row input[type=text] { flex: 1; width: auto; min-width: 200px; }
  .check { display: flex; gap: 6px; align-items: center; font-size: 13px; color: #344054; }
  .shape { display: flex; gap: 10px; flex-wrap: wrap; }
  .shape label.check { padding: 8px 10px; border: 1px solid #d0d5dd; border-radius: 8px; background: #f9fafb; cursor: pointer; }
  .shape input { width: auto; margin: 0; }
  form.dir .only-file, form.file .only-dir { display: none; }
  form.no-provider .needs-provider { opacity: .5; pointer-events: none; }
  .msg { margin: 10px 0; padding: 8px 10px; border-radius: 6px; display: none; white-space: pre-wrap; }
  .msg.ok { display: block; background: #e6f4ea; color: #1a7f37; }
  .msg.err { display: block; background: #fde8e8; color: #b42318; }
  .hint { color: #667085; font-size: 12px; }
  .result { padding: 8px 10px; border-bottom: 1px solid #eaecf0; cursor: pointer; }
  .result:hover { background: #f9fafb; }
  .result .score { float: right; color: #667085; font-size: 12px; }
  pre.content { white-space: pre-wrap; background: #f9fafb; padding: 10px; border-radius: 6px; margin: 8px 0 0; font-size: 12px; }
</style>
</head>
<body>
<h1>Context</h1>
<div class="sub">
  A project's context space. Pick a knowledge base (filesystem, source code
  repository, cloud storage) and a provider, then register one file or a recursive
  collection. Sync into the full-text index, search sections and read one.
  Needs the postgres store engine.
</div>

<div id="msg" class="msg"></div>

<div class="panel">
  <div class="row">
    <label for="f-project" style="margin:0">project_id</label>
    <input type="text" id="f-project" value="project-749a0238">
    <button type="button" id="reload">Load</button>
  </div>
</div>

<h2>Resources</h2>
<table>
  <thead><tr><th>name</th><th>type</th><th>source</th><th>revision</th><th>last sync</th><th></th></tr></thead>
  <tbody id="rows"><tr><td colspan="6" class="hint">loading…</td></tr></tbody>
</table>

<h2>Register</h2>
<form id="register" class="file" autocomplete="off">
  <div class="grid">
    <div>
      <div class="formtitle">knowledge base</div>
      <div class="shape" id="f-kind">
        <label class="check"><input type="radio" name="f-kind" value="filesystem" checked> filesystem</label>
        <label class="check"><input type="radio" name="f-kind" value="source_code_repository"> source code repository</label>
        <label class="check"><input type="radio" name="f-kind" value="cloud"> cloud storage</label>
      </div>
    </div>
    <div>
      <label for="f-provider">provider</label>
      <select id="f-provider"></select>
      <div class="hint" id="f-provider-hint"></div>
    </div>
    <div class="needs-provider">
      <div class="formtitle">shape</div>
      <div class="shape" id="f-shape">
        <label class="check"><input type="radio" name="f-shape" value="file" checked> single file</label>
        <label class="check"><input type="radio" name="f-shape" value="directory"> recursive directory</label>
      </div>
      <div class="hint only-file">One document on this provider.</div>
      <div class="hint only-dir">Recursively. Skips hidden dirs, <code>node_modules</code>. Suffixes empty = all document types (<code>.md</code>, <code>.markdown</code>).</div>
    </div>
    <div class="only-file needs-provider"><label for="f-name">name</label><input type="text" id="f-name" placeholder="runtime.md"></div>
    <div class="only-file needs-provider">
      <label for="f-type">type</label>
      <select id="f-type">
        <option value="document">document</option>
        <option value="repository">repository</option>
        <option value="service">service</option>
      </select>
    </div>
    <div class="needs-provider">
      <label for="f-location"><span class="only-file">file path</span><span class="only-dir">directory path</span></label>
      <input type="text" id="f-location" placeholder="/path/to/doc.md">
    </div>
    <div class="only-dir needs-provider">
      <label for="f-suffixes">suffixes <span class="hint">(optional; empty = all)</span></label>
      <input type="text" id="f-suffixes" placeholder="md, markdown">
      <div class="hint">Comma-separated extensions to keep. Leave empty to search every available document.</div>
    </div>
    <div class="only-file needs-provider"><label for="f-path">path <span class="hint">(inside a repository, optional)</span></label><input type="text" id="f-path"></div>
    <div class="only-file needs-provider"><label for="f-uri">uri <span class="hint">(optional)</span></label><input type="text" id="f-uri"></div>
    <div class="needs-provider"><label class="check"><input type="checkbox" id="f-sync" checked> sync after register</label></div>
  </div>
  <div class="actions"><button type="submit" class="primary" id="f-register">Register</button></div>
</form>

<h2>Search</h2>
<form id="search" autocomplete="off">
  <div class="row">
    <input type="text" id="f-query" placeholder="search terms">
    <button type="submit">Search</button>
  </div>
  <div id="results"></div>
  <div id="section"></div>
</form>

<script>
const $ = (id) => document.getElementById(id);
// A module embedded in the UI shell drops its own title (?embed=1).
if (new URLSearchParams(location.search).has("embed")) document.body.classList.add("embedded");

function say(text, kind) {
  const box = $("msg");
  box.className = "msg " + (kind || "ok");
  box.textContent = text;
}

// api answers the parsed body, or throws the Context Service's typed error: its code, its
// message, and — for the 503 a runtime without the postgres store engine answers — what to do.
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
    let msg = res.status + " " + (parsed && parsed.code ? parsed.code + ": " : "") +
      ((parsed && (parsed.message || parsed.error || parsed.raw)) || res.statusText);
    if (res.status === 503) {
      msg += "\nThe Context Service requires the postgres store engine (AUTONOMY_STORE_ENGINE=postgres).";
    }
    throw new Error(msg);
  }
  return parsed;
}

function esc(value) {
  const map = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };
  return String(value == null ? "" : value).replace(/[&<>"']/g, (c) => map[c]);
}

function project() { return $("f-project").value.trim(); }

const BACKENDS = {
  filesystem: [{ id: "local", hint: "A path on the runtime host." }],
  source_code_repository: [{ id: "git-local", hint: "A git checkout on the runtime host. Host providers (github, gitlab, gitea) are not implemented yet." }],
  cloud: [],
};

function knowledgeKind() {
  const picked = document.querySelector('input[name="f-kind"]:checked');
  return picked ? picked.value : "filesystem";
}

function shape() {
  const picked = document.querySelector('input[name="f-shape"]:checked');
  return picked ? picked.value : "file";
}

function applyKind() {
  const kind = knowledgeKind();
  const list = BACKENDS[kind] || [];
  const sel = $("f-provider");
  sel.innerHTML = list.length
    ? list.map((p) => '<option value="' + p.id + '">' + p.id + "</option>").join("")
    : '<option value="">none implemented</option>';
  sel.disabled = !list.length;
  $("f-provider-hint").textContent = list.length
    ? list[0].hint
    : "No cloud provider is implemented yet (s3, gcs, azure, oss).";
  $("register").classList.toggle("no-provider", !list.length);
  $("f-register").disabled = !list.length;
  applyShape();
}

function applyShape() {
  const form = $("register");
  const kind = shape();
  form.classList.toggle("file", kind === "file");
  form.classList.toggle("dir", kind === "directory");
  const kb = knowledgeKind();
  $("f-location").placeholder = kind === "directory"
    ? (kb === "source_code_repository" ? "/path/to/checkout" : "/path/to/docs")
    : "/path/to/doc.md";
}

document.querySelectorAll('input[name="f-kind"]').forEach((el) => el.addEventListener("change", applyKind));
document.querySelectorAll('input[name="f-shape"]').forEach((el) => el.addEventListener("change", applyShape));
applyKind();

function sourceText(s) {
  if (!s) return "";
  return [s.kind, s.provider, s.type, s.location, s.repository, s.path].filter(Boolean).join(" · ");
}

let resources = [];

async function load() {
  if (!project()) { say("project_id is required", "err"); return; }
  try {
    const out = await api("GET", "/api/context/resources?project_id=" + encodeURIComponent(project()));
    resources = (out && out.resources) || [];
    render();
  } catch (e) {
    resources = [];
    $("rows").innerHTML = '<tr><td colspan="6" class="hint">could not load resources</td></tr>';
    say(e.message, "err");
  }
}

function render() {
  const rows = $("rows");
  if (!resources.length) {
    rows.innerHTML = '<tr><td colspan="6" class="hint">no resources registered in this project yet — register one below</td></tr>';
    return;
  }
  rows.innerHTML = resources.map((r) =>
    "<tr>" +
      "<td>" + esc(r.name || r.id) + '<div class="hint"><code>' + esc(r.id) + "</code></div></td>" +
      '<td><span class="pill">' + esc(r.type) + "</span></td>" +
      "<td><code>" + esc(sourceText(r.source)) + "</code></td>" +
      "<td><code>" + esc(r.revision || (r.checksum ? r.checksum.slice(0, 12) : "—")) + "</code></td>" +
      "<td>" + esc(r.checksum ? new Date(r.updated_at).toLocaleString() : "never") + "</td>" +
      '<td><button type="button" data-sync="' + esc(r.id) + '">Sync</button> ' +
      '<button type="button" class="danger" data-delete="' + esc(r.id) + '">Delete</button></td>' +
    "</tr>").join("");
}

async function syncResource(id) {
  const out = await api("POST", "/api/context/resources/" + encodeURIComponent(id) + "/sync");
  return out || {};
}

$("rows").addEventListener("click", async (event) => {
  const syncBtn = event.target.closest("button[data-sync]");
  const delBtn = event.target.closest("button[data-delete]");
  try {
    if (syncBtn) {
      const out = await syncResource(syncBtn.dataset.sync);
      say("synced " + syncBtn.dataset.sync + ": " + (out.status || "ok") +
        (out.sections != null ? " (" + out.sections + " sections)" : ""));
      await load();
    } else if (delBtn) {
      const id = delBtn.dataset.delete;
      if (!confirm("Delete resource " + id + " and its indexed sections? The source itself is not touched.")) return;
      await api("DELETE", "/api/context/resources/" + encodeURIComponent(id));
      say("deleted " + id);
      await load();
    }
  } catch (e) {
    say(e.message, "err");
  }
});

$("register").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!project()) { say("project_id is required", "err"); return; }
  const kb = knowledgeKind();
  const provider = $("f-provider").value.trim();
  if (!provider) { say("this knowledge base has no implemented provider yet", "err"); return; }
  const kind = shape();
  const location = $("f-location").value.trim();
  if (!location) { say(kind === "directory" ? "directory path is required" : "file path is required", "err"); return; }
  const body = {
    project_id: project(),
    kind: kb,
    provider: provider,
    shape: kind,
    source: { location: location },
  };
  if (kind === "file") {
    body.name = $("f-name").value.trim();
    body.type = $("f-type").value;
    body.uri = $("f-uri").value.trim();
    body.source.path = $("f-path").value.trim();
  } else {
    const suffixes = $("f-suffixes").value.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean);
    if (suffixes.length) body.suffixes = suffixes;
  }
  try {
    const created = await api("POST", "/api/context/resources", body);
    const items = kind === "directory" ? ((created && created.resources) || []) : (created && created.id ? [created] : []);
    if (!items.length) { say("registered nothing", "err"); return; }
    let text = kind === "directory"
      ? "registered " + items.length + " files under " + location
      : "registered " + created.id;
    if ($("f-sync").checked) {
      let sections = 0, synced = 0;
      try {
        for (const item of items) {
          const out = await syncResource(item.id);
          synced += 1;
          if (out.sections) sections += Number(out.sections) || 0;
        }
        text += ", synced " + synced + (sections ? " (" + sections + " sections)" : "");
      } catch (e) {
        await load();
        say(text + ", but sync failed: " + e.message, "err");
        return;
      }
    }
    say(text);
    $("register").reset();
    applyKind();
    await load();
  } catch (e) {
    say(e.message, "err");
  }
});

$("search").addEventListener("submit", async (event) => {
  event.preventDefault();
  $("section").innerHTML = "";
  if (!project()) { say("project_id is required", "err"); return; }
  const q = $("f-query").value.trim();
  if (!q) { say("type something to search for", "err"); return; }
  try {
    const out = await api("GET", "/api/context/search?project_id=" + encodeURIComponent(project()) +
      "&q=" + encodeURIComponent(q));
    const results = (out && out.results) || [];
    $("results").innerHTML = results.length ? results.map((r) =>
      '<div class="result" data-section="' + esc(r.section_id) + '">' +
        '<span class="score">' + esc(Number(r.score).toFixed(3)) + "</span>" +
        "<strong>" + esc(r.heading || "(untitled)") + "</strong> " +
        '<span class="hint">' + esc(r.resource_name || r.resource_id) + "</span>" +
        '<div class="hint">' + esc(r.snippet) + "</div>" +
      "</div>").join("") : '<div class="hint" style="padding:8px 0">no matching sections</div>';
  } catch (e) {
    $("results").innerHTML = "";
    say(e.message, "err");
  }
});

$("results").addEventListener("click", async (event) => {
  const hit = event.target.closest("[data-section]");
  if (!hit) return;
  try {
    const s = await api("GET", "/api/context/sections/" + encodeURIComponent(hit.dataset.section));
    $("section").innerHTML = '<div class="panel" style="margin-top:10px">' +
      "<strong>" + esc(s.heading || "(untitled)") + "</strong> " +
      '<span class="hint">' + esc(s.path || "") + " · lines " + esc(s.start_line) + "–" + esc(s.end_line) +
      (s.revision ? " · rev <code>" + esc(s.revision) + "</code>" : "") + "</span>" +
      '<div class="hint">' + esc(sourceText(s.source)) + "</div>" +
      '<pre class="content">' + esc(s.content) + "</pre></div>";
  } catch (e) {
    say(e.message, "err");
  }
});

$("reload").addEventListener("click", load);
$("f-project").addEventListener("keydown", (event) => { if (event.key === "Enter") load(); });
load();
</script>
</body>
</html>
`
