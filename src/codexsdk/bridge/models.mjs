/**
 * Live Codex catalogue: spawn `codex app-server` and paginate `model/list`.
 *
 * The TypeScript SDK used for threads does not expose a catalogue. App-server
 * does, and the answer is account-aware (ChatGPT sign-in vs API key), which is
 * why a static snapshot keeps going stale and why `gpt-5-codex` 400s a ChatGPT
 * account. A missing CLI is the caller's problem — this module throws.
 */
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import readline from "node:readline";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const HERE = dirname(fileURLToPath(import.meta.url));

/** How long a picker is allowed to wait for the CLI. */
export const LIST_TIMEOUT_MS = 20_000;

/**
 * resolveCodexBin is the `codex` executable this process will spawn: an explicit
 * path, then PATH, then the SDK's bundled CLI when the global one is missing.
 */
export function resolveCodexBin(env = process.env, exists = existsSync) {
	const named = String(env.AUTONOMY_CODEX_BIN || env.CODEX_BIN || "").trim();
	if (named) return named;
	const bundled = bundledCodexBin(exists);
	return bundled || "codex";
}

function bundledCodexBin(exists = existsSync) {
	const candidates = [];
	try {
		candidates.push(join(dirname(require.resolve("@openai/codex/package.json")), "bin", "codex"));
	} catch {
		// The SDK package is optional for unit tests that inject a fake binary.
	}
	candidates.push(join(HERE, "node_modules", ".bin", "codex"));
	return candidates.find((path) => exists(path)) || "";
}

/**
 * listCodexAppServerModels returns picker-visible `{id, displayName}` rows from
 * a one-shot `codex app-server` session. `bin` / `spawnFn` are for tests.
 */
export function listCodexAppServerModels({
	apiKey = "",
	baseUrl = "",
	bin = "",
	timeoutMs = LIST_TIMEOUT_MS,
	env = process.env,
	spawnFn = spawn,
} = {}) {
	const resolved = String(bin || resolveCodexBin(env)).trim() || "codex";
	const childEnv = { ...env };
	const key = String(apiKey || "").trim();
	const url = String(baseUrl || "").trim();
	if (key) {
		childEnv.CODEX_API_KEY = key;
		childEnv.OPENAI_API_KEY = key;
	}
	if (url) childEnv.OPENAI_BASE_URL = url;

	return new Promise((resolve, reject) => {
		const child = spawnFn(resolved, ["app-server"], {
			stdio: ["pipe", "pipe", "pipe"],
			env: childEnv,
		});
		const pending = new Map();
		let nextId = 1;
		let settled = false;
		const finish = (err, value) => {
			if (settled) return;
			settled = true;
			clearTimeout(timer);
			try {
				child.stdin.end();
			} catch {
				// already closed
			}
			child.kill();
			if (err) reject(err);
			else resolve(value);
		};
		const timer = setTimeout(() => finish(new Error("codex app-server timed out")), timeoutMs);
		child.on("error", (err) => finish(err));
		child.on("exit", (code) => {
			if (!settled) finish(new Error(`codex app-server exited ${code ?? "null"}`));
		});

		const send = (method, params, { notification = false } = {}) => {
			const message = { method, params: params ?? {} };
			if (!notification) {
				const id = nextId++;
				message.id = id;
				child.stdin.write(`${JSON.stringify(message)}\n`);
				return new Promise((ok, fail) => pending.set(id, { ok, fail }));
			}
			child.stdin.write(`${JSON.stringify(message)}\n`);
			return Promise.resolve();
		};

		const rl = readline.createInterface({ input: child.stdout });
		rl.on("line", (line) => {
			let msg;
			try {
				msg = JSON.parse(line);
			} catch {
				return;
			}
			if (msg?.method && msg.id == null) return;
			const waiter = pending.get(msg?.id);
			if (!waiter) return;
			pending.delete(msg.id);
			if (msg.error) {
				waiter.fail(new Error(msg.error.message || `codex ${msg.id}`));
				return;
			}
			waiter.ok(msg.result ?? {});
		});

		(async () => {
			await send("initialize", {
				clientInfo: { name: "autonomy", title: "autonomy", version: "1" },
			});
			await send("initialized", {}, { notification: true });
			const models = [];
			const seen = new Set();
			let cursor = "";
			for (let page = 0; page < 20; page += 1) {
				const params = { limit: 100, includeHidden: false };
				if (cursor) params.cursor = cursor;
				const listed = await send("model/list", params);
				for (const row of listed?.data ?? []) {
					if (row?.hidden) continue;
					const id = String(row?.id || row?.model || "").trim();
					if (!id || seen.has(id)) continue;
					seen.add(id);
					models.push({ id, displayName: String(row?.displayName || id) });
				}
				cursor = String(listed?.nextCursor || "").trim();
				if (!cursor) break;
			}
			if (!models.length) throw new Error("codex model/list: empty catalogue");
			finish(null, models);
		})().catch((err) => finish(err));
	});
}
