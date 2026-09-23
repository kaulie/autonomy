#!/usr/bin/env node
/**
 * autonomy codex bridge — exposes the OpenAI Codex SDK (@openai/codex-sdk) over stdio
 * NDJSON, the same protocol the Cline bridge speaks (src/clinesdk/bridge/bridge.mjs), so
 * the Go side of autonomy drives both the same way and only the *event mapping* differs.
 *
 * Why a bridge: the Codex SDK is Node and wraps the `codex` CLI (it spawns it and
 * exchanges JSONL with it), while the autonomy runtime is Go. The bridge keeps the
 * *resident thread* (one Codex thread that survives many prompts) inside one Node process
 * and streams the SDK's native events to Go, which owns the neutral event model,
 * persistence and policy.
 *
 * Protocol — one JSON object per line, both directions:
 *
 *   bridge -> runtime
 *     {"type":"ready","protocol":"codex-bridge/1","pid":1,"node":"v22","sdk":"0.156.1"}
 *     {"type":"event","agentId":"cdx_...","sessionId":"<thread id>","event":{...native SDK event...}}
 *     {"type":"result","id":"<req id>","ok":true,"result":{...}}
 *     {"type":"result","id":"<req id>","ok":false,"error":{"code":"...","message":"..."}}
 *
 *   runtime -> bridge
 *     {"id":"1","cmd":"ping"}
 *     {"id":"2","cmd":"createAgent","params":{"modelId":"...","apiKey":"...","cwd":"...",
 *        "mode":"plan|agent","resumeSessionId":"<thread id>","systemPrompt":"..."}}
 *     {"id":"3","cmd":"send","params":{"agentId":"cdx_...","prompt":"..."}}
 *
 * Commands: ping | models | createAgent | send | stop | close | usage | shutdown
 *
 * Continuing a thread across bridge restarts: `codex.resumeThread(id)` re-attaches the
 * thread the agent row recorded (threads are persisted in ~/.codex/sessions), so a restart
 * continues the same conversation — unlike Cline, nothing has to be re-seeded. A thread's
 * id only exists once a turn has started, so this bridge reports the id every run reports
 * (the caller records that), exactly as the Cline bridge does.
 *
 * A Codex thread is sticky in mode (its sandbox) and working directory, so the runtime asks
 * for one per (mode, cwd) — the same shape as the Cline path.
 *
 * Native events are forwarded verbatim (the bridge never interprets them) so the Go layer
 * stays the single place that maps provider payloads onto LLMEvent.
 */
import { Codex } from "@openai/codex-sdk";
import { randomUUID } from "node:crypto";
import readline from "node:readline";

import {
	PROTOCOL,
	coerceText,
	itemText,
	normalizeUsage,
	resolveCodexDefaults,
	sandboxModeFor,
	statusOf,
} from "./config.mjs";

/** One Codex handle per bridge process, keyed by the credentials it was built with. */
const clients = new Map();
/** agentId -> resident thread handle. */
const agents = new Map();
/** agentId -> AbortController of the turn in flight, if any. */
const running = new Map();

function write(obj) {
	process.stdout.write(`${JSON.stringify(obj)}\n`);
}

function rpcError(code, message) {
	return { code, message };
}

function clientFor(params) {
	const defaults = resolveCodexDefaults();
	const apiKey = (params.apiKey || defaults.apiKey || "").trim();
	const baseUrl = (params.baseUrl || defaults.baseUrl || "").trim();
	const key = `${apiKey}|${baseUrl}`;
	let client = clients.get(key);
	if (!client) {
		const options = {};
		if (apiKey) options.apiKey = apiKey;
		if (baseUrl) options.baseUrl = baseUrl;
		client = new Codex(options);
		clients.set(key, client);
	}
	return client;
}

function createAgent(params = {}, requestId = null) {
	const cwd = (params.cwd || process.cwd()).trim();
	const model = (params.modelId || resolveCodexDefaults().model || "").trim();
	const mode = (params.mode || "agent").trim();
	const resumeSessionId = (params.resumeSessionId || "").trim();
	const systemPrompt = (params.systemPrompt || "").trim();

	const threadOptions = {
		workingDirectory: cwd,
		// Codex insists its working directory be a git repository; an autonomy agent
		// workspace is one, but this is the CLI's own guard, not our policy.
		skipGitRepoCheck: true,
		sandboxMode: sandboxModeFor(mode),
	};
	if (model) threadOptions.model = model;

	const codex = clientFor(params);
	const thread = resumeSessionId
		? codex.resumeThread(resumeSessionId, threadOptions)
		: codex.startThread(threadOptions);

	const agentId = `cdx_${randomUUID()}`;
	const agent = {
		id: agentId,
		thread,
		cwd,
		mode,
		model,
		resumedFrom: resumeSessionId || null,
		systemPrompt,
		// The SDK has no system-prompt option: on a *fresh* thread the instructions are
		// prefixed to its first turn; a resumed thread already has them in its history.
		systemPromptPending: !resumeSessionId && Boolean(systemPrompt),
	};
	agents.set(agentId, agent);
	write({
		type: "result",
		id: requestId,
		ok: true,
		result: {
			agentId,
			sessionId: thread.id || resumeSessionId || null,
			resumedFrom: agent.resumedFrom,
			providerId: "openai",
			modelId: model,
			cwd,
			mode,
			sandboxMode: threadOptions.sandboxMode,
		},
	});
	return agent;
}

