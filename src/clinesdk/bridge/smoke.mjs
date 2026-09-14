#!/usr/bin/env node
/**
 * Live smoke driver for bridge.mjs — speaks the same stdio NDJSON protocol the
 * Go client does, so a green run proves the bridge + SDK + provider chain works.
 *
 * Usage:  npm run smoke -- --provider deepseek --model deepseek-v4-pro [--api-key sk-...] [--cwd /tmp/x]
 * The API key can also come from CLINE_API_KEY / the provider's saved Cline config.
 */
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import readline from "node:readline";

function arg(name, fallback = "") {
	const i = process.argv.indexOf(`--${name}`);
	return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : fallback;
}

const providerId = arg("provider", process.env.AUTONOMY_CLINE_PROVIDER || "deepseek");
const modelId = arg("model", process.env.AUTONOMY_CLINE_MODEL || "deepseek-v4-pro");
const apiKey = arg("api-key", process.env.CLINE_API_KEY || process.env.AUTONOMY_CLINE_API_KEY || "");
const cwd = arg("cwd", process.cwd());
const dumpPath = arg("dump", "");
const dumpStream = dumpPath ? (await import("node:fs")).createWriteStream(dumpPath) : null;

const bridgePath = fileURLToPath(new URL("./bridge.mjs", import.meta.url));
const child = spawn(process.execPath, [bridgePath], { stdio: ["pipe", "pipe", "inherit"] });

let nextId = 0;
const waiters = new Map();
const stats = { events: 0, byType: new Map(), byContent: new Map(), agents: new Set(), error: null };

readline.createInterface({ input: child.stdout }).on("line", (line) => {
	const text = line.trim();
	if (!text) return;
	let msg;
	try {
		msg = JSON.parse(text);
	} catch {
		return;
	}
	if (msg.type === "event") {
		stats.events += 1;
		dumpStream?.write(`${line}\n`);
		const inner = msg.event?.payload?.event ?? msg.event;
		if (msg.agentId) stats.agents.add(msg.agentId);
		bump(stats.byType, inner?.type ?? "?");
		if (inner?.contentType) bump(stats.byContent, `${inner.type}/${inner.contentType}`);
		return;
	}
	if (msg.type === "result") {
		const w = waiters.get(msg.id);
		if (w) {
			waiters.delete(msg.id);
			w(msg);
		}
	}
});

function bump(map, key) {
	map.set(key, (map.get(key) ?? 0) + 1);
}

function request(cmd, params = {}, timeoutMs = 180000) {
	const id = `req-${++nextId}`;
	child.stdin.write(`${JSON.stringify({ id, cmd, params })}\n`);
	return new Promise((resolve, reject) => {
		const timer = setTimeout(() => {
			waiters.delete(id);
			reject(new Error(`${cmd} timed out after ${timeoutMs}ms`));
		}, timeoutMs);
		waiters.set(id, (msg) => {
			clearTimeout(timer);
			if (msg.ok) resolve(msg.result);
			else reject(new Error(`${cmd} failed: ${msg.error?.code}: ${msg.error?.message}`));
		});
	});
}

function show(label, res) {
	const usage = res.usage ?? {};
	console.log(
		`\n== ${label}\n   session=${res.sessionId} status=${res.status} finish=${res.finishReason} usageSource=${res.usageSource}\n` +
			`   text=${JSON.stringify((res.text ?? "").slice(0, 200))}\n` +
			`   tokens in=${usage.inputTokens} out=${usage.outputTokens} cacheRead=${usage.cacheReadTokens} ` +
			`cacheWrite=${usage.cacheWriteTokens} total=${usage.totalTokens} costUsd=${usage.costUsd}` +
			(res.lastError ? `\n   lastError=${res.lastError}` : ""),
	);
}

try {
	const ping = await request("ping");
	console.log(`bridge: protocol=${ping.protocol} node=${ping.node} sdk=${ping.sdk} provider=${providerId}/${modelId}`);
	console.log(`bridge knows providers: ${(ping.providers ?? []).slice(0, 12).join(", ")}${(ping.providers?.length ?? 0) > 12 ? ", …" : ""}`);

	const agent = await request("createAgent", {
		providerId,
		modelId,
		apiKey: apiKey || undefined,
		cwd,
		mode: "yolo",
		systemPrompt: "You are a terse assistant. Follow the instruction literally.",
	});
	console.log(`agent: ${agent.agentId} cwd=${agent.cwd}`);

	const first = await request("send", {
		agentId: agent.agentId,
		prompt: "Reply with exactly: pong. Do not use any tools.",
	});
	show("turn 1 (no tools)", first);

	const tool = await request("send", {
		agentId: agent.agentId,
		prompt: "Use the shell tool to run exactly: echo hello-from-cline — then reply with just the output.",
	});
	show("turn 2 (shell tool)", tool);

	const memory = await request("send", {
		agentId: agent.agentId,
		prompt: "What did I ask you in my very first message? Answer in one short line.",
	});
	show("turn 3 (resident memory check)", memory);

	const usage = await request("usage", { agentId: agent.agentId });
	console.log(`\naccumulated usage: ${JSON.stringify(usage.usage)}`);

	console.log(`\nevents=${stats.events} sessions=${stats.agents.size}`);
	console.log("byEventType:", Object.fromEntries(stats.byType));
	console.log("byContentType:", Object.fromEntries(stats.byContent));

	await request("close", { agentId: agent.agentId });
	await request("shutdown");
	child.stdin.end();
	console.log("\nSMOKE OK");
	process.exit(0);
} catch (err) {
	stats.error = err;
	console.error(`\nSMOKE FAILED: ${err?.message ?? err}`);
	child.kill("SIGKILL");
	process.exit(1);
}
