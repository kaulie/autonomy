package autonomy

// uiHomeHTML is autonomy's own UI, served at GET / : one shell that hosts the modules.
//
// The modules are the pages this runtime already serves (GET /dashboard, GET /accounts) — each
// one a self-contained page that talks to its own JSON API — and the shell embeds the selected
// one with ?embed=1, which tells a module to drop its own title because the shell owns it. That
// keeps one rule from the rest of this UI: a page is the string it is, no build step, no asset
// pipeline, and adding a module is adding a page plus one entry in the nav below.
//
// The header carries what a person wants before choosing a module at all: which LLM backend this
// runtime runs on, which pool account pays for it, and how many turns the store holds (/health).
const uiHomeHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Autonomy</title>
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; }
  body { font: 14px/1.4 -apple-system, Segoe UI, Roboto, sans-serif; margin: 0; background: #f6f7f9; color: #1c1e21; height: 100vh; display: flex; flex-direction: column; }
  header { background: #fff; border-bottom: 1px solid #eaecf0; padding: 10px 18px; display: flex; align-items: baseline; gap: 16px; flex-wrap: wrap; }
  h1 { font-size: 16px; margin: 0; }
  .status { color: #667085; font-size: 12px; }
  .status code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  .main { flex: 1; display: flex; min-height: 0; }
  nav { width: 200px; flex: none; background: #fff; border-right: 1px solid #eaecf0; padding: 10px; }
  nav button { display: block; width: 100%; text-align: left; font: inherit; padding: 8px 10px; margin-bottom: 4px; border: 0; border-radius: 6px; background: transparent; color: #344054; cursor: pointer; }
  nav button:hover { background: #f2f4f7; }
  nav button.active { background: #e8f0fe; color: #1c4ed8; font-weight: 600; }
  nav .note { color: #98a2b3; font-size: 12px; padding: 8px 10px 0; }
  .content { flex: 1; min-width: 0; display: flex; flex-direction: column; }
  .bar { padding: 6px 14px; border-bottom: 1px solid #eaecf0; background: #fcfcfd; display: flex; gap: 12px; align-items: center; font-size: 12px; color: #667085; }
  .bar a { color: #1c4ed8; }
  iframe { flex: 1; width: 100%; border: 0; background: #f6f7f9; }
</style>
</head>
<body>
<header>
  <h1>Autonomy</h1>
  <div class="status" id="status">…</div>
</header>
<div class="main">
  <nav id="nav">
    <button data-module="dashboard">Agent status</button>
    <button data-module="accounts">Harness accounts</button>
    <div class="note">Modules are pages of this runtime; each opens on its own path too.</div>
  </nav>
  <div class="content">
    <div class="bar">
      <span id="path">/dashboard</span>
      <a id="open" href="/dashboard" target="_blank" rel="noopener">open in a tab</a>
    </div>
    <iframe id="frame" title="module"></iframe>
  </div>
</div>
<script>
// The modules this shell hosts: the path each one is served at. Adding a module is adding a page
// and a line here (and a button above).
const MODULES = { dashboard: "/dashboard", accounts: "/accounts" };

const $ = (id) => document.getElementById(id);

function show(name) {
  if (!MODULES[name]) name = "dashboard";
  const path = MODULES[name];
  document.querySelectorAll("#nav button").forEach((b) => {
    b.classList.toggle("active", b.dataset.module === name);
  });
  // ?embed=1 tells the module to drop its own title: this shell owns the page.
  $("frame").src = path + "?embed=1";
  $("path").textContent = path;
  $("open").href = path;
  document.title = "Autonomy · " + name;
}

$("nav").addEventListener("click", (event) => {
  const button = event.target.closest("button[data-module]");
  if (!button) return;
  location.hash = button.dataset.module;
});

window.addEventListener("hashchange", () => show(location.hash.replace(/^#/, "")));

// /health answers what someone wants before picking a module: which backend, on whose account,
// and how much has happened.
async function refreshStatus() {
  try {
    const res = await fetch("/health", { cache: "no-store" });
    const health = await res.json();
    const parts = [];
    if (health.llm_backend) parts.push("backend <code>" + health.llm_backend + "</code>");
    if (health.llm_model) parts.push("model <code>" + health.llm_model + "</code>");
    if (health.llm_account) {
      parts.push("account <code>" + health.llm_account + "</code>" + (health.llm_account_label ? " (" + health.llm_account_label + ")" : ""));
    } else {
      parts.push('<strong>no account in the pool</strong> — add one under Harness accounts');
    }
    if (typeof health.turns === "number") parts.push(health.turns + " turns");
    $("status").innerHTML = parts.join(" · ");
  } catch (err) {
    $("status").textContent = "runtime unreachable: " + err.message;
  }
}

show(location.hash.replace(/^#/, "") || "dashboard");
refreshStatus();
setInterval(refreshStatus, 5000);
</script>
</body>
</html>
`
