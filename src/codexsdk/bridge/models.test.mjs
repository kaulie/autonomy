import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { listCodexAppServerModels, resolveCodexBin } from "./models.mjs";

const FAKE = join(dirname(fileURLToPath(import.meta.url)), "testdata", "fake-codex.mjs");

function spawnFake(bin, args, options) {
	return spawn(process.execPath, [bin, ...args], options);
}

test("resolveCodexBin prefers an explicit path over PATH", () => {
	assert.equal(resolveCodexBin({ AUTONOMY_CODEX_BIN: " /opt/codex " }, () => false), "/opt/codex");
	assert.equal(resolveCodexBin({ CODEX_BIN: "c" }, () => false), "c");
	assert.equal(resolveCodexBin({}, () => false), "codex");
});

test("listCodexAppServerModels paginates the live catalogue", async () => {
	const models = await listCodexAppServerModels({
		bin: FAKE,
		spawnFn: spawnFake,
		env: {
			...process.env,
			CODEX_FAKE_MODELS: "gpt-5.6-sol,gpt-5.6-luna",
			CODEX_FAKE_PAGE_SIZE: "1",
		},
	});
	assert.deepEqual(models, [
		{ id: "gpt-5.6-sol", displayName: "gpt-5.6-sol" },
		{ id: "gpt-5.6-luna", displayName: "gpt-5.6-luna" },
	]);
});

test("listCodexAppServerModels injects the account key", async () => {
	const dir = await mkdtemp(join(tmpdir(), "codex-models-"));
	const envFile = join(dir, "env.json");
	await listCodexAppServerModels({
		bin: FAKE,
		spawnFn: spawnFake,
		apiKey: "account-key",
		baseUrl: "https://example.invalid/v1",
		env: { ...process.env, CODEX_FAKE_ENV_FILE: envFile },
	});
	const dumped = JSON.parse(await readFile(envFile, "utf8"));
	assert.equal(dumped.CODEX_API_KEY, "account-key");
	assert.equal(dumped.OPENAI_BASE_URL, "https://example.invalid/v1");
});

test("an empty live catalogue is an error, not a silent []", async () => {
	await assert.rejects(
		() =>
			listCodexAppServerModels({
				bin: FAKE,
				spawnFn: spawnFake,
				env: { ...process.env, CODEX_FAKE_MODELS: "" },
			}),
		/empty catalogue/,
	);
});
