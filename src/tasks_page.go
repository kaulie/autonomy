package autonomy

// tasksPageHTML is the task page served at GET /tasks (src/http_server.go): the module a
// human opens to send an instruction and to see where the tasks that came of it stand.
//
// Same shape as the rest of this UI — one self-contained file, no build step, no external
// asset — and its two halves are the two questions a person has:
//
//   - the form is POST /api/tasks: an instruction, the task it belongs to (blank asks for a
//     new one), and the pool account that pays for it. That last field is the point of this
//     page: which harness, vendor, model and credential a task runs on is a pool choice
//     (src/accounts.go), and choosing it at the moment of asking is how a caller configures
//     it per task instead of by editing the deployment's environment. "pool default" keeps
//     the choice where it was — with the pool (src/agent_account.go).
//   - the list is GET /api/tasks, and a row opens GET /api/tasks/{id}: the status, the error
//     that came with it, and `state` — the newest round, what the completion contract still
//     misses, and, when the runtime itself cut the run (a restart), where the cut landed
//     (src/task_record.go). That is what a person came to find out.
//
// It polls on the interval the shell passes (?every=, 0 meaning off) and never rewrites the
// form while it does: an instruction being typed is not the page's to clear.
const tasksPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Autonomy · Tasks</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 14px/1.4 -apple-system, Segoe UI, Roboto, sans-serif; margin: 0; padding: 20px; background: #f6f7f9; color: #1c1e21; }
  body.embedded h1, body.embedded .sub { display: none; }
  h1 { font-size: 18px; margin: 0 0 4px; }
  .sub { color: #667085; margin-bottom: 14px; }
  a { color: #1c4ed8; }
  table { border-collapse: collapse; width: 100%; background: #fff; box-shadow: 0 1px 2px rgba(16,24,40,.06); }
  th, td { text-align: left; padding: 8px 10px; border-bottom: 1px solid #eaecf0; vertical-align: middle; white-space: nowrap; }
  th { background: #f9fafb; font-size: 12px; text-transform: uppercase; letter-spacing: .03em; color: #475467; }
  code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
  .tablewrap { overflow-x: auto; }
  .pill { display: inline-block; padding: 1px 8px; border-radius: 999px; font-size: 12px; font-weight: 600; }
  .st-running { background: #e8f0fe; color: #1c4ed8; }
  .st-pending { background: #f2f4f7; color: #344054; }
  .st-completed { background: #e6f4ea; color: #1a7f37; }
  .st-unverified { background: #fff4e5; color: #b54708; }
  .st-blocked, .st-error { background: #fde8e8; color: #b42318; }
  .st-need_input { background: #fef0c7; color: #b54708; }
  .st-stopped { background: #eef0f2; color: #475467; }
  button { font: inherit; padding: 3px 9px; border: 1px solid #d0d5dd; background: #fff; border-radius: 6px; cursor: pointer; }
  button:hover { background: #f9fafb; }
  button.primary { background: #1c4ed8; border-color: #1c4ed8; color: #fff; }
  button.link { border: 0; background: transparent; color: #1c4ed8; padding: 0 8px 0 0; }
  form { background: #fff; padding: 14px; margin-bottom: 16px; box-shadow: 0 1px 2px rgba(16,24,40,.06); }
  .grid { display: block; }
  .grid > div { padding: 9px 0; border-bottom: 1px dashed #eaecf0; }
  .grid > div:last-child { border-bottom: none; }
  .formtitle { font-size: 13px; font-weight: 600; color: #344054; margin-bottom: 2px; }
  .actions { border-top: 1px solid #eaecf0; padding-top: 12px; margin-top: 4px; }
  label { display: block; font-size: 12px; color: #475467; margin-bottom: 3px; }
  input[type=text], select, textarea { width: 100%; padding: 5px 7px; border: 1px solid #d0d5dd; border-radius: 6px; font: inherit; }
  textarea { resize: vertical; }
  .row { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; margin-top: 10px; }
  .msg { margin: 10px 0; padding: 8px 10px; border-radius: 6px; display: none; white-space: pre-wrap; }
  .msg.ok { display: block; background: #e6f4ea; color: #1a7f37; }
  .msg.err { display: block; background: #fde8e8; color: #b42318; }
  .hint { color: #667085; font-size: 12px; }
  .trunc { max-width: 420px; overflow: hidden; text-overflow: ellipsis; }
  tr.row { cursor: pointer; }
  tr.row:hover { background: #fcfcfd; }
  .card { background: #fff; margin-top: 16px; padding: 14px; box-shadow: 0 1px 2px rgba(16,24,40,.06); }
  .card h2 { font-size: 14px; margin: 0 0 8px; }
  .card .kv { color: #475467; font-size: 13px; margin-bottom: 6px; }
  .card .kv b { color: #1c1e21; font-weight: 600; }
  .warn { background: #fff4e5; color: #b54708; padding: 8px 10px; border-radius: 6px; margin: 8px 0; }
  .dim { color: #98a2b3; }
  .runs { margin-top: 8px; }
</style>
</head>
<body>
<h1>Tasks</h1>
<div class="sub">
  One instruction becomes a run on the agent that owns the task. The account decides which
  harness, vendor, model and credential pay for it — never the environment
  (<a href="/accounts">harness accounts</a>, <a href="/dashboard">agent status</a>).
</div>

<div id="msg" class="msg"></div>

<form id="instruction" autocomplete="off">
  <div class="formtitle">Send an instruction</div>
  <div class="grid">
    <div>
      <label for="f-description">instruction <span class="hint">(what the agent should do)</span></label>
      <textarea id="f-description" rows="3" placeholder="部署到测试环境，提交 commit，提 PR"></textarea>
    </div>
    <div>
      <label for="f-task">task id <span class="hint">(blank = a new task; an existing id continues that one)</span></label>
      <input type="text" id="f-task" placeholder="task-&#8230;">
    </div>
    <div>
      <label for="f-account">account <span class="hint">(the pool entry this task runs on)</span></label>
      <select id="f-account"></select>
    </div>
  </div>
  <div class="row actions">
    <button class="primary" id="send" type="submit">Send instruction</button>
    <button type="button" id="clear">Clear</button>
    <span class="hint" id="accounthint"></span>
  </div>
</form>

<div class="tablewrap">
<table>
  <thead>
    <tr><th>Task</th><th>Status</th><th>Instruction</th><th>Agent</th><th>Turns</th><th>Updated</th><th>Actions</th></tr>
  </thead>
  <tbody id="rows"><tr><td class="hint" colspan="7">loading&#8230;</td></tr></tbody>
</table>
</div>

<div id="detail"></div>
<script>
// A module embedded in the UI shell drops its own title (?embed=1).
if (new URLSearchParams(location.search).has("embed")) document.body.classList.add("embedded");
(function () {
  var rows = document.getElementById("rows");
  var detail = document.getElementById("detail");
  var select = document.getElementById("f-account");
  // The refresh control lives in the shell's top bar, which passes what it chose: every=<seconds>,
  // 0 meaning "no auto-refresh". Opened on its own, the page polls every 10s.
  var every = new URLSearchParams(location.search).has("every")
    ? Number(new URLSearchParams(location.search).get("every")) : 10;
  var accounts = [];
  var openTask = "";
  var timer = null;

  function esc(v) {
    return String(v == null ? "" : v)
      .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }
  function dash(v) { return (v == null || v === "") ? "—" : esc(v); }
  function say(text, kind) {
    var box = document.getElementById("msg");
    box.className = "msg " + (kind || "ok");
    box.textContent = text;
  }
  function api(method, path, body) {
    return fetch(path, {
      method: method,
      cache: "no-store",
      headers: body ? { "content-type": "application/json" } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    }).then(function (res) {
      return res.text().then(function (text) {
        var parsed = null;
        try { parsed = text ? JSON.parse(text) : null; } catch (e) { parsed = { raw: text }; }
        if (!res.ok) {
          throw new Error((parsed && (parsed.error || parsed.message)) || (res.status + " " + res.statusText));
        }
        return parsed;
      });
    });
  }
  // A detail view belongs to the shell when embedded (its nav and header stay), and to this
  // page when opened on its own.
  function nav(path) {
    if (window.top && window.top !== window) { window.top.location.hash = "#" + path; return; }
    location.href = path;
  }
  function openAgentView(agentID, taskID, view) {
    if (!agentID) { return; }
    var path = "/agents/" + encodeURIComponent(agentID) + "/" + view;
    if (view === "events" && taskID) { path += "?task=" + encodeURIComponent(taskID); }
    nav(path);
  }

  // ---- the account dropdown: the pool, as GET /api/accounts renders it (masked) ----------
  function loadAccounts() {
    return api("GET", "/api/accounts").then(function (res) {
      accounts = (res && res.accounts) || [];
      renderAccounts();
    }).catch(function () {
      // An unreadable pool is not a reason to stop: the runtime answers the same way either
      // way, and a task sent with no account still resolves through the pool.
      accounts = [];
      renderAccounts();
    });
  }
  function renderAccounts() {
    var chosen = select.value;
    var html = '<option value="">pool default</option>';
    accounts.forEach(function (a) {
      var bits = [a.harness, a.vendor, a.model].filter(Boolean).join("/");
      html += '<option value="' + esc(a.accountId) + '">' + esc(a.label + " — " + bits +
        (a.isDefault ? " · default" : "") + (a.enabled ? "" : " (disabled)")) + "</option>";
    });
    select.innerHTML = html;
    if (chosen) { select.value = chosen; }
    accountHint();
  }
  function chosenAccount() {
    var id = select.value;
    if (!id) { return null; }
    for (var i = 0; i < accounts.length; i++) { if (accounts[i].accountId === id) { return accounts[i]; } }
    return null;
  }
  // accountHint answers "what will this task actually run on", before it is sent: the pool's
  // own resolution when nothing is picked, and the picked account's harness/vendor/model/root
  // when one is.
  function accountHint() {
    var hint = document.getElementById("accounthint");
    if (!accounts.length) {
      hint.textContent = "the pool is empty — add an account under Harness accounts, or the task is refused";
      return;
    }
    var a = chosenAccount();
    if (!a) {
      hint.textContent = "the pool resolves it when the task runs: its default account for this runtime's harness";
      return;
    }
    hint.textContent = "runs on " + a.harness + "/" + a.vendor + " · model " +
      (a.model || "the harness default") + " · root " + (a.agentRootWorkspace || "runtime default") +
      (a.enabled ? "" : " · ⚠ disabled: the runtime refuses it");
  }

  // ---- the list --------------------------------------------------------------------------
  function renderRows(tasks) {
    if (!tasks || !tasks.length) {
      rows.innerHTML = '<tr><td class="hint" colspan="7">no tasks yet — send an instruction above</td></tr>';
      return;
    }
    rows.innerHTML = tasks.map(function (t) {
      var actions = '<button class="link" data-open="' + esc(t.id) + '">open</button>' +
        '<button class="link" data-agent="' + esc(t.agent_id) + '" data-task="' + esc(t.id) +
        '" data-view="events"' + (t.agent_id ? "" : ' disabled title="no agent yet"') + ">events</button>" +
        '<button class="link" data-agent="' + esc(t.agent_id) + '" data-task="' + esc(t.id) +
        '" data-view="messages"' + (t.agent_id ? "" : ' disabled title="no agent yet"') + ">messages</button>";
      return '<tr class="row" data-task="' + esc(t.id) + '">' +
        "<td><code>" + esc(t.id) + "</code></td>" +
        '<td><span class="pill st-' + esc(t.status) + '">' + esc(t.status) + "</span></td>" +
        '<td class="trunc" title="' + esc(t.description) + '">' + dash(t.description) + "</td>" +
        "<td>" + dash(t.agent_id) + "</td>" +
        "<td>" + dash(t.turns) + "</td>" +
        "<td>" + dash(t.updated_at || t.last_at) + "</td>" +
        "<td>" + actions + "</td>" +
        "</tr>";
    }).join("");
  }

  // ---- one task's detail: where it stands, in the record's own words ----------------------
  // A step's status is ok / failed / pending (src/task_record.go): the pill classes are this
  // page's, so the mapping from one vocabulary to the other lives here and nowhere else.
  function stepPill(status) {
    if (status === "ok") { return "st-completed"; }
    if (status === "failed") { return "st-error"; }
    return "st-pending";
  }
  function roundHTML(round) {
    if (!round) { return ""; }
    var html = '<div class="kv"><b>round ' + esc(round.cycle) + "</b> · decision <code>" +
      esc(round.decision) + "</code> · " + esc(round.status) + (round.at ? " · " + esc(round.at) : "") + "</div>";
    if (round.reason) { html += '<div class="kv">' + esc(round.reason) + "</div>"; }
    var steps = (round.steps || []).map(function (s) {
      var output = s.output ? JSON.stringify(s.output) : "";
      return "<tr><td>" + esc(s.idx) + "</td><td>" + dash(s.name) + "</td><td><code>" + esc(s.capability) +
        '</code></td><td><span class="pill ' + stepPill(s.status) + '">' + esc(s.status) + "</span></td>" +
        '<td class="trunc" title="' + esc(output) + '">' + dash(output) + "</td><td>" + dash(s.error) + "</td></tr>";
    }).join("");
    if (steps) {
      html += '<table><thead><tr><th>#</th><th>step</th><th>capability</th><th>status</th>' +
        "<th>output</th><th>error</th></tr></thead><tbody>" + steps + "</tbody></table>";
    }
    return html;
  }
  function renderDetail(progress) {
    if (!progress) { detail.innerHTML = ""; return; }
    var html = "<h2>Task <code>" + esc(progress.task_id) + "</code> " +
      '<span class="pill st-' + esc(progress.status) + '">' + esc(progress.status) + "</span></h2>" +
      '<div class="kv">' + esc(progress.description) + "</div>" +
      '<div class="kv">agent <b>' + esc(progress.agent_id) + "</b> · created " + esc(progress.created_at) +
      " · updated " + esc(progress.updated_at) + "</div>";
    if (progress.error) { html += '<div class="warn">' + esc(progress.error) + "</div>"; }
    html += '<div class="row"><button class="link" data-agent="' + esc(progress.agent_id) + '" data-task="' +
      esc(progress.task_id) + '" data-view="events">events</button>' +
      '<button class="link" data-agent="' + esc(progress.agent_id) + '" data-task="' + esc(progress.task_id) +
      '" data-view="messages">messages</button>' +
      '<button class="link" data-stop="' + esc(progress.task_id) + '">stop the run</button></div>';
    var state = progress.state;
    if (state) {
      // cut by a restart: the runtime's own words, and where the cut landed (src/task_record.go).
      if (state.interrupted) {
        html += '<div class="warn">cut by the runtime: ' + dash(state.interrupted.reason) +
          (state.interrupted.next_step ? " — next step " + esc(state.interrupted.next_step) +
            " (#" + esc(state.interrupted.stopped_at_step) + ")" : "") + "</div>";
      }
      html += roundHTML(state.last_round);
      if (state.open_criteria && state.open_criteria.length) {
        html += '<div class="kv">the contract still misses: <b>' + esc(state.open_criteria.join(", ")) + "</b></div>";
      }
      if (state.verdicts && state.verdicts.length) {
        html += '<div class="kv dim">' + state.verdicts.map(function (v) {
          return esc(v.criterion) + "=" + esc(v.result);
        }).join(" · ") + "</div>";
      }
    }
    var plans = progress.plans || [];
    if (plans.length) { html += '<div class="kv dim runs">' + plans.length + " rounds on record</div>"; }
    detail.innerHTML = '<div class="card">' + html + "</div>";
  }

  function loadDetail() {
    if (!openTask) { detail.innerHTML = ""; return Promise.resolve(); }
    return api("GET", "/api/tasks/" + encodeURIComponent(openTask)).then(renderDetail).catch(function (err) {
      detail.innerHTML = '<div class="card"><div class="warn">' + esc(err.message) + "</div></div>";
    });
  }

  function tick() {
    api("GET", "/api/tasks").then(function (res) {
      renderRows((res && res.tasks) || []);
    }).catch(function (err) {
      rows.innerHTML = '<tr><td class="hint" colspan="7">task list unavailable: ' + esc(err.message) + "</td></tr>";
    });
    loadDetail();
  }
  function schedule() {
    if (timer) { clearInterval(timer); }
    timer = null;
    if (every > 0) { timer = setInterval(tick, every * 1000); }
  }

  // ---- the form --------------------------------------------------------------------------
  // What is sent is exactly what the API documents: description, an optional task_id (an
  // existing one continues that task), and an optional account_id (the pool entry this task
  // runs on; absent = the pool decides).
  document.getElementById("instruction").addEventListener("submit", function (event) {
    event.preventDefault();
    var description = document.getElementById("f-description").value.trim();
    if (!description) { say("an instruction is required", "err"); return; }
    var body = { description: description };
    var taskID = document.getElementById("f-task").value.trim();
    if (taskID) { body.task_id = taskID; }
    if (select.value) { body.account_id = select.value; }
    var button = document.getElementById("send");
    button.disabled = true;
    api("POST", "/api/tasks", body).then(function (res) {
      say("accepted: task " + res.task_id + " → agent " + res.agent_id +
        (res.queued ? " (queued behind " + res.queued + ")" : ""));
      // Park the id in the field: the next thing typed here usually continues this task.
      document.getElementById("f-task").value = res.task_id;
      openTask = res.task_id;
      tick();
    }).catch(function (err) {
      say(err.message, "err");
    }).then(function () { button.disabled = false; });
  });
  document.getElementById("clear").addEventListener("click", function () {
    document.getElementById("f-description").value = "";
    document.getElementById("f-task").value = "";
    select.value = "";
    accountHint();
  });
  select.addEventListener("change", accountHint);

  rows.addEventListener("click", function (event) {
    var open = event.target.closest("button[data-open]");
    if (open) { openTask = open.dataset.open; loadDetail(); return; }
    var view = event.target.closest("button[data-view]");
    if (view && !view.disabled) { openAgentView(view.dataset.agent, view.dataset.task, view.dataset.view); return; }
    var row = event.target.closest("tr[data-task]");
    if (row) { openTask = row.dataset.task; loadDetail(); }
  });
  detail.addEventListener("click", function (event) {
    var view = event.target.closest("button[data-view]");
    if (view && !view.disabled) { openAgentView(view.dataset.agent, view.dataset.task, view.dataset.view); return; }
    var stop = event.target.closest("button[data-stop]");
    if (!stop) { return; }
    api("POST", "/api/tasks/" + encodeURIComponent(stop.dataset.stop) + "/stop", {}).then(function () {
      say("stop requested for " + stop.dataset.stop);
      tick();
    }).catch(function (err) { say(err.message, "err"); });
  });

  loadAccounts();
  tick();
  schedule();
})();
</script>
</body>
</html>
`
