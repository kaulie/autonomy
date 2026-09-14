#!/usr/bin/env node
/**
 * autonomy cline bridge — exposes the Cline SDK (@cline/sdk) over stdio NDJSON.
 *
 * Why a bridge: the Cline agent core is TypeScript/Node only (`@cline/core`,
 * `@cline/agents`, `@cline/llms`), while the autonomy runtime is Go. The bridge
 * keeps the *resident session* (a Cline session that survives many prompts)
 * inside one Node process and streams the SDK's native events to Go, which owns
 * the neutral event model, persistence and policy.
 *
 * This mirrors how web-cursor drives the same SDK (ClineCore.create + subscribe
 * + start/send per task), minus the web UI concerns.
 *
 * Protocol — one JSON object per line, both directions:
 *
 *   bridge -> runtime
 *     {"type":"ready","protocol":"cline-bridge/1","pid":1,"node":"v22","sdk":"0.0.82"}
 *     {"type":"event","agentId":"cls_...","sessionId":"cls-...","event":{...native SDK event...}}
 *     {"type":"result","id":"<req id>","ok":true,"result":{...}}
 *     {"type":"result","id":"<req id>","ok":false,"error":{"code":"...","message":"..."}}
 *
 *   runtime -> bridge
 *     {"id":"1","cmd":"ping"}
 *     {"id":"2","cmd":"createAgent","params":{"providerId":"...","modelId":"...","cwd":"..."}}
 *     {"id":"3","cmd":"send","params":{"agentId":"cls_...","prompt":"..."}}
 *
 * Commands: ping | models | createAgent | send | stop | close | usage | shutdown
 *
 * Native events are forwarded verbatim (the bridge never interprets them) so the
 * Go layer stays the single place that maps provider payloads onto LLMEvent.
 */
import { ClineCore, Llms } from "@cline/sdk";
import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import readline from "node:readline";

import { DEFAULT_MODE, DEFAULT_SYSTEM_PROMPT, PROTOCOL, coerceText, messageOf, resolveClineDefaults } from "./config.mjs";
import { eventLabel, signalLine, traceLevel, traceWidth } from "./trace.mjs";

/** One ClineCore per bridge process; sessions multiplex on it. */
const core = { client: null, promise: null };
/** agentId -> resident session handle. */
const agents = new Map();
/** sessionId -> agentId, for routing SDK events back to a request. */
const sessionOwners = new Map();

function write(obj) {
	process.stdout.write(`${JSON.stringify(obj)}\n`);
}

function log(level, message) {
	process.stderr.write(`[cline-bridge] ${level}: ${message}\n`);
}

// Trace policy (see trace.mjs). "signal" is the default and prints the run's
// milestones *with their content* — thinking blocks, tool calls with their
// arguments and output, lifecycle — while the per-token `chunk` echo is dropped.
// "full" (AUTONOMY_CLINE_TRACE=1 / AUTONOMY_LLM_DEBUG=1) prints every event,
// which is how a "no provider activity" stall gets diagnosed; "silent" prints
// nothing.
const TRACE = traceLevel();
const TRACE_WIDTH = traceWidth();

// Headless by default: autonomy has no UI to answer SDK interaction prompts, and
// an unanswered prompt looks exactly like a stalled run (no events at all).
// AUTONOMY_CLINE_INTERACTIVE=1 restores the web-cursor-style interactive host.
const INTERACTIVE = /^(1|true|on|yes)$/i.test(process.env.AUTONOMY_CLINE_INTERACTIVE ?? "");

function rpcError(code, message) {
	const err = new Error(message);
	err.code = code;
	return err;
}

function sdkVersion() {
	try {
		const pkg = JSON.parse(
			readFileSync(new URL("./node_modules/@cline/sdk/package.json", import.meta.url), "utf8"),
		);
		return pkg.version ?? "";
	} catch {
		return "";
	}
}

function newSessionId() {
	return `cls-${randomUUID().replace(/-/g, "").slice(0, 16)}`;
}

function normalizeMode(mode) {
	return String(mode ?? "").trim().toLowerCase() === "plan" ? "plan" : DEFAULT_MODE;
}

/**
 * traceEvent writes one run event to stderr under the configured trace level.
 * "signal" is the readable default: the model's thinking blocks and tool calls
 * with their content, plus the lifecycle milestones — the per-chunk noise (the
 * `chunk` echo stream and content deltas) is dropped. "full" is the firehose.
 */
