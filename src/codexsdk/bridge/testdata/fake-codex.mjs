#!/usr/bin/env node
/**
 * Fake `codex` CLI for catalogue tests: speaks app-server JSON-RPC on stdio
 * and answers `model/list` from the environment, so Go and Node tests stay
 * hermetic (no real CLI, no account).
 *
 *   CODEX_FAKE_MODELS=gpt-5.6-sol,gpt-5.6-luna
 *   CODEX_FAKE_PAGE_SIZE=1          — force pagination
 *   CODEX_FAKE_ENV_FILE=/tmp/x.json — dump the credential env we were given
 *   CODEX_FAKE_FAIL=initialize|list|exit
 */
import { writeFileSync } from "node:fs";
import readline from "node:readline";

const args = process.argv.slice(2);
if (args.length && args[0] !== "app-server") {
	process.stderr.write(`unknown command ${args[0]}\n`);
	process.exit(2);
}

if (process.env.CODEX_FAKE_ENV_FILE) {
	writeFileSync(
		process.env.CODEX_FAKE_ENV_FILE,
		JSON.stringify({
			CODEX_API_KEY: process.env.CODEX_API_KEY || "",
			OPENAI_API_KEY: process.env.OPENAI_API_KEY || "",
			OPENAI_BASE_URL: process.env.OPENAI_BASE_URL || "",
		}),
	);
}

if (process.env.CODEX_FAKE_FAIL === "exit") {
	process.exit(1);
}

const models = String(process.env.CODEX_FAKE_MODELS ?? "gpt-5.6-sol,gpt-5.6-luna")
	.split(",")
	.map((id) => id.trim())
	.filter(Boolean);
const pageSize = Math.max(1, Number(process.env.CODEX_FAKE_PAGE_SIZE || models.length) || models.length);

const write = (obj) => process.stdout.write(`${JSON.stringify(obj)}\n`);

const rl = readline.createInterface({ input: process.stdin });
rl.on("line", (line) => {
	const trimmed = line.trim();
	if (!trimmed) return;
	let req;
	try {
		req = JSON.parse(trimmed);
	} catch {
		return;
	}
	const { method, id, params } = req;
	if (method === "initialized") return;
	if (method === "initialize") {
		if (process.env.CODEX_FAKE_FAIL === "initialize") {
			write({ id, error: { code: -32000, message: "already initialized" } });
			return;
		}
		write({ id, result: { userAgent: "fake-codex", platformFamily: "test" } });
		return;
	}
	if (method === "model/list") {
		if (process.env.CODEX_FAKE_FAIL === "list") {
			write({ id, error: { code: -32000, message: "model/list failed" } });
			return;
		}
		const offset = Number(params?.cursor || 0) || 0;
		const data = models.slice(offset, offset + pageSize).map((modelId) => ({
			id: modelId,
			model: modelId,
			displayName: modelId,
			hidden: false,
		}));
		const next = offset + pageSize;
		write({
			id,
			result: {
				data,
				nextCursor: next < models.length ? String(next) : null,
			},
		});
	}
});