/**
 * send runs one turn on a resident thread and streams the SDK's events to the runtime.
 * The final result carries the thread id (the runtime records it for the next process to
 * resume), the last agent message as text, the run's status and its usage.
 */
async function send(params = {}, requestId) {
	const agent = agents.get((params.agentId || "").trim());
	if (!agent) {
		write({ type: "result", id: requestId, ok: false, error: rpcError("not_found", `agent ${params.agentId} not found`) });
		return;
	}
	let prompt = coerceText(params.prompt);
	if (agent.systemPromptPending) {
		prompt = `${agent.systemPrompt}\n\n${prompt}`;
		agent.systemPromptPending = false;
	}

	const controller = new AbortController();
	running.set(agent.id, controller);
	const started = Date.now();
	let text = "";
	let usage = null;
	let state = "";
	try {
		const { events } = await agent.thread.runStreamed(prompt, { signal: controller.signal });
		for await (const event of events) {
			// requestId is what the client routes this run's events by (one send = one run).
			write({ type: "event", requestId, agentId: agent.id, sessionId: agent.thread.id || null, event });
			switch (event?.type) {
				case "item.completed": {
					const itemText_ = itemText(event.item);
					if (itemText_) text = itemText_;
					break;
				}
				case "turn.completed":
					usage = normalizeUsage(event.usage) || usage;
					state = "finished";
					break;
				case "turn.failed":
				case "thread.error":
					state = "error";
					break;
				default:
					break;
			}
		}
		if (!state) state = controller.signal.aborted ? "aborted" : "finished";
		write({
			type: "result",
			id: requestId,
			ok: true,
			result: {
				sessionId: agent.thread.id || agent.resumedFrom || null,
				text: (text || "").trim(),
				status: statusOf(state),
				...(usage ? { usage } : {}),
				durationMs: Date.now() - started,
				modelId: agent.model,
				providerId: "openai",
			},
		});
	} catch (err) {
		const aborted = controller.signal.aborted;
		write({
			type: "result",
			id: requestId,
			ok: false,
			error: rpcError(aborted ? "cancelled" : "agent_error", err?.message || String(err)),
		});
	} finally {
		running.delete(agent.id);
	}
}

function stopAgent(agentId, { forget = false } = {}) {
	const id = (agentId || "").trim();
	const controller = running.get(id);
	if (controller) controller.abort();
	if (forget) agents.delete(id);
	return Boolean(controller);
}

function accumulatedUsage(agentId) {
	const agent = agents.get((agentId || "").trim());
	return agent?.usage || null;
}

async function handle(cmd, params, requestId) {
	switch (cmd) {
		case "ping": {
			write({
				type: "result",
				id: requestId,
				ok: true,
				result: { protocol: PROTOCOL, node: process.version, sdk: "0.156.1" },
			});
			return;
		}
		case "models": {
			// The CLI resolves the catalog; this bridge only reports what it was told to run.
			write({
				type: "result",
				id: requestId,
				ok: true,
				result: { models: [], modelId: resolveCodexDefaults().model },
			});
			return;
		}
		case "createAgent": {
			try {
				createAgent(params || {}, requestId);
			} catch (err) {
				write({
					type: "result",
					id: requestId,
					ok: false,
					error: rpcError("create_failed", err?.message || String(err)),
				});
			}
			return;
		}
		case "send":
			await send(params || {}, requestId);
			return;
		case "stop": {
			write({ type: "result", id: requestId, ok: true, result: { stopped: stopAgent(params?.agentId) } });
			return;
		}
		case "close": {
			stopAgent(params?.agentId, { forget: true });
			write({ type: "result", id: requestId, ok: true, result: { closed: true } });
			return;
		}
		case "usage": {
			write({ type: "result", id: requestId, ok: true, result: { usage: accumulatedUsage(params?.agentId) } });
			return;
		}
		case "shutdown": {
			for (const [, controller] of running) controller.abort();
			write({ type: "result", id: requestId, ok: true, result: { stopped: true } });
			setTimeout(() => process.exit(0), 10);
			return;
		}
		default:
			write({
				type: "result",
				id: requestId,
				ok: false,
				error: rpcError("unknown_command", `unknown command ${cmd}`),
			});
	}
}

const rl = readline.createInterface({ input: process.stdin });
rl.on("line", (line) => {
	const trimmed = line.trim();
	if (!trimmed) return;
	let req;
	try {
		req = JSON.parse(trimmed);
	} catch (err) {
		write({ type: "result", id: null, ok: false, error: rpcError("bad_request", err?.message || "invalid JSON") });
		return;
	}
	const requestId = req.id ?? null;
	handle(req.cmd, req.params, requestId).catch((err) => {
		write({
			type: "result",
			id: requestId,
			ok: false,
			error: rpcError("internal_error", err?.message || String(err)),
		});
	});
});
rl.on("close", () => {
	for (const [, controller] of running) controller.abort();
	process.exit(0);
});

write({ type: "ready", protocol: PROTOCOL, pid: process.pid, node: process.version, sdk: "0.156.1" });
