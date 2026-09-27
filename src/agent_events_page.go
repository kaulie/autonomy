package autonomy

// agentEventsHTML is the live event stream of one agent, served at GET /agents/{agentID}/events and
// embedded by the UI shell (src/ui_home_page.go). Each event is its own terminal-style section
// (header + body), oldest at the top. The poll carries the cursor the API hands back
// (next_poll_after_seq), so an event is never rendered twice.
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
  #events { height: calc(100vh - 140px); overflow-y: auto; display: flex; flex-direction: column; gap: 10px; padding: 2px 0; }
  .event-section { border: 1px solid #30363d; border-radius: 8px; overflow: hidden; background: #0b1020; box-shadow: 0 1px 2px rgba(0,0,0,.25); }
  .event-header { padding: 6px 10px; font-size: 11px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; background: #161b22; color: #8b949e; border-bottom: 1px solid #30363d; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .event-body { padding: 8px 10px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; color: #d7e0ff; white-space: pre-wrap; word-break: break-word; max-height: 240px; overflow-y: auto; }
  .role-user .event-body { color: #79c0ff; }
  .role-assistant .event-body { color: #e6edf3; }
  .role-tool .event-body { color: #d2a8ff; }
  .role-system .event-body { color: #8b949e; }
  .empty { color: #7c8db5; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; padding: 12px; }
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
<div id="events"><div class="empty">waiting for events…</div></div>
<script>
// A module embedded in the UI shell drops its own title (?embed=1).
if (new URLSearchParams(location.search).has("embed")) document.body.classList.add("embedded");

const params = new URLSearchParams(location.search);
const AGENT_ID = (location.pathname.match(/\/agents\/(\d+)\/events/) || [])[1] || "";
const TASK_ID = params.get("task") || "";
// every=0 turns auto-refresh off (the shell's control); anything else is the seconds between polls.
const EVERY = params.has("every") ? Number(params.get("every")) : 5;
const MAX_SECTIONS = 500;

const eventsEl = document.getElementById("events");
let after = 0;
let tailing = true;
let timer = null;

function trimSections() {
  while (eventsEl.children.length > MAX_SECTIONS) eventsEl.removeChild(eventsEl.firstChild);
}

function atBottom() {
  return eventsEl.scrollHeight - eventsEl.scrollTop - eventsEl.clientHeight < 40;
}

function stamp(iso) {
  const d = new Date(iso);
  return isNaN(d) ? "" : d.toTimeString().slice(0, 8);
}

function eventHeader(ev) {
  return [stamp(ev.created_at), "seq " + ev.message_seq, ev.role || "", ev.status || ""].filter(Boolean).join(" · ");
}

function eventBody(ev) {
  let text = [ev.content, ev.normalized_content].filter(Boolean).join("\n");
  return text.replace(/\r\n/g, "\n").trim();
}

function appendEvent(ev) {
  const role = ev.role || "system";
  const section = document.createElement("section");
  section.className = "event-section role-" + role;
  const header = document.createElement("div");
  header.className = "event-header";
  header.textContent = eventHeader(ev);
  const body = document.createElement("div");
  body.className = "event-body";
  const text = eventBody(ev);
  body.textContent = text || "(empty)";
  section.appendChild(header);
  section.appendChild(body);
  eventsEl.appendChild(section);
  trimSections();
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
    if (after === 0) eventsEl.innerHTML = "";
    for (const ev of events) appendEvent(ev);
    if (!events.length && after === 0) {
      const empty = document.createElement("div");
      empty.className = "empty";
      empty.textContent = "no events yet";
      eventsEl.appendChild(empty);
    }
    after = data.next_poll_after_seq || after;
    document.getElementById("state").textContent = "agent " + AGENT_ID + " · task " + TASK_ID + " · " + after + " messages" +
      (EVERY ? " · every " + EVERY + "s" : " · auto-refresh off");
    if (stick && tailing) eventsEl.scrollTop = eventsEl.scrollHeight;
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
  eventsEl.innerHTML = "";
});

document.getElementById("title").textContent = "Agent " + (AGENT_ID || "?") + " events";
document.getElementById("sub").textContent = TASK_ID ? "task " + TASK_ID : "no task given";
poll();
schedule();
</script>
</body>
</html>
`