function traceEvent(event, sessionId, agent) {
	if (TRACE === "silent") return;
	const where = `session=${sessionId ?? "-"} req=${agent?.currentRequest ?? "-"}`;
	if (TRACE === "full") {
		log("trace", `event ${eventLabel(event)} ${where}`);
		return;
	}
	const line = signalLine(event, TRACE_WIDTH);
	if (line) log("signal", `${line} ${where}`);
}

/** SDK event fan-out: every core event is forwarded, tagged with its owner. */
function onCoreEvent(event) {
	const sessionId = event?.payload?.sessionId ?? null;
	const agentId = sessionId ? sessionOwners.get(sessionId) ?? null : null;
	const agent = agentId ? agents.get(agentId) : null;
	if (agent) noteRunActivity(agent, event?.payload?.event ?? event);
	traceEvent(event, sessionId, agent);
	write({
		type: "event",
		requestId: agent?.currentRequest ?? null,
		event,
		sessionId,
		agentId,
	});
}

/**
 * Accumulate what the SDK does not hand back on resume: the assistant text of
 * the turn and the last error. `start()` returns text on the result, `send()`
 * on a resident session does not, so the stream is the source of truth.
 */
function noteRunActivity(agent, inner) {
	if (!agent || !inner) return;
	if (agent.firstEventAt == null) {
		agent.firstEventAt = Date.now();
		if (agent.runStartedAt != null) {
			log("info", `first event after ${agent.firstEventAt - agent.runStartedAt}ms (${inner.type}${inner.contentType ? ":" + inner.contentType : ""})`);
		}
	}
	const type = inner.type;
	const contentType = inner.contentType;
	if (type === "content_end" && contentType === "text") {
		if (typeof inner.text === "string" && inner.text) agent.text = inner.text;
		return;
	}
	if ((type === "content_start" || type === "content_update") && contentType === "text") {
		if (typeof inner.accumulated === "string" && inner.accumulated) agent.text = inner.accumulated;
		else if (typeof inner.text === "string" && inner.text) agent.text += inner.text;
		return;
	}
	if (type === "error") {
		// The SDK may report a structured error; the Go client decodes this field as
		// text, so flatten it here.
		agent.lastError = messageOf(inner.error ?? inner.message, "agent run failed");
	}
}

async function getCore() {
	if (core.client) return core.client;
	if (!core.promise) {
		core.promise = ClineCore.create({ clientName: "autonomy", backendMode: "local" })
			.then((client) => {
				client.subscribe(onCoreEvent);
				return client;
			})
			.catch((err) => {
				core.promise = null;
				throw err;
			});
	}
	core.client = await core.promise;
	return core.client;
}

function normalizeUsage(usage) {
	if (!usage) return null;
	const inputTokens = usage.inputTokens ?? 0;
	const outputTokens = usage.outputTokens ?? 0;
	const cacheReadTokens = usage.cacheReadTokens ?? 0;
	const cacheWriteTokens = usage.cacheWriteTokens ?? 0;
	const reasoning = usage.reasoningTokens ?? usage.reasoningTokenCount ?? null;
	return {
		inputTokens,
		outputTokens,
		cacheReadTokens,
		cacheWriteTokens,
		reasoningTokens: typeof reasoning === "number" ? reasoning : null,
		totalTokens: usage.totalTokens ?? inputTokens + outputTokens,
		costUsd: typeof usage.totalCost === "number" ? usage.totalCost : null,
	};
}

function statusOf(result) {
	switch (result?.finishReason) {
		case "error":
			return "error";
		case "aborted":
		case "cancelled":
			return "cancelled";
		default:
			return "finished";
	}
}

function summarize(agent, res, usageSource) {
	const result = res?.result ?? {};
	const usage = normalizeUsage(result.usage) ?? agent.runUsage;
	return {
		agentId: agent.agentId,
		sessionId: agent.sessionId,
		mode: agent.mode,
		status: statusOf(result),
		text: coerceText(result.text || agent.text),
		finishReason: coerceText(result.finishReason),
		usage,
		usageSource: usageSource ?? (result.usage ? "run" : "accumulated_delta"),
		lastError: agent.lastError == null ? null : coerceText(agent.lastError),
	};
}

/** Cumulative usage for a session (the SDK's own accounting), or null. */
async function accumulatedUsageRaw(agent) {
	if (!agent.sessionId) return null;
	const client = await getCore();
	try {
		const summary = await client.getAccumulatedUsage(agent.sessionId);
		return summary?.usage ?? null;
	} catch (err) {
		log("warn", `getAccumulatedUsage failed: ${err?.message ?? err}`);
		return null;
	}
}

