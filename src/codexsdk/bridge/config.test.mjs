import assert from "node:assert/strict";
import { test } from "node:test";

import {
	DEFAULT_SYSTEM_PROMPT,
	MODE_AGENT,
	MODE_PLAN,
	PROTOCOL,
	coerceText,
	failureMessage,
	itemText,
	normalizeUsage,
	resolveCodexDefaults,
	sandboxModeFor,
	statusOf,
} from "./config.mjs";

test("a failed turn keeps the provider's own words", () => {
	// What the CLI actually sends on stdout when the provider refuses (2026-09-24: a pooled
	// codex account with an unfunded key). The exit text alone says nothing.
	assert.equal(
		failureMessage({ type: "error", message: "Quota exceeded. Check your plan and billing details." }),
		"Quota exceeded. Check your plan and billing details.",
	);
	assert.equal(
		failureMessage({ type: "turn.failed", error: { message: "Quota exceeded." } }),
		"Quota exceeded.",
	);
	// thread.error and a message straight on the event are the other two spellings.
	assert.equal(failureMessage({ type: "thread.error", message: "stream closed" }), "stream closed");
	assert.equal(failureMessage({ type: "turn.failed", error: {} }), "");
	// Everything that is not a failure carries nothing (and never invents a reason).
	assert.equal(failureMessage({ type: "turn.completed", usage: {} }), "");
	assert.equal(failureMessage({ type: "item.completed", item: { type: "error", message: "x" } }), "");
	assert.equal(failureMessage(null), "");
	assert.equal(failureMessage({ type: "error" }), "");
});

test("protocol is its own name, not the cline one", () => {
	assert.equal(PROTOCOL, "codex-bridge/1");
});

test("a planner decides read-only, everything else may write the workspace", () => {
	assert.equal(sandboxModeFor(MODE_PLAN), "read-only");
	assert.equal(sandboxModeFor("plan"), "read-only");
	assert.equal(sandboxModeFor(MODE_AGENT), "workspace-write");
	assert.equal(sandboxModeFor(""), "workspace-write");
	assert.equal(sandboxModeFor(undefined), "workspace-write");
});

test("defaults come from the environment, never from an invented key", () => {
	assert.deepEqual(resolveCodexDefaults({}), { model: "", apiKey: "", baseUrl: "" });
	const d = resolveCodexDefaults({
		AUTONOMY_CODEX_MODEL: "gpt-5-codex",
		AUTONOMY_CODEX_API_KEY: " k ",
		AUTONOMY_CODEX_BASE_URL: "http://127.0.0.1:1",
	});
	assert.deepEqual(d, { model: "gpt-5-codex", apiKey: "k", baseUrl: "http://127.0.0.1:1" });
});

test("the CLI's own key name is read too (CODEX_API_KEY)", () => {
	assert.equal(resolveCodexDefaults({ CODEX_API_KEY: "k2" }).apiKey, "k2");
	// AUTONOMY_CODEX_API_KEY wins when both are set.
	assert.equal(resolveCodexDefaults({ AUTONOMY_CODEX_API_KEY: "a", CODEX_API_KEY: "b" }).apiKey, "a");
});

test("coerceText flattens what the runtime sends", () => {
	assert.equal(coerceText("hi"), "hi");
	assert.equal(coerceText(null), "");
	assert.equal(coerceText(["a", { text: "b" }]), "a\nb");
	assert.equal(coerceText({ prompt: "p" }), "p");
});

test("an aborted turn is cancelled, a failed one is an error", () => {
	assert.equal(statusOf("finished"), "finished");
	assert.equal(statusOf("turn.failed"), "finished"); // unknown states read as finished
	assert.equal(statusOf("error"), "error");
	assert.equal(statusOf("failed"), "error");
	assert.equal(statusOf("aborted"), "cancelled");
	assert.equal(statusOf("cancelled"), "cancelled");
});

test("only an agent message carries the turn's text", () => {
	assert.equal(itemText({ type: "agent_message", text: "done" }), "done");
	assert.equal(itemText({ type: "reasoning", text: "thinking" }), "thinking");
	assert.equal(itemText({ type: "command_execution" }), "");
	assert.equal(itemText(null), "");
});

test("usage is normalized, or absent when the provider said nothing", () => {
	assert.equal(normalizeUsage(null), null);
	assert.equal(normalizeUsage({}), null);
	assert.deepEqual(normalizeUsage({ input_tokens: 3, output_tokens: 4 }), {
		input_tokens: 3,
		output_tokens: 4,
	});
	assert.deepEqual(normalizeUsage({ inputTokens: 1, outputTokens: 2, totalTokens: 3 }), {
		input_tokens: 1,
		output_tokens: 2,
		total_tokens: 3,
	});
});

test("the default system prompt says what an autonomy worker is", () => {
	assert.match(DEFAULT_SYSTEM_PROMPT, /autonomous coding agent/);
});
