import test from "node:test";
import assert from "node:assert/strict";

import { condense, eventLabel, signalLine, traceLevel, traceWidth } from "./trace.mjs";

function agentEvent(inner) {
	return { type: "agent_event", payload: { sessionId: "cls-1", event: inner } };
}

test("traceLevel: the bridge is silent unless asked to speak", () => {
	assert.equal(traceLevel({}), "silent");
	assert.equal(traceLevel({ AUTONOMY_CLINE_TRACE: "0" }), "silent");
	assert.equal(traceLevel({ AUTONOMY_CLINE_TRACE: "off" }), "silent");
	assert.equal(traceLevel({ AUTONOMY_LLM_DEBUG: "0" }), "silent");
	assert.equal(traceLevel({ AUTONOMY_CLINE_TRACE: "signal" }), "signal");
	assert.equal(traceLevel({ AUTONOMY_CLINE_TRACE: "1" }), "full");
	assert.equal(traceLevel({ AUTONOMY_CLINE_TRACE: "true" }), "full");
	assert.equal(traceLevel({ AUTONOMY_LLM_DEBUG: "1" }), "full");
	assert.equal(traceLevel({ AUTONOMY_LLM_DEBUG: "verbose" }), "full");
});

test("traceLevel: an explicit AUTONOMY_CLINE_TRACE wins", () => {
	assert.equal(traceLevel({ AUTONOMY_CLINE_TRACE: "signal", AUTONOMY_LLM_DEBUG: "1" }), "signal");
	assert.equal(traceLevel({ AUTONOMY_CLINE_TRACE: "0", AUTONOMY_LLM_DEBUG: "1" }), "silent");
	assert.equal(traceLevel({ AUTONOMY_CLINE_TRACE: "1", AUTONOMY_LLM_DEBUG: "0" }), "full");
});

test("traceWidth defaults and overrides", () => {
	assert.equal(traceWidth({}), 600);
	assert.equal(traceWidth({ AUTONOMY_CLINE_TRACE_MAX: "120" }), 120);
	assert.equal(traceWidth({ AUTONOMY_CLINE_TRACE_MAX: "0" }), 600);
	assert.equal(traceWidth({ AUTONOMY_CLINE_TRACE_MAX: "nope" }), 600);
});

test("condense collapses whitespace and marks truncation", () => {
	assert.equal(condense("a\n  b\tc"), "a b c");
	assert.equal(condense(null), "");
	assert.equal(condense({ command: "ls", args: ["-l"] }), '{"command":"ls","args":["-l"]}');
	const long = condense("x".repeat(12), 10);
	assert.equal(long, "xxxxxxxxxx…(+2ch)");
});

test("eventLabel names the inner event with its content type", () => {
	assert.equal(eventLabel(agentEvent({ type: "content_end", contentType: "reasoning" })), "agent_event/content_end:reasoning");
	assert.equal(eventLabel({ type: "chunk", payload: { stream: "agent" } }), "chunk/chunk");
});

test("signalLine drops the per-chunk noise", () => {
	// The SDK's own JSON echo of the stream, and the process-stream chunks.
	assert.equal(signalLine({ type: "chunk", payload: { stream: "agent", chunk: "..." } }), null);
	assert.equal(signalLine({ type: "chunk", payload: { stream: "stdout", chunk: "..." } }), null);
	// One content_start per text/reasoning delta: the block is printed at end.
	assert.equal(signalLine(agentEvent({ type: "content_start", contentType: "text", text: "hel" })), null);
	assert.equal(signalLine(agentEvent({ type: "content_start", contentType: "reasoning", reasoning: "let me" })), null);
	// Streaming tool output is noise; the tool's end event carries the result.
	assert.equal(signalLine(agentEvent({ type: "content_update", contentType: "tool", update: { chunk: "a" } })), null);
	assert.equal(signalLine({ type: "session_snapshot", payload: { cwd: "/tmp", tools: [] } }), null);
	assert.equal(signalLine({ type: "hook", payload: { hookEventName: "tool_call", toolName: "read_file" } }), null);
});

test("signalLine prints thinking with its content", () => {
	const line = signalLine(agentEvent({ type: "content_end", contentType: "reasoning", reasoning: "I\n need  to look first" }));
	assert.equal(line, "think I need to look first");
	assert.equal(signalLine(agentEvent({ type: "content_end", contentType: "reasoning", redacted: true })), "think <redacted>");
	assert.equal(signalLine(agentEvent({ type: "content_end", contentType: "reasoning" })), null);
});

test("signalLine prints tool calls with arguments and output", () => {
	const start = signalLine(
		agentEvent({ type: "content_start", contentType: "tool", toolName: "execute_command", toolCallId: "call-1", input: { command: "ls -la" } }),
	);
	assert.equal(start, 'tool execute_command start call=call-1 args={"command":"ls -la"}');

	const ok = signalLine(
		agentEvent({ type: "content_end", contentType: "tool", toolName: "execute_command", toolCallId: "call-1", output: "total 8", durationMs: 42 }),
	);
	assert.equal(ok, "tool execute_command end call=call-1 ok 42ms out=total 8");

	const failed = signalLine(
		agentEvent({ type: "content_end", contentType: "tool", toolName: "read_file", toolCallId: "call-2", error: { message: "ENOENT" } }),
	);
	assert.equal(failed, "tool read_file end call=call-2 failed error=ENOENT");
});

test("signalLine prints the model's message and the run skeleton", () => {
	assert.equal(signalLine(agentEvent({ type: "content_end", contentType: "text", text: "Done, all tests pass." })), "assistant Done, all tests pass.");
	assert.equal(signalLine(agentEvent({ type: "iteration_start", iteration: 2 })), "iteration 2 start");
	assert.equal(signalLine(agentEvent({ type: "iteration_end", iteration: 2, toolCallCount: 3 })), "iteration 2 end tools=3");
	assert.equal(
		signalLine(agentEvent({ type: "usage", inputTokens: 1637, outputTokens: 4, cacheReadTokens: 1536, cost: 0.000053 })),
		"usage in=1637 out=4 cache_read=1536 cost=$0.000053",
	);
	assert.equal(signalLine(agentEvent({ type: "done", reason: "completed", iterations: 3 })), "done completed iterations=3");
	assert.equal(signalLine(agentEvent({ type: "error", error: { message: "rate limited" }, iteration: 1 })), "error rate limited");
	assert.equal(signalLine(agentEvent({ type: "notice", noticeType: "recovery", message: "retrying" })), "notice recovery retrying");
});

test("signalLine keeps unknown agent events as bare labels", () => {
	assert.equal(signalLine(agentEvent({ type: "something_new", iteration: 1 })), "something_new");
	assert.equal(signalLine(agentEvent({})), null);
});

test("signalLine prints non-agent core events (status / ended)", () => {
	assert.equal(signalLine({ type: "status", payload: { status: "running" } }), "status running");
	assert.equal(signalLine({ type: "ended", payload: { reason: "completed" } }), "ended completed");
	assert.equal(signalLine({ type: "ended", payload: {} }), "ended");
});

test("signalLine honours the width budget", () => {
	const line = signalLine(agentEvent({ type: "content_end", contentType: "reasoning", reasoning: "y".repeat(50) }), 20);
	assert.equal(line, `think ${"y".repeat(20)}…(+30ch)`);
});