/** Per-turn usage as the delta between two cumulative snapshots. */
function usageDelta(before, after) {
	if (!after) return null;
	const keys = [
		"inputTokens",
		"outputTokens",
		"cacheReadTokens",
		"cacheWriteTokens",
		"reasoningTokens",
		"totalTokens",
		"totalCost",
	];
	const delta = {};
	for (const key of keys) {
		const a = Number(after[key] ?? 0);
		const b = Number(before?.[key] ?? 0);
		delta[key] = Math.max(0, a - b);
	}
	return delta;
}

function createAgent(params) {
	const agentId = `cls_${randomUUID().replace(/-/g, "")}`;
	const cwd = params.cwd ?? process.cwd();
	// The SDK requires both an explicit provider and model; fall back to what
	// `cline auth` saved so a machine that already authenticated works as-is.
	const defaults = params.providerId && params.modelId ? null : resolveClineDefaults();
	const providerId = (params.providerId || defaults?.providerId || "").trim();
	const modelId = (params.modelId || defaults?.modelId || "").trim();
	if (!providerId || !modelId) {
		throw rpcError(
			"missing_provider",
			"Cline provider and model are required: set AUTONOMY_CLINE_PROVIDER and " +
				"AUTONOMY_CLINE_MODEL, or run `cline auth` to save a provider",
		);
	}
	if (defaults) {
		log("info", `resolved provider=${providerId} model=${modelId} from ${defaults.source}`);
	}
	const handle = {
		agentId,
		mode: normalizeMode(params.mode),
		sessionId: null,
		started: false,
		prompts: 0,
		config: {
			providerId,
			modelId,
			apiKey: params.apiKey || undefined,
			baseUrl: params.baseUrl || undefined,
			cwd,
			enableTools: params.enableTools ?? true,
			// Mirrors web-cursor: no sub-agents/teams for a single task agent.
			enableSpawnAgent: false,
			enableAgentTeams: false,
			systemPrompt: (params.systemPrompt ?? "").trim() || DEFAULT_SYSTEM_PROMPT,
		},
	};
	agents.set(agentId, handle);
	return { agentId, mode: handle.mode, cwd, providerId, modelId };
}

/**
 * Send one prompt to a resident session: the first send starts the SDK session
 * (minting its id unless the caller already has one), later sends resume it.
 * The caller's request id is echoed on the result so the runtime can pair the
 * streamed events with the prompt that produced them.
 */
async function send(params, requestId) {
	const agent = agents.get(params.agentId);
	if (!agent) throw rpcError("unknown_agent", `unknown agent ${params.agentId}`);
	const prompt = typeof params.prompt === "string" ? params.prompt : "";
	if (!prompt.trim()) throw rpcError("invalid_params", "prompt is required");
	const client = await getCore();
	const mode = normalizeMode(params.mode ?? agent.mode);
	agent.text = "";
	agent.lastError = null;

	let res;
	try {
		if (!agent.started) {
			const sessionId = agent.sessionId ?? newSessionId();
			const config = { ...agent.config, mode, sessionId };
			// Register the owner *before* starting: the SDK streams the run's
			// events while start() is still awaiting, and they can only be
			// routed to this request if the session is already mapped.
			agent.sessionId = sessionId;
			sessionOwners.set(sessionId, agent.agentId);
			log("info", `start session=${sessionId} model=${config.modelId ?? "default"} cwd=${config.cwd} req=${requestId}`);
			agent.currentRequest = requestId;
			// Bridge-side progress: warn when nothing at all arrived shortly after a
			// start, because a stalled provider looks exactly like a slow one.
			agent.runStartedAt = Date.now();
			agent.firstEventAt = null;
			setTimeout(() => {
				if (agent.firstEventAt == null) {
					log("warn", `no SDK events ${Date.now() - agent.runStartedAt}ms after start (session ${agent.sessionId}) — provider may be queueing/throttling`);
				}
			}, 30000);
	res = await client.start({ prompt, interactive: INTERACTIVE, config });
			agent.sessionId = res?.sessionId ?? sessionId;
			agent.started = true;
			sessionOwners.set(agent.sessionId, agent.agentId);
		} else if (mode !== agent.mode) {
			throw rpcError(
				"mode_mismatch",
				`session ${agent.sessionId} is in mode ${agent.mode}; create a new agent to switch to ${mode}`,
			);
		} else {
			log("info", `send session=${agent.sessionId} bytes=${prompt.length} req=${requestId}`);
			agent.currentRequest = requestId;
			res = await client.send({ sessionId: agent.sessionId, prompt, mode });
		}
	} finally {
		agent.currentRequest = null;
	}
	agent.mode = mode;
	agent.prompts += 1;

	// Resident sends come back without text/usage, so fall back to what we
	// accumulated from the stream and to the cumulative-usage delta.
	let usageSource = res?.result?.usage ? "run" : "";
	const accumulated = await accumulatedUsageRaw(agent);
	if (!res?.result?.usage) {
		agent.runUsage = normalizeUsage(usageDelta(agent.lastUsage, accumulated));
		if (agent.runUsage) usageSource = "accumulated_delta";
	} else {
		agent.runUsage = null;
	}
	agent.lastUsage = accumulated ?? agent.lastUsage;
	agent.cumulativeUsage = normalizeUsage(accumulated ?? undefined);
	const summary = summarize(agent, res, usageSource || undefined);
	agent.text = "";
	return summary;
}

