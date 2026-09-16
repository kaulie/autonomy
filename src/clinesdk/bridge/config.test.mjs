import test from "node:test";
import assert from "node:assert/strict";

import { clineConfigCandidates, coerceText, errorReason, interactiveSession, messageOf, resolveClineDefaults, runResultErrorEvent, withErrorReason } from "./config.mjs";

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

test("coerceText renders provider values as text", () => {
	assert.equal(coerceText("boom"), "boom");
	assert.equal(coerceText(""), "");
	assert.equal(coerceText(null), "");
	assert.equal(coerceText(undefined), "");
	assert.equal(coerceText(12), "12");
	assert.equal(coerceText(false), "false");
	assert.equal(coerceText({ code: "x", message: "boom" }), '{"code":"x","message":"boom"}');
	assert.equal(coerceText([1, 2]), "[1,2]");
});

test("interactiveSession defaults on, because only interactive sessions stay resident", () => {
	// A non-interactive session is disposed when its run ends, so the second send
	// fails with session_not_found; the default must keep the session alive.
	assert.equal(interactiveSession({}), true);
	assert.equal(interactiveSession({ AUTONOMY_CLINE_INTERACTIVE: "1" }), true);
	assert.equal(interactiveSession({ AUTONOMY_CLINE_INTERACTIVE: "true" }), true);
	assert.equal(interactiveSession({ AUTONOMY_CLINE_INTERACTIVE: "0" }), false);
	assert.equal(interactiveSession({ AUTONOMY_CLINE_INTERACTIVE: "off" }), false);
	assert.equal(interactiveSession({ AUTONOMY_CLINE_INTERACTIVE: "false" }), false);
	assert.equal(interactiveSession({ AUTONOMY_CLINE_INTERACTIVE: "no" }), false);
});

test("errorReason finds the reason wherever the SDK left it", () => {
	assert.equal(errorReason({ error: { message: "provider exploded" } }), "provider exploded");
	assert.equal(errorReason({ error: "boom" }), "boom");
	// The case an exhausted account produces: an empty error object with the
	// message beside it. `error ?? message` would pick the empty object.
	assert.equal(errorReason({ error: {}, message: "Insufficient Balance" }), "Insufficient Balance");
	assert.equal(errorReason({ message: "Insufficient Balance" }), "Insufficient Balance");
	assert.equal(errorReason({ error: {}, errorClass: "unknown" }, "agent run failed"), "agent run failed");
	assert.equal(errorReason(undefined, "agent run failed"), "agent run failed");
});

test("withErrorReason puts the reason back on the event", () => {
	const repaired = withErrorReason({
		type: "agent_event",
		payload: { sessionId: "s1", event: { type: "error", error: {}, iteration: 5, message: "Insufficient Balance" } },
	});
	assert.deepEqual(repaired.payload.event, {
		type: "error",
		error: { message: "Insufficient Balance" },
		iteration: 5,
		message: "Insufficient Balance",
	});
	// A bare inner event is repaired in place (as a copy), and the original is left alone.
	const inner = { type: "error", error: {}, message: "boom" };
	assert.deepEqual(withErrorReason(inner), { type: "error", error: { message: "boom" }, message: "boom" });
	assert.deepEqual(inner, { type: "error", error: {}, message: "boom" });
	// Nothing to repair: no reason to find, or the reason is already there.
	const silent = { type: "error", error: {} };
	assert.equal(withErrorReason(silent), silent);
	const said = { type: "error", error: { message: "rate limited" } };
	assert.equal(withErrorReason(said), said);
	const moved = { type: "tool", error: {} };
	assert.equal(withErrorReason(moved), moved);
});

test("runResultErrorEvent streams a reason the client never received", () => {
	// The failure arrived outside the request window: the stream has to carry it.
	assert.deepEqual(runResultErrorEvent("Insufficient Balance", ""), {
		type: "error",
		error: { message: "Insufficient Balance" },
		source: "run_result",
		recoverable: false,
	});
	// Already streamed: an extra event would be noise.
	assert.equal(runResultErrorEvent("Insufficient Balance", "Insufficient Balance"), null);
	// Nothing to say: a reason that already reached the stream is not repeated.
	assert.equal(runResultErrorEvent("agent run failed", "agent run failed"), null);
	// A failure with no reason of its own is still visible: the bridge's phrase is
	// what the run result says too, so the stream is not silent about a failed run.
	assert.deepEqual(runResultErrorEvent("", ""), {
		type: "error",
		error: { message: "agent run failed" },
		source: "run_result",
		recoverable: false,
	});
	assert.deepEqual(runResultErrorEvent(null, ""), {
		type: "error",
		error: { message: "agent run failed" },
		source: "run_result",
		recoverable: false,
	});
});

test("messageOf picks a human message out of an error-ish value", () => {
	assert.equal(messageOf("boom", "fallback"), "boom");
	assert.equal(messageOf({ code: "x", message: "provider exploded" }, "fallback"), "provider exploded");
	assert.equal(messageOf({ error: { message: "nested boom" } }, "fallback"), "nested boom");
	assert.equal(messageOf({ code: "provider_error" }, "fallback"), "provider_error");
	assert.equal(messageOf({ foo: 1 }, "fallback"), '{"foo":1}');
	assert.equal(messageOf({}, "fallback"), "fallback");
	assert.equal(messageOf("   ", "fallback"), "fallback");
	assert.equal(messageOf(null, "fallback"), "fallback");
});
