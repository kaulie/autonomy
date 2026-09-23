package autonomy

// agentMessagesHTML is one agent's message view, served at GET /agents/{agentID}/messages and
// embedded by the UI shell. Two lists, one line per message:
//
//   - **received**: the agent's inbox — a user instruction, a delegated prompt from another agent,
//     a stop from the runtime (src/inbox.go);
//   - **sent**: the agent's own turns — the input it was given and the output it answered with
//     (the reason-turn log, src/turn_api.go).
//
// A line is a line: long text is truncated with the full text in the tooltip, because the point of
// this page is to see *what happened, in order*, not to read a whole transcript (that is what the
// turn / event views are for).
const agentMessagesHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Autonomy · Agent messages</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 13px/1.6 -apple-system, Segoe UI, Roboto, sans-serif; margin: 0; padding: 14px 18px; background: #f6f7f9; color: #1c1e21; }
  h1 { font-size: 16px; margin: 0 0 4px; }
  body.embedded h1, body.embedded .sub { display: none; }
  .sub { color: #667085; margin-bottom: 10px; }
  .bar { display: flex; gap: 12px; align-items: center; flex-wrap: wrap; margin-bottom: 12px; font-size: 12px; color: #475467; }
  button { font: inherit; padding: 3px 9px; border: 1px solid #d0d5dd; background: #fff; border-radius: 6px; cursor: pointer; }
  h2 { font-size: 13px; margin: 16px 0 6px; color: #344054; }
  table { border-collapse: collapse; width: 100%; background: #fff; box-shadow: 0 1px 2px rgba(16,24,40,.06); table-layout: fixed; }
  th, td { text-align: left; padding: 5px 9px; border-bottom: 1px solid #eaecf0; vertical-align: top; }
  th { background: #f9fafb; font-size: 11px; text-transform: uppercase; letter-spacing: .03em; color: #475467; }
  td { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
  .when { width: 90px; color: #667085; }
  .who { width: 150px; }
  .kind { width: 120px; color: #475467; }
  .status-ok { color: #1a7f37; }
  .status-bad { color: #b42318; }
  .in { color: #1c4ed8; }
  .out { color: #6f42c1; }
  .empty { color: #667085; }
</style>
</head>
<body>
<h1 id="title">Agent messages</h1>
<div class="sub" id="sub"></div>
<div class="bar">
  <button id="pause">pause</button>
  <button id="reload">refresh now</button>
  <span id="state">…</span>
</div>
<h2 id="received-title">Received</h2>
<table><thead><tr><th class="when">when</th><th class="who">from</th><th class="kind">kind</th><th>message</th><th class="kind">status</th></tr></thead><tbody id="received"></tbody></table>
<h2 id="sent-title">Sent</h2>
<table><thead><tr><th class="when">when</th><th class="who">cycle</th><th class="kind">status</th><th>asked (input)</th></tr></thead><tbody id="sent"></tbody></table>
<table style="margin-top:4px"><thead><tr><th class="when">&nbsp;</th><th class="who">&nbsp;</th><th class="kind">&nbsp;</th><th>answered (output)</th></tr></thead><tbody id="answers"></tbody></table>
<script>
// A module embedded in the UI shell drops its own title (?embed=1).
if (new URLSearchParams(location.search).has("embed")) document.body.classList.add("embedded");

const params = new URLSearchParams(location.search);
const AGENT_ID = (location.pathname.match(/\/agents\/(\d+)\/messages/) || [])[1] || "";
const EVERY = params.has("every") ? Number(params.get("every")) : 5;
const LIMIT = Number(params.get("limit") || 200);

const $ = (id) => document.getElementById(id);
let live = true;
let timer = null;

function esc(value) {
  const map = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };
  return String(value == null ? "" : value).replace(/[&<>"']/g, (c) => map[c]);
}

function stamp(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  return isNaN(d) ? String(iso).slice(11, 19) : d.toTimeString().slice(0, 8);
}

function oneLine(text) {
  return String(text == null ? "" : text).replace(/\s+/g, " ").trim();
}

function statusClass(status) {
  if (!status) return "";
  return /^(queued|running|done)$/.test(status) ? "status-ok" : "status-bad";
}

function load() {
  if (!AGENT_ID) {
    $("state").textContent = "no agent id in the URL";
    return;
  }
  fetch("/api/agents/" + encodeURIComponent(AGENT_ID) + "/messages?limit=" + LIMIT, { cache: "no-store" })
    .then((res) => {
      if (!res.ok) throw new Error(res.status + " " + res.statusText);
      return res.json();
    })
    .then((data) => {
      const received = data.received || [];
      const sent = data.sent || [];
      $("title").textContent = "Agent " + AGENT_ID + " messages";
      $("sub").textContent = (data.agent || "") + " · " + received.length + " received · " + sent.length + " sent" +
        (EVERY ? " · every " + EVERY + "s" : " · auto-refresh off");
      $("received").innerHTML = received.length
        ? received.map((m) => {
            const body = oneLine(m.content);
            return '<tr><td class="when">' + esc(stamp(m.created_at)) + '</td>' +
              '<td class="who in">' + esc(m.sender + (m.sender_id ? ":" + m.sender_id : "")) + "</td>" +
              '<td class="kind">' + esc(m.kind) + "</td>" +
              '<td title="' + esc(body) + '">' + esc(body) + "</td>" +
              '<td class="kind ' + statusClass(m.status) + "\">" + esc(m.status || "") + (m.error ? " · " + esc(oneLine(m.error)) : "") + "</td></tr>";
          }).join("")
        : '<tr><td colspan="5" class="empty">nothing was sent to this agent yet</td></tr>';
      $("sent").innerHTML = sent.length
        ? sent.map((t) => {
            const asked = oneLine(t.input);
            return '<tr><td class="when">' + esc(stamp(t.created_at)) + '</td>' +
              '<td class="who">' + esc(t.cycle || "") + "</td>" +
              '<td class="kind ' + statusClass(t.status) + "\">" + esc(t.status || "") + "</td>" +
              '<td title="' + esc(asked) + '">' + esc(asked) + "</td></tr>";
          }).join("")
        : '<tr><td colspan="4" class="empty">this agent has not run yet</td></tr>';
      $("answers").innerHTML = sent.length
        ? sent.map((t) => {
            const answered = oneLine(t.output);
            return '<tr><td class="when">' + esc(t.duration_ms ? t.duration_ms + "ms" : "") + '</td>' +
              '<td class="who"></td><td class="kind"></td>' +
              '<td class="out" title="' + esc(answered) + '">' + esc(answered) + "</td></tr>";
          }).join("")
        : "";
      $("state").textContent = "updated " + new Date().toTimeString().slice(0, 8);
    })
    .catch((err) => {
      $("state").textContent = "load failed: " + err.message;
    });
}

function schedule() {
  if (timer) clearInterval(timer);
  timer = null;
  if (live && EVERY > 0) timer = setInterval(load, EVERY * 1000);
}

$("pause").addEventListener("click", (event) => {
  live = !live;
  event.target.textContent = live ? "pause" : "resume";
  schedule();
  if (live) load();
});
$("reload").addEventListener("click", load);

load();
schedule();
</script>
</body>
</html>
`
