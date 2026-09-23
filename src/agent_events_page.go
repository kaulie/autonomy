package autonomy

// agentEventsHTML is the live event stream of one agent, served at GET /agents/{agentID}/events and
// embedded by the UI shell (src/ui_home_page.go). One event per line, oldest at the top, and it
// keeps its place: the poll carries the cursor the API hands back (next_poll_after_seq), so a line
// is never rendered twice.
//
// The page is self-contained like every other page here, and it takes the two things it needs from
// its own URL: the task whose conversation is being tailed (?task=) and how often to poll
// (&every=, 0 = no auto-refresh — the shell's control sets it).
const agentEventsHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Autonomy · Agent events</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 13px/1.5 -apple-system, Segoe UI, Roboto, sans-serif; margin: 0; padding: 14px 18px; background: #f6f7f9; color: #1c1e21; }
  h1 { font-size: 16px; margin: 0 0 4px; }
  body.embedded h1, body.embedded .sub { display: none; }
  .sub { color: #667085; margin-bottom: 10px; }
  .bar { display: flex; gap: 12px; align-items: center; flex-wrap: wrap; margin-bottom: 10px; font-size: 12px; color: #475467; }
  button { font: inherit; padding: 3px 9px; border: 1px solid #d0d5dd; background: #fff; border-radius: 6px; cursor: pointer; }
  #log { background: #0b1020; color: #d7e0ff; border-radius: 8px; padding: 10px 12px; height: calc(100vh - 140px); overflow-y: auto; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
  .line { white-space: pre-wrap; word-break: break-word; padding: 1px 0; }
  .t { color: #7c8db5; }
  .k { color: #7ee787; }
  .role-user { color: #79c0ff; }
  .role-assistant { color: #e6edf3; }
  .role-tool { color: #d2a8ff; }
  .role-system { color: #8b949e; }
  .err { color: #ff7b72; }
  .empty { color: #7c8db5; }
</style>
</head>
<body>
<h1 id="title">Agent events</h1>
<div class="sub" id="sub">live event stream</div>
<div class="bar">
  <button id="tail">pause</button>
  <button id="clear">clear</button>
  <span id="state">…</span>
</div>
<div id="log"><div class="line empty">waiting for events…</div></div>
<script>
// A module embedded in the UI shell drops its own title (?embed=1).
if (new URLSearchParams(location.search).has("embed")) document.body.classList.add("embedded");

const params = new URLSearchParams(location.search);
const AGENT_ID = (location.pathname.match(/\/agents\/(\d+)\/events/) || [])[1] || "";
const TASK_ID = params.get("task") || "";
// every=0 turns auto-refresh off (the shell's control); anything else is the seconds between polls.
const EVERY = params.has("every") ? Number(params.get("every")) : 5;
const MAX_LINES = 500;

const log = document.getElementById("log");
let after = 0;
let tailing = true;
let timer = null;

function line(text, cls) {
  const div = document.createElement("div");
  div.className = "line " + (cls || "");
  div.textContent = text;
  log.appendChild(div);
  while (log.children.length > MAX_LINES) log.removeChild(log.firstChild);
}

function atBottom() {
  return log.scrollHeight - log.scrollTop - log.clientHeight < 40;
}

function stamp(iso) {
  const d = new Date(iso);
  return isNaN(d) ? "" : d.toTimeString().slice(0, 8);
}

function eventLine(ev) {
  const parts = [stamp(ev.created_at), "seq " + ev.message_seq, ev.role || "", ev.status || ""].filter(Boolean);
  let text = [ev.content, ev.normalized_content].filter(Boolean).join(" ");
  text = text.replace(/\s+/g, " ").trim();
  return parts.join(" · ") + (text ? "  " + text : "");
}

async function poll() {
  if (!AGENT_ID || !TASK_ID) {
    document.getElementById("state").textContent = "no task given: open this from agent status";
    return;
  }
  try {
    const res = await fetch("/api/tasks/" + encodeURIComponent(TASK_ID) + "/agents/" + encodeURIComponent(AGENT_ID) +
      "/events?last_synced_message_seq=" + after, { cache: "no-store" });
    if (!res.ok) throw new Error(res.status + " " + res.statusText);
    const data = await res.json();
    const events = data.events || [];
    const stick = atBottom();
    if (after === 0) log.innerHTML = "";
    for (const ev of events) line(eventLine(ev), "role-" + (ev.role || "system"));
    if (!events.length && after === 0) line("no events yet", "empty");
    after = data.next_poll_after_seq || after;
    document.getElementById("state").textContent = "agent " + AGENT_ID + " · task " + TASK_ID + " · " + after + " messages" +
      (EVERY ? " · every " + EVERY + "s" : " · auto-refresh off");
    if (stick && tailing) log.scrollTop = log.scrollHeight;
  } catch (err) {
    document.getElementById("state").textContent = "poll failed: " + err.message;
  }
}

function schedule() {
  if (timer) clearInterval(timer);
  timer = null;
  if (tailing && EVERY > 0) timer = setInterval(poll, EVERY * 1000);
}

document.getElementById("tail").addEventListener("click", (event) => {
  tailing = !tailing;
  event.target.textContent = tailing ? "pause" : "resume";
  schedule();
  if (tailing) poll();
});

document.getElementById("clear").addEventListener("click", () => {
  log.innerHTML = "";
});

document.getElementById("title").textContent = "Agent " + (AGENT_ID || "?") + " events";
document.getElementById("sub").textContent = TASK_ID ? "task " + TASK_ID : "no task given";
poll();
schedule();
</script>
</body>
</html>
`
