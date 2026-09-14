import test from "node:test";
import assert from "node:assert/strict";

import { clineConfigCandidates, resolveClineDefaults } from "./config.mjs";

const file = JSON.stringify({
	version: 1,
	lastUsedProvider: "deepseek",
	providers: {
		deepseek: { settings: { provider: "deepseek", apiKey: "sk-x", model: "deepseek-v4-pro" } },
	},
});

function readFileFor(map) {
	return (target) => {
		if (!(target in map)) {
			const err = new Error(`ENOENT: ${target}`);
			throw err;
		}
		return map[target];
	};
}

test("reads the provider and model cline auth saved", () => {
	const target = clineConfigCandidates({}, "/home/me")[0];
	const found = resolveClineDefaults({ env: {}, home: "/home/me", readFile: readFileFor({ [target]: file }) });
	assert.deepEqual(found, { providerId: "deepseek", modelId: "deepseek-v4-pro", source: target });
});

test("returns null when no provider is configured", () => {
	const found = resolveClineDefaults({ env: {}, home: "/home/me", readFile: readFileFor({}) });
	assert.equal(found, null);
});

test("ignores configs without a model", () => {
	const target = clineConfigCandidates({}, "/home/me")[0];
	const noModel = JSON.stringify({ lastUsedProvider: "deepseek", providers: { deepseek: { settings: {} } } });
	assert.equal(resolveClineDefaults({ env: {}, home: "/home/me", readFile: readFileFor({ [target]: noModel }) }), null);
});

test("an explicit data dir wins over the home default", () => {
	const env = { AUTONOMY_CLINE_DATA_DIR: "/srv/cline" };
	const candidates = clineConfigCandidates(env, "/home/me");
	assert.equal(candidates[0], "/srv/cline/settings/providers.json");
	const custom = JSON.stringify({ lastUsedProvider: "anthropic", providers: { anthropic: { settings: { model: "claude-sonnet-4-6" } } } });
	const found = resolveClineDefaults({ env, home: "/home/me", readFile: readFileFor({ [candidates[0]]: custom }) });
	assert.equal(found.providerId, "anthropic");
	assert.equal(found.modelId, "claude-sonnet-4-6");
});