async function stopAgent(agentId, { forget = false } = {}) {
	const agent = agents.get(agentId);
	if (!agent) throw rpcError("unknown_agent", `unknown agent ${agentId}`);
	if (agent.started && agent.sessionId) {
		const client = await getCore();
		try {
			await client.stop(agent.sessionId);
		} catch (err) {
			log("warn", `stop ${agent.sessionId} failed: ${err?.message ?? err}`);
		}
		sessionOwners.delete(agent.sessionId);
		agent.started = false;
	}
	if (forget) agents.delete(agentId);
	return { agentId, stopped: true, forgotten: forget };
}

async function accumulatedUsage(agentId) {
	const agent = agents.get(agentId);
	if (!agent) throw rpcError("unknown_agent", `unknown agent ${agentId}`);
	if (!agent.sessionId) return { usage: null };
	const client = await getCore();
	try {
		const summary = await client.getAccumulatedUsage(agent.sessionId);
		return { usage: normalizeUsage(summary?.usage) };
	} catch (err) {
		log("warn", `accumulated usage failed: ${err?.message ?? err}`);
		return { usage: null };
	}
}

async function handle(cmd, params, requestId) {
	switch (cmd) {
		case "ping": {
			let providers = [];
			try {
				providers = Llms.getProviderIds();
			} catch (err) {
				log("warn", `provider list failed: ${err?.message ?? err}`);
			}
			return { protocol: PROTOCOL, node: process.version, sdk: sdkVersion(), providers, pid: process.pid };
		}
		case "models": {
			const models = await Llms.getModelsForProvider(params.providerId);
			return {
				models: Object.entries(models ?? {}).map(([id, m]) => ({ id, displayName: m?.name ?? id })),
			};
		}
		case "createAgent":
			return createAgent(params);
		case "send":
			return send(params, requestId);
		case "stop":
			return stopAgent(params.agentId, { forget: false });
		case "close":
			return stopAgent(params.agentId, { forget: true });
		case "usage":
			return accumulatedUsage(params.agentId);
		case "shutdown":
			setTimeout(shutdown, 10);
			return { shutdown: true };
		default:
			throw rpcError("unknown_command", `unknown command ${cmd}`);
	}
}

async function shutdown() {
	for (const agentId of [...agents.keys()]) {
		try {
			await stopAgent(agentId, { forget: true });
		} catch (err) {
			log("warn", `shutdown stop ${agentId}: ${err?.message ?? err}`);
		}
	}
	try {
		if (core.client) await core.client.dispose();
	} catch (err) {
		log("warn", `dispose failed: ${err?.message ?? err}`);
	}
	process.exit(0);
}

const rl = readline.createInterface({ input: process.stdin });
rl.on("line", (line) => {
	const text = line.trim();
	if (!text) return;
	let req;
	try {
		req = JSON.parse(text);
	} catch (err) {
		log("error", `bad request json: ${err?.message ?? err}`);
		return;
	}
	const id = req?.id ?? null;
	void (async () => {
		try {
			const result = await handle(req?.cmd, req?.params ?? {}, id);
			write({ type: "result", id, ok: true, result });
		} catch (err) {
			write({
				type: "result",
				id,
				ok: false,
				error: { code: err?.code ?? "error", message: err?.message ?? String(err) },
			});
		}
	})();
});
rl.on("close", () => {
	log("info", "stdin closed; shutting down");
	void shutdown();
});

write({ type: "ready", protocol: PROTOCOL, pid: process.pid, node: process.version, sdk: sdkVersion() });
process.on("unhandledRejection", (err) => log("error", `unhandled rejection: ${err?.message ?? err}`));
