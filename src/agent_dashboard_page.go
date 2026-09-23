package autonomy

// agentDashboardHTML is the agent-status monitoring page served at GET /dashboard
// (src/http_server.go). It is deliberately one self-contained file: no build step,
// no bundler, no external asset — the page's whole job is to fetch GET /api/agents
// on a timer and render the answer as a table, so it ships as the string it is.
//
// The poll interval is the refresh box (default 3s); a page that cannot reach the
// feed says so instead of silently going stale, because "the dashboard is frozen"
// and "nothing is happening" must not look the same to the person watching.
const agentDashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Autonomy · Agent Status</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 14px/1.4 -apple-system, Segoe UI, Roboto, sans-serif; margin: 0; padding: 20px; background: #f6f7f9; color: #1c1e21; }
  /* Embedded in the UI shell (GET /?embed=1): the shell owns the page title, so a module drops
     its own instead of printing it twice. */
  body.embedded h1, body.embedded .sub { display: none; }
  h1 { font-size: 18px; margin: 0 0 4px; }
  .sub { color: #667085; margin-bottom: 14px; }
  .bar { display: flex; gap: 14px; align-items: center; flex-wrap: wrap; margin-bottom: 12px; }
  .bar label { color: #475467; }
  .bar input[type=number] { width: 64px; padding: 4px 6px; }
  .pill { display: inline-block; padding: 1px 8px; border-radius: 999px; font-size: 12px; font-weight: 600; }
  .state-running { background: #e6f4ea; color: #1a7f37; }
  .state-idle { background: #eef0f2; color: #475467; }
  .state-deleted { background: #fde8e8; color: #b42318; }
  .health-ok { color: #1a7f37; }
  .health-deleted { color: #b42318; }
  .work-yes { color: #1a7f37; font-weight: 600; }
  .work-no { color: #98a2b3; }
  table { border-collapse: collapse; width: 100%; background: #fff; box-shadow: 0 1px 2px rgba(16,24,40,.06); border-radius: 8px; overflow: hidden; }
  /* One agent per row: a row never wraps, so "who is this agent" reads in one line; a table wider
     than the window scrolls sideways rather than folding cells onto extra lines. */
  .tablewrap { overflow-x: auto; }
  th, td { text-align: left; padding: 8px 10px; border-bottom: 1px solid #eaecf0; vertical-align: middle; white-space: nowrap; }
  td.ellipsis { max-width: 260px; overflow: hidden; text-overflow: ellipsis; }
  th { background: #fafbfc; font-size: 12px; text-transform: uppercase; letter-spacing: .04em; color: #667085; }
  tr:last-child td { border-bottom: 0; }
  .muted { color: #98a2b3; }
  .run { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; color: #475467; word-break: break-all; }
  #status { color: #667085; }
  #status.err { color: #b42318; font-weight: 600; }
  .empty { padding: 18px; color: #667085; }
  button.link { font: inherit; padding: 1px 7px; margin-right: 4px; border: 1px solid #d0d5dd; background: #fff; border-radius: 6px; cursor: pointer; color: #1c4ed8; }
  button.link:disabled { color: #98a2b3; border-color: #eaecf0; cursor: default; }
</style>
</head>
<body>
<h1>Autonomy · Agent Status</h1>
<div class="sub">Live status of the agents this runtime knows about, polled from <code>/api/agents</code>.</div>
<div class="bar">
  <span id="status">loading…</span>
  <span id="every">auto-refresh: …</span>
  <span class="muted" id="everyhint">(interval and on/off are in the top bar)</span>
  <label><input id="includeDeleted" type="checkbox"> include deleted</label>
  <button id="refresh">Refresh now</button>
</div>
<div class="tablewrap">
<table>
  <thead>
    <tr>
      <th>ID</th><th>Name</th><th>Role</th><th>Lifecycle</th><th>State</th>
      <th>Health</th><th>Current task</th><th>Working</th><th>Provider / model</th><th>Account</th><th>Run id</th><th>Actions</th>
    </tr>
  </thead>
  <tbody id="rows"><tr><td class="empty" colspan="12">loading…</td></tr></tbody>
</table>
</div>
<script>
// A module embedded in the UI shell drops its own title (?embed=1).
if (new URLSearchParams(location.search).has("embed")) document.body.classList.add("embedded");
(function () {
  var rows = document.getElementById('rows');
  var status = document.getElementById('status');
  // The refresh control lives in the shell's top bar, which passes what it chose: every=<seconds>,
  // 0 meaning "no auto-refresh". Opened on its own, the page polls every 5s.
  var every = new URLSearchParams(location.search).has('every') ? Number(new URLSearchParams(location.search).get('every')) : 5;
  var includeDeleted = document.getElementById('includeDeleted');
  var timer = null;

  function esc(v) {
    return String(v == null ? '' : v)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }
  function dash(v) { return (v == null || v === '') ? '\u2014' : esc(v); }

  function render(agents) {
    if (!agents || !agents.length) {
      rows.innerHTML = '<tr><td class="empty" colspan="12">no agents</td></tr>';
      return;
    }
    rows.innerHTML = agents.map(function (a) {
      var state = a.state || 'idle';
      var health = a.health || 'ok';
      var working = a.working
        ? '<span class="work-yes">yes</span>'
        : '<span class="work-no">no</span>';
      var provider = [a.llm_provider, a.model].filter(Boolean).join(' / ');
      var account = a.account || '-';
      return '<tr>' +
        '<td>' + esc(a.agent_id) + '</td>' +
        '<td>' + dash(a.name) + '</td>' +
        '<td>' + dash(a.role) + '</td>' +
        '<td>' + dash(a.lifecycle) + '</td>' +
        '<td><span class="pill state-' + esc(state) + '">' + esc(state) + '</span></td>' +
        '<td class="health-' + esc(health) + '">' + esc(health) + '</td>' +
        '<td class="run">' + dash(a.current_task) + '</td>' +
        '<td>' + working + '</td>' +
        '<td>' + dash(provider) + '</td>' +
        '<td>' + dash(account) + '</td>' +
        '<td class="run ellipsis">' + dash(a.agent_run_id) + '</td>' +
        '<td>' +
          '<button class="link" data-agent="' + esc(a.agent_id) + '" data-task="' + esc(a.current_task || '') + '" data-view="events"' +
            ((a.current_task || '') ? '' : ' disabled title="no current task: an event stream belongs to a task"') + '>events</button>' +
          '<button class="link" data-agent="' + esc(a.agent_id) + '" data-task="' + esc(a.current_task || '') + '" data-view="messages">messages</button>' +
        '</td>' +
      '</tr>';
    }).join('');
  }

  function tick() {
    var url = '/api/agents';
    if (includeDeleted.checked) { url += '?include_deleted=1'; }
    fetch(url, { headers: { 'Accept': 'application/json' } })
      .then(function (r) {
        if (!r.ok) { throw new Error('HTTP ' + r.status); }
        return r.json();
      })
      .then(function (data) {
        render(data.agents);
        status.className = '';
        status.textContent = data.count + ' agent(s) · updated ' + new Date().toLocaleTimeString();
      })
      .catch(function (err) {
        status.className = 'err';
        status.textContent = 'feed unavailable: ' + err.message;
      });
  }

  function schedule() {
    if (timer) { clearInterval(timer); }
    timer = null;
    document.getElementById('every').textContent = every > 0 ? 'auto-refresh: ' + every + 's' : 'auto-refresh: off';
    if (every > 0) { timer = setInterval(tick, every * 1000); }
  }

  // A row's entries open one agent's views. Embedded in the shell they navigate it (so the nav and
  // the header stay); opened on their own the page goes to the view's own path.
  function openAgentView(agentId, taskId, view) {
    var path = '/agents/' + encodeURIComponent(agentId) + '/' + view;
    if (view === 'events' && taskId) { path += '?task=' + encodeURIComponent(taskId); }
    if (window.top && window.top !== window) { window.top.location.hash = '#' + path; return; }
    location.href = path;
  }

  document.getElementById('rows').addEventListener('click', function (event) {
    var button = event.target.closest('button[data-view]');
    if (!button || button.disabled) { return; }
    openAgentView(button.dataset.agent, button.dataset.task, button.dataset.view);
  });
  includeDeleted.addEventListener('change', tick);
  document.getElementById('refresh').addEventListener('click', tick);

  tick();
  schedule();
})();
</script>
</body>
</html>
`
