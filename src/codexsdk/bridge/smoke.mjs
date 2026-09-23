#!/usr/bin/env node
/**
 * Live smoke for the codex bridge — the requirement it must satisfy for autonomy's
 * restart recovery: a thread opened by one bridge process is continued by another one.
 *
 * Phase A: a bridge process opens a thread and tells the model to remember a number.
 * Phase B: a *second* bridge process resumes the thread id A reported, and asks for it.
 * Pass = the number came back, i.e. the conversation survives the process (and so a
 * deploy/restart does not cost the agent its memory).
 *
 * Gated: run with CODEX_LIVE=1 (it spends real Codex usage and needs `codex` credentials).
 *   cd src/codexsdk/bridge && CODEX_LIVE=1 node smoke.mjs
 */
import { spawn } from "node:child_process";
import readline from "node:readline";

const CWD = process.env.CODEX_SMOKE_CWD || "/tmp/codex-bridge-smoke";
const NUMBER = "4321";

if (process.env.CODEX_LIVE !== "1") {
	console.log("codex bridge smoke skipped: set CODEX_LIVE=1 to run it (spends usage)");
	process.exit(0);
}

function runBridge(steps, timeoutMs = 180000) {
	return new Promise((resolve, reject) => {
		const child = spawn(process.execPath, ["bridge.mjs"], { stdio: ["pipe", "pipe", "pipe"], cwd: import.meta.dirname });
		const rl = readline.createInterface({ input: child.stdout });
		const send = (o) => child.stdin.write(`${JSON.stringify(o)}\n`);
		const out = { events: new Set(), results: [] };
		const timer = setTimeout(() => { child.kill(); reject(new Error(`bridge timed out after ${timeoutMs}ms`)); }, timeoutMs);
		child.stderr.on("data", (d) => process.stderr.write(`[bridge] ${d}`));
		rl.on("line", (line) => {
			let msg;
			try { msg = JSON.parse(line); } catch { return; }
			if (msg.type === "ready") { steps.ready(send); return; }
			if (msg.type === "event") { out.events.add(msg.event?.type); return; }
			if (msg.type !== "result") return;
			out.results.push(msg.result ?? msg.error);
			try {
				steps.result?.(msg, send);
			} catch (err) {
				clearTimeout(timer);
				reject(err);
			}
			if (msg.id === "9") { clearTimeout(timer); setTimeout(() => resolve(out), 300); }
		});
	});
}

let threadId = "";
let agentA = "";

await runBridge({
	ready: (send) => send({ id: "2", cmd: "createAgent", params: { cwd: CWD, mode: "plan", systemPrompt: "You are a test agent. Be brief." } }),
	result: (msg, send) => {
		if (msg.id === "2") { agentA = msg.result.agentId; send({ id: "3", cmd: "send", params: { agentId: agentA, prompt: `Remember this number: ${NUMBER}. Reply with just OK.` } }); }
		if (msg.id === "3") { threadId = msg.result.sessionId; send({ id: "9", cmd: "shutdown" }); }
	},
});
console.log(`phase A: thread=${threadId} text=${JSON.stringify(threadId ? "recorded" : "missing")}`);
if (!threadId) throw new Error("phase A reported no thread id: nothing to resume");

const second = await runBridge({
	ready: (send) => send({ id: "2", cmd: "createAgent", params: { cwd: CWD, mode: "plan", resumeSessionId: threadId } }),
	result: (msg, send) => {
		if (msg.id === "2") {
			if (msg.result.resumedFrom !== threadId) throw new Error(`phase B did not resume ${threadId}: ${JSON.stringify(msg.result)}`);
			send({ id: "3", cmd: "send", params: { agentId: msg.result.agentId, prompt: "What number did I ask you to remember? Reply with just the number." } });
		}
		if (msg.id === "3") send({ id: "9", cmd: "shutdown" });
	},
});

const reply = second.results.at(-1)?.text ?? "";
console.log(`phase B: resumed=${threadId} text=${JSON.stringify(reply)} events=${[...second.events].join(",")}`);
if (!reply.includes(NUMBER)) throw new Error(`the resumed thread did not remember ${NUMBER} (got ${JSON.stringify(reply)})`);
console.log("codex bridge smoke PASS: a second process continued the thread and recalled the conversation");
